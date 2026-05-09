package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// InboxHandler serves the unified human-in-the-loop inbox. The list +
// state-transition endpoints read/write inbox_items directly; the
// rows themselves are inserted by the source-of-truth handlers
// (waitpoint create, escalation create, run-failure terminal) via the
// helpers in inbox_writer.go. This handler is strictly the read +
// state-flip surface the UI consumes.
type InboxHandler struct {
	db     *sql.DB
	logger *slog.Logger
	hub    *ws.Hub
}

func NewInboxHandler(db *sql.DB, logger *slog.Logger, hub *ws.Hub) *InboxHandler {
	return &InboxHandler{db: db, logger: logger, hub: hub}
}

// inboxItemResponse is the wire shape for a single inbox row. We
// inline payload as a parsed map so the UI doesn't need to JSON.parse
// it client-side, and omit empty optional fields so consumers can
// switch on `routine_id != null`-style checks without first checking
// undefined.
type inboxItemResponse struct {
	ID               string                 `json:"id"`
	WorkspaceID      string                 `json:"workspace_id"`
	Kind             string                 `json:"kind"`
	SourceID         string                 `json:"source_id"`
	TargetUserID     string                 `json:"target_user_id,omitempty"`
	TargetRole       string                 `json:"target_role,omitempty"`
	Title            string                 `json:"title"`
	BodyMD           string                 `json:"body_md,omitempty"`
	SenderType       string                 `json:"sender_type,omitempty"`
	SenderID         string                 `json:"sender_id,omitempty"`
	SenderName       string                 `json:"sender_name,omitempty"`
	State            string                 `json:"state"`
	Priority         string                 `json:"priority"`
	Blocking         bool                   `json:"blocking"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
	ReadAt           string                 `json:"read_at,omitempty"`
	ResolvedAt       string                 `json:"resolved_at,omitempty"`
	ResolvedByUserID string                 `json:"resolved_by_user_id,omitempty"`
	ResolvedAction   string                 `json:"resolved_action,omitempty"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`
}

// inboxListResponse keeps the count + cursor metadata next to the
// rows so the UI can render pagination + the bell badge from one
// fetch.
type inboxListResponse struct {
	Rows        []inboxItemResponse `json:"rows"`
	Count       int                 `json:"count"`
	UnreadCount int                 `json:"unread_count"`
}

// List serves GET /api/v1/inbox. Filter by ?state=unread|read|resolved|all
// (default 'all' to drive Linear-Triage UX where resolved items stay
// visible-but-dimmed). ?kind= narrows by item type. ?limit defaults to
// 100, capped at 500. Sorted by created_at DESC so newest is at the
// top — same convention as Linear / GitHub Notifications.
func (h *InboxHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}

	state := r.URL.Query().Get("state")
	kind := r.URL.Query().Get("kind")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	q := strings.Builder{}
	q.WriteString(`SELECT id, workspace_id, kind, source_id,
		COALESCE(target_user_id, ''), COALESCE(target_role, ''),
		title, COALESCE(body_md, ''),
		COALESCE(sender_type, ''), COALESCE(sender_id, ''), COALESCE(sender_name, ''),
		state, priority, blocking, payload_json,
		COALESCE(read_at, ''), COALESCE(resolved_at, ''),
		COALESCE(resolved_by_user_id, ''), COALESCE(resolved_action, ''),
		created_at, updated_at
	FROM inbox_items WHERE workspace_id = ?`)
	args := []interface{}{workspaceID}

	if state != "" && state != "all" {
		if state != "unread" && state != "read" && state != "resolved" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid state"})
			return
		}
		q.WriteString(" AND state = ?")
		args = append(args, state)
	}
	if kind != "" {
		q.WriteString(" AND kind = ?")
		args = append(args, kind)
	}
	q.WriteString(" ORDER BY created_at DESC LIMIT ?")
	args = append(args, limit)

	rows, err := h.db.QueryContext(r.Context(), q.String(), args...)
	if err != nil {
		h.logger.Error("inbox list", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	defer rows.Close()

	out := make([]inboxItemResponse, 0)
	for rows.Next() {
		var item inboxItemResponse
		var blocking int
		var payloadJSON string
		if err := rows.Scan(
			&item.ID, &item.WorkspaceID, &item.Kind, &item.SourceID,
			&item.TargetUserID, &item.TargetRole,
			&item.Title, &item.BodyMD,
			&item.SenderType, &item.SenderID, &item.SenderName,
			&item.State, &item.Priority, &blocking, &payloadJSON,
			&item.ReadAt, &item.ResolvedAt,
			&item.ResolvedByUserID, &item.ResolvedAction,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			h.logger.Error("inbox scan", "error", err)
			continue
		}
		item.Blocking = blocking != 0
		if payloadJSON != "" {
			_ = json.Unmarshal([]byte(payloadJSON), &item.Payload)
		}
		out = append(out, item)
	}

	// Bell badge fetched in the same response so the UI doesn't need
	// a second round-trip on every poll. Cheap because it's a partial-
	// indexed COUNT(*) on the workspace partition.
	var unreadCount int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND state = 'unread'`,
		workspaceID).Scan(&unreadCount); err != nil {
		h.logger.Warn("inbox unread count", "error", err)
		unreadCount = 0
	}

	writeJSON(w, http.StatusOK, inboxListResponse{
		Rows:        out,
		Count:       len(out),
		UnreadCount: unreadCount,
	})
}

// UnreadCount serves GET /api/v1/inbox/count — the bell-badge endpoint.
// Tiny payload, cheaper than List for the polling worker the top-bar
// bell uses.
func (h *InboxHandler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	var n int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND state = 'unread'`,
		workspaceID).Scan(&n); err != nil {
		h.logger.Warn("inbox unread count", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "count failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread_count": n})
}

// PatchState handles PATCH /api/v1/inbox/{id} to flip an item's state
// between unread/read/resolved. Resolved transitions also accept a
// `resolved_action` discriminator (approved / rejected / retried /
// cancelled) so the audit trail records what the user did, not just
// that they did something.
func (h *InboxHandler) PatchState(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if workspaceID == "" || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}

	var body struct {
		State          string `json:"state"`
		ResolvedAction string `json:"resolved_action,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.State != "unread" && body.State != "read" && body.State != "resolved" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "state must be unread|read|resolved"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "tx failed"})
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Verify the row exists in this workspace before flipping. A
	// cross-workspace id should 404 rather than silently no-op.
	var existing string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id FROM inbox_items WHERE id = ? AND workspace_id = ?`, id, workspaceID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		h.logger.Error("inbox patch lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}

	switch body.State {
	case "read":
		_, err = tx.ExecContext(r.Context(), `
			UPDATE inbox_items
			SET state = 'read',
			    read_at = COALESCE(read_at, ?),
			    read_by_user_id = COALESCE(read_by_user_id, ?),
			    updated_at = ?
			WHERE id = ?`,
			now, user.ID, now, id)
	case "unread":
		_, err = tx.ExecContext(r.Context(), `
			UPDATE inbox_items
			SET state = 'unread',
			    read_at = NULL,
			    read_by_user_id = NULL,
			    resolved_at = NULL,
			    resolved_by_user_id = NULL,
			    resolved_action = NULL,
			    updated_at = ?
			WHERE id = ?`,
			now, id)
	case "resolved":
		_, err = tx.ExecContext(r.Context(), `
			UPDATE inbox_items
			SET state = 'resolved',
			    resolved_at = ?,
			    resolved_by_user_id = ?,
			    resolved_action = ?,
			    updated_at = ?
			WHERE id = ?`,
			now, user.ID, body.ResolvedAction, now, id)
	}
	if err != nil {
		h.logger.Error("inbox patch state", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "commit failed"})
		return
	}

	if h.hub != nil {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{
			"id":    id,
			"state": body.State,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": body.State})
}
