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
func (h *KeeperStatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
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

	// Query request stats from DB
	if h.db != nil {
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests`).Scan(&resp.TotalRequests)
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests WHERE decision='ALLOW'`).Scan(&resp.AllowCount)
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests WHERE decision='DENY'`).Scan(&resp.DenyCount)
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM keeper_requests WHERE decision='ESCALATE'`).Scan(&resp.EscalateCount)
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
