package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
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
