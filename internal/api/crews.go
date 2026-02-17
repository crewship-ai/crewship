package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

type CrewHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCrewHandler(db *sql.DB, logger *slog.Logger) *CrewHandler {
	return &CrewHandler{db: db, logger: logger}
}

type crewResponse struct {
	ID                string  `json:"id"`
	WorkspaceID       string  `json:"workspace_id"`
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	Description       *string `json:"description"`
	Color             *string `json:"color"`
	Icon              *string `json:"icon"`
	ContainerMemoryMB int     `json:"container_memory_mb"`
	ContainerCPUs     float64 `json:"container_cpus"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	AgentCount        int     `json:"_count_agents"`
	MemberCount       int     `json:"_count_members"`
}

func (h *CrewHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id is required"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon,
			c.container_memory_mb, c.container_cpus, c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL
		ORDER BY c.created_at DESC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list crews", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []crewResponse
	for rows.Next() {
		var c crewResponse
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
			&c.Color, &c.Icon, &c.ContainerMemoryMB, &c.ContainerCPUs,
			&c.CreatedAt, &c.UpdatedAt, &c.AgentCount, &c.MemberCount); err != nil {
			h.logger.Error("scan crew", "error", err)
			continue
		}
		result = append(result, c)
	}

	if result == nil {
		result = []crewResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type createCrewRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
	Icon        *string `json:"icon"`
}

func (h *CrewHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req createCrewRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 2-100 characters"})
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE workspace_id = ? AND slug = ?", workspaceID, req.Slug).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Crew slug already taken in this workspace"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	crewID := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO crews (id, workspace_id, name, slug, description, color, icon, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, workspaceID, req.Name, req.Slug, req.Description, req.Color, req.Icon, now, now)
	if err != nil {
		h.logger.Error("insert crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, crewResponse{
		ID:                crewID,
		WorkspaceID:       workspaceID,
		Name:              req.Name,
		Slug:              req.Slug,
		Description:       req.Description,
		Color:             req.Color,
		Icon:              req.Icon,
		ContainerMemoryMB: 4096,
		ContainerCPUs:     2.0,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
}
