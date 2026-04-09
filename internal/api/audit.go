package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
)

// AuditHandler provides endpoints for querying the workspace audit log.
type AuditHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewAuditHandler creates an AuditHandler with the given database and logger.
func NewAuditHandler(db *sql.DB, logger *slog.Logger) *AuditHandler {
	return &AuditHandler{db: db, logger: logger}
}

type auditResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      *string `json:"user_id"`
	Action      string  `json:"action"`
	EntityType  string  `json:"entity_type"`
	EntityID    *string `json:"entity_id"`
	Metadata    *string `json:"metadata"`
	IPAddress   *string `json:"ip_address"`
	UserAgent   *string `json:"user_agent"`
	CreatedAt   string  `json:"created_at"`
	UserEmail   *string `json:"user_email,omitempty"`
	UserName    *string `json:"user_name,omitempty"`
}

type auditListResponse struct {
	Data       []auditResponse `json:"data"`
	Pagination pagination      `json:"pagination"`
}

// List returns a paginated list of audit log entries for the workspace.
// GET /api/v1/audit — supports filtering by action, entity_type, entity_id, user_id, and date range.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset := (page - 1) * limit

	action := r.URL.Query().Get("action")
	entityType := r.URL.Query().Get("entity_type")
	entityID := r.URL.Query().Get("entity_id")
	userID := r.URL.Query().Get("user_id")
	dateFrom := r.URL.Query().Get("date_from")
	dateTo := r.URL.Query().Get("date_to")

	query := `
		SELECT a.id, a.workspace_id, a.user_id, a.action, a.entity_type, a.entity_id,
			a.metadata, a.ip_address, a.user_agent, a.created_at,
			u.email, u.full_name
		FROM audit_logs a
		LEFT JOIN users u ON u.id = a.user_id
		WHERE a.workspace_id = ?`
	countQuery := `SELECT COUNT(*) FROM audit_logs WHERE workspace_id = ?`
	args := []interface{}{workspaceID}
	countArgs := []interface{}{workspaceID}

	if action != "" {
		query += " AND a.action = ?"
		countQuery += " AND action = ?"
		args = append(args, action)
		countArgs = append(countArgs, action)
	}
	if entityType != "" {
		query += " AND a.entity_type = ?"
		countQuery += " AND entity_type = ?"
		args = append(args, entityType)
		countArgs = append(countArgs, entityType)
	}
	if entityID != "" {
		query += " AND a.entity_id = ?"
		countQuery += " AND entity_id = ?"
		args = append(args, entityID)
		countArgs = append(countArgs, entityID)
	}
	if userID != "" {
		query += " AND a.user_id = ?"
		countQuery += " AND user_id = ?"
		args = append(args, userID)
		countArgs = append(countArgs, userID)
	}
	if dateFrom != "" {
		query += " AND a.created_at >= ?"
		countQuery += " AND created_at >= ?"
		args = append(args, dateFrom)
		countArgs = append(countArgs, dateFrom)
	}
	if dateTo != "" {
		query += " AND a.created_at <= ?"
		countQuery += " AND created_at <= ?"
		args = append(args, dateTo)
		countArgs = append(countArgs, dateTo)
	}

	query += fmt.Sprintf(" ORDER BY a.created_at DESC LIMIT %d OFFSET %d", limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list audit logs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []auditResponse
	for rows.Next() {
		var a auditResponse
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.UserID, &a.Action,
			&a.EntityType, &a.EntityID, &a.Metadata, &a.IPAddress,
			&a.UserAgent, &a.CreatedAt, &a.UserEmail, &a.UserName); err != nil {
			h.logger.Error("scan audit log", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (audit logs)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []auditResponse{}
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(), countQuery, countArgs...).Scan(&total); err != nil {
		h.logger.Error("count audit logs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, auditListResponse{
		Data: result,
		Pagination: pagination{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: int(math.Ceil(float64(total) / float64(limit))),
		},
	})
}
