package orchestrator

import (
	"log/slog"
	"strings"
)

// modelFamily extracts the coarse Claude model family (opus / sonnet / haiku)
// from a model id such as "claude-opus-4-8", "claude-sonnet-4-5-20250101" or
// "us.anthropic.claude-opus-4-8". Returns "" for any id without a recognised
// family token (other providers, or a blank id) so callers treat it as
// "don't compare" rather than forcing a false mismatch.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	default:
		return ""
	}
}

// logResolvedModel records the model an agent run ACTUALLY resolved to versus
// the one Crewship requested (--model). The actual model is ground truth from
// the CLI's session-init event; the requested model is the AgentRunRequest
// override.
//
// We auth Claude via a subscription token ($0 cost), and a subscription only
// honours --model if its tier includes that model — a Pro plan asked for Opus
// silently serves Sonnet. So when the requested family is known and differs
// from the served family (asked opus, got sonnet/haiku) we escalate to WARN,
// turning a silent tier fallback into a loud, greppable signal.
//
// Best-effort: a blank actual model (no init event, or a non-Claude adapter
// that doesn't report one) logs nothing and never errors a run.
func logResolvedModel(logger *slog.Logger, agentID, requested, actual string) {
	if logger == nil || actual == "" {
		return
	}
	logger.Info("agent model resolved",
		"agent_id", agentID,
		"requested_model", requested,
		"actual_model", actual,
	)
	// Only flag a fallback when we asked for a specific family AND both
	// families are recognised AND they differ. An empty requested model
	// (subscription default, no override) can't be called a fallback.
	rf, af := modelFamily(requested), modelFamily(actual)
	if requested != "" && rf != "" && af != "" && rf != af {
		logger.Warn("requested model not served — subscription tier fallback?",
			"agent_id", agentID,
			"requested_model", requested,
			"actual_model", actual,
		)
	}
}
