package api

// Crew update handler — applies partial patches with role-based
// guards, devcontainer-config diffing, and runtime-restart triggers.
// Extracted from crews.go.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type updateCrewRequest struct {
	Name               *string   `json:"name"`
	Slug               *string   `json:"slug"`
	Description        *string   `json:"description"`
	Color              *string   `json:"color"`
	Icon               *string   `json:"icon"`
	AvatarStyle        *string   `json:"avatar_style"`
	ContainerMemoryMB  *int      `json:"container_memory_mb"`
	ContainerCPUs      *float64  `json:"container_cpus"`
	ContainerTTLHours  *int      `json:"container_ttl_hours"`
	NetworkMode        *string   `json:"network_mode"`
	AllowedDomains     *[]string `json:"allowed_domains"`
	MCPConfigJSON      *string   `json:"mcp_config_json"`
	EscalationConfig   *string   `json:"escalation_config"`
	IssuePrefix        *string   `json:"issue_prefix"`
	RuntimeImage       *string   `json:"runtime_image"`
	DevcontainerConfig *string   `json:"devcontainer_config"`
	MiseConfig         *string   `json:"mise_config"`
	ServicesJSON       *string   `json:"services_json"`
	// MaxEphemeralAgents is the hire-flow quota (see v103 migration
	// + agents_hire.go). PR-G surfaces this on the policy panel so
	// operators can raise/lower the cap without dropping to the CLI.
	// Server-side CHECK(>=0) already exists; we also reject anything
	// above 100 here as a sanity cap (no legit reason to over-quota).
	MaxEphemeralAgents *int `json:"max_ephemeral_agents"`
}

// List returns all non-deleted crews in the workspace with member and agent counts.
// GET /api/v1/crews

func (h *CrewHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Verify crew exists and belongs to workspace
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("get crew for update", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}

	var req updateCrewRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Name != nil && (len(*req.Name) < 2 || len(*req.Name) > 100) {
		replyError(w, http.StatusBadRequest, "name must be 2-100 characters")
		return
	}
	if req.Slug != nil && (len(*req.Slug) < 2 || len(*req.Slug) > 50) {
		replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
		return
	}
	if req.Slug != nil && !validSlugFormat(*req.Slug) {
		replyError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, underscores, and hyphens")
		return
	}

	if req.Slug != nil {
		var slugOwnerID string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND id != ? AND deleted_at IS NULL",
			workspaceID, *req.Slug, crewID).Scan(&slugOwnerID)
		if err == nil {
			replyError(w, http.StatusConflict, "Crew slug already taken in this workspace")
			return
		}
		if err != sql.ErrNoRows {
			h.logger.Error("check crew slug", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
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
	// Services validation mirrors the create path. Empty/null/
	// whitespace-only services_json clears the column; a populated
	// body must match the validator's schema before storage. The
	// TrimSpace handles a payload of "   " or "\n", which the
	// previous != "" check would have stored verbatim, diverging
	// from the documented clear-on-empty semantics.
	if req.ServicesJSON != nil {
		trimmedServices := strings.TrimSpace(*req.ServicesJSON)
		req.ServicesJSON = &trimmedServices
		if trimmedServices != "" {
			if len(trimmedServices) > 64*1024 {
				replyError(w, http.StatusBadRequest, "services_json exceeds 64KB limit")
				return
			}
			if err := validateServicesJSON(trimmedServices); err != nil {
				replyError(w, http.StatusBadRequest, "invalid services_json: "+err.Error())
				return
			}
		}
	}

	// Build dynamic update
	ub := newUpdate()

	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Slug != nil {
		ub.Set("slug", *req.Slug)
	}
	if req.Description != nil {
		ub.Set("description", *req.Description)
	}
	if req.Color != nil {
		ub.Set("color", *req.Color)
	}
	if req.Icon != nil {
		ub.Set("icon", *req.Icon)
	}
	if req.AvatarStyle != nil {
		ub.Set("avatar_style", *req.AvatarStyle)
	}
	if req.ContainerMemoryMB != nil {
		ub.Set("container_memory_mb", *req.ContainerMemoryMB)
	}
	if req.ContainerCPUs != nil {
		ub.Set("container_cpus", *req.ContainerCPUs)
	}
	if req.ContainerTTLHours != nil {
		if *req.ContainerTTLHours < 0 {
			replyError(w, http.StatusBadRequest, "container_ttl_hours cannot be negative")
			return
		}
		if *req.ContainerTTLHours == 0 {
			ub.SetNull("container_ttl_hours")
		} else {
			ub.Set("container_ttl_hours", *req.ContainerTTLHours)
		}
	}
	if req.MaxEphemeralAgents != nil {
		// Server-side CHECK already enforces >= 0; the 100 ceiling is a
		// product sanity bound (typical crews run 1-20 ephemerals; a
		// four-digit quota is almost certainly a typo). Reject early
		// with an honest 400 instead of letting the CHECK fire a
		// 500-shaped error.
		if *req.MaxEphemeralAgents < 0 || *req.MaxEphemeralAgents > 100 {
			replyError(w, http.StatusBadRequest, "max_ephemeral_agents must be between 0 and 100")
			return
		}
		ub.Set("max_ephemeral_agents", *req.MaxEphemeralAgents)
	}
	if req.MCPConfigJSON != nil {
		if *req.MCPConfigJSON != "" {
			var mcpCheck struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(*req.MCPConfigJSON), &mcpCheck); err != nil {
				replyError(w, http.StatusBadRequest, "mcp_config_json is not valid JSON: "+err.Error())
				return
			}
			if mcpCheck.MCPServers == nil {
				replyError(w, http.StatusBadRequest, "mcp_config_json must contain a \"mcpServers\" object")
				return
			}
		}
		ub.Set("mcp_config_json", *req.MCPConfigJSON)
	}
	if req.IssuePrefix != nil {
		if *req.IssuePrefix == "" {
			ub.Set("issue_prefix", nil)
		} else {
			ub.Set("issue_prefix", *req.IssuePrefix)
		}
	}
	if req.EscalationConfig != nil {
		if *req.EscalationConfig != "" {
			var cfg orchestrator.EscalationConfig
			if err := json.Unmarshal([]byte(*req.EscalationConfig), &cfg); err != nil {
				replyError(w, http.StatusBadRequest, "escalation_config is not valid JSON: "+err.Error())
				return
			}
			for _, v := range []float64{cfg.AutoApproveThreshold, cfg.NotifyThreshold, cfg.RequireApprovalBelow} {
				if v < 0 || v > 1 {
					replyError(w, http.StatusBadRequest, "escalation_config thresholds must be between 0 and 1")
					return
				}
			}
			if cfg.AutoApproveThreshold > 0 && cfg.RequireApprovalBelow > 0 && cfg.AutoApproveThreshold <= cfg.RequireApprovalBelow {
				replyError(w, http.StatusBadRequest, "auto_approve_threshold must be greater than require_approval_below")
				return
			}
		}
		if *req.EscalationConfig == "" {
			ub.Set("escalation_config", nil)
		} else {
			ub.Set("escalation_config", *req.EscalationConfig)
		}
	}
	if req.RuntimeImage != nil {
		if *req.RuntimeImage == "" {
			ub.Set("runtime_image", nil)
		} else {
			// Fail-fast: catch typos like "debian:bogus" before provisioning.
			// Uses anonymous auth with a short timeout; private images that
			// require auth are allowed through (isAuthError path).
			if err := devcontainer.ValidateImageExists(r.Context(), *req.RuntimeImage); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "invalid runtime_image: " + err.Error(),
				})
				return
			}
			ub.Set("runtime_image", *req.RuntimeImage)
		}
		// Invalidate cached image when runtime image changes
		ub.Set("cached_image", nil)
		ub.Set("config_hash", nil)
		ub.Set("cached_requirements", nil)
	}
	if req.DevcontainerConfig != nil {
		if *req.DevcontainerConfig == "" {
			ub.Set("devcontainer_config", nil)
		} else {
			ub.Set("devcontainer_config", *req.DevcontainerConfig)
		}
		// Invalidate cached image when devcontainer config changes
		ub.Set("cached_image", nil)
		ub.Set("config_hash", nil)
		ub.Set("cached_requirements", nil)
	}
	if req.MiseConfig != nil {
		if *req.MiseConfig == "" {
			ub.Set("mise_config", nil)
		} else {
			ub.Set("mise_config", *req.MiseConfig)
		}
		// Invalidate cached image when mise config changes
		ub.Set("cached_image", nil)
		ub.Set("config_hash", nil)
		ub.Set("cached_requirements", nil)
	}
	if req.ServicesJSON != nil {
		if *req.ServicesJSON == "" {
			ub.Set("services_json", nil)
		} else {
			ub.Set("services_json", *req.ServicesJSON)
		}
		// Services do NOT participate in the cached image hash —
		// they're separate containers built from upstream images,
		// not baked into the agent runtime. Changing services
		// triggers a sidecar restart at next EnsureCrewRuntime,
		// not a devcontainer rebuild.
	}
	// Track whether the resolved mode is free — if so, always clear allowed_domains.
	updatedModeFree := false
	if req.NetworkMode != nil {
		mode := strings.ToLower(*req.NetworkMode)
		if mode != "free" && mode != "restricted" {
			replyError(w, http.StatusBadRequest, "network_mode must be 'free' or 'restricted'")
			return
		}
		ub.Set("network_mode", mode)
		if mode == "free" {
			updatedModeFree = true
			ub.SetNull("allowed_domains")
		}
	}
	// If mode was not explicitly set in this request, check the current DB mode.
	// Skip persisting allowed_domains when effective mode is free to prevent hidden state.
	if !updatedModeFree && req.NetworkMode == nil && req.AllowedDomains != nil {
		var currentMode string
		if err := h.db.QueryRowContext(r.Context(), "SELECT network_mode FROM crews WHERE id = ?", crewID).Scan(&currentMode); err == nil && currentMode == "free" {
			updatedModeFree = true
		}
	}
	if !updatedModeFree && req.AllowedDomains != nil {
		if len(*req.AllowedDomains) == 0 {
			ub.SetNull("allowed_domains")
		} else {
			normalized := make([]string, 0, len(*req.AllowedDomains))
			for _, d := range *req.AllowedDomains {
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
			ub.Set("allowed_domains", string(domainsJSON))
		}
	}

	query, args := ub.Build("crews", "id = ?", crewID)
	_, err = h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("update crew", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Return updated crew
	var c crewResponse
	var updatedDomainsJSON *string
	err = h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains,
			c.mcp_config_json, c.escalation_config,
			c.runtime_image, c.devcontainer_config, c.mise_config, c.cached_image, c.config_hash,
			c.max_ephemeral_agents,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.id = ? AND c.deleted_at IS NULL
	`, crewID).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
		&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
		&c.ContainerTTLHours, &c.NetworkMode, &updatedDomainsJSON,
		&c.MCPConfigJSON, &c.EscalationConfig,
		&c.RuntimeImage, &c.DevcontainerConfig, &c.MiseConfig, &c.CachedImage, &c.ConfigHash,
		&c.MaxEphemeralAgents,
		&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members)
	if err != nil {
		h.logger.Error("get crew after update", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	c.AllowedDomains = parseAllowedDomains(updatedDomainsJSON)
	writeJSON(w, http.StatusOK, c)

	h.broadcastCrewEvent("crew.updated", workspaceID, map[string]string{
		"id": crewID, "name": c.Name, "slug": c.Slug,
	})

	// Restart crew container when network policy or sidecar services
	// change so the docker provider picks up the new config on the
	// next agent run. services_json edits otherwise stay stale
	// against a cached running container — the docker provider only
	// re-reads services_json on EnsureCrewRuntime, and a reused
	// warm container skips that path. Runs after response is sent
	// to avoid SQLite lock contention.
	if req.NetworkMode != nil || req.AllowedDomains != nil || req.ServicesJSON != nil {
		go h.restartCrewContainer(crewID)
	}
}

// Delete soft-deletes a crew and all its associated agents.
// DELETE /api/v1/crews/{crewId}
