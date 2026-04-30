package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/paymaster"
)

// sidecarCostRecordRequest is the wire format the sidecar POSTs after it
// has parsed an LLM-provider response. Authoritative scope fields come from
// the sidecar's IPCConfig (workspace/crew/agent IDs were stamped by
// crewshipd at boot), which means an agent cannot forge a row attributed
// to a different crew or workspace by tampering with this body.
//
// Fields the sidecar has no authority over — ledger ID, timestamp,
// confidence — are derived server-side. The sidecar reports BillingMode
// because it learned that from CREWSHIP_BILLING_MODE env var (set by the
// orchestrator based on credential type at exec time), which is the
// closest thing to ground truth available at the proxy hot path.
type sidecarCostRecordRequest struct {
	WorkspaceID string `json:"workspace_id"`
	CrewID      string `json:"crew_id"`
	AgentID     string `json:"agent_id"`
	MissionID   string `json:"mission_id,omitempty"`

	Provider string `json:"provider"`
	Model    string `json:"model"`

	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CachedInputTokens   int64 `json:"cached_input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`

	BillingMode       string  `json:"billing_mode"`
	SubscriptionPlan  string  `json:"subscription_plan,omitempty"`
	QuotaRemainingPct float64 `json:"quota_remaining_pct,omitempty"`
	QuotaWindow       string  `json:"quota_window,omitempty"`
	HadStatus429      bool    `json:"had_status_429,omitempty"`
}

// handleSidecarCostRecord persists one cost_ledger row submitted by the
// sidecar after intercepting an LLM provider response. Auth is provided by
// internalAuth() middleware (X-Internal-Token).
//
// Validation:
//   - WorkspaceID, Provider, Model are required (paymaster.Record enforces
//     this too, but we return a clean 400 instead of bubbling a 500).
//   - BillingMode must be 'metered' or 'flat_rate'; defaults to 'metered'.
//   - Token counts default to zero — providers occasionally omit usage
//     blocks, and we'd rather record an audit row with zeros than drop the
//     event entirely.
//
// The handler does NOT pre-flight $ budgets — that's done at request time
// in the Go middleware path; CLI traffic bypasses the middleware so the
// budget check would have already been moot. EnforceQuota IS called when
// quota signals are present so the journal's budget.warning /
// budget.exceeded entries fire consistently across direct-API and CLI
// traffic.
//
// On success returns {"id": "<ledger id>"} with 202 Accepted because the
// underlying journal entries that pair with the ledger row are batched.
func (r *Router) handleSidecarCostRecord(w http.ResponseWriter, req *http.Request) {
	// 16 KiB is generous for a single cost row.
	req.Body = http.MaxBytesReader(w, req.Body, 16*1024)

	var body sidecarCostRecordRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if strings.TrimSpace(body.WorkspaceID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}
	if strings.TrimSpace(body.Provider) == "" || strings.TrimSpace(body.Model) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider and model required"})
		return
	}

	mode := paymaster.BillingMetered
	switch strings.ToLower(strings.TrimSpace(body.BillingMode)) {
	case "flat_rate":
		mode = paymaster.BillingFlatRate
	case "metered", "":
		mode = paymaster.BillingMetered
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "billing_mode must be 'metered' or 'flat_rate'"})
		return
	}

	// Confidence is precise when the sidecar saw a usage block in the
	// response body, else estimate. We treat any non-zero token count as
	// "saw a usage block" — it's a heuristic but a reliable one given the
	// providers we support.
	confidence := paymaster.ConfidenceEstimate
	if body.InputTokens > 0 || body.OutputTokens > 0 {
		confidence = paymaster.ConfidencePrecise
	}

	scope := paymaster.Scope{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     body.AgentID,
		MissionID:   body.MissionID,
	}

	// Cost is filled by Estimate when metered; flat-rate is forced to 0 by
	// Record. We skip Estimate here for flat_rate to avoid burning a price
	// table lookup we'll throw away.
	cost := 0.0
	if mode == paymaster.BillingMetered {
		cost = paymaster.Estimate(body.Provider, body.Model,
			body.InputTokens, body.OutputTokens,
			body.CachedInputTokens, body.CacheCreationTokens)
	}

	rec, err := paymaster.Record(req.Context(), r.db, r.Journal(), paymaster.Call{
		Scope:               scope,
		Provider:            body.Provider,
		Model:               body.Model,
		InputTokens:         body.InputTokens,
		OutputTokens:        body.OutputTokens,
		CachedInputTokens:   body.CachedInputTokens,
		CacheCreationTokens: body.CacheCreationTokens,
		CostUSD:             cost,
		BillingMode:         mode,
		Confidence:          confidence,
		SubscriptionPlan:    body.SubscriptionPlan,
		QuotaRemainingPct:   body.QuotaRemainingPct,
		QuotaWindow:         paymaster.QuotaWindow(body.QuotaWindow),
		Tags:                map[string]any{"source": "sidecar"},
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "paymaster:") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		r.logger.Error("sidecar cost record failed", "err", err, "provider", body.Provider, "model", body.Model)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record failed"})
		return
	}

	// Quota enforcement is best-effort and runs only when the sidecar
	// surfaced a signal. We don't propagate the error — the agent has
	// already received the upstream response by the time this handler
	// runs, so blocking would just delay the next call without changing
	// the outcome of this one. The journal entry it emits is the actual
	// payload the operator cares about.
	if body.HadStatus429 || body.QuotaRemainingPct > 0 {
		_ = paymaster.EnforceQuota(req.Context(), r.Journal(), scope,
			body.QuotaRemainingPct, paymaster.QuotaWindow(body.QuotaWindow), body.HadStatus429)
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"id": rec.ID})
}
