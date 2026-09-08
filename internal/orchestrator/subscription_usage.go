package orchestrator

import (
	"context"
	"math"
	"strings"
	"time"
)

// SubscriptionUsage is an observed CLI usage envelope, attributed using the
// run's selected grant, never a credential identifier supplied by the CLI.
type SubscriptionUsage struct {
	WorkspaceID, CrewID, AgentID, MissionID, RunID                    string
	CredentialID, Provider, Model, Plan                               string
	InputTokens, OutputTokens, CachedInputTokens, CacheCreationTokens int64
}

func (o *Orchestrator) SetSubscriptionUsageRecorder(record func(context.Context, SubscriptionUsage) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.subscriptionUsageRecorder = record
}

func subscriptionTokenCount(v any) int64 {
	switch n := v.(type) {
	case float64:
		if n >= 0 && n < math.MaxInt64 && !math.IsNaN(n) {
			return int64(n)
		}
	case int:
		if n >= 0 {
			return int64(n)
		}
	case int64:
		if n >= 0 {
			return n
		}
	}
	return 0
}

func subscriptionUsageForEvent(req AgentRunRequest, runID, model string, event AgentEvent) (SubscriptionUsage, bool) {
	if event.Type != "result" {
		return SubscriptionUsage{}, false
	}
	d := getAdapter(req.CLIAdapter).AuthDelivery()
	login, ok := loginCredentialFor(req, d.Kind)
	if d.FileDelivered() {
		_, login, ok = fileLogin(req)
	}
	if !ok || login.ID == "" {
		return SubscriptionUsage{}, false
	}
	meta, ok := event.Metadata.(map[string]any)
	if !ok {
		return SubscriptionUsage{}, false
	}
	usage, ok := meta["usage"].(map[string]any)
	if !ok && req.CLIAdapter == "GEMINI_CLI" {
		usage, ok = meta["stats"].(map[string]any)
	}
	if !ok || usage == nil {
		return SubscriptionUsage{}, false // no usage reported is not a zero-token call
	}
	if model == "" {
		model = "unknown"
	}
	provider := strings.ToLower(login.Provider)
	if provider == "" && d.Kind == oauthAnthropic {
		provider = "anthropic" // legacy setup-token shape without provider metadata
	}
	return SubscriptionUsage{
		WorkspaceID: req.WorkspaceID, CrewID: req.CrewID, AgentID: req.AgentID,
		MissionID: req.MissionID, RunID: runID, CredentialID: login.ID,
		Provider: provider, Model: model, Plan: loginPlanLabel(d, login),
		InputTokens:         subscriptionTokenCount(usage["input_tokens"]),
		OutputTokens:        subscriptionTokenCount(usage["output_tokens"]),
		CachedInputTokens:   max(subscriptionTokenCount(usage["cached_input_tokens"]), subscriptionTokenCount(usage["cache_read_input_tokens"])),
		CacheCreationTokens: subscriptionTokenCount(usage["cache_creation_input_tokens"]),
	}, true
}

func (o *Orchestrator) recordSubscriptionUsage(ctx context.Context, req AgentRunRequest, runID, model string, event AgentEvent) {
	o.mu.RLock()
	record := o.subscriptionUsageRecorder
	o.mu.RUnlock()
	if record == nil {
		return
	}
	usage, ok := subscriptionUsageForEvent(req, runID, model, event)
	if !ok {
		return
	}
	// An already-billed call must remain visible if the run is cancelled as
	// its final envelope arrives. Bound persistence independently of the CLI.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := record(ctx, usage); err != nil {
		o.logger.Warn("record subscription usage", "run_id", runID, "credential_id", usage.CredentialID, "error", err)
	}
}
