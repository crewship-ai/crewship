package api

// Art. 17 erasure — unnaming the subject on the Pages tables (issue #1976,
// defect 2; docs/prd/pages.md §7.1b, §7.3.2, §10b.5c).
//
// # The contract this file implements
//
// A workspace-scoped erasure promises that the subject is UNNAMED IN THAT
// WORKSPACE on every table the cascade enumerates — see admin_gdpr.go's "The
// contract" for what that sentence does and does not cover instance-wide.
// This file is what makes it true of the Pages tables, which is the sentence
// #1976 asked for and did not have: the
// cascade in admin_gdpr.go transferred the subject's pages (§7.1 rule 1b) and
// stopped, so four columns kept naming a person whose SAR ticket had been
// closed as "erased":
//
//	page_versions.author_user_id       ON DELETE SET NULL
//	page_grants.granted_by_user_id     ON DELETE CASCADE
//	page_public_tokens.created_by_user_id  ON DELETE CASCADE
//	page_webhooks.created_by_user_id   ON DELETE CASCADE
//
// Every one of those columns already carries the right answer in its FK
// action — and none of them ever fires, because a workspace-scoped erasure
// deliberately never deletes the `users` row (a person may be a member of
// several workspaces, and one workspace's SAR cannot speak for another's).
// The schema had decided what should happen to these rows when the human
// behind them goes away; nothing was executing that decision.
//
// So this file does not invent a policy. It RUNS THE POLICY THE SCHEMA
// ALREADY DECLARES, restricted to one workspace:
//
//   - SET NULL for page_versions: history stays, identity goes. A version is
//     worth keeping without its author ("a version whose author was erased is
//     still a version worth keeping" — the migration's own words), and the
//     rollback that version exists for still works.
//   - CASCADE (delete) for the other three: each is an AUTHORITY, not a
//     record. §7.1b rule 1 is that only a human issues a grant, and its
//     second half is that a grant whose issuer no longer exists is authority
//     delegated by nobody; page_webhooks' migration says the same of a token
//     ("a capability nobody is accountable for, and it dies with them"). The
//     NOT NULL on all three issuer columns is that rule in the schema, which
//     is also why nulling the issuer is not an option here without relaxing a
//     constraint that exists to prevent exactly the row we would be creating.
//
// # Why the two capability tables are deleted rather than marked revoked
//
// Both page_public_tokens and page_webhooks carry `revoked_at`, and the
// ordinary revoke path (pages_public_tokens.go, pages_webhooks.go) marks
// rather than deletes — "was it used after we pulled it" is a question a
// deleted row cannot answer. That reasoning is about an INCIDENT, and it is
// not the reasoning that applies here: a marked-revoked row still carries
// `created_by_user_id NOT NULL`, i.e. it still names the subject, which is
// the one thing an erasure may not leave behind. Nulling the column instead
// would need a migration relaxing a NOT NULL whose whole purpose is "issued
// only by a human" — permanently weakening an invariant on every row, for
// every workspace, to serve the rare erasure. The schema's own answer for a
// vanished issuer is the CASCADE. So: delete — but carry the forensic fields
// those columns exist for (last_fired_at, fire_count, last_seen_at) into the
// journal entry first, so "was it used after we pulled it" still has an
// answer once the row is gone.
//
// # Why every removal is journalled, one entry per row
//
// pages_grants.go states the rule for these tables outright: "an ACL nobody
// can audit is not a security control". Every ordinary revoke on these three
// tables writes one journal entry per row changed, and an erasure that
// removed the same rows behind an aggregate count would leave nobody able to
// answer "which page did the crew lose access to, and which integration did
// we just break". So this step emits the SAME entry types the ordinary revoke
// path emits — page.grant_removed, page.link_revoked, page.webhook_revoked —
// with the gdpr_actions id in the payload, so a reader lands on the erasure
// that caused it.
//
// The one thing those entries must NOT carry is the subject's own id.
// journal_entries is on the deliberately-excluded list (a SAR does not erase
// the SAR), so writing the erased user's id into it would have the erasure
// itself re-name the subject in a table nothing will ever clean. The actor is
// the ADMIN who ran the erasure, the payload carries the action id, and a
// grant that named the subject as its grantee is recorded as
// `subject_erased: true` with no id. The identity lives in gdpr_actions,
// which is the row designed to hold it.
//
// # Scoping
//
// None of the four tables has a workspace_id column, so every statement joins
// through pages.workspace_id (page_webhooks through page_panels first). That
// is what stops an erasure in one workspace from touching the subject's rows
// in another — the guarantee the whole workspace-scoped design rests on, and
// it is asserted directly in admin_gdpr_pages_identity_test.go.
//
// # Append-only tables are NOT reached from here
//
// journal_entries, audit_logs and gdpr_actions still name the subject after
// this runs, by design — see the "Tables intentionally excluded" section of
// admin_gdpr.go. A SAR does not erase the record of the SAR.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// pageIdentityErasure is the per-table receipt this step hands back to the
// cascade, so the audit row and the operator's terminal both say what
// happened rather than reporting one opaque total.
type pageIdentityErasure struct {
	VersionsAnonymised  int
	GrantsRemoved       int
	PublicTokensRevoked int
	WebhooksRevoked     int

	// revoked describes each capability row that went, for the journal. Built
	// inside the transaction (the rows are unreadable afterwards) and emitted
	// after it commits, so a journal failure can never roll back an erasure
	// that the operator has been told is done.
	revoked []pageCapabilityRevocation
}

// pageCapabilityRevocation is one removed capability, already shaped for the
// journal entry it becomes.
type pageCapabilityRevocation struct {
	Type    journal.EntryType
	Summary string
	Payload map[string]any
}

// erasePagesIdentity unnames targetID on the four Pages tables, inside wsID
// only, and journals every capability it revoked. All four statements run in
// one transaction: they are one promise ("the subject is unnamed on Pages in
// this workspace"), and a half-applied promise is worse than a refused one —
// an operator reading a receipt that says the grants went would have no
// reason to suspect the webhook token the same person minted is still live.
//
// The rest of the cascade in admin_gdpr.go is deliberately NOT wrapped in
// this transaction. It is statement-at-a-time with a firstErr accumulator and
// a 207 for partial success, because several of its steps have on-disk side
// effects (peer card files, user model files) that no database transaction
// can roll back; making the whole cascade atomic would be a promise the
// filesystem cannot keep. These four steps touch nothing but rows, so they
// can keep the stronger guarantee among themselves.
//
// On ANY error the returned receipt is zeroed, not partial: the transaction
// rolled back, so counts accumulated before the failure describe work that no
// longer exists, and an audit row is the last place to report work that was
// undone.
func (h *AdminGDPRHandler) erasePagesIdentity(ctx context.Context, actionID, actorID, wsID, targetID string) (pageIdentityErasure, error) {
	var out pageIdentityErasure

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin pages identity erasure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1) page_versions — anonymise. The column is nullable precisely so an
	// erased author leaves a version behind rather than a hole in the
	// history, and the SET NULL on its FK is the same decision. Nothing to
	// journal: no authority changed hands, and the version is still there.
	res, err := tx.ExecContext(ctx, `
		UPDATE page_versions
		   SET author_user_id = NULL
		 WHERE author_user_id = ?
		   AND page_id IN (SELECT id FROM pages WHERE workspace_id = ?)`,
		targetID, wsID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("anonymise page_versions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("anonymise page_versions: rows affected: %w", err)
	}
	out.VersionsAnonymised = int(n)

	// 2) page_grants — remove. Read first, so the journal can name what went;
	// then delete with the SAME predicate, in one statement, so a row where
	// the subject is both issuer and grantee is counted once, not twice.
	grants, err := collectErasedGrants(ctx, tx, wsID, targetID)
	if err != nil {
		return pageIdentityErasure{}, err
	}
	res, err = tx.ExecContext(ctx, `
		DELETE FROM page_grants
		 WHERE page_id IN (SELECT id FROM pages WHERE workspace_id = ?)
		   AND (granted_by_user_id = ? OR (subject_type = 'user' AND subject_id = ?))`,
		wsID, targetID, targetID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("remove page_grants: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("remove page_grants: rows affected: %w", err)
	}
	out.GrantsRemoved = int(n)
	out.revoked = append(out.revoked, grants...)

	// 3) page_public_tokens — revoke by removal. Every live /p/{token} link
	// the subject published stops resolving at once.
	links, err := collectErasedPublicLinks(ctx, tx, wsID, targetID)
	if err != nil {
		return pageIdentityErasure{}, err
	}
	res, err = tx.ExecContext(ctx, `
		DELETE FROM page_public_tokens
		 WHERE created_by_user_id = ?
		   AND page_id IN (SELECT id FROM pages WHERE workspace_id = ?)`,
		targetID, wsID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_public_tokens: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_public_tokens: rows affected: %w", err)
	}
	out.PublicTokensRevoked = int(n)
	out.revoked = append(out.revoked, links...)

	// 4) page_webhooks — revoke by removal. Two hops to the workspace: the
	// token names only a panel (deliberately — see the migration's "WHY THERE
	// IS NO page_id COLUMN"), and the panel names the page.
	hooks, err := collectErasedWebhooks(ctx, tx, wsID, targetID)
	if err != nil {
		return pageIdentityErasure{}, err
	}
	res, err = tx.ExecContext(ctx, `
		DELETE FROM page_webhooks
		 WHERE created_by_user_id = ?
		   AND panel_id IN (
		       SELECT pl.id FROM page_panels pl
		       JOIN pages p ON p.id = pl.page_id
		       WHERE p.workspace_id = ?)`,
		targetID, wsID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_webhooks: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_webhooks: rows affected: %w", err)
	}
	out.WebhooksRevoked = int(n)
	out.revoked = append(out.revoked, hooks...)

	if err := tx.Commit(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("commit pages identity erasure: %w", err)
	}

	h.journalRevocations(ctx, actionID, actorID, wsID, out.revoked)
	return out, nil
}

// collectErasedGrants reads the grants step 2 is about to delete, under the
// same predicate, so each one can be journalled by page and grantee.
func collectErasedGrants(ctx context.Context, tx *sql.Tx, wsID, targetID string) ([]pageCapabilityRevocation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.id, p.slug, g.subject_type, g.subject_id, g.level, g.granted_by_user_id
		  FROM page_grants g
		  JOIN pages p ON p.id = g.page_id
		 WHERE p.workspace_id = ?
		   AND (g.granted_by_user_id = ? OR (g.subject_type = 'user' AND g.subject_id = ?))`,
		wsID, targetID, targetID)
	if err != nil {
		return nil, fmt.Errorf("read page_grants to revoke: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []pageCapabilityRevocation
	for rows.Next() {
		var pageID, slug, subjectType, subjectID, level, grantedBy string
		if err := rows.Scan(&pageID, &slug, &subjectType, &subjectID, &level, &grantedBy); err != nil {
			return nil, fmt.Errorf("read page_grants to revoke: %w", err)
		}
		payload := map[string]any{
			"page":         slug,
			"page_id":      pageID,
			"subject_type": subjectType,
			"level":        level,
			// Which arc of the predicate matched, so a reader can tell
			// "authority this person delegated" from "access this person had".
			"issued_by_subject": grantedBy == targetID,
		}
		grantee := subjectType + "/" + subjectID
		if subjectID == targetID {
			// Never write the erased subject's id into journal_entries — see
			// the file header. The fact is recorded, the identity is not.
			payload["subject_erased"] = true
			grantee = "the erased subject"
		} else {
			payload["subject_id"] = subjectID
		}
		out = append(out, pageCapabilityRevocation{
			Type:    journal.EntryPageGrantRemoved,
			Summary: fmt.Sprintf("erasure revoked %s on page %s from %s", level, slug, grantee),
			Payload: payload,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read page_grants to revoke: %w", err)
	}
	return out, nil
}

// collectErasedPublicLinks reads the public links step 3 is about to delete,
// carrying last_seen_at into the entry — a deleted row cannot answer "was it
// used after we pulled it", so the answer is written down before it goes.
func collectErasedPublicLinks(ctx context.Context, tx *sql.Tx, wsID, targetID string) ([]pageCapabilityRevocation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT t.id, p.id, p.slug, COALESCE(t.last_seen_at, ''), COALESCE(t.revoked_at, ''), t.expires_at
		  FROM page_public_tokens t
		  JOIN pages p ON p.id = t.page_id
		 WHERE p.workspace_id = ? AND t.created_by_user_id = ?`,
		wsID, targetID)
	if err != nil {
		return nil, fmt.Errorf("read page_public_tokens to revoke: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []pageCapabilityRevocation
	for rows.Next() {
		var tokenID, pageID, slug, lastSeen, revokedAt, expiresAt string
		if err := rows.Scan(&tokenID, &pageID, &slug, &lastSeen, &revokedAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("read page_public_tokens to revoke: %w", err)
		}
		payload := map[string]any{
			"page":       slug,
			"page_id":    pageID,
			"token_id":   tokenID,
			"expires_at": expiresAt,
			// Already revoked before the erasure? Then this removal changed
			// nothing an outsider could reach, and the entry should say so.
			"was_live": revokedAt == "",
		}
		if lastSeen != "" {
			payload["last_seen_at"] = lastSeen
		}
		out = append(out, pageCapabilityRevocation{
			Type:    journalPageLinkRevoked,
			Summary: fmt.Sprintf("erasure revoked a public link on page %s", slug),
			Payload: payload,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read page_public_tokens to revoke: %w", err)
	}
	return out, nil
}

// collectErasedWebhooks reads the inbound tokens step 4 is about to delete.
// fire_count and last_fired_at travel with the entry for the same reason as
// last_seen_at above, and because the operator running the SAR is about to
// break somebody's cron and deserves to know which panel it wrote to.
func collectErasedWebhooks(ctx context.Context, tx *sql.Tx, wsID, targetID string) ([]pageCapabilityRevocation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT wh.id, COALESCE(wh.name, ''), pl.id, pl.panel_id, p.id, p.slug,
		       COALESCE(wh.last_fired_at, ''), wh.fire_count, COALESCE(wh.revoked_at, '')
		  FROM page_webhooks wh
		  JOIN page_panels pl ON pl.id = wh.panel_id
		  JOIN pages p ON p.id = pl.page_id
		 WHERE p.workspace_id = ? AND wh.created_by_user_id = ?`,
		wsID, targetID)
	if err != nil {
		return nil, fmt.Errorf("read page_webhooks to revoke: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []pageCapabilityRevocation
	for rows.Next() {
		var hookID, name, panelRowID, panelName, pageID, slug, lastFired, revokedAt string
		var fireCount int
		if err := rows.Scan(&hookID, &name, &panelRowID, &panelName, &pageID, &slug,
			&lastFired, &fireCount, &revokedAt); err != nil {
			return nil, fmt.Errorf("read page_webhooks to revoke: %w", err)
		}
		payload := map[string]any{
			"page":       slug,
			"page_id":    pageID,
			"panel":      panelName,
			"panel_id":   panelRowID,
			"webhook_id": hookID,
			"fire_count": fireCount,
			"was_live":   revokedAt == "",
		}
		if name != "" {
			payload["name"] = name
		}
		if lastFired != "" {
			payload["last_fired_at"] = lastFired
		}
		label := "a webhook"
		if name != "" {
			label = fmt.Sprintf("webhook %q", name)
		}
		out = append(out, pageCapabilityRevocation{
			Type:    journalPageWebhookRevoked,
			Summary: fmt.Sprintf("erasure revoked %s on panel %s of page %s", label, panelName, slug),
			Payload: payload,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read page_webhooks to revoke: %w", err)
	}
	return out, nil
}

// journalRevocations writes one entry per revoked capability. Best-effort with
// respect to the erasure — the rows are already gone and committed, and an
// erasure the operator has been told is done must not be reported as failed
// because a journal write did not land — but a failure is logged loudly, for
// the reason the rule exists: an ACL nobody can audit is not a security
// control.
func (h *AdminGDPRHandler) journalRevocations(ctx context.Context, actionID, actorID, wsID string, revoked []pageCapabilityRevocation) {
	if h.journal == nil {
		return
	}
	for _, rev := range revoked {
		payload := rev.Payload
		// The link back to the erasure. gdpr_actions is where the subject's
		// identity lives; this entry only says an erasure caused the change.
		payload["gdpr_action_id"] = actionID
		payload["cause"] = "gdpr_erasure"
		payload["actor_user_id"] = actorID
		if _, err := h.journal.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			Type:        rev.Type,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorUser,
			ActorID:     actorID,
			Summary:     rev.Summary,
			Payload:     payload,
		}); err != nil {
			h.logger.Warn("gdpr delete: capability revocation was not journalled",
				"action_id", actionID, "workspace_id", wsID, "type", string(rev.Type), "err", err)
		}
	}
}
