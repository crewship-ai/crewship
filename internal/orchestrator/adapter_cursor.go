package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// cursorAdapter wires Cursor's `cursor-agent` headless mode. Cursor's
// CLI doc page (cursor.com/docs/cli/headless) lists `agent` as the primary
// binary as of 2026-01, with `cursor-agent` retained as a backward-compat
// alias. We stay on `cursor-agent` so the cursor.com/install script (which
// installs both names) keeps working without us guessing which symlink wins.
//
// Canonical non-interactive form: `cursor-agent -p --output-format stream-json
// --force [-m MODEL] -- <message>`. Relevant flags:
//   - -p, --print              : non-interactive print mode
//   - --output-format          : text | json | stream-json
//   - --stream-partial-output  : include incremental delta events (token-level)
//   - --force                  : let the agent edit files without confirmation
//     — without this in headless mode the agent
//     blocks on a permission prompt and the run
//     hangs until killed
//   - -m, --model              : override model
//
// Stream-json event types per cursor.com/docs/cli/reference/output-format
// (JSONL, very Anthropic-like):
//   - system    (subtype init)            — apiKeySource, cwd, session_id, model
//   - user      message.role + content[]
//   - assistant message.role + content[] of {type:text} blocks
//   - tool_call (subtype started/completed) — readToolCall / writeToolCall / function
//   - result    (subtype success/error)   — duration_ms, duration_api_ms, is_error, result
//
// System instructions: Cursor reads `.cursor/rules/`, `AGENTS.md`, and
// `CLAUDE.md` from the working directory. SetupSystemPrompt drops AGENTS.md
// (the most universally-supported of the three) so a baseline persona is
// always available; the user's project may add its own .cursor/rules on top.
type cursorAdapter struct{}

func (cursorAdapter) Name() string { return "CURSOR_CLI" }

func (cursorAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"cursor-agent", "-p", "--output-format", "stream-json", "--stream-partial-output"}
	// --force lets the agent edit files without prompting. Without it any
	// write tool call hangs on a permission prompt that no one can answer in
	// headless mode. We default to --force because Crewship's filesystem
	// boundary (chroot, --cap-drop=ALL, secrets in /secrets/) is the
	// authoritative permission layer; per-tool prompts are redundant.
	cmd = append(cmd, "--force")
	if req.LLMModel != "" {
		cmd = append(cmd, "-m", req.LLMModel)
	}
	cmd = append(cmd, "--", req.UserMessage)
	return cmd
}

// UseStreamJSON returns true: cursor-agent emits NDJSON when invoked with
// --output-format stream-json. parseCursorStreamJSON handles the schema.
func (cursorAdapter) UseStreamJSON() bool { return true }

func (cursorAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseCursorStreamJSON(line, handler)
}

// SetupSystemPrompt drops AGENTS.md into the working directory. Cursor reads
// it as the agent persona at session start.
func (cursorAdapter) SetupSystemPrompt(
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

func (cursorAdapter) SupportsMCP() bool { return false }
