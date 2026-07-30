package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// KeeperStatusHandler provides the Keeper health and configuration status endpoint.
type KeeperStatusHandler struct {
	db       *sql.DB
	cfg      *config.KeeperConfig
	settings *keepercfg.Store
	gk       gatekeeper.Evaluator
	govModel GovModelStatusProvider
	logger   *slog.Logger
}

// NewKeeperStatusHandler creates a KeeperStatusHandler with the given configuration and gatekeeper evaluator.
func NewKeeperStatusHandler(db *sql.DB, cfg *config.KeeperConfig, gk gatekeeper.Evaluator, logger *slog.Logger) *KeeperStatusHandler {
	return &KeeperStatusHandler{db: db, cfg: cfg, gk: gk, logger: logger}
}

// WithKeeperSettings wires the runtime instance judge configuration so the
// status card reports what is IN FORCE rather than what the process booted with.
// Without it, an operator who repointed the judge at runtime would keep reading
// the old endpoint back — the diagnosis this card exists for would be a lie.
// nil-safe: unset leaves the handler reading cfg.Keeper directly.
func (h *KeeperStatusHandler) WithKeeperSettings(s *keepercfg.Store) *KeeperStatusHandler {
	h.settings = s
	return h
}

// WithGovModelStatus wires the per-workspace governance-model status provider
// (M2a, #1001) so the status card can surface a configured gov model and any
// §4.4 degrade. nil-safe: unset leaves the gov-model fields empty. Returns the
// handler for chaining at the router call site.
func (h *KeeperStatusHandler) WithGovModelStatus(s GovModelStatusProvider) *KeeperStatusHandler {
	h.govModel = s
	return h
}

type keeperStatusResponse struct {
	Enabled      bool   `json:"enabled"`
	OllamaURL    string `json:"ollama_url,omitempty"`
	Model        string `json:"model,omitempty"`
	OllamaOnline bool   `json:"ollama_online"`
	// OllamaProbed separates "we dialled and got nothing" from "we never
	// dialled". The probe used to be skipped whenever the engine was off, and
	// ollama_online then stayed false — so every disabled instance reported its
	// model server as OFFLINE, which is the single most confusing thing the status
	// card can say to somebody who is configuring Keeper for the first time and
	// has not turned it on yet. The probe now runs whenever an endpoint is
	// configured (it is a 3s HEAD against a loopback or LAN address, and knowing
	// the endpoint answers BEFORE enabling is the whole point of checking).
	OllamaProbed  bool `json:"ollama_probed"`
	GatekeeperSet bool `json:"gatekeeper_configured"`
	TotalRequests int  `json:"total_requests"`
	AllowCount    int  `json:"allow_count"`
	DenyCount     int  `json:"deny_count"`
	EscalateCount int  `json:"escalate_count"`
	SecretCount   int  `json:"secret_count"`

	// Provenance of the three fields above: "default", "env" (KEEPER_* at boot)
	// or "instance" (a runtime override). Without this, "enabled: false" cannot
	// be told apart from "an operator turned it off here", which is exactly the
	// question an admin looking at a dead judge is asking.
	EnabledSource   string `json:"enabled_source,omitempty"`
	OllamaURLSource string `json:"ollama_url_source,omitempty"`
	ModelSource     string `json:"model_source,omitempty"`

	// Governance model (M2a, #1001). GovModelConfigured=false → the workspace
	// uses the server default judge (the OllamaURL/Model above). When degraded,
	// a revoked/broken gov-model credential fell back to the default judge —
	// GovModelDegradeReason says why (§4.4 revoke-safety).
	GovModelConfigured    bool   `json:"gov_model_configured"`
	GovModelProvider      string `json:"gov_model_provider,omitempty"`
	GovModelName          string `json:"gov_model,omitempty"`
	GovModelDegraded      bool   `json:"gov_model_degraded"`
	GovModelDegradeReason string `json:"gov_model_degrade_reason,omitempty"`
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

	// The store is the authority when wired: it layers any runtime override over
	// the same cfg.Keeper, so reading cfg directly would report the boot-time
	// values even after an operator changed them.
	if h.settings != nil {
		eff := h.settings.Effective()
		resp.Enabled = eff.Enabled.Value
		resp.OllamaURL = eff.EndpointURL.Value
		resp.Model = eff.Model.Value
		resp.EnabledSource = string(eff.Enabled.Source)
		resp.OllamaURLSource = string(eff.EndpointURL.Source)
		resp.ModelSource = string(eff.Model.Source)
		// A wired gatekeeper that Keeper has switched off is not a configured
		// gatekeeper: the evaluator is always attached now (it builds lazily),
		// so this bit has to come from the setting rather than from a non-nil
		// interface, or the card would claim a judge that never runs.
		resp.GatekeeperSet = h.gk != nil && eff.Enabled.Value
	} else if h.cfg != nil {
		resp.Enabled = h.cfg.Enabled
		resp.OllamaURL = h.cfg.OllamaURL
		resp.Model = h.cfg.Model
	}

	// Probe whenever there is an endpoint to probe, enabled or not: an operator
	// setting Keeper up needs to know the endpoint answers before they turn it on,
	// and reporting "offline" for an instance we never dialled is worse than
	// saying nothing.
	if resp.OllamaURL != "" {
		resp.OllamaProbed = true
		resp.OllamaOnline = probeOllama(r.Context(), resp.OllamaURL)
	}

	// Per-workspace governance model (M2a, #1001) + any §4.4 degrade.
	if h.govModel != nil {
		gm := h.govModel.Status(r.Context(), workspaceID)
		resp.GovModelConfigured = gm.Configured
		resp.GovModelProvider = gm.Provider
		resp.GovModelName = gm.Model
		resp.GovModelDegraded = gm.Degraded
		resp.GovModelDegradeReason = gm.Reason
	}

	// Query request stats from DB, scoped to this workspace's agents.
	if h.db != nil {
		const inWorkspace = ` WHERE requesting_agent_id IN (SELECT id FROM agents WHERE workspace_id = ?)`
		// #1055: one conditional-aggregate scan instead of four separate
		// COUNT(*) passes over the append-only, unbounded keeper_requests
		// (which has no workspace_id column and no (agent, decision) index, so
		// each pass is a scan). COALESCE guards the empty-table SUM→NULL case.
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*),
			        COALESCE(SUM(CASE WHEN decision='ALLOW' THEN 1 ELSE 0 END), 0),
			        COALESCE(SUM(CASE WHEN decision='DENY' THEN 1 ELSE 0 END), 0),
			        COALESCE(SUM(CASE WHEN decision='ESCALATE' THEN 1 ELSE 0 END), 0)
			 FROM keeper_requests`+inWorkspace, workspaceID).
			Scan(&resp.TotalRequests, &resp.AllowCount, &resp.DenyCount, &resp.EscalateCount)
		// Keeper-managed secrets in this workspace — same predicate the
		// SecretStore loads with (keeper/secrets/store.go), workspace-scoped.
		// The CLI has always printed this field; it was documented output
		// that the server never returned (always rendered 0).
		h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM credentials
			 WHERE workspace_id = ? AND type = 'SECRET' AND status = 'ACTIVE' AND deleted_at IS NULL`,
			workspaceID).Scan(&resp.SecretCount)
	}

	writeJSON(w, http.StatusOK, resp)
}

// probeOllama checks if the Ollama server is reachable.
//
// The URL used to come from the process environment only. With the runtime
// settings store wired it is whatever an OWNER/ADMIN last PUT to
// /admin/keeper/config, which turns this into a dial of an API-supplied address
// — so it goes through the same fence as the judge itself
// (httpsafe.TrustedEndpointClient): private and loopback stay reachable because
// that is where operators run Ollama, the hard tier does not, and redirects are
// not followed. Only a boolean escapes to the caller, but a read path is exactly
// where a weaker trust decision goes unnoticed.
func probeOllama(ctx context.Context, ollamaURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, ollamaURL, nil)
	if err != nil {
		return false
	}
	resp, err := httpsafe.TrustedEndpointClient(3 * time.Second).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
