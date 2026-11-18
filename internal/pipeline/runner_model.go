package pipeline

import (
	"context"
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

// Validate explicit pins without loading credentials or acquiring a runtime.
// RunStep rechecks the effective agent config before dispatch.
func (r *OrchestratorRunner) validateModelPin(ctx context.Context, workspaceID, crewID string, step Step) error {
	var info chatbridge.ChatInfo
	err := r.db.QueryRowContext(ctx, `SELECT a.cli_adapter,COALESCE(a.llm_model,'') FROM agents a
		JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
		WHERE a.workspace_id=? AND a.crew_id=? AND a.slug=? AND a.deleted_at IS NULL`,
		workspaceID, crewID, step.AgentSlug).Scan(&info.CLIAdapter, &info.LLMModel)
	if err != nil {
		return fmt.Errorf("step %q: resolve model pin agent: %w", step.ID, err)
	}
	pin := splitAdapterModel(step.ModelOverride, "")
	if _, err := routineAgentModel(&info, AgentStepRequest{
		AgentSlug: step.AgentSlug, Adapter: pin.Adapter, Model: pin.Model, ModelOverride: step.ModelOverride,
	}); err != nil {
		return fmt.Errorf("step %q: %w", step.ID, err)
	}
	return nil
}

func (e *Executor) validateRoutineModelPins(ctx context.Context, workspaceID, crewID string, steps []Step, restored map[string]string) error {
	validator, ok := e.runner.(interface {
		validateModelPin(context.Context, string, string, Step) error
	})
	if !ok {
		return nil
	}
	for _, step := range steps {
		if _, done := restored[step.ID]; done {
			continue
		}
		if step.Type == StepAgentRun && step.ModelOverride != "" {
			if err := validator.validateModelPin(ctx, workspaceID, crewID, step); err != nil {
				return err
			}
		}
		if step.Foreach != nil {
			if err := e.validateRoutineModelPins(ctx, workspaceID, crewID, step.Foreach.Steps, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
