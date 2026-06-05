package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-E F6 — GDPR primitives for per-user peer cards.
//
// Three user-facing routes — every authenticated user can act on
// their own data without being a workspace admin:
//
//   GET    /api/v1/users/me/peer-consent     — read opt-out state
//   PUT    /api/v1/users/me/peer-consent     — flip opt-out (with auto-purge)
//   GET    /api/v1/users/me/peer-cards       — list ALL cards anywhere about me
//   DELETE /api/v1/users/me/peer-cards       — delete ALL cards anywhere about me
//
// The "me" path component (instead of {userId}) is deliberate:
//   - Only the requesting user can act on their own data via these
//     endpoints. Cross-user GDPR action by admins goes through
//     /api/v1/admin/users/{id}/data (Phase 2, scoped to a future
//     compliance UI).
//   - It removes the slug derivation gymnastics from the URL — the
//     server reads the caller's user_id from the auth context.
//
// Opt-out is a HARD STOP: setting opted_out=true triggers immediate
// purge of every existing card across every agent in the current
// workspace, plus a 'opt_out' audit row + per-deleted-card 'delete'
// audit rows so the timeline is complete. We don't wait for the
// next routine sweep — operator promised the user "opt-out means
// gone" and the user expects "immediately" not "tomorrow".

type UserPeerPrivacyHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	outputBasePath string
}

func NewUserPeerPrivacyHandler(db *sql.DB, logger *slog.Logger, outputBasePath string) *UserPeerPrivacyHandler {
	return &UserPeerPrivacyHandler{db: db, logger: logger, outputBasePath: outputBasePath}
}

func (h *UserPeerPrivacyHandler) requireUser(w http.ResponseWriter, r *http.Request) (userID, wsID string, ok bool) {
	u := UserFromContext(r.Context())
	if u == nil || u.ID == "" {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return "", "", false
	}
	wsID = WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return "", "", false
	}
	return u.ID, wsID, true
}

// GET /api/v1/users/me/peer-consent
func (h *UserPeerPrivacyHandler) GetConsent(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var (
		opted   int
		optedAt sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT opted_out, opted_out_at FROM user_peer_consent
		WHERE user_id = ? AND workspace_id = ?
	`, userID, wsID).Scan(&opted, &optedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logger.Error("peer consent read failed", "user_id", userID, "workspace_id", wsID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":      userID,
		"workspace_id": wsID,
		"opted_out":    opted == 1,
		"opted_out_at": optedAt.String,
	})
}

// PutConsent flips opt-out. When opted_out=true, every existing
// peer card for the user in this workspace is purged on the same
// request (disk + index + audit) — the user is told "opt-out is
// effective immediately", we honour that literally.
//
// PUT /api/v1/users/me/peer-consent  Body: {"opted_out": true|false}
func (h *UserPeerPrivacyHandler) PutConsent(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body struct {
		OptedOut bool `json:"opted_out"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	opt := 0
	if body.OptedOut {
		opt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO user_peer_consent (user_id, workspace_id, opted_out, opted_out_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, workspace_id) DO UPDATE SET
		    opted_out    = excluded.opted_out,
		    opted_out_at = CASE WHEN excluded.opted_out = 1 THEN excluded.opted_out_at ELSE NULL END,
		    updated_at   = excluded.updated_at
	`, userID, wsID, opt, now, now); err != nil {
		h.logger.Error("peer consent write failed", "user_id", userID, "workspace_id", wsID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// Audit the flip itself.
	action := "opt_in"
	if body.OptedOut {
		action = "opt_out"
	}
	insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
		WorkspaceID:  wsID,
		ActorUserID:  userID,
		ActorKind:    "user",
		Action:       action,
		TargetUserID: userID,
	})

	purged := 0
	if body.OptedOut {
		// Walk every card for this user in this workspace and
		// purge DB rows + audit unconditionally; on-disk delete is
		// best-effort and only runs when outputBasePath is set.
		// Gating the DB purge on storage config would leave stale
		// peer_cards rows after opt-out — that's a GDPR bug.
		// Bounded query — peer_cards is indexed on (user_id,
		// workspace_id) so this is O(N) in the user's card count.
		purged = h.purgeUserCards(r, userID, wsID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":      userID,
		"workspace_id": wsID,
		"opted_out":    body.OptedOut,
		"purged":       purged,
	})
}

// purgeUserCards iterates every peer_cards row for (user, workspace),
// deletes the on-disk file via the per-agent path, removes the DB
// row, and emits a delete audit. Returns the count purged for the
// response payload. Each cycle is best-effort: a single failed
// delete (file gone out of band, agent row missing) logs + advances
// rather than blocking the entire purge.
func (h *UserPeerPrivacyHandler) purgeUserCards(r *http.Request, userID, wsID string) int {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT pc.id, pc.agent_id, pc.user_slug, a.slug, a.crew_id
		FROM peer_cards pc
		JOIN agents a ON a.id = pc.agent_id
		WHERE pc.user_id = ? AND pc.workspace_id = ?
	`, userID, wsID)
	if err != nil {
		h.logger.Warn("opt-out purge query failed", "user_id", userID, "err", err)
		return 0
	}
	defer rows.Close()
	type todo struct {
		cardID, agentID, slug, agentSlug, crewID string
	}
	var work []todo
	for rows.Next() {
		var t todo
		var crewID sql.NullString
		if err := rows.Scan(&t.cardID, &t.agentID, &t.slug, &t.agentSlug, &crewID); err != nil {
			continue
		}
		t.crewID = crewID.String
		work = append(work, t)
	}
	_ = rows.Close()
	purged := 0
	for _, t := range work {
		// On-disk delete is best-effort: only attempt when a base
		// path is configured AND the agent actually has a crew_id
		// (solo agents don't carry peer cards). DB delete + audit
		// happen unconditionally so opt-out / SAR-delete are
		// honoured even when the deployment has no storage path
		// configured (e.g. fresh install before first start).
		if h.outputBasePath != "" && t.crewID != "" {
			paths := memory.PeerPaths{
				AgentDir: filepath.Join(h.outputBasePath, "crews", t.crewID, "agents", t.agentSlug, ".memory"),
			}
			if err := memory.DeletePeerCardBySlug(paths, t.slug); err != nil {
				h.logger.Warn("opt-out file delete failed",
					"agent_id", t.agentID, "user_id", userID, "err", err)
				// fall through — DB row still gets removed so SAR
				// "show me what you have" returns nothing.
			}
		}
		if _, err := h.db.ExecContext(r.Context(), `
			DELETE FROM peer_cards WHERE id = ?
		`, t.cardID); err != nil {
			h.logger.Warn("opt-out row delete failed",
				"card_id", t.cardID, "err", err)
			continue
		}
		insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
			WorkspaceID:  wsID,
			ActorUserID:  userID,
			ActorKind:    "user",
			Action:       "delete",
			TargetUserID: userID,
			AgentID:      t.agentID,
			Metadata:     `{"reason":"opt_out_immediate_purge"}`,
		})
		purged++
	}
	return purged
}

// GetMyCards lists every peer card mentioning the requesting user
// across every agent in the current workspace. Includes content
// since the user has a right to see what was stored.
//
// GET /api/v1/users/me/peer-cards
func (h *UserPeerPrivacyHandler) GetMyCards(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT pc.id, pc.agent_id, a.slug, a.crew_id, pc.user_slug, pc.bytes, pc.created_at, pc.updated_at
		FROM peer_cards pc
		JOIN agents a ON a.id = pc.agent_id
		WHERE pc.user_id = ? AND pc.workspace_id = ?
		ORDER BY pc.updated_at DESC
	`, userID, wsID)
	if err != nil {
		h.logger.Error("peer cards list failed", "user_id", userID, "workspace_id", wsID, "error", err)
		replyError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer rows.Close()
	type entry struct {
		ID        string `json:"id"`
		AgentID   string `json:"agent_id"`
		AgentSlug string `json:"agent_slug"`
		UserSlug  string `json:"user_slug"`
		Bytes     int    `json:"bytes"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		Content   string `json:"content,omitempty"`
	}
	out := []entry{}
	for rows.Next() {
		var (
			e      entry
			crewID sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.AgentID, &e.AgentSlug, &crewID, &e.UserSlug, &e.Bytes, &e.CreatedAt, &e.UpdatedAt); err != nil {
			continue
		}
		if h.outputBasePath != "" {
			paths := memory.PeerPaths{
				AgentDir: filepath.Join(h.outputBasePath, "crews", crewID.String, "agents", e.AgentSlug, ".memory"),
			}
			body, _ := memory.LoadPeerCardBySlug(paths, e.UserSlug)
			e.Content = body
		}
		// Audit the read keyed on the actor (the user reading
		// about themselves IS the data subject). One row per
		// card so a future "show me everything you logged"
		// query for this user lists every card the user has
		// looked at.
		insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
			WorkspaceID:  wsID,
			ActorUserID:  userID,
			ActorKind:    "user",
			Action:       "read",
			TargetUserID: userID,
			AgentID:      e.AgentID,
		})
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userID,
		"peers":   out,
	})
}

// DeleteMyCards drops every card about the requesting user across
// every agent in the workspace. Same purge path the opt-out flow
// uses, minus the consent table mutation — a user can delete
// without opting out (e.g. "delete the current stuff, but you may
// re-extract tomorrow").
//
// DELETE /api/v1/users/me/peer-cards
func (h *UserPeerPrivacyHandler) DeleteMyCards(w http.ResponseWriter, r *http.Request) {
	userID, wsID, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	purged := h.purgeUserCards(r, userID, wsID)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userID,
		"purged":  purged,
	})
}
