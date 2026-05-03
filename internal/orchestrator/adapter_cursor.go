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
	// --approve-mcps unblocks MCP tool calls in --print mode. The Cursor
	// docs page implies MCP "just works" headlessly; in practice (forum
	// #143045 + #148397) MCP servers are listed but their tools never
	// invoked unless this flag is on. Add it whenever the agent has any
	// MCP source configured so MCP-equipped agents actually use their
	// servers; omit otherwise to keep the command shape minimal.
	if len(req.MCPServers) > 0 || req.CrewMCPConfigJSON != "" || req.AgentMCPConfigJSON != "" {
		cmd = append(cmd, "--approve-mcps")
	}
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
	return writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger)
}

// SupportsMCP returns true *with caveat*. Cursor docs claim MCP support in
// CLI, and we write .cursor/mcp.json. HOWEVER multiple community reports
// (forum #143045, #148397) confirm the MCP servers are NOT invoked when
// cursor-agent runs in --print / non-interactive mode — only the interactive
// TUI honours them. We write the file anyway for parity (so the moment
// upstream fixes the bug nothing else changes), but the user-visible effect
// today is "no MCP tools surface in chat for Cursor agents".
func (cursorAdapter) SupportsMCP() bool { return true }

func (cursorAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return writeMCPCursor(ctx, container, containerID, req, workDir, logger)
}
