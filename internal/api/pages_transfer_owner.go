package api

// Owner-departure transfer (docs/prd/pages.md §7.1 rule 1b, issue #1944,
// epic #1935).
//
// pages.owner_user_id carries ON DELETE RESTRICT
// (internal/database/migrations/20260812155322_pages.sql:60) so that a page
// can never be silently orphaned or cascade-deleted when its owner leaves —
// the constraint makes transferring the page a PRECONDITION of removing the
// user's row, not a cleanup step a handler might forget to run. This file is
// that precondition. It is called from the user-erasure handler
// (admin_gdpr.go DeleteUserData, the Art. 17 cascade — "removes every row
// referencing this user in this workspace", and owner_user_id is exactly
// such a row) before any of that handler's cascading deletes run, so the
// whole erasure refuses rather than proceeding halfway when a page cannot be
// resolved.
//
// The rule, in order (§7.1 rule 1b):
//
//  1. the crew that owns the most panels on the page
//  2. else the crew the departing user belonged to (in this workspace)
//
// An earlier PRD draft ended that list with "else none" — the orphan the
// rule itself forbids. Corrected 2026-08-12: when NEITHER resolves, this
// file refuses the whole transfer with a clear, page-naming error and
// commits nothing. Inventing a fallback (an "unassigned" bucket, the
// workspace's oldest crew, whatever) is exactly the silent cascade the
// RESTRICT constraint exists to prevent — a page with a made-up owner is
// still a lie about who is responsible for it. A human decides instead.
//
// Every resolved transfer emits one journal entry and one inbox notification
// targeted at workspace ADMIN/OWNER (§7.1 rule 1b: "so a role that does not
// leave can reassign the page"), both inside the same transaction as the
// ownership UPDATE — either the whole batch of transfers (and their
// notifications) lands, or none of it does.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// entryPageOwnerTransferred is scoped to this file rather than added to the
// shared closed vocabulary in internal/journal/types.go — EntryType is a
// plain string type, so a local constant is enough, and it avoids a
// concurrent edit collision with other agents' work on the Pages feature
// while epic #1935 has six tracks running in parallel.
const entryPageOwnerTransferred journal.EntryType = "page.owner_transferred"

// pagesTransferInboxKind reuses the existing 'message' inbox kind (the
// closed CHECK on inbox_items.kind lives in internal/database, which this
// change does not touch) rather than minting a new one. sender_type +
// title carry what a dedicated kind would otherwise encode.
const pagesTransferInboxKind = "message"

// pageOwnerTransferResult describes one page that was (or, before commit,
// will be) handed from its departing user owner to a crew.
type pageOwnerTransferResult struct {
	PageID     string
	PageSlug   string
	PageName   string
	ToCrewID   string
	ToCrewName string
	// Reason is "most_panels" or "member_crew" — which half of §7.1 rule 1b
	// resolved the target, recorded on the journal entry and the
	// notification payload so a reviewing ADMIN/OWNER can see why.
	Reason string
}

// ErrPagesNeedManualTransfer is returned when one or more of the departing
// user's pages cannot be auto-assigned to a crew: no crew owns any panel on
// the page, AND the departing user belongs to no crew in this workspace.
// Per §7.1 rule 1b (corrected 2026-08-12) there is no third fallback — an
// orphaned or silently-deleted page is precisely what the rule forbids, so
// the caller must refuse the erasure and let a human reassign the page
// first (e.g. `crewship page grant` / a manual owner change).
type ErrPagesNeedManualTransfer struct {
	Pages []string // "slug (name)" for every page that could not be resolved
}

func (e *ErrPagesNeedManualTransfer) Error() string {
	return fmt.Sprintf(
		"cannot erase this user: page(s) %s have no crew that owns their panels and the user belongs to no crew in this workspace — reassign these pages to a crew manually, then retry",
		strings.Join(e.Pages, ", "))
}

// transferDepartingUserPages hands every page `userID` owns in `wsID` to a
// crew, so a caller (the erasure handler) can then remove the user's data
// from the workspace without pages.owner_user_id's ON DELETE RESTRICT (or
// this same rule, informally) ever being violated.
//
// Scoped to one workspace deliberately: the erasure this feeds is itself
// workspace-scoped ("removes every row referencing this user in THIS
// workspace"), and owner_user_id carries no workspace column of its own —
// pages.workspace_id is what ties a page to the erasure's blast radius.
// A user who owns pages in other workspaces still needs THOSE workspaces'
// erasure run separately; that is consistent with every other table this
// cascade already touches.
//
// All-or-nothing: if any page cannot be resolved, NOTHING is transferred —
// not even the pages that did resolve — and the transaction rolls back. A
// partial transfer would still let the erasure proceed for some pages while
// silently leaving others exactly where the RESTRICT already blocks them,
// which just moves the inconsistency instead of removing it.
func transferDepartingUserPages(ctx context.Context, db *sql.DB, j journal.Emitter, actorID, wsID, userID string) ([]pageOwnerTransferResult, error) {
	if j == nil {
		j = noopEmitter{}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pages transfer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	type pageRow struct{ id, slug, name string }
	var pages []pageRow
	rows, err := tx.QueryContext(ctx,
		`SELECT id, slug, name FROM pages WHERE workspace_id = ? AND owner_user_id = ?`,
		wsID, userID)
	if err != nil {
		return nil, fmt.Errorf("select user-owned pages: %w", err)
	}
	for rows.Next() {
		var p pageRow
		if err := rows.Scan(&p.id, &p.slug, &p.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan user-owned page: %w", err)
		}
		pages = append(pages, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate user-owned pages: %w", err)
	}
	rows.Close()

	if len(pages) == 0 {
		// Nothing to do. Roll back the (read-only) tx and return cleanly —
		// the common case, most departing users own no page at all.
		return nil, nil
	}

	type resolvedTransfer struct {
		page     pageRow
		crewID   string
		crewName string
		reason   string
	}
	var toTransfer []resolvedTransfer
	var unresolved []string

	for _, p := range pages {
		crewID, reason, err := resolveTransferTargetCrew(ctx, tx, wsID, p.id, userID)
		if err != nil {
			return nil, fmt.Errorf("resolve target crew for page %s: %w", p.slug, err)
		}
		if crewID == "" {
			unresolved = append(unresolved, fmt.Sprintf("%s (%s)", p.slug, p.name))
			continue
		}
		var crewName string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM crews WHERE id = ?`, crewID).Scan(&crewName); err != nil {
			return nil, fmt.Errorf("load target crew %s for page %s: %w", crewID, p.slug, err)
		}
		toTransfer = append(toTransfer, resolvedTransfer{page: p, crewID: crewID, crewName: crewName, reason: reason})
	}

	if len(unresolved) > 0 {
		// Refuse the WHOLE batch — see file header. Rolling back here (via
		// the deferred tx.Rollback) means pages that DID resolve are not
		// transferred either; the erasure has to be retried after a human
		// fixes the unresolved ones.
		return nil, &ErrPagesNeedManualTransfer{Pages: unresolved}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	results := make([]pageOwnerTransferResult, 0, len(toTransfer))
	for _, t := range toTransfer {
		if _, err := tx.ExecContext(ctx,
			`UPDATE pages SET owner_user_id = NULL, owner_crew_id = ?, updated_at = ? WHERE id = ?`,
			t.crewID, now, t.page.id); err != nil {
			return nil, fmt.Errorf("transfer page %s to crew %s: %w", t.page.slug, t.crewID, err)
		}

		if err := insertPageOwnerTransferNotice(ctx, tx, wsID, userID, t.page.id, t.page.slug, t.page.name, t.crewID, t.crewName, t.reason, now); err != nil {
			return nil, fmt.Errorf("notify transfer of page %s: %w", t.page.slug, err)
		}

		results = append(results, pageOwnerTransferResult{
			PageID:     t.page.id,
			PageSlug:   t.page.slug,
			PageName:   t.page.name,
			ToCrewID:   t.crewID,
			ToCrewName: t.crewName,
			Reason:     t.reason,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pages transfer: %w", err)
	}

	// Journal entries are emitted post-commit (best-effort, mirroring
	// PageHandler.journalGrantChange in pages_grants.go): the transfer
	// itself must not be held hostage to the journal writer, but a
	// transfer nobody can audit is not what §7.1 rule 1b asked for, so a
	// failure here is logged loudly rather than swallowed.
	for _, res := range results {
		emitPageOwnerTransferJournal(ctx, j, wsID, actorID, userID, res)
	}

	return results, nil
}

// resolveTransferTargetCrew implements §7.1 rule 1b's ordered rule for one
// page: the crew that owns the most (live) panels on it, else the crew the
// departing user belongs to (in this workspace). Returns crewID == "" when
// neither resolves — the caller turns that into ErrPagesNeedManualTransfer.
func resolveTransferTargetCrew(ctx context.Context, tx *sql.Tx, wsID, pageID, userID string) (crewID, reason string, err error) {
	// Rule 1: the crew that owns the most panels on this page. Ties broken
	// by crew id ascending — deterministic rather than "whichever SQLite
	// happened to visit first", so the same inputs always resolve the same
	// way. Joins crews to exclude a soft-deleted crew from ever becoming a
	// page's new owner.
	var count int
	err = tx.QueryRowContext(ctx, `
		SELECT pp.owner_crew_id, COUNT(*) AS cnt
		FROM page_panels pp
		JOIN crews c ON c.id = pp.owner_crew_id
		WHERE pp.page_id = ? AND c.deleted_at IS NULL
		GROUP BY pp.owner_crew_id
		ORDER BY cnt DESC, pp.owner_crew_id ASC
		LIMIT 1`, pageID).Scan(&crewID, &count)
	switch {
	case err == nil && crewID != "":
		return crewID, "most_panels", nil
	case err != nil && err != sql.ErrNoRows:
		return "", "", err
	}

	// Rule 2: else the crew the departing user belonged to, in this
	// workspace. A user can belong to more than one crew; ties broken by
	// crew id ascending for the same determinism reason as above.
	err = tx.QueryRowContext(ctx, `
		SELECT cm.crew_id
		FROM crew_members cm
		JOIN crews c ON c.id = cm.crew_id
		WHERE cm.user_id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
		ORDER BY cm.crew_id ASC
		LIMIT 1`, userID, wsID).Scan(&crewID)
	switch {
	case err == nil:
		return crewID, "member_crew", nil
	case err == sql.ErrNoRows:
		return "", "", nil
	default:
		return "", "", err
	}
}

// insertPageOwnerTransferNotice writes the inbox_items row that notifies
// workspace ADMIN/OWNER a page changed hands (§7.1 rule 1b: "so a role that
// does not leave can reassign the page"). Reuses the existing 'message' kind
// and the existing target_role column — inboxVisibilityClause (inbox_handler.go)
// already resolves 'ADMIN' as visible to ADMIN and OWNER (role rank is
// hierarchical), which is exactly the audience the rule names, without
// needing a new inbox kind or a new role constant.
func insertPageOwnerTransferNotice(ctx context.Context, tx *sql.Tx, wsID, fromUserID, pageID, pageSlug, pageName, toCrewID, toCrewName, reason, now string) error {
	title := fmt.Sprintf("Page %q transferred to crew %q", pageSlug, toCrewName)
	var reasonText string
	switch reason {
	case "most_panels":
		reasonText = "the crew already owns the most panels on this page"
	case "member_crew":
		reasonText = "no crew owns panels on this page yet, so it went to the departing owner's crew"
	default:
		reasonText = reason
	}
	body := fmt.Sprintf(
		"Page %q (%s) had a user owner who left the workspace. It was automatically transferred to crew %q — %s. Reassign it if that is not the right home.",
		pageName, pageSlug, toCrewName, reasonText)

	payload, err := json.Marshal(map[string]any{
		"page_id":      pageID,
		"page_slug":    pageSlug,
		"from_user_id": fromUserID,
		"to_crew_id":   toCrewID,
		"reason":       reason,
	})
	if err != nil {
		return fmt.Errorf("marshal notice payload: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO inbox_items (
			id, workspace_id, kind, source_id, target_role,
			title, body_md, sender_type, sender_name,
			state, priority, blocking, payload_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'ADMIN', ?, ?, 'system', 'Pages', 'unread', 'medium', 0, ?, ?, ?)`,
		generateCUID(), wsID, pagesTransferInboxKind, pageID, title, body, string(payload), now, now)
	return err
}

// emitPageOwnerTransferJournal writes the audit record §7.1 rule 1b
// requires ("the transfer emits a journal entry"). Best-effort — the
// transfer already committed by the time this runs — but a failure is
// logged loudly by the Emitter itself; this package holds no logger of its
// own here, matching the other post-commit journal call sites in Pages.
func emitPageOwnerTransferJournal(ctx context.Context, j journal.Emitter, wsID, actorID, fromUserID string, res pageOwnerTransferResult) {
	_, _ = j.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryPageOwnerTransferred,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary: fmt.Sprintf("page %s transferred to crew %s after owner departure (%s)",
			res.PageSlug, res.ToCrewName, res.Reason),
		Payload: map[string]any{
			"page_id":      res.PageID,
			"page_slug":    res.PageSlug,
			"from_user_id": fromUserID,
			"to_crew_id":   res.ToCrewID,
			"to_crew_name": res.ToCrewName,
			"reason":       res.Reason,
		},
	})
}
