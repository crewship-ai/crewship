package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// normalizeDomain extracts and validates a bare hostname from a domain entry.
// It handles inputs like "https://api.github.com/path", "api.github.com:443",
// and "api.github.com" — always returning just the hostname (lowercase, trimmed).
// Returns empty string for invalid entries.
func normalizeDomain(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// If it looks like a URL (has scheme or slashes), parse it.
	if strings.Contains(s, "://") || strings.HasPrefix(s, "//") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Hostname()
	}
	// Strip port if present (e.g. "api.github.com:443")
	host, _, err := net.SplitHostPort(s)
	if err == nil {
		s = host
	}
	s = strings.ToLower(s)
	// Basic validation: must contain at least one dot, no spaces/newlines
	if !strings.Contains(s, ".") || strings.ContainsAny(s, " \t\n\r") {
		return ""
	}
	return s
}

// CrewHandler provides CRUD endpoints for managing crews (teams of agents) within a workspace.
type CrewHandler struct {
	db         *sql.DB
	hub        *ws.Hub
	logger     *slog.Logger
	license    *license.License
	socketPath string
}

// NewCrewHandler creates a CrewHandler with the given database and logger.
func NewCrewHandler(db *sql.DB, logger *slog.Logger) *CrewHandler {
	return &CrewHandler{db: db, logger: logger}
}

// SetHub attaches a WebSocket hub for broadcasting crew events.
func (h *CrewHandler) SetHub(hub *ws.Hub) { h.hub = hub }

func (h *CrewHandler) broadcastCrewEvent(eventType, workspaceID string, payload map[string]string) {
	broadcastWorkspaceEvent(h.hub, workspaceID, eventType, payload)
}

// SetLicense attaches the license for enforcing crew count limits.
func (h *CrewHandler) SetLicense(lic *license.License) { h.license = lic }

// SetSocketPath sets the Unix socket path used to restart crew containers via IPC.
func (h *CrewHandler) SetSocketPath(path string) { h.socketPath = path }

// restartCrewContainer stops the crew container via IPC so it gets recreated
// with the new network policy on the next agent run.
func (h *CrewHandler) restartCrewContainer(crewID string) {
	if h.socketPath == "" {
		return
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", h.socketPath)
			},
		},
	}
	url := fmt.Sprintf("http://crewshipd/crews/%s/container/stop", crewID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		h.logger.Warn("failed to build container stop request", "error", err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		h.logger.Debug("container stop via IPC (may not be running)", "crew_id", crewID, "error", err)
		return
	}
	resp.Body.Close()
	h.logger.Info("crew container stopped after network policy change", "crew_id", crewID, "status", resp.StatusCode)
}

type crewCountResponse struct {
	Agents  int `json:"agents"`
	Members int `json:"members"`
}

type crewResponse struct {
	ID                string           `json:"id"`
	WorkspaceID       string           `json:"workspace_id"`
	Name              string           `json:"name"`
	Slug              string           `json:"slug"`
	Description       *string          `json:"description"`
	Color             *string          `json:"color"`
	Icon              *string          `json:"icon"`
	AvatarStyle       *string          `json:"avatar_style"`
	ContainerMemoryMB int              `json:"container_memory_mb"`
	ContainerCPUs     float64          `json:"container_cpus"`
	ContainerTTLHours *int             `json:"container_ttl_hours"`
	NetworkMode       string           `json:"network_mode"`
	AllowedDomains    []string         `json:"allowed_domains"`
	MCPConfigJSON     *string          `json:"mcp_config_json,omitempty"`
	EscalationConfig  *string          `json:"escalation_config,omitempty"`
	IssuePrefix       *string          `json:"issue_prefix"`
	CreatedAt         string           `json:"created_at"`
	UpdatedAt         string           `json:"updated_at"`
	Count             crewCountResponse `json:"_count"`
}

// List returns all non-deleted crews in the workspace with member and agent counts.
// GET /api/v1/crews
func (h *CrewHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id is required"})
		return
	}

	limit, offset := parseListPagination(r, 100, 500)

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains,
			c.mcp_config_json, c.escalation_config,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL
		-- c.id DESC is the pagination tiebreaker: c.created_at is second-precision,
		-- so timestamp ties are realistic and would otherwise make LIMIT/OFFSET
		-- windows drop or duplicate rows between pages.
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT ? OFFSET ?
	`, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("list crews", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []crewResponse
	for rows.Next() {
		var c crewResponse
		var allowedDomainsJSON *string
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
			&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
			&c.ContainerTTLHours, &c.NetworkMode, &allowedDomainsJSON,
			&c.MCPConfigJSON, &c.EscalationConfig,
			&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members); err != nil {
			h.logger.Error("scan crew", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		c.AllowedDomains = parseAllowedDomains(allowedDomainsJSON)
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (crews)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []crewResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type createCrewRequest struct {
	Name              string   `json:"name"`
	Slug              string   `json:"slug"`
	Description       *string  `json:"description"`
	Color             *string  `json:"color"`
	Icon              *string  `json:"icon"`
	ContainerMemoryMB *int     `json:"container_memory_mb"`
	ContainerCPUs     *float64 `json:"container_cpus"`
	ContainerTTLHours *int     `json:"container_ttl_hours"`
	NetworkMode       *string  `json:"network_mode"`
	AllowedDomains    []string `json:"allowed_domains"`
}

// Create provisions a new crew in the workspace with the given name, slug, and configuration.
// POST /api/v1/crews
func (h *CrewHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if h.license != nil {
		if err := h.license.CheckCrewLimit(r.Context(), h.db, workspaceID); err != nil {
			if license.IsLimitError(err) {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": err.Error()})
				return
			}
			h.logger.Error("check crew limit", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
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
	// V-17: Validate slug format to prevent injection via container names / file paths
	if !validSlugFormat(req.Slug) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain only lowercase letters, numbers, and hyphens"})
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL", workspaceID, req.Slug).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Crew slug already taken in this workspace"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check crew slug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "network_mode must be 'free' or 'restricted'"})
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
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid domain: %q", d)})
				return
			}
			normalized = append(normalized, h)
		}
		domainsJSON, err := json.Marshal(normalized)
		if err != nil {
			h.logger.Error("marshal allowed_domains", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		s := string(domainsJSON)
		allowedDomainsDB = &s
		allowedDomainsOut = normalized
	} else {
		allowedDomainsOut = []string{}
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

	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO crews (id, workspace_id, name, slug, description, color, icon, container_memory_mb, container_cpus, container_ttl_hours, network_mode, allowed_domains, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, workspaceID, req.Name, req.Slug, req.Description, req.Color, req.Icon, memoryMB, cpus, ttlHours, networkMode, allowedDomainsDB, now, now)
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
		ContainerMemoryMB: memoryMB,
		ContainerCPUs:     cpus,
		ContainerTTLHours: ttlHours,
		NetworkMode:       networkMode,
		AllowedDomains:    allowedDomainsOut,
		CreatedAt:         now,
		UpdatedAt:         now,
	})

	h.broadcastCrewEvent("crew.created", workspaceID, map[string]string{
		"id": crewID, "name": req.Name, "slug": req.Slug,
	})
}

// Get returns a single crew by ID with full details.
// GET /api/v1/crews/{crewId}
func (h *CrewHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	var c crewResponse
	var allowedDomainsJSON *string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains,
			c.mcp_config_json, c.escalation_config, c.issue_prefix,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
	`, crewID, workspaceID).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
		&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
		&c.ContainerTTLHours, &c.NetworkMode, &allowedDomainsJSON,
		&c.MCPConfigJSON, &c.EscalationConfig, &c.IssuePrefix,
		&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("get crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	c.AllowedDomains = parseAllowedDomains(allowedDomainsJSON)
	writeJSON(w, http.StatusOK, c)
}

type updateCrewRequest struct {
	Name              *string   `json:"name"`
	Slug              *string   `json:"slug"`
	Description       *string   `json:"description"`
	Color             *string   `json:"color"`
	Icon              *string   `json:"icon"`
	AvatarStyle       *string   `json:"avatar_style"`
	ContainerMemoryMB *int      `json:"container_memory_mb"`
	ContainerCPUs     *float64  `json:"container_cpus"`
	ContainerTTLHours *int      `json:"container_ttl_hours"`
	NetworkMode       *string   `json:"network_mode"`
	AllowedDomains    *[]string `json:"allowed_domains"`
	MCPConfigJSON     *string   `json:"mcp_config_json"`
	EscalationConfig  *string   `json:"escalation_config"`
	IssuePrefix       *string   `json:"issue_prefix"`
}

// Update modifies crew properties such as name, description, network policy, and escalation config.
// PATCH /api/v1/crews/{crewId}
func (h *CrewHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	// Verify crew exists and belongs to workspace
	err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("get crew for update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req updateCrewRequest
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
	if req.Slug != nil && !validSlugFormat(*req.Slug) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain only lowercase letters, numbers, underscores, and hyphens"})
		return
	}

	if req.Slug != nil {
		var slugOwnerID string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND id != ? AND deleted_at IS NULL",
			workspaceID, *req.Slug, crewID).Scan(&slugOwnerID)
		if err == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Crew slug already taken in this workspace"})
			return
		}
		if err != sql.ErrNoRows {
			h.logger.Error("check crew slug", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Build dynamic update
	query := "UPDATE crews SET updated_at = ?"
	args := []any{now}

	if req.Name != nil {
		query += ", name = ?"
		args = append(args, *req.Name)
	}
	if req.Slug != nil {
		query += ", slug = ?"
		args = append(args, *req.Slug)
	}
	if req.Description != nil {
		query += ", description = ?"
		args = append(args, *req.Description)
	}
	if req.Color != nil {
		query += ", color = ?"
		args = append(args, *req.Color)
	}
	if req.Icon != nil {
		query += ", icon = ?"
		args = append(args, *req.Icon)
	}
	if req.AvatarStyle != nil {
		query += ", avatar_style = ?"
		args = append(args, *req.AvatarStyle)
	}
	if req.ContainerMemoryMB != nil {
		query += ", container_memory_mb = ?"
		args = append(args, *req.ContainerMemoryMB)
	}
	if req.ContainerCPUs != nil {
		query += ", container_cpus = ?"
		args = append(args, *req.ContainerCPUs)
	}
	if req.ContainerTTLHours != nil {
		if *req.ContainerTTLHours < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "container_ttl_hours cannot be negative"})
			return
		}
		if *req.ContainerTTLHours == 0 {
			query += ", container_ttl_hours = NULL"
		} else {
			query += ", container_ttl_hours = ?"
			args = append(args, *req.ContainerTTLHours)
		}
	}
	if req.MCPConfigJSON != nil {
		if *req.MCPConfigJSON != "" {
			var mcpCheck struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(*req.MCPConfigJSON), &mcpCheck); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_config_json is not valid JSON: " + err.Error()})
				return
			}
			if mcpCheck.MCPServers == nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_config_json must contain a \"mcpServers\" object"})
				return
			}
		}
		query += ", mcp_config_json = ?"
		args = append(args, *req.MCPConfigJSON)
	}
	if req.IssuePrefix != nil {
		query += ", issue_prefix = ?"
		if *req.IssuePrefix == "" {
			args = append(args, nil)
		} else {
			args = append(args, *req.IssuePrefix)
		}
	}
	if req.EscalationConfig != nil {
		if *req.EscalationConfig != "" {
			var cfg orchestrator.EscalationConfig
			if err := json.Unmarshal([]byte(*req.EscalationConfig), &cfg); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "escalation_config is not valid JSON: " + err.Error()})
				return
			}
			for _, v := range []float64{cfg.AutoApproveThreshold, cfg.NotifyThreshold, cfg.RequireApprovalBelow} {
				if v < 0 || v > 1 {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "escalation_config thresholds must be between 0 and 1"})
					return
				}
			}
			if cfg.AutoApproveThreshold > 0 && cfg.RequireApprovalBelow > 0 && cfg.AutoApproveThreshold <= cfg.RequireApprovalBelow {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "auto_approve_threshold must be greater than require_approval_below"})
				return
			}
		}
		query += ", escalation_config = ?"
		if *req.EscalationConfig == "" {
			args = append(args, nil)
		} else {
			args = append(args, *req.EscalationConfig)
		}
	}
	// Track whether the resolved mode is free — if so, always clear allowed_domains.
	updatedModeFree := false
	if req.NetworkMode != nil {
		mode := strings.ToLower(*req.NetworkMode)
		if mode != "free" && mode != "restricted" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "network_mode must be 'free' or 'restricted'"})
			return
		}
		query += ", network_mode = ?"
		args = append(args, mode)
		if mode == "free" {
			updatedModeFree = true
			query += ", allowed_domains = NULL"
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
			query += ", allowed_domains = NULL"
		} else {
			normalized := make([]string, 0, len(*req.AllowedDomains))
			for _, d := range *req.AllowedDomains {
				h := normalizeDomain(d)
				if h == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid domain: %q", d)})
					return
				}
				normalized = append(normalized, h)
			}
			domainsJSON, err := json.Marshal(normalized)
			if err != nil {
				h.logger.Error("marshal allowed_domains", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
			query += ", allowed_domains = ?"
			args = append(args, string(domainsJSON))
		}
	}

	query += " WHERE id = ?"
	args = append(args, crewID)

	_, err = h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("update crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Return updated crew
	var c crewResponse
	var updatedDomainsJSON *string
	err = h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains,
			c.mcp_config_json, c.escalation_config,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.id = ? AND c.deleted_at IS NULL
	`, crewID).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
		&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
		&c.ContainerTTLHours, &c.NetworkMode, &updatedDomainsJSON,
		&c.MCPConfigJSON, &c.EscalationConfig,
		&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members)
	if err != nil {
		h.logger.Error("get crew after update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	c.AllowedDomains = parseAllowedDomains(updatedDomainsJSON)
	writeJSON(w, http.StatusOK, c)

	h.broadcastCrewEvent("crew.updated", workspaceID, map[string]string{
		"id": crewID, "name": c.Name, "slug": c.Slug,
	})

	// Restart crew container when network policy changes so the sidecar
	// picks up the new config on the next agent run. Runs after response
	// is sent to avoid SQLite lock contention.
	if req.NetworkMode != nil || req.AllowedDomains != nil {
		go h.restartCrewContainer(crewID)
	}
}

// Delete soft-deletes a crew and all its associated agents.
// DELETE /api/v1/crews/{crewId}
func (h *CrewHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	// Verify crew exists and belongs to workspace
	err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("get crew for delete", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Cascade: hard-delete orphan-prone children before soft-deleting the crew.
	// Missions have a UNIQUE(identifier) constraint that is NOT workspace-scoped,
	// so leaving them behind blocks future crews from reusing identifier prefixes.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM mission_tasks WHERE mission_id IN (SELECT id FROM missions WHERE crew_id = ?)", crewID); err != nil {
		h.logger.Warn("cascade delete mission_tasks", "crew_id", crewID, "error", err)
	}
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM missions WHERE crew_id = ?", crewID); err != nil {
		h.logger.Warn("cascade delete missions", "crew_id", crewID, "error", err)
	}
	// Also remove crew members — they reference this crew
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM crew_members WHERE crew_id = ?", crewID); err != nil {
		h.logger.Warn("cascade delete crew_members", "crew_id", crewID, "error", err)
	}

	_, err = h.db.ExecContext(r.Context(),
		"UPDATE crews SET deleted_at = ? WHERE id = ?",
		now, crewID)
	if err != nil {
		h.logger.Error("soft delete crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	h.broadcastCrewEvent("crew.deleted", workspaceID, map[string]string{"id": crewID})
}

type crewMemberResponse struct {
	ID        string      `json:"id"`
	CrewID    string      `json:"crew_id"`
	UserID    string      `json:"user_id"`
	CreatedAt string      `json:"created_at"`
	User      *memberUser `json:"user,omitempty"`
}

// ListMembers returns all human members of a crew with their user details.
// GET /api/v1/crews/{crewId}/members
func (h *CrewHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	// Verify crew exists and belongs to workspace
	err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("get crew for list members", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cm.id, cm.crew_id, cm.user_id, cm.created_at,
			u.id, u.email, u.full_name, u.avatar_url
		FROM crew_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.crew_id = ?
		ORDER BY cm.created_at ASC
	`, crewID)
	if err != nil {
		h.logger.Error("list crew members", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []crewMemberResponse
	for rows.Next() {
		var m crewMemberResponse
		var u memberUser
		if err := rows.Scan(&m.ID, &m.CrewID, &m.UserID, &m.CreatedAt,
			&u.ID, &u.Email, &u.FullName, &u.AvatarURL); err != nil {
			h.logger.Error("scan crew member", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		m.User = &u
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (crew members)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []crewMemberResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type addCrewMemberRequest struct {
	UserID string `json:"user_id"`
}

// AddMember adds a workspace user as a member of the crew.
// POST /api/v1/crews/{crewId}/members
func (h *CrewHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	// Verify crew exists and belongs to workspace
	err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("get crew for add member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req addCrewMemberRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	// Check user is a workspace member
	var wsMemberID string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		workspaceID, req.UserID).Scan(&wsMemberID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "User is not a member of this workspace"})
		return
	}
	if err != nil {
		h.logger.Error("check workspace membership", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Check not already a crew member
	var existingMemberID string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crew_members WHERE crew_id = ? AND user_id = ?",
		crewID, req.UserID).Scan(&existingMemberID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "User is already a member of this crew"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check crew membership", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	memberID := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		"INSERT INTO crew_members (id, crew_id, user_id, created_at) VALUES (?, ?, ?, ?)",
		memberID, crewID, req.UserID, now)
	if err != nil {
		h.logger.Error("insert crew member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Return member with user info
	var m crewMemberResponse
	var u memberUser
	err = h.db.QueryRowContext(r.Context(), `
		SELECT cm.id, cm.crew_id, cm.user_id, cm.created_at,
			u.id, u.email, u.full_name, u.avatar_url
		FROM crew_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.id = ?
	`, memberID).Scan(&m.ID, &m.CrewID, &m.UserID, &m.CreatedAt,
		&u.ID, &u.Email, &u.FullName, &u.AvatarURL)
	if err != nil {
		h.logger.Error("get crew member after insert", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	m.User = &u

	writeJSON(w, http.StatusCreated, m)
}

// RemoveMember removes a user from the crew.
// DELETE /api/v1/crews/{crewId}/members/{memberId}
func (h *CrewHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")
	memberID := r.PathValue("memberId")

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}
	if memberID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "memberId is required"})
		return
	}

	// Verify crew exists and belongs to workspace
	err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("get crew for remove member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Verify member exists in this crew
	var existingMemberID string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crew_members WHERE id = ? AND crew_id = ?",
		memberID, crewID).Scan(&existingMemberID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew member not found"})
			return
		}
		h.logger.Error("get crew member for remove", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		"DELETE FROM crew_members WHERE id = ? AND crew_id = ?",
		memberID, crewID)
	if err != nil {
		h.logger.Error("delete crew member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ApplyAvatarStyle updates the crew's avatar style configuration (icon and color).
// POST /api/v1/crews/{crewId}/avatar-style
func (h *CrewHandler) ApplyAvatarStyle(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	if err := crewExists(r.Context(), h.db, crewID, workspaceID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("apply avatar style: lookup crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var body struct {
		AvatarStyle string `json:"avatar_style"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if body.AvatarStyle == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "avatar_style is required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	res, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET avatar_style = ?, updated_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
		body.AvatarStyle, now, crewID)
	if err != nil {
		h.logger.Error("apply avatar style to agents", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	affected, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": affected,
		"style":   body.AvatarStyle,
	})
}

func parseAllowedDomains(raw *string) []string {
	if raw == nil || *raw == "" {
		return []string{}
	}
	var domains []string
	if err := json.Unmarshal([]byte(*raw), &domains); err != nil {
		return []string{}
	}
	return domains
}
