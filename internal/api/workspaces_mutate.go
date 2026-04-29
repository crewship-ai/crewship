package api

// Workspace write paths — Create + Update. Each enforces tenant
// uniqueness and validates the language preference. Extracted from
// workspaces.go for readability.

import (
	"database/sql"
	"net/http"
	"time"
)

type createWorkspaceRequest struct {
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	PreferredLanguage *string `json:"preferred_language"`
}

// Create provisions a new workspace and adds the current user as OWNER.
// POST /api/v1/workspaces

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req createWorkspaceRequest
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
	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		req.PreferredLanguage = &resolved
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM workspaces WHERE slug = ?", req.Slug).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Workspace slug already taken"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check workspace slug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wsID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, preferred_language, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		wsID, req.Name, req.Slug, req.PreferredLanguage, now, now)
	if err != nil {
		h.logger.Error("insert workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, 'OWNER', ?, ?)",
		memberID, wsID, user.ID, now, now)
	if err != nil {
		h.logger.Error("insert workspace member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, workspaceResponse{
		ID:                wsID,
		Name:              req.Name,
		Slug:              req.Slug,
		PreferredLanguage: req.PreferredLanguage,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
}

// Get returns a single workspace by ID with crew, agent, and member counts.
// GET /api/v1/workspaces/{workspaceId}

type updateWorkspaceRequest struct {
	Name              *string `json:"name"`
	Slug              *string `json:"slug"`
	PreferredLanguage *string `json:"preferred_language"`
}

// Update modifies workspace settings such as name, slug, logo, and preferred language.
// PATCH /api/v1/workspaces/{workspaceId}

func (h *WorkspaceHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req updateWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name != nil && (len(*req.Name) < 2 || len(*req.Name) > 100) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 2-100 characters"})
		return
	}
	if req.Slug != nil && (len(*req.Slug) < 2 || len(*req.Slug) > 50) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
		return
	}

	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		req.PreferredLanguage = &resolved
	}

	if req.Slug != nil {
		var existingID string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM workspaces WHERE slug = ? AND id != ?", *req.Slug, workspaceID).Scan(&existingID)
		if err == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Workspace slug already taken"})
			return
		}
		if err != sql.ErrNoRows {
			h.logger.Error("check workspace slug", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	ub := newUpdate()
	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Slug != nil {
		ub.Set("slug", *req.Slug)
	}
	if req.PreferredLanguage != nil {
		if *req.PreferredLanguage == "" {
			ub.SetNull("preferred_language")
		} else {
			ub.Set("preferred_language", *req.PreferredLanguage)
		}
	}
	if !ub.Empty() {
		query, args := ub.Build("workspaces", "id = ?", workspaceID)
		if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
			h.logger.Error("update workspace", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	var ws workspaceResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT w.id, w.name, w.slug, w.logo_url, w.preferred_language, w.created_at, w.updated_at,
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL) AS crew_count,
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id) AS member_count
		FROM workspaces w
		WHERE w.id = ? AND w.deleted_at IS NULL
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.LogoURL, &ws.PreferredLanguage,
		&ws.CreatedAt, &ws.UpdatedAt, &ws.CrewCount, &ws.AgentCount, &ws.MemberCount)
	if err != nil {
		h.logger.Error("get workspace after update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, ws)
}
