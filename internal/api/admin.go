package api

import (
	"database/sql"
	"log/slog"
	"net/http"
)

type AdminHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewAdminHandler(db *sql.DB, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{db: db, logger: logger}
}

func (h *AdminHandler) Stats(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if role != "OWNER" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: OWNER only"})
		return
	}

	type stats struct {
		Workspaces int `json:"workspaces"`
		Users      int `json:"users"`
		Agents     int `json:"agents"`
		Running    int `json:"running"`
	}

	var s stats
	queries := []struct {
		sql  string
		dest *int
	}{
		{"SELECT COUNT(*) FROM workspaces WHERE deleted_at IS NULL", &s.Workspaces},
		{"SELECT COUNT(*) FROM users", &s.Users},
		{"SELECT COUNT(*) FROM agents WHERE deleted_at IS NULL", &s.Agents},
		{"SELECT COUNT(*) FROM agent_runs WHERE status = 'RUNNING'", &s.Running},
	}
	for _, q := range queries {
		if err := h.db.QueryRowContext(r.Context(), q.sql).Scan(q.dest); err != nil {
			h.logger.Error("stats query", "sql", q.sql, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	writeJSON(w, http.StatusOK, s)
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if role != "OWNER" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: OWNER only"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT u.id, u.email, u.full_name, u.avatar_url, u.created_at,
			wm.workspace_id, w.name AS workspace_name, w.slug AS workspace_slug, wm.role
		FROM users u
		LEFT JOIN workspace_members wm ON wm.user_id = u.id
		LEFT JOIN workspaces w ON w.id = wm.workspace_id
		ORDER BY u.created_at DESC
	`)
	if err != nil {
		h.logger.Error("list users", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type userRow struct {
		ID        string  `json:"id"`
		Email     string  `json:"email"`
		FullName  *string `json:"full_name"`
		AvatarURL *string `json:"avatar_url"`
		CreatedAt string  `json:"created_at"`
		Workspace *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"workspace"`
		Role *string `json:"role"`
	}

	var result []userRow
	for rows.Next() {
		var u userRow
		var wsID, wsName, wsSlug, role sql.NullString
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName, &u.AvatarURL, &u.CreatedAt,
			&wsID, &wsName, &wsSlug, &role); err != nil {
			h.logger.Error("scan user", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if wsID.Valid {
			u.Workspace = &struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			}{ID: wsID.String, Name: wsName.String, Slug: wsSlug.String}
		}
		if role.Valid {
			u.Role = &role.String
		}
		result = append(result, u)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (users)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []userRow{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if role != "OWNER" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: OWNER only"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.plan, w.created_at, w.updated_at,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count
		FROM workspaces w
		WHERE w.deleted_at IS NULL
		ORDER BY w.created_at DESC
	`)
	if err != nil {
		h.logger.Error("list workspaces (admin)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type wsRow struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Slug        string  `json:"slug"`
		Plan        *string `json:"plan"`
		CreatedAt   string  `json:"created_at"`
		UpdatedAt   string  `json:"updated_at"`
		MemberCount int     `json:"_count_members"`
		AgentCount  int     `json:"_count_agents"`
		CrewCount   int     `json:"_count_crews"`
	}

	var result []wsRow
	for rows.Next() {
		var ws wsRow
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.Plan,
			&ws.CreatedAt, &ws.UpdatedAt,
			&ws.MemberCount, &ws.AgentCount, &ws.CrewCount); err != nil {
			h.logger.Error("scan workspace (admin)", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, ws)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (workspaces)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []wsRow{}
	}
	writeJSON(w, http.StatusOK, result)
}
