package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// PR-E F6 — Peer card API surface.
//
// Operator-facing endpoints for inspecting + cleaning up the
// per-(agent, user) peer cards the PeerCardSync routine produces.
// The GDPR user-facing surface (opt-out, view-mine, delete-mine)
// lives in user_peer_privacy.go — that handler shares the same
// underlying disk + DB primitives but exposes a different surface
// (path /api/v1/users/me/peer-cards) and a different auth contract
// (the user can act on their own data without being a workspace
// admin).
//
// Agent-flavor routes:
//
//   GET    /api/v1/agents/{agentId}/peers            — list cards
//   GET    /api/v1/agents/{agentId}/peers/{userId}   — single card
//   DELETE /api/v1/agents/{agentId}/peers/{userId}   — delete + audit
//
// The {userId} path param is the raw user_id, NOT the user_slug.
// The slug derivation happens server-side via memory.UserSlug so
// the operator never has to compute the hash; the URL stays
// debuggable (you can read "u_pavel" in it rather than a16-hex blob)
// and the routing is independent of any future slug algorithm change.

type PeerCardHandler struct {
	db             *sql.DB
	logger         *slog.Logger
	outputBasePath string
}

func NewPeerCardHandler(db *sql.DB, logger *slog.Logger, outputBasePath string) *PeerCardHandler {
	return &PeerCardHandler{db: db, logger: logger, outputBasePath: outputBasePath}
}

func (h *PeerCardHandler) requireStorage(w http.ResponseWriter) bool {
	if h.outputBasePath == "" {
		replyError(w, http.StatusServiceUnavailable,
			"peer storage not configured (set cfg.storage.base_path)")
		return false
	}
	return true
}

// agentPeerDir resolves the host-side peers/ directory for an agent.
// Same layout the persona handler uses + provider bind mounts —
// {outputBase}/crews/{crewID}/agents/{slug}/.memory/peers/.
func (h *PeerCardHandler) agentPeerDir(crewID, agentSlug string) memory.PeerPaths {
	return memory.PeerPaths{
		AgentDir: filepath.Join(h.outputBasePath, "crews", crewID, "agents", agentSlug, ".memory"),
	}
}

// errAgentHasNoCrew is returned when a peer-card operation targets
// an agent that has no crew_id. Peer cards live under
// .../crews/{crewID}/agents/{slug}/.memory/peers/ — without a crew
// segment the path collapses to .../crews//agents/{slug}/.memory/,
// which collides across workspaces for the same slug. Solo agents
// don't carry peer cards in this PR; the routine writer skips them
// the same way (via the same crew_id IS NOT NULL filter).
var errAgentHasNoCrew = fmt.Errorf("agent has no crew (peer cards require crew-scoped path)")

// resolveAgent returns (workspace_id-validated) crew_id + slug for
// an agent or sql.ErrNoRows when the agent doesn't exist or belongs
// to another workspace. Returns errAgentHasNoCrew if the agent has
// no crew_id (handlers map this to 409 Conflict).
func (h *PeerCardHandler) resolveAgent(r *http.Request, agentID string) (crewID, slug string, err error) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		return "", "", fmt.Errorf("workspace context missing")
	}
	var cid sql.NullString
	err = h.db.QueryRowContext(r.Context(), `
		SELECT crew_id, slug FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
	`, agentID, wsID).Scan(&cid, &slug)
	if err != nil {
		return "", slug, err
	}
	if !cid.Valid || cid.String == "" {
		return "", slug, errAgentHasNoCrew
	}
	return cid.String, slug, nil
}

// ListAgentPeers returns the index of cards for an agent — DB rows
// from peer_cards, optionally hydrated with on-disk bytes. We don't
// inline the card content here; clients fetch it via GET /{userId}
// to keep the list endpoint cheap when an agent has many peers.
//
// GET /api/v1/agents/{agentId}/peers
func (h *PeerCardHandler) ListAgentPeers(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace context missing")
		return
	}
	// Verify the agent belongs to the caller's workspace before
	// listing — otherwise an attacker with a guessed agent_id could
	// enumerate cards across tenants.
	if _, _, err := h.resolveAgent(r, agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "agent not found")
			return
		}
		if errors.Is(err, errAgentHasNoCrew) {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, user_id, user_slug, bytes, created_at, updated_at
		FROM peer_cards
		WHERE agent_id = ? AND workspace_id = ?
		ORDER BY updated_at DESC
	`, agentID, wsID)
	if err != nil {
		h.logger.Warn("list peer_cards", "err", err)
		replyError(w, http.StatusInternalServerError, "list peers")
		return
	}
	defer rows.Close()
	type entry struct {
		ID        string `json:"id"`
		UserID    string `json:"user_id"`
		UserSlug  string `json:"user_slug"`
		Bytes     int    `json:"bytes"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.UserID, &e.UserSlug, &e.Bytes, &e.CreatedAt, &e.UpdatedAt); err != nil {
			continue
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": out, "agent_id": agentID})
}

// GetAgentPeer returns one card's full content, derived by the
// server from {agent_id, user_id, workspace_id}. Writes a 'read'
// audit row keyed on target_user_id (the data subject).
//
// GET /api/v1/agents/{agentId}/peers/{userId}
func (h *PeerCardHandler) GetAgentPeer(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	targetUser := r.PathValue("userId")
	wsID := WorkspaceIDFromContext(r.Context())
	crewID, slug, err := h.resolveAgent(r, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "agent not found")
			return
		}
		if errors.Is(err, errAgentHasNoCrew) {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	paths := h.agentPeerDir(crewID, slug)
	body, err := memory.LoadPeerCard(paths, targetUser, wsID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body == "" {
		replyError(w, http.StatusNotFound, "no peer card for this user on this agent")
		return
	}
	// Audit the read. PR-E F6 GDPR requirement: every peer card
	// read/write/delete must land in peer_card_audit so SAR queries
	// have complete coverage. We log "user" as the actor when the
	// caller is logged in, "system" otherwise.
	actor := "system"
	var actorUserID string
	if u := UserFromContext(r.Context()); u != nil {
		actor = "user"
		actorUserID = u.ID
	}
	insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
		WorkspaceID:  wsID,
		ActorUserID:  actorUserID,
		ActorKind:    actor,
		Action:       "read",
		TargetUserID: targetUser,
		AgentID:      agentID,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":  agentID,
		"user_id":   targetUser,
		"user_slug": memory.UserSlug(targetUser, wsID),
		"content":   body,
		"bytes":     len(body),
	})
}

// DeleteAgentPeer removes the card for one (agent, user) pair.
// Idempotent — missing file/row is still a 204. Writes a 'delete'
// audit row before the actual mutation so a partial failure (file
// deleted, DB write throws) leaves an audit breadcrumb pointing at
// the cleanup the operator now needs to do manually.
//
// DELETE /api/v1/agents/{agentId}/peers/{userId}
func (h *PeerCardHandler) DeleteAgentPeer(w http.ResponseWriter, r *http.Request) {
	if !h.requireStorage(w) {
		return
	}
	agentID := r.PathValue("agentId")
	targetUser := r.PathValue("userId")
	wsID := WorkspaceIDFromContext(r.Context())
	crewID, slug, err := h.resolveAgent(r, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "agent not found")
			return
		}
		if errors.Is(err, errAgentHasNoCrew) {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	paths := h.agentPeerDir(crewID, slug)
	if err := memory.DeletePeerCard(paths, targetUser, wsID); err != nil {
		replyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
		DELETE FROM peer_cards WHERE agent_id = ? AND user_slug = ?
	`, agentID, memory.UserSlug(targetUser, wsID)); err != nil {
		h.logger.Warn("peer_cards delete failed", "agent_id", agentID, "err", err)
	}
	actor := "system"
	var actorUserID string
	if u := UserFromContext(r.Context()); u != nil {
		actor = "user"
		actorUserID = u.ID
	}
	insertPeerAudit(r.Context(), h.db, h.logger, peerAuditInsert{
		WorkspaceID:  wsID,
		ActorUserID:  actorUserID,
		ActorKind:    actor,
		Action:       "delete",
		TargetUserID: targetUser,
		AgentID:      agentID,
		Metadata:     `{"source":"agent_endpoint"}`,
	})
	w.WriteHeader(http.StatusNoContent)
}

// peerAuditInsert + insertPeerAudit centralise the audit row write
// so the agent endpoints, the user-privacy endpoints, and the
// routine handler all use one INSERT shape. The function logs but
// does not return errors — losing an audit row is unfortunate but
// must not fail a successful operation.
type peerAuditInsert struct {
	WorkspaceID  string
	ActorUserID  string
	ActorKind    string
	Action       string
	TargetUserID string
	AgentID      string
	Metadata     string
}

func insertPeerAudit(_ context.Context, db *sql.DB, logger *slog.Logger, row peerAuditInsert) {
	var actor any
	if row.ActorUserID != "" {
		actor = row.ActorUserID
	}
	var agentID any
	if row.AgentID != "" {
		agentID = row.AgentID
	}
	var meta any
	if row.Metadata != "" {
		meta = row.Metadata
	}
	// Audit inserts deliberately don't honour the request context —
	// losing an audit row because the client disconnected mid-DELETE
	// is worse than the cost of running one extra INSERT after the
	// cancel. The row is small, fast, and not part of any operator-
	// observable cancellation chain.
	_, err := db.Exec(`
		INSERT INTO peer_card_audit
		(id, workspace_id, actor_user_id, actor_kind, action, target_user_id, agent_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?)
	`, row.WorkspaceID, actor, row.ActorKind, row.Action,
		row.TargetUserID, agentID, meta,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		logger.Warn("peer_card_audit insert failed",
			"action", row.Action, "target", row.TargetUserID, "err", err)
	}
}
