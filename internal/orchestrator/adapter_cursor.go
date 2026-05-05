package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
	// --approve-mcps would unblock MCP tool calls in --print mode IF Cursor
	// honoured them, but it does not (see SupportsMCP comment below). Keep
	// the flag wired conditionally so the moment upstream fixes headless
	// MCP and we flip SupportsMCP() to true, MCP-equipped agents auto-light
	// up without a second commit.
	if len(req.MCPServers) > 0 || req.CrewMCPConfigJSON != "" || req.AgentMCPConfigJSON != "" {
		cmd = append(cmd, "--approve-mcps")
	}
	if req.LLMModel != "" {
		cmd = append(cmd, "-m", req.LLMModel)
	}
	// Turn-1 parity: Cursor's headless mode has no --system-prompt flag and
	// the .cursor/rules + AGENTS.md + CLAUDE.md files SetupSystemPrompt drops
	// are read by the CLI between invocations, NOT before the first user
	// message in a fresh container. Without prepending, a cold-start Cursor
	// agent sees zero preamble / persona / memory on turn 1 — drift across
	// the same conversation. Same fix Codex/Droid/Gemini already use.
	prompt := req.UserMessage
	if sys := strings.TrimSpace(crewshipSystemPreamble + req.SystemPrompt); sys != "" {
		prompt = "[SYSTEM]\n" + sys + "\n\n[USER]\n" + req.UserMessage
	}
	cmd = append(cmd, "--", prompt)
	return cmd
}

// UseStreamJSON returns true: cursor-agent emits NDJSON when invoked with
// --output-format stream-json. parseCursorStreamJSON handles the schema.
func (cursorAdapter) UseStreamJSON() bool { return true }

func (cursorAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseCursorStreamJSON(line, handler)
}

// SetupSystemPrompt drops the full canonical memory file set. Cursor reads
// .cursor/rules/, AGENTS.md, AND CLAUDE.md (Cursor merges all three) — the
// unified writer covers all three plus parity files. Pre-fix only AGENTS.md
// was written; .cursor/rules/ users were silently overriding our memory
// because Cursor prioritises .cursor/rules over AGENTS.md.
func (cursorAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("cursor adapter setup system prompt: %w", err)
	}
	if err := writeAgentSkills(ctx, container, containerID, workDir, req.Skills, logger); err != nil {
		logger.Warn("cursor adapter write agent skills failed", "error", err)
	}
	return nil
}

// SupportsMCP returns false: cursor-agent's --print / non-interactive mode
// does NOT actually invoke MCP servers at runtime, even when --approve-mcps
// is set and .cursor/mcp.json is present. Forum #143045 and #148397 confirm
// servers are listed but their tools are never called — only the interactive
// TUI honours MCP. Returning true here would mislead the rest of the system
// (paymaster, Crow's Nest tool surface, agent template auto-bind logic) into
// treating Cursor agents as MCP-capable; users would see no MCP tools fire.
//
// writeMCPCursor / .cursor/mcp.json are still in the tree so the moment
// upstream fixes headless MCP we flip this to true and the writer is wired
// back automatically — no other change required.
func (cursorAdapter) SupportsMCP() bool { return false }

func (cursorAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	// Unreachable while SupportsMCP() returns false (orchestrator gates the
	// call). Kept as a working implementation so flipping SupportsMCP to true
	// in the future is the only change needed.
	if err := writeMCPCursor(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("cursor adapter write MCP config: %w", err)
	}
	return nil
}
