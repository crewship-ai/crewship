package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// NotificationHandler implements endpoints for user notifications.
type NotificationHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *NotificationHandler {
	return &NotificationHandler{db: db, hub: hub, logger: logger}
}

// ── Response type ──────────────────────────────────────────────────────────

type notificationResponse struct {
	ID          string  `json:"id"`
	ActorType   string  `json:"actor_type"`
	ActorID     string  `json:"actor_id"`
	ActorName   *string `json:"actor_name,omitempty"`
	Action      string  `json:"action"`
	EntityType  string  `json:"entity_type"`
	EntityID    *string `json:"entity_id"`
	EntityTitle *string `json:"entity_title"`
	ReadAt      *string `json:"read_at"`
	CreatedAt   string  `json:"created_at"`
}

// ── Helper — CreateNotification ───────────────────────────────────────────

// CreateNotification inserts a notification row and broadcasts it via
// WebSocket. It is exported so other handlers can create notifications.
func CreateNotification(db *sql.DB, hub *ws.Hub, wsID, userID, actorType, actorID, action, entityType, entityID, entityTitle string) {
	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := db.Exec(`
		INSERT INTO notifications (id, workspace_id, user_id, actor_type, actor_id,
		    action, entity_type, entity_id, entity_title, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, userID, actorType, actorID, action, entityType, entityID, entityTitle, now)
	if err != nil {
		return // best-effort; caller should not fail because of notification
	}

	broadcastChannelEvent(hub, "user", userID, "notification.created",
		map[string]string{
			"id":           id,
			"action":       action,
			"entity_type":  entityType,
			"entity_id":    entityID,
			"entity_title": entityTitle,
		})
}

// ── 1. List — GET /api/v1/notifications ───────────────────────────────────

// List returns paginated notifications for the authenticated user.
// GET /api/v1/notifications
func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Pagination
	limit, offset := parsePagination(r, 50, 100)

	query := `
		SELECT n.id, n.actor_type, n.actor_id,
		       COALESCE(u.full_name, ag.name),
		       n.action, n.entity_type, n.entity_id, n.entity_title,
		       n.read_at, n.created_at
		FROM notifications n
		LEFT JOIN users u ON n.actor_type = 'user' AND u.id = n.actor_id
		LEFT JOIN agents ag ON n.actor_type = 'agent' AND ag.id = n.actor_id
		WHERE n.user_id = ?`
	args := []any{user.ID}

	// Filter by read status
	if readParam := r.URL.Query().Get("read"); readParam != "" {
		switch readParam {
		case "true":
			query += " AND n.read_at IS NOT NULL"
		case "false":
			query += " AND n.read_at IS NULL"
		}
	}

	query += " ORDER BY n.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		internalError(w, r, h.logger, "list notifications", err)
		return
	}
	defer rows.Close()

	var result []notificationResponse
	for rows.Next() {
		var n notificationResponse
		if err := rows.Scan(
			&n.ID, &n.ActorType, &n.ActorID, &n.ActorName,
			&n.Action, &n.EntityType, &n.EntityID, &n.EntityTitle,
			&n.ReadAt, &n.CreatedAt,
		); err != nil {
			internalError(w, r, h.logger, "scan notification", err)
			return
		}
		result = append(result, n)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (notifications)", err)
		return
	}

	if result == nil {
		result = []notificationResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. MarkRead — POST /api/v1/notifications/{id}/read ───────────────────

// MarkRead marks a single notification as read.
// POST /api/v1/notifications/{notificationId}/read
func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	notifID := r.PathValue("notificationId")
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := h.db.ExecContext(r.Context(),
		`UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at IS NULL`,
		now, notifID, user.ID)
	if err != nil {
		internalError(w, r, h.logger, "mark notification read", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "mark read rows affected", err)
		return
	}
	if affected == 0 {
		// Either not found or already read — verify existence
		var exists int
		err := h.db.QueryRowContext(r.Context(),
			`SELECT 1 FROM notifications WHERE id = ? AND user_id = ?`,
			notifID, user.ID).Scan(&exists)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeProblem(w, r, http.StatusNotFound, "Notification not found")
				return
			}
			internalError(w, r, h.logger, "check notification exists", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── 3. MarkAllRead — POST /api/v1/notifications/read-all ─────────────────

// MarkAllRead marks all notifications as read for the authenticated user.
// POST /api/v1/notifications/read-all
func (h *NotificationHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	res, err := h.db.ExecContext(r.Context(),
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`,
		now, user.ID)
	if err != nil {
		internalError(w, r, h.logger, "mark all notifications read", err)
		return
	}
	affected, _ := res.RowsAffected()

	writeJSON(w, http.StatusOK, map[string]int64{"updated": affected})
}

// ── 4. Delete — DELETE /api/v1/notifications/{id} ─────────────────────────

// Delete removes a notification for the authenticated user.
// DELETE /api/v1/notifications/{notificationId}
func (h *NotificationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	notifID := r.PathValue("notificationId")

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM notifications WHERE id = ? AND user_id = ?`,
		notifID, user.ID)
	if err != nil {
		internalError(w, r, h.logger, "delete notification", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "delete notification rows affected", err)
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Notification not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── 5. Count — GET /api/v1/notifications/count ────────────────────────────

// Count returns the number of unread notifications for the authenticated user.
// GET /api/v1/notifications/count
func (h *NotificationHandler) Count(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var unread int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`,
		user.ID).Scan(&unread)
	if err != nil {
		internalError(w, r, h.logger, "count notifications", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]int{"unread": unread})
}
