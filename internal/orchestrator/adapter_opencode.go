package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// opencodeAdapter wires sst.dev's `opencode` CLI (opencode-ai npm package).
// OpenCode is BYOK across providers — it reads its own opencode.json for
// provider routing, and the chosen provider's API key needs to be in the
// container env (ANTHROPIC_API_KEY / OPENAI_API_KEY / etc).
//
// Native MCP support exists upstream via the `mcp` section of opencode.json;
// SetupSystemPrompt also drops AGENTS.md (system instructions) into the work
// directory so the agent persona persists across CLI invocations.
type opencodeAdapter struct{}

func (opencodeAdapter) Name() string { return "OPENCODE" }

func (opencodeAdapter) BuildCommand(req AgentRunRequest) []string {
	// Bit-for-bit preservation of the pre-refactor command shape — failover
	// tests assert this exact argv. Output-format flag will be added when
	// parser_opencode.go is fleshed out and validated against fixture output.
	return []string{"opencode", "run", req.UserMessage}
}

func (opencodeAdapter) UseStreamJSON() bool { return false }

func (opencodeAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseOpenCodeStreamJSON(line, handler)
}

// SetupSystemPrompt writes AGENTS.md into the working directory. OpenCode reads
// it on startup as the agent persona. Bit-for-bit preserved from the old
// setupSystemPromptFiles switch in exec_mcp.go.
func (opencodeAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	systemPrompt := crewshipSystemPreamble + req.SystemPrompt
	return writeFileViaContainer(ctx, container, containerID, workDir, "AGENTS.md", systemPrompt, logger)
}

// SupportsMCP returns true. OpenCode reads MCP servers from opencode.json under
// the `mcp` key — see exec_mcp_opencode.go for the config writer (added in a
// follow-up commit; current behaviour is no MCP config written, parity with
// pre-refactor state).
func (opencodeAdapter) SupportsMCP() bool { return false }
