package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// droidAdapter wires Factory's `droid exec` headless mode. Out of scope for
// the multi-CLI parity wave (no parser/MCP work planned in this PR), but the
// adapter exists so the registry is exhaustive and BuildCLICommand routes
// FACTORY_DROID requests through the same dispatch as the rest.
//
// Tiered autonomy via --auto: low (read-only), medium (file edits), high
// (fully autonomous). MINIMAL/CONSULTATIVE tool profiles downgrade to low
// because those signal "agent should not mutate"; everything else stays at
// medium — the API normalises empty ToolProfile to "CODING" before this point,
// so production traffic is overwhelmingly medium.
type droidAdapter struct{}

func (droidAdapter) Name() string { return "FACTORY_DROID" }

func (droidAdapter) BuildCommand(req AgentRunRequest) []string {
	autonomy := "medium"
	switch req.ToolProfile {
	case "MINIMAL", "CONSULTATIVE":
		autonomy = "low"
	}
	cmd := []string{"droid", "exec", "--auto", autonomy}
	if req.LLMModel != "" {
		cmd = append(cmd, "--model", req.LLMModel)
	}
	cmd = append(cmd, req.UserMessage)
	return cmd
}

func (droidAdapter) UseStreamJSON() bool { return false }

func (droidAdapter) ParseStreamLine(line []byte, handler EventHandler) {}

func (droidAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

func (droidAdapter) SupportsMCP() bool { return false }
