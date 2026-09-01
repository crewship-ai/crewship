package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-F F6 — Admin GDPR cascade endpoints.
//
// Two routes, both ADMIN+ in the current workspace:
//
//   GET    /api/v1/admin/users/{userId}/data — Art. 15 Right of Access:
//                                              return everything we
//                                              hold about the user in
//                                              this workspace.
//   DELETE /api/v1/admin/users/{userId}/data — Art. 17 Right to Erasure:
//                                              cascade purge every row
//                                              referencing the user.
//
// Both write one row into gdpr_actions (v107) recording who acted on
// whom, when, what the scope ended up being, and the operator-supplied
// reason. The audit row is the operator's defensible artefact for the
// compliance ticket.
//
// # Why ADMIN+ rather than OWNER-only
//
// The auditor's framing was "the operator who handles SAR tickets is
// not necessarily the workspace owner — Compliance vs Founder is a
// real separation of duties in EU teams". canRole("manage") admits
// OWNER and ADMIN, which is the right floor: workspace MANAGER (who
// can hire/fire agents) is intentionally NOT a SAR actor.
//
// # Scope of the cascade
//
// Tables enumerated (every table that v107's PRD § Per-table choice
// rationale documented):
//
//   - peer_cards (by user_id) — on-disk file purge is best-effort
//     per row; DB row delete is unconditional so the SAR is honoured
//     even when the host filesystem path is misconfigured.
//   - memory_versions (by data_subject_id) — does NOT touch the
//     content-addressed blob on disk. Blobs are deduplicated across
//     workspaces, so a single SAR cannot safely delete them; the
//     append-only audit row is dropped, and a follow-up sweep job
//     (planned for v108 — see Known Gaps) will GC unreferenced
//     blobs.
//   - inbox_items (by data_subject_id) — hard delete. Soft-delete
//     would leave the proposal title visible in the inbox feed,
//     which the SAR forbids.
//   - inbox_item_reads (A7, by user_id, scoped to this workspace via
//     the inbox_items join) — the subject's own record of which
//     inbox items they opened. Independent of the item's
//     data_subject_id: this is the subject's activity, not content
//     about them, and it must go even when the item itself (someone
//     else's) is untouched.
//   - approvals_queue (by requested_by OR decided_by) — hard delete
//     (#2233). The table predates v107 and was never considered for
//     either list — grep for "approvals" in this file returned zero
//     hits before this. It has no data_subject_id column, so unlike
//     the two rows above it is matched on the two columns that ARE
//     user-attributable: requested_by (a user id for an
//     interactively-triggered gate, an agent id for an
//     autonomously-triggered one — matching is still correct because
//     ids never collide across the two) and decided_by (always a
//     user id — only a human decides). docs/security/gdpr.mdx already
//     frames "agent decisions" as operator-owned content erasure may
//     reach, distinct from the audit trail that must survive it — see
//     "Why erase rather than exclude" below for the full argument. The
//     row's own accountability trail (who decided what, when) survives
//     in journal_entries regardless, which is on the excluded list.
//
// # Why erase approvals_queue rather than add it to the excluded list
//
// The three excluded tables below share one property: no user-attributable
// content, or content that is itself the SAR's own accountability record.
// approvals_queue is neither. Its payload is (pre-#2250) a raw tool-call
// prompt the requester wrote, and requested_by / decided_by / reason name
// the two participants directly — exactly the "content the operator owns"
// gdpr.mdx's Article 17 section calls out ("agent decisions") as distinct
// from "audit records that have to survive operator's own retention
// obligations". Leaving it erased-nowhere was not a considered position,
// it was the absence of one — the issue that opened this (#2233) exists
// precisely because nobody had decided. A retention sweep (see
// internal/harbormaster/retention.go) bounds how long an UNDECIDED-to-erase
// row survives, but a live SAR ticket cannot wait out a 90-day default, so
// the cascade needs its own deliberate answer: erase.
//
// Tables intentionally excluded:
//
//   - keeper_requests — agent/crew/credential scoped, no user-
//     attributable content. See migrate_consts_v107 commentary.
//   - audit_logs / journal_entries / gdpr_actions — accountability
//     records. The auditor framing is "you log what you did about a
//     user, AND you keep that log even after you've deleted the
//     user — that's how the regulator verifies compliance". A SAR
//     does not erase the SAR itself.
//   - lessons.md content scan — Punted. Lessons content can mention
//     a user by free-text slug; scanning + redacting requires content
//     awareness we don't have at this layer. The handler logs a
//     warning naming the deleted user_id so the operator knows to
//     manually review lessons.md if any. See Known Gaps.
//
// # Pages (docs/prd/pages.md §7.1 rule 1b, issue #1944)
//
// pages.owner_user_id references users(id) ON DELETE RESTRICT, so a page a
// departing user owns is a row that would otherwise BLOCK this cascade
// (once whatever caller eventually hard-deletes the users row runs into
// it), not one this handler could safely skip. Before any of the deletes
// below run, transferDepartingUserPages (pages_transfer_owner.go) hands
// every page the subject owns IN THIS WORKSPACE to a crew — never deletes
// it — and if a page cannot be resolved to a crew, the ENTIRE erasure
// refuses rather than deleting some rows and leaving the page problem for
// later. See transferOrRefuseUserPages below.
//
// # Idempotency
//
// Running DELETE twice for the same user is a no-op the second time
// from a row-count perspective (every WHERE clause matches zero
// rows), but writes a SECOND gdpr_actions row with scope_json
// showing zero rows purged. The audit trail captures both attempts,
// which is the right shape — auditors want to see "did the operator
// try this twice and what happened each time", not "we deduplicated
// the call".

// AdminGDPRHandler exposes the cascade DELETE and the read-only
// EXPORT endpoint. outputBasePath is the host-side root the
// container provider bind-mounts; passed in so the cascade can
// purge on-disk peer card files alongside their DB rows. Empty
// outputBasePath disables on-disk delete (DB rows still purged —
// see purgeUserCards rationale in user_peer_privacy.go).
type AdminGDPRHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	outputBasePath string
	// journal records the pages owner-transfer audit trail §7.1 rule 1b
	// requires. Defaults to noopEmitter so a handler built without
	// SetJournal (e.g. an older test) still runs — see SetJournal.
	journal journal.Emitter
}

// NewAdminGDPRHandler constructs a handler. outputBasePath should be
// the same root the persona / peer card handlers receive so the
// per-agent .memory paths resolve consistently across endpoints.
func NewAdminGDPRHandler(db *sql.DB, logger *slog.Logger, outputBasePath string) *AdminGDPRHandler {
	return &AdminGDPRHandler{db: db, logger: logger, outputBasePath: outputBasePath, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter so a transferred page's audit entry
// (§7.1 rule 1b) lands in the real Crew Journal once the router has
// resolved one. A nil argument collapses back to noopEmitter, matching
// every other SetJournal in this package.
func (h *AdminGDPRHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// adminContext bundles the boilerplate every handler in this file
// shares: actor identity, workspace, and the target user_id from
// the path. Returns ok=false after writing the appropriate error
// response so callers can early-return cleanly.
func (h *AdminGDPRHandler) adminContext(w http.ResponseWriter, r *http.Request) (actorID, wsID, targetID string, ok bool) {
	actor := UserFromContext(r.Context())
	if actor == nil || actor.ID == "" {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return "", "", "", false
	}
	wsID = WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return "", "", "", false
	}
	// canRole("manage") admits OWNER and ADMIN — see helpers.go
	// for the role tier table. MANAGER and below get 403.
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN+ only")
		return "", "", "", false
	}
	targetID = strings.TrimSpace(r.PathValue("userId"))
	if targetID == "" {
		replyError(w, http.StatusBadRequest, "userId path parameter required")
		return "", "", "", false
	}
	return actor.ID, wsID, targetID, true
}

// gdprActionScope records the per-table count summary written into
// gdpr_actions.scope_json. Open shape — extensible without a schema
// migration when a new cascadable table is added.
type gdprActionScope struct {
	PeerCards       int `json:"peer_cards"`
	MemoryVersions  int `json:"memory_versions"`
	InboxItems      int `json:"inbox_items"`
	InboxItemReads  int `json:"inbox_item_reads"`
	UserModels      int `json:"user_models"`
	PeerCardsOnDisk int `json:"peer_cards_on_disk,omitempty"`
	// ApprovalsQueue (#2233) counts rows deleted because the subject was
	// either the requester or the decider — see the file header "Why erase
	// approvals_queue rather than add it to the excluded list".
	ApprovalsQueue int `json:"approvals_queue,omitempty"`
	// PagesTransferred counts pages handed to a crew because the subject
	// owned them (§7.1 rule 1b). Never a delete count — a page is never
	// deleted by this cascade, only reassigned.
	PagesTransferred int `json:"pages_transferred,omitempty"`
}

// newGDPRActionID returns a short hex id for a gdpr_actions row.
// Inlined (vs reusing newAuditID) so a future newAuditID format
// change can't accidentally change the id shape compliance tooling
// already grep'd for.
func newGDPRActionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Should never happen on a healthy host; fall back to a
		// time-based id so we never write a NULL primary key.
		return "ga_fallback_" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return "ga_" + hex.EncodeToString(b)
}

// insertGDPRAction inserts the initial in_progress row and returns
// its id. Failure to insert the audit row is fatal — without it the
// cascade has no defensible artefact, which is the whole point. The
// 500 here matches the auditor's "never silently lose audit" rule.
func (h *AdminGDPRHandler) insertGDPRAction(ctx context.Context, wsID, subjectID, actorID, action, reason string) (string, error) {
	id := newGDPRActionID()
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO gdpr_actions
		(id, workspace_id, data_subject_id, actor_user_id, action, reason, initiated_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'in_progress')
	`, id, wsID, subjectID, actorID, action, nilIfEmpty(reason), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	return id, nil
}

// finalizeGDPRAction marks the row complete or failed with the
// scope summary attached. Best-effort: a write failure here is
// logged but cannot un-do the cascade that already ran. The row
// will stay at status='in_progress' and a future reconcile job
// can sweep it.
func (h *AdminGDPRHandler) finalizeGDPRAction(ctx context.Context, id string, scope gdprActionScope, finalErr error) {
	scopeJSON, _ := json.Marshal(scope)
	status := "completed"
	var errMsg any
	if finalErr != nil {
		status = "failed"
		errMsg = finalErr.Error()
	}
	if _, err := h.db.ExecContext(ctx, `
		UPDATE gdpr_actions
		SET status = ?, scope_json = ?, completed_at = ?, error = ?
		WHERE id = ?
	`, status, string(scopeJSON), time.Now().UTC().Format(time.RFC3339Nano), errMsg, id); err != nil {
		h.logger.Warn("finalize gdpr_action failed",
			"id", id, "status", status, "err", err)
	}
}

// transferOrRefuseUserPages runs the §7.1 rule 1b precondition: every page
// targetID owns in wsID moves to a crew, never gets deleted, and if any
// page can't be resolved (see ErrPagesNeedManualTransfer) the whole erasure
// is refused before it mutates anything else. On success, scope.PagesTransferred
// is filled in so the audit row and the response both record it — including
// the zero-pages, zero-transferred common case.
func (h *AdminGDPRHandler) transferOrRefuseUserPages(ctx context.Context, actionID, actorID, wsID, targetID string, scope *gdprActionScope) error {
	results, err := transferDepartingUserPages(ctx, h.db, h.journal, actorID, wsID, targetID)
	if err != nil {
		return err
	}
	scope.PagesTransferred = len(results)
	if len(results) > 0 {
		h.logger.Info("gdpr delete: transferred user-owned pages ahead of erasure",
			"action_id", actionID, "workspace_id", wsID, "target", targetID, "count", len(results))
	}
	return nil
}

// DeleteUserData is the Art. 17 cascade. POST-condition:
//
//   - pages the subject owned are transferred to a crew, never deleted
//     (§7.1 rule 1b) — and if any page can't be resolved to a crew, NONE
//     of the rest of this cascade runs either; see transferOrRefuseUserPages.
//   - peer_cards rows referencing the user are gone (DB + best-
//     effort on-disk).
//   - memory_versions rows tagged data_subject_id = user are gone.
//   - inbox_items rows tagged data_subject_id = user are gone.
//   - approvals_queue rows where the user was requester or decider are
//     gone (#2233).
//   - gdpr_actions has a 'delete' row with scope_json + status=
//     'completed' (or 'failed' with error populated).
//
// Body shape (matches the frontend already shipped in PR-F2 UI):
//
//	{ "reason": "GDPR SAR ticket #1234" }
//
// reason is required for the audit trail. The frontend gates the
// destructive button behind a confirm dialog; we DO NOT also
// require a `confirm: true` body field because the live UI does
// not send it and adding it post-hoc would 400 every existing UI
// invocation. The path itself is destructive; the audit row +
// reason satisfy the auditor's accountability requirement.
//
// DELETE /api/v1/admin/users/{userId}/data
func (h *AdminGDPRHandler) DeleteUserData(w http.ResponseWriter, r *http.Request) {
	actorID, wsID, targetID, ok := h.adminContext(w, r)
	if !ok {
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		replyError(w, http.StatusBadRequest, "reason is required for the audit trail")
		return
	}

	actionID, err := h.insertGDPRAction(r.Context(), wsID, targetID, actorID, "delete", reason)
	if err != nil {
		h.logger.Error("gdpr delete: failed to insert audit row",
			"workspace_id", wsID, "target", targetID, "err", err)
		replyError(w, http.StatusInternalServerError, "failed to record audit entry")
		return
	}

	var (
		scope    gdprActionScope
		firstErr error
	)

	// 0) pages: transfer BEFORE anything else purges. §7.1 rule 1b makes
	// this a precondition, not a cleanup step — a page transfer that ran
	// after the rest of the cascade would mean an erasure that already
	// deleted the subject's peer_cards/memory_versions/inbox_items/
	// user_models rows could still fail on a page it couldn't resolve,
	// leaving a half-erased subject. Refusing FIRST, before any delete
	// runs, means a failed erasure changes nothing.
	if err := h.transferOrRefuseUserPages(r.Context(), actionID, actorID, wsID, targetID, &scope); err != nil {
		var needsManual *ErrPagesNeedManualTransfer
		status := http.StatusInternalServerError
		msg := "failed to transfer subject's pages ahead of erasure"
		if errors.As(err, &needsManual) {
			status = http.StatusConflict
			msg = err.Error()
		} else {
			h.logger.Error("gdpr delete: pages transfer failed",
				"action_id", actionID, "workspace_id", wsID, "target", targetID, "err", err)
		}
		h.finalizeGDPRAction(r.Context(), actionID, scope, err)
		writeJSON(w, status, map[string]any{
			"action_id":    actionID,
			"data_subject": targetID,
			"workspace_id": wsID,
			"error":        msg,
		})
		return
	}

	// 1) peer_cards: walk rows so we can purge the on-disk file
	// per row before the DB row goes (best-effort on disk). Match
	// purgeUserCards in user_peer_privacy.go so the two paths
	// stay in lockstep.
	cardRows, err := h.db.QueryContext(r.Context(), `
		SELECT pc.id, pc.agent_id, pc.user_slug, COALESCE(a.slug,''), COALESCE(a.crew_id,'')
		FROM peer_cards pc
		LEFT JOIN agents a ON a.id = pc.agent_id
		WHERE pc.user_id = ? AND pc.workspace_id = ?
	`, targetID, wsID)
	if err != nil {
		firstErr = err
		h.logger.Warn("gdpr delete: peer_cards select failed",
			"action_id", actionID, "err", err)
	} else {
		type cardRow struct {
			cardID, agentID, slug, agentSlug, crewID string
		}
		var cards []cardRow
		for cardRows.Next() {
			var c cardRow
			if scanErr := cardRows.Scan(&c.cardID, &c.agentID, &c.slug, &c.agentSlug, &c.crewID); scanErr != nil {
				// Don't silently `continue` past a Scan failure in a
				// GDPR cascade — a malformed row would otherwise drop
				// past the delete loop entirely, leaving the data on
				// disk while the subject's SAR ticket says "deleted".
				// Propagate via firstErr so the handler returns 500
				// and the operator retries with the underlying schema
				// drift fixed. CodeRabbit round-9 catch.
				h.logger.Error("gdpr delete: peer_cards scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			cards = append(cards, c)
		}
		if iterErr := cardRows.Err(); iterErr != nil {
			// Iteration errors aren't just observability noise — if the
			// underlying scan was interrupted (network blip on the SQLite
			// page cache, fs error mid-read), we silently delete only
			// the rows we DID see, and the rest survives. Propagate via
			// firstErr so the handler returns 500 and the operator
			// retries the SAR call. CodeRabbit round-11 catch.
			h.logger.Error("gdpr delete: peer_cards iteration error",
				"action_id", actionID, "err", iterErr)
			if firstErr == nil {
				firstErr = iterErr
			}
		}
		_ = cardRows.Close()
		for _, c := range cards {
			// On-disk file delete is best-effort. The DB row
			// goes regardless so SAR "show me everything you
			// have" returns nothing post-call.
			if h.outputBasePath != "" && c.crewID != "" && c.agentSlug != "" {
				paths := memory.PeerPaths{
					AgentDir: filepath.Join(h.outputBasePath, "crews", c.crewID, "agents", c.agentSlug, ".memory"),
				}
				if delErr := memory.DeletePeerCardBySlug(paths, c.slug); delErr != nil {
					h.logger.Warn("gdpr delete: on-disk peer card delete failed",
						"action_id", actionID, "agent_id", c.agentID, "err", delErr)
				} else {
					scope.PeerCardsOnDisk++
				}
			}
			res, delErr := h.db.ExecContext(r.Context(),
				`DELETE FROM peer_cards WHERE id = ?`, c.cardID)
			if delErr != nil {
				h.logger.Warn("gdpr delete: peer_cards row delete failed",
					"action_id", actionID, "card_id", c.cardID, "err", delErr)
				if firstErr == nil {
					firstErr = delErr
				}
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				scope.PeerCards++
			}
		}
	}

	// 2) memory_versions: bulk delete by data_subject_id. The
	// content-addressed blob on disk is NOT touched — see
	// commentary in the file header (blobs are deduplicated
	// across workspaces; orphan GC is a separate concern).
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM memory_versions WHERE workspace_id = ? AND data_subject_id = ?`,
		wsID, targetID)
	if err != nil {
		h.logger.Warn("gdpr delete: memory_versions delete failed",
			"action_id", actionID, "err", err)
		if firstErr == nil {
			firstErr = err
		}
	} else if n, _ := res.RowsAffected(); n > 0 {
		scope.MemoryVersions = int(n)
	}

	// 3) inbox_items: bulk delete by data_subject_id.
	res, err = h.db.ExecContext(r.Context(),
		`DELETE FROM inbox_items WHERE workspace_id = ? AND data_subject_id = ?`,
		wsID, targetID)
	if err != nil {
		h.logger.Warn("gdpr delete: inbox_items delete failed",
			"action_id", actionID, "err", err)
		if firstErr == nil {
			firstErr = err
		}
	} else if n, _ := res.RowsAffected(); n > 0 {
		scope.InboxItems = int(n)
	}

	// 3b) inbox_item_reads (A7): the subject's OWN per-item read markers.
	// Distinct from step 3 above — that deletes items ABOUT the subject
	// (data_subject_id); this deletes the subject's activity record of
	// having READ items, which may belong to anyone (a role-targeted
	// escalation the subject happened to open). Rows for items step 3 just
	// deleted are already gone via inbox_item_reads.inbox_item_id's
	// ON DELETE CASCADE; this catches the rest. Scoped to this workspace
	// via the inbox_items join — the subject's read markers in a DIFFERENT
	// workspace are a separate SAR ticket.
	res, err = h.db.ExecContext(r.Context(), `
		DELETE FROM inbox_item_reads
		WHERE user_id = ? AND inbox_item_id IN (SELECT id FROM inbox_items WHERE workspace_id = ?)`,
		targetID, wsID)
	if err != nil {
		h.logger.Warn("gdpr delete: inbox_item_reads delete failed",
			"action_id", actionID, "err", err)
		if firstErr == nil {
			firstErr = err
		}
	} else if n, _ := res.RowsAffected(); n > 0 {
		scope.InboxItemReads = int(n)
	}

	// 4) approvals_queue: hard delete every row the subject touched, either
	// as requester or as decider (#2233) — a single statement, so a row
	// where the subject happens to be both (a self-approved gate) is still
	// counted once in RowsAffected, not twice. See the file header "Why
	// erase approvals_queue rather than add it to the excluded list" for
	// why this table is erased at all.
	res, err = h.db.ExecContext(r.Context(),
		`DELETE FROM approvals_queue WHERE workspace_id = ? AND (requested_by = ? OR decided_by = ?)`,
		wsID, targetID, targetID)
	if err != nil {
		h.logger.Warn("gdpr delete: approvals_queue delete failed",
			"action_id", actionID, "err", err)
		if firstErr == nil {
			firstErr = err
		}
	} else if n, _ := res.RowsAffected(); n > 0 {
		scope.ApprovalsQueue = int(n)
	}

	// 5) user_models: the operator model — the one surface holding the
	// subject's OWN stated facts (#1669). UNIQUE on (workspace_id,
	// user_slug), so at most one row.
	//
	// This step had no equivalent before the extractor stopped being a
	// no-op, and unlike the opt-out path there is nothing behind an admin
	// erase to catch it later: consolidate.SyncUserModel purges on
	// user_peer_consent, which a SAR erase does not set. Without this the
	// file survived indefinitely and kept being read into agent prompts
	// after the ticket was closed.
	umRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, user_slug, COALESCE(crew_id,'')
		FROM user_models
		WHERE user_id = ? AND workspace_id = ?
	`, targetID, wsID)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		h.logger.Warn("gdpr delete: user_models select failed",
			"action_id", actionID, "err", err)
	} else {
		type modelRow struct{ id, slug, crewID string }
		var models []modelRow
		for umRows.Next() {
			var m modelRow
			if scanErr := umRows.Scan(&m.id, &m.slug, &m.crewID); scanErr != nil {
				// Same rule as peer_cards above: never `continue` past a
				// scan failure silently in a GDPR cascade, or the row is
				// skipped while the ticket says deleted.
				h.logger.Error("gdpr delete: user_models scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			models = append(models, m)
		}
		if iterErr := umRows.Err(); iterErr != nil {
			h.logger.Error("gdpr delete: user_models iteration error",
				"action_id", actionID, "err", iterErr)
			if firstErr == nil {
				firstErr = iterErr
			}
		}
		_ = umRows.Close()
		for _, m := range models {
			if h.outputBasePath != "" {
				// Deletes from EVERY crew directory on disk, not just
				// m.crewID. The index row only ever names the operator's
				// CURRENT most-active crew — a prior crew reassignment
				// moves crew_id forward without removing the file it left
				// behind in the crew the operator has since left, and an
				// erase that reconstructed a single "expected" path from
				// m.crewID would leave that copy readable after the SAR
				// ticket said "deleted" (#1701).
				if _, delErr := memory.DeleteUserModelEverywhere(h.outputBasePath, m.slug); delErr != nil {
					h.logger.Warn("gdpr delete: on-disk user model delete failed",
						"action_id", actionID, "err", delErr)
					if firstErr == nil {
						firstErr = delErr
					}
					// Do NOT delete the index row when a copy could not be
					// removed. This loop is reached by walking user_models
					// WHERE user_id/workspace_id — the only enumerator that
					// ever revisits this slug — so deleting the row here
					// would make the surviving copy permanently unfindable
					// by a future retry of this same cascade, while the
					// response still has to admit the failure (207) rather
					// than report the erasure as complete.
					continue
				}
			}
			res, delErr := h.db.ExecContext(r.Context(),
				`DELETE FROM user_models WHERE id = ?`, m.id)
			if delErr != nil {
				h.logger.Warn("gdpr delete: user_models row delete failed",
					"action_id", actionID, "model_id", m.id, "err", delErr)
				if firstErr == nil {
					firstErr = delErr
				}
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				scope.UserModels++
			}
		}
	}

	// Punted: lessons.md content scan. We do not have a content-
	// aware redactor at this layer and a naive substring sweep
	// could corrupt lesson semantics. Log a clear warning so the
	// operator knows to manually review lessons.md for any
	// mention of the deleted user_id and act if needed. The SAR
	// is otherwise honoured; this is a documented gap, not a
	// silent failure.
	h.logger.Warn("gdpr delete: lessons.md content scan deferred — operator must manually review",
		"action_id", actionID, "workspace_id", wsID, "data_subject_id", targetID)

	h.finalizeGDPRAction(r.Context(), actionID, scope, firstErr)

	rowsDeleted := scope.PeerCards + scope.MemoryVersions + scope.InboxItems + scope.InboxItemReads + scope.UserModels + scope.ApprovalsQueue
	status := http.StatusAccepted
	resp := map[string]any{
		"action_id":    actionID,
		"data_subject": targetID,
		"workspace_id": wsID,
		"rows_deleted": rowsDeleted,
		"scope":        scope,
	}
	if firstErr != nil {
		resp["error"] = firstErr.Error()
		status = http.StatusMultiStatus // 207: partial success, audit row tells full story
	}
	writeJSON(w, status, resp)
}

// gdprExportBundle is the Art. 15 response payload. Stable JSON
// shape so external SAR-ticket tooling can parse it without
// breakage when new tables are added — new top-level keys are
// additive.
type gdprExportBundle struct {
	DataSubjectID  string                `json:"data_subject_id"`
	WorkspaceID    string                `json:"workspace_id"`
	ExportedAt     string                `json:"exported_at"`
	ActionID       string                `json:"action_id"`
	PeerCards      []exportPeerCard      `json:"peer_cards"`
	MemoryVersion  []exportMemoryVersion `json:"memory_versions"`
	InboxItems     []exportInboxItem     `json:"inbox_items"`
	InboxItemReads []exportInboxItemRead `json:"inbox_item_reads"`
	UserModels     []exportUserModel     `json:"user_models"`
}

// exportInboxItemRead is the subject's own per-item read marker (A7):
// "I read inbox item X at time Y". title/kind are carried alongside the id
// so the SAR answer is legible without a second lookup against inbox_items
// (which may itself be a different subject's item the requester merely
// opened).
type exportInboxItemRead struct {
	InboxItemID string `json:"inbox_item_id"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	ReadAt      string `json:"read_at"`
}

// exportUserModel carries the operator model INCLUDING its body. The
// other export shapes are index rows because their content is either
// elsewhere (memory_versions' blob) or already in the row; here the body
// is the whole point — an access request answered with "30 bytes exist
// somewhere" tells the subject nothing about what is stored about them.
type exportUserModel struct {
	ID        string `json:"id"`
	UserSlug  string `json:"user_slug"`
	Path      string `json:"path"`
	Bytes     int    `json:"bytes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Content   string `json:"content,omitempty"`
}

type exportPeerCard struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	UserSlug  string `json:"user_slug"`
	Path      string `json:"path"`
	Bytes     int    `json:"bytes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type exportMemoryVersion struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Tier       string `json:"tier"`
	SHA256     string `json:"sha256"`
	Bytes      int    `json:"bytes"`
	WrittenAt  string `json:"written_at"`
	WrittenBy  string `json:"written_by,omitempty"`
	PayloadRef string `json:"payload_ref"`
}

type exportInboxItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	Title       string `json:"title"`
	BodyMD      string `json:"body_md,omitempty"`
	State       string `json:"state"`
	PayloadJSON string `json:"payload_json,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// ExportUserData is the Art. 15 access endpoint — returns every
// row we hold about the user across the four cascadable tables.
// Writes a gdpr_actions row with action='export' and status=
// 'completed' immediately (export is read-only, no failure modes
// after the SELECTs return).
//
// GET /api/v1/admin/users/{userId}/data
func (h *AdminGDPRHandler) ExportUserData(w http.ResponseWriter, r *http.Request) {
	actorID, wsID, targetID, ok := h.adminContext(w, r)
	if !ok {
		return
	}

	actionID, err := h.insertGDPRAction(r.Context(), wsID, targetID, actorID, "export", "")
	if err != nil {
		h.logger.Error("gdpr export: failed to insert audit row",
			"workspace_id", wsID, "target", targetID, "err", err)
		replyError(w, http.StatusInternalServerError, "failed to record audit entry")
		return
	}

	bundle := gdprExportBundle{
		DataSubjectID:  targetID,
		WorkspaceID:    wsID,
		ExportedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		ActionID:       actionID,
		PeerCards:      []exportPeerCard{},
		MemoryVersion:  []exportMemoryVersion{},
		InboxItems:     []exportInboxItem{},
		InboxItemReads: []exportInboxItemRead{},
		UserModels:     []exportUserModel{},
	}

	scope := gdprActionScope{}
	var firstErr error

	// peer_cards
	pcRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, agent_id, user_slug, path, bytes,
		       created_at, updated_at
		FROM peer_cards
		WHERE workspace_id = ? AND user_id = ?
		ORDER BY updated_at DESC
	`, wsID, targetID)
	if err != nil {
		firstErr = err
		h.logger.Warn("gdpr export: peer_cards query failed",
			"action_id", actionID, "err", err)
	} else {
		for pcRows.Next() {
			var e exportPeerCard
			if scanErr := pcRows.Scan(&e.ID, &e.AgentID, &e.UserSlug, &e.Path,
				&e.Bytes, &e.CreatedAt, &e.UpdatedAt); scanErr != nil {
				h.logger.Error("gdpr export: peer_cards scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			bundle.PeerCards = append(bundle.PeerCards, e)
		}
		if iterErr := pcRows.Err(); iterErr != nil {
			h.logger.Warn("gdpr export: peer_cards iteration error",
				"action_id", actionID, "err", iterErr)
		}
		_ = pcRows.Close()
		scope.PeerCards = len(bundle.PeerCards)
	}

	// memory_versions
	mvRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, path, tier, sha256, bytes, written_at,
		       COALESCE(written_by,''), payload_ref
		FROM memory_versions
		WHERE workspace_id = ? AND data_subject_id = ?
		ORDER BY written_at DESC
	`, wsID, targetID)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		h.logger.Warn("gdpr export: memory_versions query failed",
			"action_id", actionID, "err", err)
	} else {
		for mvRows.Next() {
			var e exportMemoryVersion
			if scanErr := mvRows.Scan(&e.ID, &e.Path, &e.Tier, &e.SHA256,
				&e.Bytes, &e.WrittenAt, &e.WrittenBy, &e.PayloadRef); scanErr != nil {
				h.logger.Error("gdpr export: memory_versions scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			bundle.MemoryVersion = append(bundle.MemoryVersion, e)
		}
		if iterErr := mvRows.Err(); iterErr != nil {
			h.logger.Warn("gdpr export: memory_versions iteration error",
				"action_id", actionID, "err", iterErr)
		}
		_ = mvRows.Close()
		scope.MemoryVersions = len(bundle.MemoryVersion)
	}

	// inbox_items
	ibRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, kind, source_id, title, COALESCE(body_md,''),
		       state, COALESCE(payload_json,''), created_at
		FROM inbox_items
		WHERE workspace_id = ? AND data_subject_id = ?
		ORDER BY created_at DESC
	`, wsID, targetID)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		h.logger.Warn("gdpr export: inbox_items query failed",
			"action_id", actionID, "err", err)
	} else {
		for ibRows.Next() {
			var e exportInboxItem
			if scanErr := ibRows.Scan(&e.ID, &e.Kind, &e.SourceID, &e.Title,
				&e.BodyMD, &e.State, &e.PayloadJSON, &e.CreatedAt); scanErr != nil {
				h.logger.Error("gdpr export: inbox_items scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			bundle.InboxItems = append(bundle.InboxItems, e)
		}
		if iterErr := ibRows.Err(); iterErr != nil {
			h.logger.Warn("gdpr export: inbox_items iteration error",
				"action_id", actionID, "err", iterErr)
		}
		_ = ibRows.Close()
		scope.InboxItems = len(bundle.InboxItems)
	}

	// inbox_item_reads (A7): the subject's own per-item read activity,
	// scoped to this workspace via the inbox_items join — mirrors the
	// DELETE cascade's step 3b.
	irRows, err := h.db.QueryContext(r.Context(), `
		SELECT ir.inbox_item_id, ii.title, ii.kind, ir.read_at
		FROM inbox_item_reads ir
		JOIN inbox_items ii ON ii.id = ir.inbox_item_id
		WHERE ii.workspace_id = ? AND ir.user_id = ?
		ORDER BY ir.read_at DESC
	`, wsID, targetID)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		h.logger.Warn("gdpr export: inbox_item_reads query failed",
			"action_id", actionID, "err", err)
	} else {
		for irRows.Next() {
			var e exportInboxItemRead
			if scanErr := irRows.Scan(&e.InboxItemID, &e.Title, &e.Kind, &e.ReadAt); scanErr != nil {
				h.logger.Error("gdpr export: inbox_item_reads scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			bundle.InboxItemReads = append(bundle.InboxItemReads, e)
		}
		if iterErr := irRows.Err(); iterErr != nil {
			h.logger.Warn("gdpr export: inbox_item_reads iteration error",
				"action_id", actionID, "err", iterErr)
			if firstErr == nil {
				firstErr = iterErr
			}
		}
		_ = irRows.Close()
		scope.InboxItemReads = len(bundle.InboxItemReads)
	}

	// user_models — the operator model (#1669). Body included, not just
	// the index row: this is the surface holding the subject's own stated
	// facts, and it is the one table here whose content is not derivable
	// from anything else in the bundle.
	umRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, user_slug, path, bytes, created_at, updated_at, COALESCE(crew_id,'')
		FROM user_models
		WHERE workspace_id = ? AND user_id = ?
		ORDER BY updated_at DESC
	`, wsID, targetID)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		h.logger.Warn("gdpr export: user_models query failed",
			"action_id", actionID, "err", err)
	} else {
		for umRows.Next() {
			var (
				e      exportUserModel
				crewID string
			)
			if scanErr := umRows.Scan(&e.ID, &e.UserSlug, &e.Path, &e.Bytes,
				&e.CreatedAt, &e.UpdatedAt, &crewID); scanErr != nil {
				h.logger.Error("gdpr export: user_models scan failed",
					"action_id", actionID, "err", scanErr)
				if firstErr == nil {
					firstErr = scanErr
				}
				continue
			}
			if h.outputBasePath != "" {
				body, readErr := memory.LoadUserModelBySlug(
					userModelPathsFor(h.outputBasePath, crewID), e.UserSlug)
				if readErr != nil {
					// An access request that silently omits a body it
					// could not read is an access request that lies.
					h.logger.Error("gdpr export: user model body read failed",
						"action_id", actionID, "err", readErr)
					if firstErr == nil {
						firstErr = readErr
					}
				}
				e.Content = body
			}
			bundle.UserModels = append(bundle.UserModels, e)
		}
		if iterErr := umRows.Err(); iterErr != nil {
			h.logger.Warn("gdpr export: user_models iteration error",
				"action_id", actionID, "err", iterErr)
			if firstErr == nil {
				firstErr = iterErr
			}
		}
		_ = umRows.Close()
		scope.UserModels = len(bundle.UserModels)
	}

	h.finalizeGDPRAction(r.Context(), actionID, scope, firstErr)

	if firstErr != nil {
		// ANY query failure → 500. Auditor catch: previously we
		// returned 200 + the partial bundle if at least one table
		// had data, on the theory "give the operator something."
		// For a GDPR Article 15 access request that's the worst
		// failure mode — the operator hands the data subject an
		// incomplete export that LOOKS authoritative, and the
		// missing rows from a failed table query stay missing
		// until the subject notices. Better to return 500 and
		// have the operator retry / investigate than to silently
		// ship an incomplete answer to a regulatory request.
		// The action_id is still recorded in gdpr_actions with
		// status='failed' via finalizeGDPRAction above, so the
		// audit trail of the attempt is preserved.
		replyError(w, http.StatusInternalServerError, "export failed: "+firstErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}
