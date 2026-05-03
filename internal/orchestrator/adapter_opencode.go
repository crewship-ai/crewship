package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// opencodeAdapter wires sst.dev's `opencode` CLI (opencode-ai npm package,
// 1.14.33 as of 2026-05). OpenCode is BYOK across providers: the user picks
// which provider/model to route to via the -m flag (e.g. anthropic/claude-...)
// and the corresponding provider's API key needs to be in the container env.
//
// Canonical non-interactive form per opencode.ai/docs/cli/ is `opencode run
// <message>`. Relevant flags:
//   - --format        : default | json   (NOT --output-format — different name)
//     json emits a single raw JSON event blob, not a stream
//   - --model, -m     : "provider/model" — e.g. anthropic/claude-sonnet-4-6
//   - --continue, -c  : resume last session
//   - --session, -s   : resume specific session
//
// Native MCP support exists upstream via the `mcp` section of opencode.json;
// SetupSystemPrompt drops AGENTS.md (system instructions) into the work
// directory so the agent persona persists across CLI invocations.
type opencodeAdapter struct{}

func (opencodeAdapter) Name() string { return "OPENCODE" }

func (opencodeAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"opencode", "run", "--format", "json"}
	if req.LLMModel != "" {
		// LLMModel may already be in "provider/model" form; if not, opencode
		// errors out with a clear message — better than us guessing the prefix.
		cmd = append(cmd, "--model", req.LLMModel)
	}
	cmd = append(cmd, req.UserMessage)
	return cmd
}

// UseStreamJSON returns true: --format json emits a JSON object that
// parseOpenCodeStreamJSON handles as a single event. (Unlike Cursor/Gemini/
// Claude, opencode does not currently expose a streaming JSONL mode — the
// whole response is buffered until completion.)
func (opencodeAdapter) UseStreamJSON() bool { return true }

func (opencodeAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseOpenCodeStreamJSON(line, handler)
}

// SetupSystemPrompt writes AGENTS.md into the working directory. OpenCode
// reads it on startup as the agent persona. Bit-for-bit preserved from the
// pre-refactor setupSystemPromptFiles switch.
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

// SupportsMCP returns false for now. OpenCode reads MCP servers from
// opencode.json under the `mcp` key — a follow-up commit (see plan
// t-m-ukulem-bude-purring-cray.md krok 7) generates that file via
// exec_mcp_opencode.go. Until that lands, this adapter ships without MCP.
func (opencodeAdapter) SupportsMCP() bool { return false }
