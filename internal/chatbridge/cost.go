package chatbridge

import "github.com/crewship-ai/crewship/internal/orchestrator"

// RunCostUsage carries the token usage parsed from a completed agent run's
// CLI-reported "result" event, scoped for a paymaster cost-ledger write. See
// #1205 and resultUsageForLedger below for why this fallback path exists.
type RunCostUsage struct {
	WorkspaceID string
	CrewID      string
	AgentID     string

	Provider string
	Model    string

	InputTokens  int64
	OutputTokens int64
}

// ResultUsageForLedger derives a RunCostUsage from a completed run's parsed
// CLI "result" metadata (the same map orchestrator.ParseResultUsage/
// MergeResultUsageMeta already consume), or ok=false when there's no usable
// signal to record.
//
// Why this exists (#1205): every live agent run — chat-driven (bridge.go)
// and routine/scheduled (scheduler.go) — already captures accurate
// token/cost usage from the CLI adapter's own stdout "result" event (see
// adapter_claude.go's parseClaudeCodeStreamJSON and the equivalent parsers
// for every other CLI adapter — all emit the same total_cost_usd/usage
// contract). That parsed usage was, until this fix, ONLY merged into the
// run's display metadata (MergeResultUsageMeta → UpdateRun) and never
// forwarded to the paymaster ledger, so `crewship cost` had nothing to
// aggregate for these runs even though 39 real completed runs had happened.
//
// The reason a *second*, adapter-side write path is needed at all (rather
// than relying solely on the sidecar's HTTP-level cost observation) is that
// the sidecar mechanism is structurally blind to the two transports CLI
// coding agents actually use:
//   - OAuth/subscription-mode runs (CLAUDE_CODE_OAUTH_TOKEN) tunnel through
//     the sidecar via an opaque HTTPS CONNECT proxy that is deliberately
//     never decrypted (internal/sidecar/proxy.go's handleConnect) — no
//     usage is observable from that transport at all.
//   - API-key-mode runs go through the sidecar's plaintext reverse proxy,
//     but CLI coding agents request streaming (SSE) responses to render
//     output incrementally, and internal/sidecar/usage.go's parseLLMUsage
//     only parses non-streaming `application/json` bodies (documented,
//     intentional limitation — streaming usage tracking was deferred).
//
// The CLI's own stdout doesn't depend on either transport detail — Claude
// Code (and every other adapter) reports its own total_cost_usd/usage
// regardless of how the underlying HTTP call was shaped. Forwarding it here
// closes the gap without touching the sidecar's proxy hot path.
func ResultUsageForLedger(workspaceID, crewID, agentID, model string, resultMeta any) (RunCostUsage, bool) {
	_, tokIn, tokOut := orchestrator.ParseResultUsage(resultMeta)
	if tokIn <= 0 && tokOut <= 0 {
		return RunCostUsage{}, false
	}
	provider := orchestrator.ModelNameProviderID(model)
	if provider == "" {
		return RunCostUsage{}, false
	}
	return RunCostUsage{
		WorkspaceID:  workspaceID,
		CrewID:       crewID,
		AgentID:      agentID,
		Provider:     provider,
		Model:        model,
		InputTokens:  int64(tokIn),
		OutputTokens: int64(tokOut),
	}, true
}
