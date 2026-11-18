package pipeline

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// A workspace's default tiers often name Claude. Passing that model unchanged
// to a Codex employee creates an invalid invocation; switching its CLI instead
// would change its credentials and authority. Implicit incompatible tiers keep
// the employee's configured model. Explicit cross-adapter pins fail clearly.
func routineAgentModel(info *chatbridge.ChatInfo, req AgentStepRequest) (string, error) {
	pin := strings.TrimSpace(req.ModelOverride)
	adapter, model, qualified := strings.Cut(pin, ":")
	if req.ModelOverride != "" && (pin == "" || (qualified && (strings.TrimSpace(adapter) == "" || strings.TrimSpace(model) == ""))) {
		return "", fmt.Errorf("routine model override must name both adapter and model when qualified: %q", req.ModelOverride)
	}
	if req.Model == "" {
		return info.LLMModel, nil
	}
	actual, known := devcontainer.AdapterCLIFor(info.CLIAdapter)
	desired, desiredKnown := devcontainer.AdapterCLIFor(req.Adapter)
	if pin != "" {
		if !known || (qualified && (!desiredKnown || desired.Adapter != actual.Adapter)) {
			return "", fmt.Errorf("routine model override adapter %q does not match agent %q adapter %q", req.Adapter, req.AgentSlug, info.CLIAdapter)
		}
		// These adapters only run one provider's model family. The other
		// catalogued adapters accept multiple providers by design.
		expected := map[string]string{"CLAUDE_CODE": "anthropic", "CODEX_CLI": "openai", "GEMINI_CLI": "google"}[actual.Adapter]
		if expected != "" && !routineModelMatchesProvider(req.Model, expected) {
			return "", fmt.Errorf("routine model override %q does not match agent %q adapter %q", req.Model, req.AgentSlug, info.CLIAdapter)
		}
		return req.Model, nil
	}
	if req.Adapter == "" || (known && desiredKnown && desired.Adapter == actual.Adapter) {
		return req.Model, nil
	}
	if info.LLMModel == "" {
		return "", fmt.Errorf("routine tier adapter %q does not match agent adapter %q and the agent has no configured model", req.Adapter, info.CLIAdapter)
	}
	return info.LLMModel, nil
}

func routineModelMatchesProvider(model, expected string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	// Claude Code also accepts its short routing aliases, including older
	// routine pins that already use these names.
	if expected == "anthropic" && model == "fable" {
		return true
	}
	if expected == "openai" && strings.HasPrefix(model, "codex-") {
		return true
	}
	return orchestrator.ModelNameProviderID(model) == expected
}
