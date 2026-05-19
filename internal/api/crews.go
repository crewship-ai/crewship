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
	ID                 string            `json:"id"`
	WorkspaceID        string            `json:"workspace_id"`
	Name               string            `json:"name"`
	Slug               string            `json:"slug"`
	Description        *string           `json:"description"`
	Color              *string           `json:"color"`
	Icon               *string           `json:"icon"`
	AvatarStyle        *string           `json:"avatar_style"`
	ContainerMemoryMB  int               `json:"container_memory_mb"`
	ContainerCPUs      float64           `json:"container_cpus"`
	ContainerTTLHours  *int              `json:"container_ttl_hours"`
	NetworkMode        string            `json:"network_mode"`
	AllowedDomains     []string          `json:"allowed_domains"`
	MCPConfigJSON      *string           `json:"mcp_config_json,omitempty"`
	EscalationConfig   *string           `json:"escalation_config,omitempty"`
	RuntimeImage       *string           `json:"runtime_image,omitempty"`
	DevcontainerConfig *string           `json:"devcontainer_config,omitempty"`
	MiseConfig         *string           `json:"mise_config,omitempty"`
	ServicesJSON       *string           `json:"services_json,omitempty"`
	CachedImage        *string           `json:"cached_image,omitempty"`
	ConfigHash         *string           `json:"config_hash,omitempty"`
	IssuePrefix        *string           `json:"issue_prefix"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
	Count              crewCountResponse `json:"_count"`
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
