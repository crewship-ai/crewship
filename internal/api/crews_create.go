package api

// Crew creation handler — large enough on its own to deserve a file.
// Owns createCrewRequest type. Extracted from crews.go.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/license"
)

type createCrewRequest struct {
	Name               string   `json:"name"`
	Slug               string   `json:"slug"`
	Description        *string  `json:"description"`
	Color              *string  `json:"color"`
	Icon               *string  `json:"icon"`
	ContainerMemoryMB  *int     `json:"container_memory_mb"`
	ContainerCPUs      *float64 `json:"container_cpus"`
	ContainerTTLHours  *int     `json:"container_ttl_hours"`
	NetworkMode        *string  `json:"network_mode"`
	AllowedDomains     []string `json:"allowed_domains"`
	RuntimeImage       *string  `json:"runtime_image"`
	DevcontainerConfig *string  `json:"devcontainer_config"`
	MiseConfig         *string  `json:"mise_config"`
}

func (h *CrewHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if h.license != nil {
		if err := h.license.CheckCrewLimit(r.Context(), h.db, workspaceID); err != nil {
			if license.IsLimitError(err) {
				replyError(w, http.StatusPaymentRequired, err.Error())
				return
			}
			h.logger.Error("check crew limit", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	var req createCrewRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		replyError(w, http.StatusBadRequest, "name must be 2-100 characters")
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
		return
	}
	// V-17: Validate slug format to prevent injection via container names / file paths
	if !validSlugFormat(req.Slug) {
		replyError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, hyphens, and underscores")
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL", workspaceID, req.Slug).Scan(&existingID)
	if err == nil {
		replyError(w, http.StatusConflict, "Crew slug already taken in this workspace")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check crew slug", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Clean up soft-deleted crews: cascade-delete their missions to free global
	// UNIQUE identifier space (e.g. "ENG-5" from deleted crew blocks new "ENG-5").
	// Match by exact slug OR already-renamed "{slug}_deleted_*" pattern.
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM mission_tasks WHERE mission_id IN
			(SELECT id FROM missions WHERE crew_id IN
				(SELECT id FROM crews WHERE workspace_id = ? AND deleted_at IS NOT NULL
				 AND (slug = ? OR slug LIKE ? || '_deleted_%')))`,
		workspaceID, req.Slug, req.Slug); err != nil {
		h.logger.Warn("cascade delete mission_tasks for old crew", "slug", req.Slug, "error", err)
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM missions WHERE crew_id IN
			(SELECT id FROM crews WHERE workspace_id = ? AND deleted_at IS NOT NULL
			 AND (slug = ? OR slug LIKE ? || '_deleted_%'))`,
		workspaceID, req.Slug, req.Slug); err != nil {
		h.logger.Warn("cascade delete missions for old crew", "slug", req.Slug, "error", err)
	}
	// Free slug from soft-deleted crews so the UNIQUE constraint doesn't block re-creation.
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE crews SET slug = slug || '_deleted_' || id WHERE workspace_id = ? AND slug = ? AND deleted_at IS NOT NULL",
		workspaceID, req.Slug); err != nil {
		h.logger.Warn("free deleted crew slug", "slug", req.Slug, "error", err)
	}

	// Validate and prepare network policy fields
	networkMode := "free"
	var allowedDomainsDB *string
	if req.NetworkMode != nil && *req.NetworkMode != "" {
		mode := strings.ToLower(*req.NetworkMode)
		if mode != "free" && mode != "restricted" {
			replyError(w, http.StatusBadRequest, "network_mode must be 'free' or 'restricted'")
			return
		}
		networkMode = mode
	}
	// Only persist allowed_domains when mode is restricted;
	// free mode ignores any supplied domains to avoid hidden DB state.
	var allowedDomainsOut []string
	if networkMode == "restricted" && len(req.AllowedDomains) > 0 {
		normalized := make([]string, 0, len(req.AllowedDomains))
		for _, d := range req.AllowedDomains {
			h := normalizeDomain(d)
			if h == "" {
				replyError(w, http.StatusBadRequest, fmt.Sprintf("invalid domain: %q", d))
				return
			}
			normalized = append(normalized, h)
		}
		domainsJSON, err := json.Marshal(normalized)
		if err != nil {
			h.logger.Error("marshal allowed_domains", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		s := string(domainsJSON)
		allowedDomainsDB = &s
		allowedDomainsOut = normalized
	} else {
		allowedDomainsOut = []string{}
	}

	// Validate devcontainer_config and mise_config size and syntax.
	if req.DevcontainerConfig != nil && len(*req.DevcontainerConfig) > 102400 {
		replyError(w, http.StatusBadRequest, "devcontainer_config exceeds 100KB limit")
		return
	}
	if req.MiseConfig != nil && len(*req.MiseConfig) > 10240 {
		replyError(w, http.StatusBadRequest, "mise_config exceeds 10KB limit")
		return
	}
	if req.DevcontainerConfig != nil && *req.DevcontainerConfig != "" {
		if _, err := devcontainer.ParseBytes([]byte(*req.DevcontainerConfig)); err != nil {
			replyError(w, http.StatusBadRequest, "invalid devcontainer_config: "+err.Error())
			return
		}
	}
	if req.MiseConfig != nil && *req.MiseConfig != "" {
		if _, err := devcontainer.ParseMiseConfig(*req.MiseConfig); err != nil {
			replyError(w, http.StatusBadRequest, "invalid mise_config: "+err.Error())
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	crewID := generateCUID()

	memoryMB := 4096
	if req.ContainerMemoryMB != nil && *req.ContainerMemoryMB > 0 {
		memoryMB = *req.ContainerMemoryMB
	}
	cpus := 2.0
	if req.ContainerCPUs != nil && *req.ContainerCPUs > 0 {
		cpus = *req.ContainerCPUs
	}
	var ttlHours *int
	if req.ContainerTTLHours != nil && *req.ContainerTTLHours > 0 {
		ttlHours = req.ContainerTTLHours
	}

	// Fail-fast: catch typos like "debian:bogus" before the crew is persisted.
	// Matches the validation performed on PATCH in Update.
	if req.RuntimeImage != nil && *req.RuntimeImage != "" {
		if err := devcontainer.ValidateImageExists(r.Context(), *req.RuntimeImage); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid runtime_image: " + err.Error(),
			})
			return
		}
	}

	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO crews (id, workspace_id, name, slug, description, color, icon, container_memory_mb, container_cpus, container_ttl_hours, network_mode, allowed_domains, runtime_image, devcontainer_config, mise_config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, workspaceID, req.Name, req.Slug, req.Description, req.Color, req.Icon, memoryMB, cpus, ttlHours, networkMode, allowedDomainsDB, req.RuntimeImage, req.DevcontainerConfig, req.MiseConfig, now, now)
	if err != nil {
		h.logger.Error("insert crew", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, crewResponse{
		ID:                 crewID,
		WorkspaceID:        workspaceID,
		Name:               req.Name,
		Slug:               req.Slug,
		Description:        req.Description,
		Color:              req.Color,
		Icon:               req.Icon,
		ContainerMemoryMB:  memoryMB,
		ContainerCPUs:      cpus,
		ContainerTTLHours:  ttlHours,
		NetworkMode:        networkMode,
		AllowedDomains:     allowedDomainsOut,
		RuntimeImage:       req.RuntimeImage,
		DevcontainerConfig: req.DevcontainerConfig,
		MiseConfig:         req.MiseConfig,
		CreatedAt:          now,
		UpdatedAt:          now,
	})

	h.broadcastCrewEvent("crew.created", workspaceID, map[string]string{
		"id": crewID, "name": req.Name, "slug": req.Slug,
	})
}

// Get returns a single crew by ID with full details.
// GET /api/v1/crews/{crewId}
