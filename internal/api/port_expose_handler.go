package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// DockerInspector abstracts the single Docker call the port-expose handler
// needs: looking up the IPv4 address a container has on a specific bridge
// network. Declared as an interface here (rather than importing the concrete
// Docker provider) so tests can substitute a fake and production wires up
// the real provider from router.go.
type DockerInspector interface {
	ContainerIP(ctx context.Context, containerID, network string) (string, error)
}

// DockerInspectorFunc adapts a plain function to DockerInspector. Keeps the
// router wiring concise when we only need a closure over *dockerclient.Client.
type DockerInspectorFunc func(ctx context.Context, containerID, network string) (string, error)

// ContainerIP delegates to the wrapped function.
func (f DockerInspectorFunc) ContainerIP(ctx context.Context, containerID, network string) (string, error) {
	return f(ctx, containerID, network)
}

// PortExposeConfig holds the tunables the handler reads on every request.
// Defaults come from NewPortExposeHandler; production may override via a
// WithPortExposeConfig router option in a later change.
type PortExposeConfig struct {
	// PublicBaseURL is what we hand back to the agent as the clickable URL.
	// Defaults to http://localhost:8080 for dev; in production point this at
	// the workspace's external hostname so the URL is actually reachable.
	PublicBaseURL string

	// NetworkName is the Docker bridge crew containers are attached to. A
	// container not on this network is rejected during IP lookup so we
	// don't proxy to crewshipd itself, host services, or containers that
	// belong to a different Crewship instance.
	NetworkName string

	// DefaultTTL and MaxTTL bound the lifetime of each exposure. Agent can
	// request ttl_seconds in the payload but we clamp to [1, MaxTTL].
	DefaultTTL time.Duration
	MaxTTL     time.Duration

	// Rate limits. Counts apply to rows currently in ACTIVE or PENDING
	// status. EXPIRED/REVOKED rows don't count.
	MaxActivePerAgent     int
	MaxActivePerWorkspace int
}

// DefaultPortExposeConfig returns the config defaults that don't depend on
// deployment-specific state. PublicBaseURL is intentionally left empty so
// that a misconfigured install (no CREWSHIP_PUBLIC_URL) surfaces as a
// visible error on the first expose request rather than silently handing
// agents localhost URLs nobody can reach.
func DefaultPortExposeConfig() PortExposeConfig {
	return PortExposeConfig{
		PublicBaseURL:         "", // caller must set via WithPortExposePublicURL
		NetworkName:           "crewship-agents",
		DefaultTTL:            time.Hour,
		MaxTTL:                24 * time.Hour,
		MaxActivePerAgent:     5,
		MaxActivePerWorkspace: 20,
	}
}

// PortExposeHandler serves the four HTTP concerns related to port exposures:
// the internal request endpoint hit by the sidecar, the user-facing list and
// revoke endpoints, and the reverse-proxy path that users' browsers hit.
type PortExposeHandler struct {
	db       *sql.DB
	registry *PortExposeRegistry
	docker   DockerInspector
	policy   PortExposePolicy
	hub      *ws.Hub
	logger   *slog.Logger
	cfg      PortExposeConfig
}

// NewPortExposeHandler wires the dependencies. Missing optional pieces get
// safe fallbacks (nop policy, default logger) so the zero-value router
// option doesn't panic on null deref.
func NewPortExposeHandler(
	db *sql.DB,
	registry *PortExposeRegistry,
	docker DockerInspector,
	policy PortExposePolicy,
	hub *ws.Hub,
	cfg PortExposeConfig,
	logger *slog.Logger,
) *PortExposeHandler {
	if policy == nil {
		policy = AllowAllPolicy{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = time.Hour
	}
	if cfg.MaxTTL <= 0 {
		cfg.MaxTTL = 24 * time.Hour
	}
	if cfg.MaxActivePerAgent <= 0 {
		cfg.MaxActivePerAgent = 5
	}
	if cfg.MaxActivePerWorkspace <= 0 {
		cfg.MaxActivePerWorkspace = 20
	}
	if cfg.NetworkName == "" {
		cfg.NetworkName = "crewship-agents"
	}
	return &PortExposeHandler{
		db:       db,
		registry: registry,
		docker:   docker,
		policy:   policy,
		hub:      hub,
		logger:   logger,
		cfg:      cfg,
	}
}

// requestPayload is what the sidecar POSTs. All the contextual ids
// (workspace, crew, agent, container, chat) come from the sidecar's IPC
// config — NOT from the agent's process — so an agent can't request an
// exposure targeting some other container in the workspace.
type requestPayload struct {
	WorkspaceID string `json:"workspace_id"`
	CrewID      string `json:"crew_id"`
	AgentID     string `json:"agent_id"`
	ContainerID string `json:"container_id"`
	ChatID      string `json:"chat_id"`
	Port        int    `json:"port"`
	Description string `json:"description"`
	TTLSeconds  int    `json:"ttl_seconds,omitempty"`
}

// requestResponse is handed back to the sidecar and ultimately to the agent.
type requestResponse struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// RequestExpose is POST /api/v1/internal/port-expose. The sidecar calls it
// after validating the agent's request payload; this handler trusts the IPC
// context but still enforces the agent↔crew↔workspace boundary against the
// DB in case the sidecar config ever diverges from DB reality.
func (h *PortExposeHandler) RequestExpose(w http.ResponseWriter, r *http.Request) {
	// Fail fast if the operator didn't configure where to point clients.
	// Returning a url built from "" would hand the agent a broken link.
	if h.cfg.PublicBaseURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "port-expose not configured: set CREWSHIP_PUBLIC_URL to this server's externally reachable origin",
		})
		return
	}
	var body requestPayload
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	if body.WorkspaceID == "" || body.CrewID == "" || body.AgentID == "" ||
		body.ContainerID == "" || body.Port < 1 || body.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "workspace_id, crew_id, agent_id, container_id, and port (1-65535) are required",
		})
		return
	}
	if len(body.Description) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description too long (max 200 chars)"})
		return
	}

	// Anti-spoof: verify the agent actually belongs to the crew + workspace
	// the sidecar claims. This mirrors the pattern KeeperHandler uses.
	agentSlug, err := h.validateAgentBoundary(r.Context(), body.AgentID, body.CrewID, body.WorkspaceID)
	if err != nil {
		if errors.Is(err, errAgentNotFound) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "agent does not belong to the requested crew/workspace"})
			return
		}
		h.logger.Error("port_expose: agent boundary check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Clamp TTL into config bounds. Zero / negative falls back to the default.
	ttl := h.cfg.DefaultTTL
	if body.TTLSeconds > 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	if ttl > h.cfg.MaxTTL {
		ttl = h.cfg.MaxTTL
	}
	if ttl < time.Second {
		ttl = time.Second
	}

	// Rate limits: count rows the agent already has live.
	if err := h.checkQuota(r.Context(), body.AgentID, body.WorkspaceID); err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}

	// Policy decision. MVP's AllowAllPolicy returns "allow". A future
	// ApprovalPolicy would return "pending" here; the handler doesn't
	// branch into the approval flow yet because the sidecar synchronous
	// response shape doesn't support it. When we add approval, the
	// "pending" branch writes a PENDING row, creates an escalation, and
	// the sidecar switches to long-poll — no schema changes.
	decision, reason, err := h.policy.Check(r.Context(), &PortExposeRequest{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     body.AgentID,
		AgentSlug:   agentSlug,
		Port:        body.Port,
		Description: body.Description,
		TTLSeconds:  int(ttl.Seconds()),
	})
	if err != nil {
		h.logger.Error("port_expose: policy check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	switch decision {
	case ExposeDeny:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "denied by policy: " + reason})
		return
	case ExposePending:
		// Reserved for a future change. Surface a clear error so the sidecar
		// doesn't silently misbehave if someone wires ApprovalPolicy before
		// the approval long-poll lands.
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "approval-required policy not yet supported"})
		return
	case ExposeAllow:
		// fall through
	default:
		h.logger.Error("port_expose: unknown policy decision", "decision", string(decision))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Look up the container's IP on the crew bridge. Rejecting here also
	// blocks the agent from asking us to proxy to crewshipd or host
	// services — those aren't on the crewship-agents network.
	containerIP, err := h.docker.ContainerIP(r.Context(), body.ContainerID, h.cfg.NetworkName)
	if err != nil {
		h.logger.Warn("port_expose: container IP lookup",
			"container_id", body.ContainerID, "network", h.cfg.NetworkName, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "container not reachable on crew network"})
		return
	}

	token, err := generateExposeToken()
	if err != nil {
		h.logger.Error("port_expose: token generation", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	id := generateCUID()
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	var chatIDVal interface{}
	if body.ChatID != "" {
		chatIDVal = body.ChatID
	}
	var descVal interface{}
	if body.Description != "" {
		descVal = body.Description
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO port_exposures (
			id, workspace_id, crew_id, agent_id, chat_id, token,
			container_id, container_ip, container_port, description,
			status, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ACTIVE', ?, ?)
	`,
		id, body.WorkspaceID, body.CrewID, body.AgentID, chatIDVal, token,
		body.ContainerID, containerIP, body.Port, descVal,
		expiresAt.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		h.logger.Error("port_expose: insert row", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	entry := &ExposeEntry{
		ID:            id,
		Token:         token,
		ContainerID:   body.ContainerID,
		ContainerIP:   containerIP,
		ContainerPort: body.Port,
		ExpiresAt:     expiresAt,
	}
	h.registry.Add(entry)

	url := h.exposeURL(token)

	if h.hub != nil {
		payload := map[string]string{
			"id":          id,
			"agent_id":    body.AgentID,
			"agent_slug":  agentSlug,
			"crew_id":     body.CrewID,
			"description": body.Description,
			"url":         url,
			"expires_at":  expiresAt.Format(time.RFC3339),
		}
		broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "port_expose.created", payload)
		if body.ChatID != "" {
			broadcastChannelEvent(h.hub, "session", body.ChatID, "port_expose_created", payload)
		}
	}

	h.logger.Info("port exposure created",
		"id", id, "agent_slug", agentSlug, "crew_id", body.CrewID,
		"port", body.Port, "ttl", ttl, "policy_reason", reason)

	writeJSON(w, http.StatusCreated, requestResponse{
		ID:        id,
		Token:     token,
		URL:       url,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

// listItem is the row shape returned by List. Token is intentionally omitted
// so a MEMBER without revoke rights can't reconstruct the public URL of
// someone else's exposure — only the issuing agent gets the URL.
type listItem struct {
	ID            string  `json:"id"`
	AgentID       string  `json:"agent_id"`
	AgentSlug     string  `json:"agent_slug"`
	ContainerPort int     `json:"container_port"`
	Description   string  `json:"description,omitempty"`
	Status        string  `json:"status"`
	ExpiresAt     string  `json:"expires_at"`
	RevokedAt     *string `json:"revoked_at,omitempty"`
	RevokedReason *string `json:"revoked_reason,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// List is GET /api/v1/crews/{crewId}/port-expose. Any workspace MEMBER can
// list exposures for their crew. Filtering by ?status=active|revoked|expired
// is optional; default is ACTIVE only (matches the CLI's common case).
func (h *PortExposeHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId required"})
		return
	}

	statusFilter := strings.ToUpper(r.URL.Query().Get("status"))
	if statusFilter == "" {
		statusFilter = "ACTIVE"
	}
	if statusFilter != "ACTIVE" && statusFilter != "REVOKED" && statusFilter != "EXPIRED" && statusFilter != "ALL" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "status must be active, revoked, expired, or all",
		})
		return
	}

	var rows *sql.Rows
	var err error
	const baseQuery = `
		SELECT pe.id, pe.agent_id, a.slug, pe.container_port, pe.description,
		       pe.status, pe.expires_at, pe.revoked_at, pe.revoked_reason, pe.created_at
		FROM port_exposures pe
		JOIN agents a ON a.id = pe.agent_id
		WHERE pe.workspace_id = ? AND pe.crew_id = ?
	`
	if statusFilter == "ALL" {
		rows, err = h.db.QueryContext(r.Context(), baseQuery+" ORDER BY pe.created_at DESC", workspaceID, crewID)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			baseQuery+" AND pe.status = ? ORDER BY pe.created_at DESC",
			workspaceID, crewID, statusFilter)
	}
	if err != nil {
		h.logger.Error("port_expose: list query", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	out := make([]listItem, 0, 8)
	for rows.Next() {
		var it listItem
		var description, revokedAt, revokedReason sql.NullString
		if err := rows.Scan(&it.ID, &it.AgentID, &it.AgentSlug, &it.ContainerPort,
			&description, &it.Status, &it.ExpiresAt, &revokedAt, &revokedReason, &it.CreatedAt); err != nil {
			h.logger.Error("port_expose: list scan", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if description.Valid {
			it.Description = description.String
		}
		if revokedAt.Valid {
			v := revokedAt.String
			it.RevokedAt = &v
		}
		if revokedReason.Valid {
			v := revokedReason.String
			it.RevokedReason = &v
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("port_expose: list rows err", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, out)
}

// revokePayload carries the human-provided reason. Not required, but strongly
// encouraged — it's what shows up in audit queries.
type revokePayload struct {
	Reason string `json:"reason"`
}

// Revoke is POST /api/v1/crews/{crewId}/port-expose/{id}/revoke. Flips the
// row to REVOKED, drops the token from the registry, and broadcasts so any
// UI showing the live list updates. Requires MANAGER+ to match the policy
// used by issue review / escalation resolve.
func (h *PortExposeHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}
	crewID := r.PathValue("crewId")
	exposeID := r.PathValue("id")
	if crewID == "" || exposeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId and id required"})
		return
	}

	var body revokePayload
	// Body is optional — ignore decode error on empty payloads.
	_ = readJSON(r, &body)
	if len(body.Reason) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason too long (max 500)"})
		return
	}

	// Two-phase: conditional UPDATE (status was ACTIVE or PENDING) + lookup
	// token afterwards so we can drop it from the registry and broadcast.
	now := time.Now().UTC().Format(time.RFC3339)
	var reasonVal interface{}
	if body.Reason != "" {
		reasonVal = body.Reason
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE port_exposures
		SET status = 'REVOKED', revoked_at = ?, revoked_reason = ?
		WHERE id = ? AND workspace_id = ? AND crew_id = ? AND status IN ('ACTIVE','PENDING')
	`, now, reasonVal, exposeID, workspaceID, crewID)
	if err != nil {
		h.logger.Error("port_expose: revoke update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "exposure not found or already revoked/expired"})
		return
	}

	// Fetch token to clear the in-memory entry.
	var token string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT token FROM port_exposures WHERE id = ?`, exposeID).Scan(&token); err == nil && token != "" {
		h.registry.Remove(token)
	}

	if h.hub != nil {
		broadcastWorkspaceEvent(h.hub, workspaceID, "port_expose.revoked", map[string]string{
			"id":      exposeID,
			"crew_id": crewID,
			"reason":  body.Reason,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ServeExposed handles GET /exposed/{token}/... — the capability URL the
// agent hands to the user. No authentication: the token IS the capability.
// Unknown tokens get 404, expired get 410, WebSocket upgrade gets 426.
// Everything else is reverse-proxied into the target container.
//
// The container IP stored on the entry is re-verified against Docker on
// every request when a DockerInspector is wired up. Crew containers can
// restart and grab a different bridge IP; we'd rather do one docker
// inspect per request (~1ms locally) than silently forward to whoever
// inherited the old IP.
func (h *PortExposeHandler) ServeExposed(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	entry, ok := h.registry.Lookup(token)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if entry.Expired(time.Now().UTC()) {
		http.Error(w, "gone (expired)", http.StatusGone)
		return
	}

	// MVP does not proxy WebSocket upgrades — the code path is small but
	// the security surface isn't. Block the upgrade outright rather than
	// silently downgrading to HTTP.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket not supported", http.StatusUpgradeRequired)
		return
	}

	// Resolve the live container IP. If the DockerInspector is not wired
	// (unit tests) we fall back to the cached IP on the entry.
	target := entry.Target()
	if h.docker != nil && entry.ContainerID != "" {
		freshIP, err := h.docker.ContainerIP(r.Context(), entry.ContainerID, h.cfg.NetworkName)
		if err != nil {
			h.logger.Warn("port_expose proxy: container IP re-resolve failed",
				"token", safeTokenPrefix(token), "container_id", entry.ContainerID, "error", err)
			http.Error(w, "bad gateway: container unreachable", http.StatusBadGateway)
			return
		}
		if freshIP != entry.ContainerIP {
			// Container was recreated and re-IP'd. Update the cache so
			// future requests skip the "IP changed" branch until the next
			// restart.
			h.registry.UpdateIP(token, freshIP)
		}
		target = &url.URL{
			Scheme: "http",
			Host:   freshIP + ":" + strconv.Itoa(entry.ContainerPort),
		}
	}

	// Strip the /exposed/{token} prefix before handing the request to the
	// reverse proxy so the target sees the URL it would normally see.
	prefix := "/exposed/" + token
	r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	r.URL.RawPath = ""

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = slog.NewLogLogger(h.logger.Handler(), slog.LevelWarn)
	proxy.ErrorHandler = func(rw http.ResponseWriter, rq *http.Request, err error) {
		h.logger.Warn("port_expose proxy error",
			"token", safeTokenPrefix(token), "target", target.String(), "error", err)
		http.Error(rw, "bad gateway", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// ----- helpers -----

// errAgentNotFound is returned from validateAgentBoundary when the (agent,
// crew, workspace) tuple doesn't exist. Callers convert it to 403.
var errAgentNotFound = errors.New("agent not in crew/workspace")

func (h *PortExposeHandler) validateAgentBoundary(ctx context.Context, agentID, crewID, workspaceID string) (string, error) {
	var slug string
	err := h.db.QueryRowContext(ctx, `
		SELECT a.slug FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.crew_id = ? AND c.workspace_id = ? AND a.deleted_at IS NULL
	`, agentID, crewID, workspaceID).Scan(&slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errAgentNotFound
		}
		return "", err
	}
	return slug, nil
}

func (h *PortExposeHandler) checkQuota(ctx context.Context, agentID, workspaceID string) error {
	var perAgent, perWorkspace int
	err := h.db.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM port_exposures WHERE agent_id = ?     AND status IN ('ACTIVE','PENDING')),
		  (SELECT COUNT(*) FROM port_exposures WHERE workspace_id = ? AND status IN ('ACTIVE','PENDING'))
	`, agentID, workspaceID).Scan(&perAgent, &perWorkspace)
	if err != nil {
		return fmt.Errorf("quota check: %w", err)
	}
	if perAgent >= h.cfg.MaxActivePerAgent {
		return fmt.Errorf("agent has %d active exposures (max %d)", perAgent, h.cfg.MaxActivePerAgent)
	}
	if perWorkspace >= h.cfg.MaxActivePerWorkspace {
		return fmt.Errorf("workspace has %d active exposures (max %d)", perWorkspace, h.cfg.MaxActivePerWorkspace)
	}
	return nil
}

func (h *PortExposeHandler) exposeURL(token string) string {
	base := strings.TrimRight(h.cfg.PublicBaseURL, "/")
	return base + "/exposed/" + token + "/"
}

// generateExposeToken returns 32 random bytes encoded as url-safe base64
// (43 chars, no padding). The token is a capability; anyone with it can
// reach the forwarded port until expiry.
func generateExposeToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// safeTokenPrefix returns the first 8 chars of a token for logging, so we
// don't leak full capability tokens into log aggregators.
func safeTokenPrefix(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "…"
}
