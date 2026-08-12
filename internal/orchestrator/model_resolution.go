package orchestrator

import (
	"log/slog"
	"strings"
)

// modelFamily extracts the coarse Claude model family (fable / opus / sonnet /
// haiku) from a model id such as "claude-fable-5", "claude-opus-4-8",
// "claude-sonnet-4-5-20250101" or "us.anthropic.claude-opus-4-8". Returns ""
// for any id without a recognised family token (other providers, or a blank
// id) so callers treat it as "don't compare" rather than forcing a false
// mismatch.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "fable"):
		return "fable"
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
// knownAPIKeySources is the closed set of apiKeySource values that may be
// logged verbatim. The field names WHERE the credential came from, not the
// credential itself — but it is an upstream field we do not control, the log
// line goes to disk on every run, and the reason we log it at all is that we
// cannot predict what it will say. Report anything else as having changed
// without quoting it.
//
// Returning the element from THIS slice rather than the argument is deliberate:
// it means no value read off the stream can reach the writer, which is also
// what makes the flow provably clean rather than merely unlikely.
var knownAPIKeySources = []string{
	"none",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"apiKeyHelper",
	"temporary",
	"bedrock",
	"vertex",
}

func safeAPIKeySource(v string) string {
	if v == "" {
		return ""
	}
	for _, known := range knownAPIKeySources {
		if v == known {
			return known
		}
	}
	return "other"
}

// cliVersion and apiKeySource come off the same init event and answer the
// sibling question: which BINARY served the run. The adapter is validated
// against a pinned npm version while agent containers install the
// `claude-code:2` devcontainer feature — latest — so the two drift, and a
// capability can disappear for a hundred releases with nothing to grep (#1932).
// Both are optional: non-Claude adapters report neither, and Claude Code below
// 2.1.205 reports no apiKeySource.
func logResolvedModel(logger *slog.Logger, agentID, requested, actual, cliVersion, apiKeySource string) {
	if logger == nil || actual == "" {
		return
	}
	attrs := []any{
		"agent_id", agentID,
		"requested_model", requested,
		"actual_model", actual,
	}
	if cliVersion != "" {
		attrs = append(attrs, "cli_version", cliVersion)
	}
	if src := safeAPIKeySource(apiKeySource); src != "" {
		attrs = append(attrs, "api_key_source", src)
	}
	logger.Info("agent model resolved", attrs...)
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
