package api

// Crew update handler — applies partial patches with role-based
// guards, devcontainer-config diffing, and runtime-restart triggers.
// Extracted from crews.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type updateCrewRequest struct {
	Name                  *string   `json:"name"`
	Slug                  *string   `json:"slug"`
	Description           *string   `json:"description"`
	Color                 *string   `json:"color"`
	Icon                  *string   `json:"icon"`
	AvatarStyle           *string   `json:"avatar_style"`
	ContainerMemoryMB     *int      `json:"container_memory_mb"`
	ContainerCPUs         *float64  `json:"container_cpus"`
	ContainerTTLHours     *int      `json:"container_ttl_hours"`
	NetworkMode           *string   `json:"network_mode"`
	AllowedDomains        *[]string `json:"allowed_domains"`
	AllowPrivateEndpoints *bool     `json:"allow_private_endpoints"`
	MCPConfigJSON         *string   `json:"mcp_config_json"`
	EscalationConfig      *string   `json:"escalation_config"`
	IssuePrefix           *string   `json:"issue_prefix"`
	RuntimeImage          *string   `json:"runtime_image"`
	DevcontainerConfig    *string   `json:"devcontainer_config"`
	MiseConfig            *string   `json:"mise_config"`
	ServicesJSON          *string   `json:"services_json"`
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
		replyInternalError(w, h.logger, "get crew for update", err)
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
	// #2035: the prefix ends up inside missions.identifier, which every issue
	// route addresses as one path segment, so "A/B" would mint an issue nothing
	// can open. "" is exempt — it is the clear, applied below.
	if req.IssuePrefix != nil && *req.IssuePrefix != "" && !validIssuePrefixFormat(*req.IssuePrefix) {
		replyError(w, http.StatusBadRequest, issuePrefixFormatRule)
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
			replyInternalError(w, h.logger, "check crew slug", err)
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
		cfg, err := devcontainer.ParseBytes([]byte(*req.DevcontainerConfig))
		if err != nil {
			replyError(w, http.StatusBadRequest, "invalid devcontainer_config: "+err.Error())
			return
		}
		// #1380: enforce the container-privilege controls server-side on
		// update too — otherwise an operator could sidestep the create-time
		// gate by PATCHing privileged/capAdd/mounts onto an existing crew.
		allowPriv, err := h.workspaceAllowsPrivileged(r.Context(), workspaceID)
		if err != nil {
			replyInternalError(w, h.logger, "check workspace privileged flag", err)
			return
		}
		if verr := cfg.ValidateSecurity(allowPriv); verr != nil {
			replyDevcontainerSecurityError(w, verr)
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
	// #1627: this used to write both values straight through — not even the
	// negative guard container_ttl_hours gets three lines below. A patch of
	// container_cpus: 0.005 landed in the row, and the daemon then refused
	// every subsequent container create for the crew.
	if err := validateCrewContainerResources(req.ContainerMemoryMB, req.ContainerCPUs); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	// #1638: an explicit 0 means "reset to the server default" — the wording
	// docs/cli/crew.mdx uses for `--memory-mb 0`. This used to store the 0,
	// and the runtime's `<= 0` fallback then sized the crew at 8 GiB: double
	// what the identical request produces through Create, and double what the
	// docs promise. Resolve it here, from the same constant Create uses, so
	// the row always carries a real size.
	if req.ContainerMemoryMB != nil {
		ub.Set("container_memory_mb", resolveCrewContainerMemoryMB(*req.ContainerMemoryMB))
	}
	if req.ContainerCPUs != nil {
		ub.Set("container_cpus", resolveCrewContainerCPUs(*req.ContainerCPUs))
	}
	if req.ContainerTTLHours != nil {
		if *req.ContainerTTLHours < 0 {
			replyError(w, http.StatusBadRequest, "container_ttl_hours cannot be negative")
			return
		}
		// #1662: an explicit 0 used to be written as NULL. Both read as
		// "never stop" back then, so it made no difference. Now NULL is
		// "never configured — use the server default", and clearing the
		// column would flip a deliberate never-stop into a four-hour
		// auto-stop on the next sweep. Store the 0.
		ub.Set("container_ttl_hours", *req.ContainerTTLHours)
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
		// Format already checked above; "" clears the column, after which the
		// prefix falls back to the first three letters of the slug.
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
		// Invalidate the cached image so the dispatch gate reprovisions.
		// cached_requirements is deliberately NOT invalidated here (#1032):
		// resolveAgentConfig's fail-closed credential gate reads it as the
		// only signal for "is this crew's actual RUNNING container
		// privileged", and reprovisioning is async — nulling it out
		// synchronously would open a window where the container is STILL
		// privileged (unchanged until the rebuild completes) but the gate
		// reads "unknown" and hands out credentials anyway. The stale value
		// stays accurate until crew_provisioning_jobs.go's completion
		// handler overwrites it with the freshly computed one.
		ub.Set("cached_image", nil)
		ub.Set("config_hash", nil)
	}
	if req.DevcontainerConfig != nil {
		if *req.DevcontainerConfig == "" {
			ub.Set("devcontainer_config", nil)
		} else {
			ub.Set("devcontainer_config", *req.DevcontainerConfig)
		}
		// Invalidate the cached image when devcontainer config changes —
		// cached_requirements is left alone; see the runtime_image branch
		// above for why.
		ub.Set("cached_image", nil)
		ub.Set("config_hash", nil)
	}
	if req.MiseConfig != nil {
		if *req.MiseConfig == "" {
			ub.Set("mise_config", nil)
		} else {
			ub.Set("mise_config", *req.MiseConfig)
		}
		// Invalidate the cached image when mise config changes —
		// cached_requirements is left alone; see the runtime_image branch
		// above for why.
		ub.Set("cached_image", nil)
		ub.Set("config_hash", nil)
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
	// #961 private-endpoint egress opt-in.
	if req.AllowPrivateEndpoints != nil {
		v := 0
		if *req.AllowPrivateEndpoints {
			v = 1
		}
		ub.Set("allow_private_endpoints", v)
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
				replyInternalError(w, h.logger, "marshal allowed_domains", err)
				return
			}
			ub.Set("allowed_domains", string(domainsJSON))
		}
	}

	query, args := ub.Build("crews", "id = ?", crewID)
	_, err = h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "update crew", err)
		return
	}

	// A crew's avatar_style is the default for every agent in it that hasn't
	// set its own, so changing it changes those agents' faces — and a stored
	// render (#1297) still depicts the OLD style. Drop the renders for the
	// inheriting agents only; ones with their own avatar_style are unaffected
	// by this field and must keep theirs.
	//
	// The dedicated apply-avatar-style endpoint clears renders for the whole
	// crew because it rewrites every agent's own style. This path is the
	// quieter one — the crew settings dropdown — and without this the change
	// would appear to do nothing for any agent already backfilled.
	if req.AvatarStyle != nil {
		if _, err := h.db.ExecContext(r.Context(), `
			UPDATE agents SET avatar_svg = NULL, avatar_svg_hash = NULL
			WHERE crew_id = ? AND avatar_style IS NULL AND avatar_svg IS NOT NULL AND deleted_at IS NULL`,
			crewID); err != nil {
			replyInternalError(w, h.logger, "clear inherited agent avatars", err)
			return
		}
	}

	// Return updated crew
	var c crewResponse
	err = scanCrewRow(h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains, c.allow_private_endpoints,
			c.mcp_config_json, c.escalation_config,
			c.runtime_image, c.devcontainer_config, c.mise_config, c.cached_image, c.config_hash,
			c.max_ephemeral_agents,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.id = ? AND c.deleted_at IS NULL
	`, crewID), &c, false, false)
	if err != nil {
		replyInternalError(w, h.logger, "get crew after update", err)
		return
	}

	// The changed FIELDS, not their values: a crew update can carry an
	// allowed-domain list or an MCP config, and the audit log is read by more
	// people than those settings are meant for. What changed is the question
	// the log has to answer; the current value is one GET away.
	auditFromRequest(r, h.db, "crew.update", "CREW", crewID, map[string]interface{}{
		"name": c.Name, "fields": changedCrewFields(&req),
	})

	// #1638: same advisory as Create, computed from the row as it now stands
	// rather than from the patch — a crew can be shrunk into the undersized
	// band by a request that only mentions one of the two fields.
	advisories := crewSizingAdvisories(r.Context(), h.db, c.ContainerMemoryMB, c.ContainerCPUs)
	for _, a := range advisories {
		h.logger.Warn("crew sized below the usable floor", "crew_id", crewID, "slug", c.Slug, "advisory", a)
	}

	// A resize is stored immediately and takes effect much later (#1681).
	// container_memory_mb and container_cpus are applied at ContainerCreate and
	// nowhere else, so a crew whose container is already up keeps its old
	// cgroup limits while this 200 — and every subsequent `crew get` — reports
	// the new ones. Said here because the response to the request that made the
	// change is the one place the operator is certainly looking.
	//
	// Only when the crew is actually RUNNING, which costs one bounded IPC call
	// on a rare request. A stopped crew needs no notice: the provider rebuilds
	// a stopped container whose limits no longer match on the next wake
	// (internal/provider/docker/crew_resource_drift.go), so the change is not
	// pending on anything the operator has to do. Warning regardless would put
	// a line on every resize, including the ones that are already correct,
	// which is how a warnings channel stops being read.
	if fields := crewResizeFields(&req); len(fields) > 0 && h.crewContainerIsRunning(r.Context(), crewID) {
		advisories = append(advisories, fmt.Sprintf(
			"%s changed, but this crew's container is already running and keeps the limits it was "+
				"created with until it is recreated. `crewship crew container-status %s` reports the gap; "+
				"an idle-TTL stop or `crewship crew restart-agents %s` closes it.",
			strings.Join(fields, " and "), c.Slug, c.Slug))
	}

	// Same as Create: computed from the row as it now stands, so switching a
	// crew TO restricted on an instance that cannot apply it is answered by
	// the response to the request that switched it.
	h.annotateEgressEnforcement(&c)
	if adv := h.egressEnforcementAdvisory(&c); adv != "" {
		h.logger.Warn("crew egress mode is not enforced by this instance's provider",
			"crew_id", crewID, "slug", c.Slug, "network_mode", c.NetworkMode)
		advisories = append(advisories, adv)
	}

	writeJSON(w, http.StatusOK, crewResponseWithAdvisories{crewResponse: c, Warnings: advisories})

	h.broadcastCrewEvent("crew.updated", workspaceID, map[string]string{
		"id": crewID, "name": c.Name, "slug": c.Slug,
	})

	// Devcontainer / mise / runtime-image changes invalidated the cached image
	// above. Proactively rebuild now (background) so the crew stays runnable
	// without the operator clicking "Build now". c holds the freshly-stored
	// config, so partial updates (only mise, only devcontainer) resolve the
	// combined need correctly.
	if req.DevcontainerConfig != nil || req.MiseConfig != nil || req.RuntimeImage != nil {
		h.maybeAutoProvision(crewID, workspaceID, derefStr(c.DevcontainerConfig), derefStr(c.MiseConfig))
	}

	// Restart crew container when network policy or sidecar services
	// change so the docker provider picks up the new config on the
	// next agent run. services_json edits otherwise stay stale
	// against a cached running container — the docker provider only
	// re-reads services_json on EnsureCrewRuntime, and a reused
	// warm container skips that path. Runs after response is sent
	// to avoid SQLite lock contention.
	if req.NetworkMode != nil || req.AllowedDomains != nil || req.ServicesJSON != nil || req.AllowPrivateEndpoints != nil {
		// WithoutCancel preserves the request's OTel span + auth values
		// so the async IPC stop is observable, while shedding the
		// request's cancellation -- the 200 has already been flushed.
		// Mirrors the pattern used in webhook / eval / consolidate
		// handler-spawned goroutines (audit PR #481).
		ctx := context.WithoutCancel(r.Context())
		finish := beginBackgroundWork()
		go func() {
			defer finish()
			h.restartCrewContainer(ctx, crewID)
		}()
	}
}

// Delete soft-deletes a crew and all its associated agents.
// DELETE /api/v1/crews/{crewId}

// crewResizeFields names the container limits a PATCH carried, in the order
// they appear on the response.
//
// Deliberately narrow: only settings that CANNOT be applied to an existing
// container belong here, because each one produces an advisory on the
// response, and a notice attached to edits that take effect immediately is
// boilerplate that teaches its reader to skip the field.
func crewResizeFields(req *updateCrewRequest) []string {
	fields := make([]string, 0, 2)
	if req.ContainerMemoryMB != nil {
		fields = append(fields, "container_memory_mb")
	}
	if req.ContainerCPUs != nil {
		fields = append(fields, "container_cpus")
	}
	return fields
}

// changedCrewFields names the fields a PATCH actually carried.
//
// The audit row records WHICH settings moved, not what they moved to: a crew
// update can carry an allowed-domain list, an MCP config or an escalation
// policy, and the audit log has a wider readership than any of those. "Who
// touched the network policy, and when" is the question it must answer; the
// current value is one GET away for anyone entitled to it.
func changedCrewFields(req *updateCrewRequest) []string {
	fields := make([]string, 0, 8)
	add := func(name string, set bool) {
		if set {
			fields = append(fields, name)
		}
	}
	add("name", req.Name != nil)
	add("slug", req.Slug != nil)
	add("description", req.Description != nil)
	add("color", req.Color != nil)
	add("icon", req.Icon != nil)
	add("avatar_style", req.AvatarStyle != nil)
	add("container_memory_mb", req.ContainerMemoryMB != nil)
	add("container_cpus", req.ContainerCPUs != nil)
	add("container_ttl_hours", req.ContainerTTLHours != nil)
	add("network_mode", req.NetworkMode != nil)
	add("allowed_domains", req.AllowedDomains != nil)
	add("allow_private_endpoints", req.AllowPrivateEndpoints != nil)
	add("mcp_config_json", req.MCPConfigJSON != nil)
	add("escalation_config", req.EscalationConfig != nil)
	add("issue_prefix", req.IssuePrefix != nil)
	add("runtime_image", req.RuntimeImage != nil)
	add("devcontainer_config", req.DevcontainerConfig != nil)
	add("mise_config", req.MiseConfig != nil)
	add("services_json", req.ServicesJSON != nil)
	return fields
}
