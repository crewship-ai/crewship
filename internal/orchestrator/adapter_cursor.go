package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// cursorAdapter wires Cursor's `cursor-agent` headless mode (now also exposed
// as `agent`). The CLI's NDJSON `--output-format stream-json` aligns with
// Claude Code's stream-json shape closely enough that the parser only needs
// minor field-name remapping — implemented in parser_cursor.go.
//
// System instructions are read from the working directory: `.cursor/rules/`,
// `AGENTS.md`, and `CLAUDE.md`. SetupSystemPrompt drops them all so users get
// the same crewship preamble regardless of which file Cursor prefers in a
// given release.
type cursorAdapter struct{}

func (cursorAdapter) Name() string { return "CURSOR_CLI" }

func (cursorAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"cursor-agent", "-p", "--output-format", "stream-json"}
	if req.LLMModel != "" {
		cmd = append(cmd, "-m", req.LLMModel)
	}
	cmd = append(cmd, "--", req.UserMessage)
	return cmd
}

// UseStreamJSON is false until parser_cursor.go ships and is validated against
// real cursor-agent output. The argv already requests stream-json so the parser
// can be wired without changing BuildCommand.
func (cursorAdapter) UseStreamJSON() bool { return false }

func (cursorAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseCursorStreamJSON(line, handler)
}

// SetupSystemPrompt is a no-op pending the per-adapter file write — currently
// matches pre-refactor behaviour where setupSystemPromptFiles only wrote
// AGENTS.md for OPENCODE. Cursor operating without a system prompt is
// suboptimal but matches today's shipping behaviour; a follow-up commit can
// add the .cursor/rules + AGENTS.md + CLAUDE.md drop here.
func (cursorAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

func (cursorAdapter) SupportsMCP() bool { return false }
