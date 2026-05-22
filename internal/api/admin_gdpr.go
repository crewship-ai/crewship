package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

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
}

// NewAdminGDPRHandler constructs a handler. outputBasePath should be
// the same root the persona / peer card handlers receive so the
// per-agent .memory paths resolve consistently across endpoints.
func NewAdminGDPRHandler(db *sql.DB, logger *slog.Logger, outputBasePath string) *AdminGDPRHandler {
	return &AdminGDPRHandler{db: db, logger: logger, outputBasePath: outputBasePath}
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
	PeerCardsOnDisk int `json:"peer_cards_on_disk,omitempty"`
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

// DeleteUserData is the Art. 17 cascade. POST-condition:
//
//   - peer_cards rows referencing the user are gone (DB + best-
//     effort on-disk).
//   - memory_versions rows tagged data_subject_id = user are gone.
//   - inbox_items rows tagged data_subject_id = user are gone.
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
			h.logger.Warn("gdpr delete: peer_cards iteration error",
				"action_id", actionID, "err", iterErr)
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

	rowsDeleted := scope.PeerCards + scope.MemoryVersions + scope.InboxItems
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
	DataSubjectID string                `json:"data_subject_id"`
	WorkspaceID   string                `json:"workspace_id"`
	ExportedAt    string                `json:"exported_at"`
	ActionID      string                `json:"action_id"`
	PeerCards     []exportPeerCard      `json:"peer_cards"`
	MemoryVersion []exportMemoryVersion `json:"memory_versions"`
	InboxItems    []exportInboxItem     `json:"inbox_items"`
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
		DataSubjectID: targetID,
		WorkspaceID:   wsID,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		ActionID:      actionID,
		PeerCards:     []exportPeerCard{},
		MemoryVersion: []exportMemoryVersion{},
		InboxItems:    []exportInboxItem{},
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
