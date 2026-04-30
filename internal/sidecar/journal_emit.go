package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// buildEgressObserver returns a proxy-side egress hook that emits one
// network.egress journal entry per allowed outbound HTTP request. The
// emit is asynchronous so the proxy hot path stays cheap — a saturated
// journal endpoint or transient crewshipd restart doesn't back-pressure
// agent traffic.
//
// This design choice is deliberate: the live audit signal the user cares
// about ("my agent called X") is already captured by the proxy logger.
// The journal entry is the persistence + UI surface, which can tolerate
// being a few milliseconds behind the wire.
func (s *Server) buildEgressObserver() EgressObserver {
	return func(host, method, provider string, statusCode int) {
		// Drop the emit entirely when IPC isn't configured — we have no
		// way to reach crewshipd, and a background goroutine that fails
		// 20 req/sec would fill the sidecar logs for no reason.
		if s == nil || s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.WorkspaceID == "" {
			return
		}

		// Spawn a small goroutine so the proxy RoundTrip path returns
		// immediately. The closure captures only stable values (host,
		// method, provider, statusCode) plus s, so there's no lifetime
		// issue with the request body or response.
		go func(host, method, provider string, status int) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			payload := map[string]any{
				"host":        host,
				"method":      method,
				"status_code": status,
			}
			if provider != "" {
				payload["provider"] = provider
			}

			summary := fmt.Sprintf("%s %s → %d", method, host, status)
			if status == 0 {
				summary = fmt.Sprintf("%s %s → transport error", method, host)
			}
			s.emitJournal(ctx, "network.egress", summary, payload, nil)
		}(host, method, provider, statusCode)
	}
}

// journalEmitRequest mirrors the wire format defined by the crewshipd
// handler in internal/api/internal_journal.go. Kept as a private struct
// here so a breaking change on either side surfaces as a compile error
// instead of a silent serialization mismatch.
type journalEmitRequest struct {
	WorkspaceID string         `json:"workspace_id"`
	CrewID      string         `json:"crew_id"`
	AgentID     string         `json:"agent_id"`
	MissionID   string         `json:"mission_id,omitempty"`
	Type        string         `json:"type"`
	Summary     string         `json:"summary"`
	Payload     map[string]any `json:"payload,omitempty"`
	Refs        map[string]any `json:"refs,omitempty"`
}

// buildLLMCallObserver returns the proxy-side hook that forwards parsed
// LLM usage + quota signal to crewshipd's /internal/cost/record endpoint.
// Mirrors buildEgressObserver in shape: fire-and-forget goroutine, drop
// when IPC is unconfigured, no back-pressure on the proxy hot path.
//
// Authoritative scope (workspace/crew/agent IDs) comes from s.ipc — the
// sidecar learns those at boot via crewshipd-stamped IPCConfig — so an
// agent cannot spoof a row attributed to a different scope by fiddling
// with response bodies.
func (s *Server) buildLLMCallObserver() LLMCallObserver {
	return func(usage LLMUsage, quota QuotaInfo, mode, plan string) {
		if s == nil || s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.WorkspaceID == "" {
			return
		}
		// Drop totally-empty events: nothing to record, nothing to enforce.
		// We need to be careful with two edge cases CodeRabbit flagged:
		//   1. cache-only observations — InputTokens/OutputTokens can be 0
		//      while CachedInputTokens / CacheCreationTokens are non-zero
		//      (e.g. a small completion mostly served from prompt cache).
		//      Dropping these would erase a real billing event.
		//   2. RemainingPct == 0 — that's the exhausted-quota signal we
		//      explicitly want to surface so EnforceQuota can fire 429s.
		//      "No signal" is window=="" instead.
		hasUsage := usage.InputTokens != 0 ||
			usage.OutputTokens != 0 ||
			usage.CachedInputTokens != 0 ||
			usage.CacheCreationTokens != 0
		hasQuota := quota.HadStatus429 || quota.Window != ""
		if !hasUsage && !hasQuota {
			return
		}

		go func(usage LLMUsage, quota QuotaInfo, mode, plan string) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s.postCostRecord(ctx, usage, quota, mode, plan)
		}(usage, quota, mode, plan)
	}
}

// sidecarCostRecord matches the wire format declared in
// internal/api/internal_cost.go. Kept as a private struct so a breaking
// change on either side surfaces as a compile error during build instead
// of silent serialization drift at runtime.
type sidecarCostRecord struct {
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

// postCostRecord serialises one cost record and POSTs it to crewshipd.
// All transport / encoding errors are logged at debug level — same
// philosophy as emitJournal: observability fail closed against the proxy
// hot path is worse than missing rows in the ledger.
func (s *Server) postCostRecord(ctx context.Context, usage LLMUsage, quota QuotaInfo, mode, plan string) {
	if usage.Provider == "" {
		// Caller passed only a quota signal with no provider tag — we have
		// nowhere to attribute the row, so skip rather than write garbage.
		return
	}
	body, err := json.Marshal(sidecarCostRecord{
		WorkspaceID:         s.ipc.WorkspaceID,
		CrewID:              s.ipc.CrewID,
		AgentID:             s.ipc.AgentID,
		Provider:            usage.Provider,
		Model:               usage.Model,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CachedInputTokens:   usage.CachedInputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		BillingMode:         mode,
		SubscriptionPlan:    plan,
		QuotaRemainingPct:   quota.RemainingPct,
		QuotaWindow:         quota.Window,
		HadStatus429:        quota.HadStatus429,
	})
	if err != nil {
		s.logger.Debug("cost record marshal failed", "err", err, "provider", usage.Provider)
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/cost/record", bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Debug("cost record IPC failed", "err", err, "provider", usage.Provider)
		return
	}
	defer resp.Body.Close()
	// 2xx is the only outcome that means crewshipd accepted the row. A
	// 4xx (auth drift, schema regression) or 5xx (server panic) silently
	// drops the ledger entry; logging at debug means the failure is
	// recoverable from journalctl during incident response without
	// drowning normal operation in noise.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logger.Debug("cost record IPC rejected",
			"status", resp.StatusCode,
			"provider", usage.Provider,
			"model", usage.Model)
	}
}

// emitJournal posts a single journal entry to crewshipd over IPC. The
// sidecar itself has no DB access (security boundary: sidecar runs
// agent-adjacent, DB writes must go through the trusted plane), so every
// observability emit takes this round-trip.
//
// The emit is fire-and-forget from the caller's perspective: we swallow
// transport errors so a down crewshipd never blocks agent outbound
// traffic. Success/failure is logged at debug level; a persistent drop
// would surface as missing Crow's Nest entries, which is the right tier
// of signal to catch a broken pipeline.
//
// The caller's ctx is used so a cancelled outer request doesn't leak a
// background HTTP call. We do NOT timeout aggressively here because
// ipcClient already carries a 30s overall timeout — stacking another
// timeout would hide real slowness behind early cancellation.
func (s *Server) emitJournal(ctx context.Context, entryType, summary string, payload, refs map[string]any) {
	if s == nil || s.ipc == nil || s.ipc.BaseURL == "" {
		return
	}
	// Drop events before the sidecar's identity is fully populated.
	// IPCConfig is injected at startup via stdin JSON; a partially
	// populated config (e.g. missing WorkspaceID) means we'd emit an
	// invalid entry that crewshipd would reject anyway.
	if s.ipc.WorkspaceID == "" {
		return
	}

	body, err := json.Marshal(journalEmitRequest{
		WorkspaceID: s.ipc.WorkspaceID,
		CrewID:      s.ipc.CrewID,
		AgentID:     s.ipc.AgentID,
		Type:        entryType,
		Summary:     summary,
		Payload:     payload,
		Refs:        refs,
	})
	if err != nil {
		// json.Marshal on map[string]any can fail if callers stuff in
		// non-JSON-able values (channels, functions); treat as a no-op
		// since this path is observability, not correctness.
		s.logger.Debug("journal emit marshal failed", "err", err, "type", entryType)
		return
	}

	// Short, dedicated timeout: network.egress is emitted inline with
	// the proxy forward path, so blocking more than ~2s per request
	// would be noticeable tail latency for the agent.
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/journal/emit", bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Debug("journal emit IPC failed", "err", err, "type", entryType)
		return
	}
	// Drain + close so the HTTP transport can reuse the connection.
	// We don't care about the response body — crewshipd returns the
	// generated id, but the sidecar has nowhere to use it.
	_ = resp.Body.Close()
}
