package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/provider"
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

// crewProvisionEnqueuer kicks off an async devcontainer build for a crew.
// Satisfied by *ProvisioningHandler; an interface so the crew handler stays
// testable and works with provisioning disabled (nil = skip).
type crewProvisionEnqueuer interface {
	EnqueueForCrew(ctx context.Context, crewID, workspaceID string) (EnqueueResult, error)
}

type CrewHandler struct {
	db          *sql.DB
	hub         *ws.Hub
	logger      *slog.Logger
	license     *license.License
	socketPath  string
	provisioner crewProvisionEnqueuer
	// container reaches the live container runtime for the /services live
	// inventory endpoint (#services). nil when Docker isn't wired (tests,
	// --no-docker) — Services then answers an empty list rather than
	// erroring. Set via SetContainer.
	container provider.ContainerProvider
}

// NewCrewHandler creates a CrewHandler with the given database and logger.

func NewCrewHandler(db *sql.DB, logger *slog.Logger) *CrewHandler {
	return &CrewHandler{db: db, logger: logger}
}

// SetHub attaches a WebSocket hub for broadcasting crew events.

func (h *CrewHandler) SetHub(hub *ws.Hub) { h.hub = hub }

// SetProvisioner wires proactive provisioning: when a crew is created or its
// devcontainer/mise config changes, the build is kicked off in the background
// straight away — so the crew is runnable by the time the operator dispatches
// an issue, without them ever touching a "Build now" button. nil disables it
// (the dispatch-time gate still provisions lazily as a safety net).
func (h *CrewHandler) SetProvisioner(p crewProvisionEnqueuer) { h.provisioner = p }

// maybeAutoProvision enqueues a background devcontainer build when the crew's
// config actually needs one. Fire-and-forget: EnqueueForCrew returns as soon as
// the job is spawned (and is idempotent — a no-op if a build is already in
// flight), and the provision.* WS events light up the toolbar popover. Failures
// to enqueue are logged, not surfaced — the dispatch-time EnsureProvisioned gate
// will retry and surface a real error if the crew is run before it's ready.
func (h *CrewHandler) maybeAutoProvision(crewID, workspaceID, devcontainerCfg, miseCfg string) {
	if h.provisioner == nil || !crewNeedsProvision(devcontainerCfg, miseCfg) {
		return
	}
	if _, err := h.provisioner.EnqueueForCrew(context.Background(), crewID, workspaceID); err != nil {
		h.logger.Warn("proactive auto-provision enqueue failed (dispatch gate will retry)",
			"crew_id", crewID, "error", err)
	}
}

// derefStr returns the pointed-to string or "" for a nil *string.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *CrewHandler) broadcastCrewEvent(eventType, workspaceID string, payload map[string]string) {
	broadcastWorkspaceEvent(h.hub, workspaceID, eventType, payload)
}

// SetLicense attaches the license for enforcing crew count limits.

func (h *CrewHandler) SetLicense(lic *license.License) { h.license = lic }

// SetSocketPath sets the Unix socket path used to restart crew containers via IPC.

func (h *CrewHandler) SetSocketPath(path string) { h.socketPath = path }

// SetContainer wires the container provider used by the live service
// inventory endpoint (GET /api/v1/crews/{crewId}/services).
func (h *CrewHandler) SetContainer(cp provider.ContainerProvider) { h.container = cp }

// restartCrewContainer stops the crew container via IPC so it gets recreated
// with the new network policy on the next agent run.
//
// The caller passes a context with WithoutCancel applied (the
// request that triggered the restart has already returned 200, so
// we don't want its cancellation to propagate here -- but we do
// want its OTel span + auth values so the sidecar call is
// observable and audited). 60-second timeout still applies via
// the http.Client to bound the outbound call.

func (h *CrewHandler) restartCrewContainer(ctx context.Context, crewID string) {
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
	reqURL := fmt.Sprintf("http://crewshipd/crews/%s/container/stop", url.PathEscape(crewID))
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, nil)
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

// ContainerStatus proxies a crew's container status from crewshipd over the
// IPC unix socket. It backs the dashboard's restart-progress feedback after a
// network-policy change (which stops the container so it gets recreated with
// the new policy on the next run) and the `crewship crew container-status` CLI
// command.
//
// GET /api/v1/crews/{crewId}/container-status
//
// Failure modes are deliberately soft: the endpoint answers 200 with a coarse
// status string ("not_configured" when there is no IPC socket, "unknown" when
// crewshipd is unreachable) rather than a 5xx, because the caller is a polling
// UI/CLI that treats those as transient "still settling" states, not errors.
// Only crew-not-found (workspace scoping) and a missing id are hard failures.
func (h *CrewHandler) ContainerStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Scope to the caller's workspace — never leak another workspace's crew.
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "container status: crew lookup", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}

	if h.socketPath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "status": "not_configured"})
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", h.socketPath)
			},
		},
	}
	// Key the IPC call by the raw crew ID (matching restartCrewContainer); the
	// crewshipd side resolves slug→container name before inspecting Docker.
	reqURL := fmt.Sprintf("http://crewshipd/crews/%s/container/status", url.PathEscape(crewID))
	ipcReq, err := http.NewRequestWithContext(r.Context(), "GET", reqURL, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "status": "unknown"})
		return
	}
	resp, err := client.Do(ipcReq)
	if err != nil {
		h.logger.Debug("container status via IPC (may not be running)", "crew_id", crewID, "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "status": "unknown"})
		return
	}
	defer resp.Body.Close()

	var ipcResp map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ipcResp); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"crew_id": crewID, "status": "unknown"})
		return
	}
	// Always stamp the caller's crew ID — never trust the IPC-supplied value.
	ipcResp["crew_id"] = crewID
	writeJSON(w, http.StatusOK, ipcResp)
}

type crewCountResponse struct {
	Agents  int `json:"agents"`
	Members int `json:"members"`
}

type crewResponse struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	Name              string   `json:"name"`
	Slug              string   `json:"slug"`
	Description       *string  `json:"description"`
	Color             *string  `json:"color"`
	Icon              *string  `json:"icon"`
	AvatarStyle       *string  `json:"avatar_style"`
	ContainerMemoryMB int      `json:"container_memory_mb"`
	ContainerCPUs     float64  `json:"container_cpus"`
	ContainerTTLHours *int     `json:"container_ttl_hours"`
	NetworkMode       string   `json:"network_mode"`
	AllowedDomains    []string `json:"allowed_domains"`
	// AllowPrivateEndpoints (#961) opts this crew into reaching a
	// private/LAN model endpoint (RFC1918/loopback); link-local/metadata
	// stay blocked regardless. Default false = strict SSRF fence.
	AllowPrivateEndpoints bool    `json:"allow_private_endpoints"`
	MCPConfigJSON         *string `json:"mcp_config_json,omitempty"`
	EscalationConfig      *string `json:"escalation_config,omitempty"`
	RuntimeImage          *string `json:"runtime_image,omitempty"`
	DevcontainerConfig    *string `json:"devcontainer_config,omitempty"`
	MiseConfig            *string `json:"mise_config,omitempty"`
	ServicesJSON          *string `json:"services_json,omitempty"`
	CachedImage           *string `json:"cached_image,omitempty"`
	ConfigHash            *string `json:"config_hash,omitempty"`
	IssuePrefix           *string `json:"issue_prefix"`
	// MaxEphemeralAgents is the per-crew quota enforced by the hire
	// flow (see agents_hire.go). Surfaced on the crew response so the
	// PR-G policy panel can render + PATCH it without a second fetch.
	MaxEphemeralAgents int               `json:"max_ephemeral_agents"`
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

// scanCrewRow scans one crew row in the shared column order used by the
// List / Get / Update SELECTs and applies the parseAllowedDomains
// post-step on success. The three queries are NOT column-identical —
// Get additionally selects issue_prefix (after escalation_config) and
// Update omits services_json — so the two optional columns are toggled
// by flags to keep the destination list aligned with each caller's
// SELECT exactly. Works for both *sql.Row and *sql.Rows via the Scan
// interface.
func scanCrewRow(sc interface{ Scan(...any) error }, c *crewResponse, withIssuePrefix, withServicesJSON bool) error {
	var allowedDomainsJSON *string
	dest := make([]any, 0, 28)
	dest = append(dest, &c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
		&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
		&c.ContainerTTLHours, &c.NetworkMode, &allowedDomainsJSON, &c.AllowPrivateEndpoints,
		&c.MCPConfigJSON, &c.EscalationConfig)
	if withIssuePrefix {
		dest = append(dest, &c.IssuePrefix)
	}
	dest = append(dest, &c.RuntimeImage, &c.DevcontainerConfig, &c.MiseConfig)
	if withServicesJSON {
		dest = append(dest, &c.ServicesJSON)
	}
	dest = append(dest, &c.CachedImage, &c.ConfigHash, &c.MaxEphemeralAgents,
		&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members)
	if err := sc.Scan(dest...); err != nil {
		return err
	}
	c.AllowedDomains = parseAllowedDomains(allowedDomainsJSON)
	return nil
}
