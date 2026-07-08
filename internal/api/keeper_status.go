package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// KeeperStatusHandler provides the Keeper health and configuration status endpoint.
type KeeperStatusHandler struct {
	db     *sql.DB
	cfg    *config.KeeperConfig
	gk     gatekeeper.Evaluator
	logger *slog.Logger
}

// NewKeeperStatusHandler creates a KeeperStatusHandler with the given configuration and gatekeeper evaluator.
func NewKeeperStatusHandler(db *sql.DB, cfg *config.KeeperConfig, gk gatekeeper.Evaluator, logger *slog.Logger) *KeeperStatusHandler {
	return &KeeperStatusHandler{db: db, cfg: cfg, gk: gk, logger: logger}
}

type keeperStatusResponse struct {
	Enabled       bool   `json:"enabled"`
	OllamaURL     string `json:"ollama_url,omitempty"`
	Model         string `json:"model,omitempty"`
	OllamaOnline  bool   `json:"ollama_online"`
	GatekeeperSet bool   `json:"gatekeeper_configured"`
	TotalRequests int    `json:"total_requests"`
	AllowCount    int    `json:"allow_count"`
	DenyCount     int    `json:"deny_count"`
	EscalateCount int    `json:"escalate_count"`
}

// Status returns the current Keeper configuration and health status.
// GET /api/v1/system/keeper
//
// Gated ADMIN+ at the route (authedAdmin, #865) — the Ollama URL/model and
// request stats are operational data, not for every workspace member. The
// request counts are scoped to the caller's workspace: keeper_requests has no
// direct workspace_id, so we filter through the requesting agent's workspace
// exactly as the keeper audit log does (keeper_log.go), instead of the old
// instance-wide COUNT that leaked cross-tenant volume.
func (h *KeeperStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	resp := keeperStatusResponse{
		GatekeeperSet: h.gk != nil,
	}

	if h.cfg != nil {
		resp.Enabled = h.cfg.Enabled
		resp.OllamaURL = h.cfg.OllamaURL
		resp.Model = h.cfg.Model
	}

	// Probe Ollama health if configured
	if resp.Enabled && resp.OllamaURL != "" {
		resp.OllamaOnline = probeOllama(r.Context(), resp.OllamaURL)
	}

	// Query request stats from DB, scoped to this workspace's agents.
	if h.db != nil {
		const inWorkspace = ` WHERE requesting_agent_id IN (SELECT id FROM agents WHERE workspace_id = ?)`
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests`+inWorkspace, workspaceID).Scan(&resp.TotalRequests)
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests`+inWorkspace+` AND decision='ALLOW'`, workspaceID).Scan(&resp.AllowCount)
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests`+inWorkspace+` AND decision='DENY'`, workspaceID).Scan(&resp.DenyCount)
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests`+inWorkspace+` AND decision='ESCALATE'`, workspaceID).Scan(&resp.EscalateCount)
	}

	writeJSON(w, http.StatusOK, resp)
}

// probeOllama checks if the Ollama server is reachable.
func probeOllama(ctx context.Context, ollamaURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, ollamaURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
