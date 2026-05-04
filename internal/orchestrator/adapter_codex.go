package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

	// Sandbox policy: MINIMAL profile is read-only, all other profiles get
	// workspace-write so the agent can edit code. danger-full-access is
	// never opted into automatically — it bypasses the workspace boundary.
	// MESSAGING was retired in #261; only the three remaining profiles
	// flow through here now.
	sandbox := "workspace-write"
	if req.ToolProfile == "MINIMAL" {
		sandbox = "read-only"
	}
	cmd = append(cmd, "--sandbox", sandbox)

	if req.LLMModel != "" {
		cmd = append(cmd, "--model", req.LLMModel)
	}

	// Codex exec has NO --system-prompt flag and AGENTS.md is only read on
	// the second turn (the first invocation hits the model before the file
	// is loaded into context). To guarantee turn-1 parity with Claude Code
	// we prepend the system prompt + memory directly into the user message
	// using [SYSTEM]/[USER] delimiters — the same strategy adapter_gemini.go
	// uses for the same reason. SetupSystemPrompt also drops AGENTS.md so
	// turn-2+ has the persistent context via the CLI's own discovery path.
	prompt := req.UserMessage
	if sys := strings.TrimSpace(crewshipSystemPreamble + req.SystemPrompt); sys != "" {
		prompt = "[SYSTEM]\n" + sys + "\n\n[USER]\n" + req.UserMessage
	}

	// `--` separator stops Codex from re-parsing user message tokens that
	// happen to start with `-` (e.g. "--help") as flags. Without this a
	// user prompt of "describe --force option" would crash with "unknown
	// flag: --force" before reaching the model.
	cmd = append(cmd, "--", prompt)
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

// SetupSystemPrompt drops the canonical memory blob to AGENTS.md (Codex's
// auto-discovery path) plus the cross-CLI parity files. Combined with the
// turn-1 prepend in BuildCommand above, Codex agents now see the same memory
// every other CLI sees regardless of session number. Pre-fix this was a
// no-op + a doc lie ("prepended upstream" was never true) — Codex agents had
// zero memory or persona context.
func (codexAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("codex adapter setup system prompt: %w", err)
	}
	if err := writeAgentSkills(ctx, container, containerID, workDir, req.Skills, logger); err != nil {
		logger.Warn("codex adapter write agent skills failed", "error", err)
	}
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
	if err := writeMCPCodex(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("codex adapter write MCP config: %w", err)
	}
	return nil
}
