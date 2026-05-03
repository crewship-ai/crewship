package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// codexAdapter wires OpenAI's `codex` CLI (Rust port distributed as the
// @openai/codex npm package, current as of 0.128.0). Auth is BYO API key via
// OPENAI_API_KEY.
//
// Canonical non-interactive form per developers.openai.com/codex/cli/reference
// is `codex exec --json` — NOT `codex --quiet` (no such flag in the Rust port).
// Relevant flags:
//   - exec / e         : non-interactive subcommand
//   - --json           : newline-delimited JSON events on stdout
//   - --sandbox MODE   : read-only | workspace-write | danger-full-access
//     (requires a value; bare --sandbox is rejected)
//   - --model, -m      : override model
//   - --output-last-message, -o FILE : write final assistant message to file
//
// Codex does not document a --system-prompt-equivalent flag for exec mode;
// crewship preamble + agent persona are prepended to UserMessage at the
// orchestrator layer (see orchestrator_run.go promptBuf assembly).
type codexAdapter struct{}

func (codexAdapter) Name() string { return "CODEX_CLI" }

func (codexAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"codex", "exec", "--json"}

	// Sandbox policy: agents that should mutate code get workspace-write,
	// MINIMAL/CONSULTATIVE profiles get read-only. danger-full-access is
	// never opted into automatically — it bypasses the workspace boundary.
	sandbox := "workspace-write"
	switch req.ToolProfile {
	case "MINIMAL", "CONSULTATIVE":
		sandbox = "read-only"
	}
	cmd = append(cmd, "--sandbox", sandbox)

	if req.LLMModel != "" {
		cmd = append(cmd, "--model", req.LLMModel)
	}

	cmd = append(cmd, req.UserMessage)
	return cmd
}

// UseStreamJSON returns true: --json emits newline-delimited events
// (parser_codex.go consumes them). The parser is currently a stub — until it
// ships the live event stream falls through the parser as no-ops, which means
// the agent surfaces no incremental UI events but the run still completes and
// the journal entry captures the raw output for debugging.
func (codexAdapter) UseStreamJSON() bool { return true }

func (codexAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseCodexStreamJSON(line, handler)
}

// SetupSystemPrompt is a no-op for Codex: exec mode has no documented system
// prompt flag, so the crewship preamble is prepended to UserMessage upstream.
func (codexAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

// SupportsMCP returns true: Codex Rust port reads .codex/config.toml at session
// start and registers MCP servers automatically (no flag required, per
// developers.openai.com/codex). HTTP and stdio transports both supported.
func (codexAdapter) SupportsMCP() bool { return true }

func (codexAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return writeMCPCodex(ctx, container, containerID, req, workDir, logger)
}
