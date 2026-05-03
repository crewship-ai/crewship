package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// codexAdapter wires OpenAI's `codex` CLI (Rust port distributed as the
// @openai/codex npm package). Auth is BYO API key via OPENAI_API_KEY; the
// sidecar proxy intercepts api.openai.com when sidecar mode is on.
//
// Status (2026-05): command shape preserved bit-for-bit from the pre-refactor
// switch in exec.go. Stream parsing wired through ParseStreamLine but the
// concrete event schema is implemented in parser_codex.go.
type codexAdapter struct{}

func (codexAdapter) Name() string { return "CODEX_CLI" }

func (codexAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"codex", "--quiet"}
	if req.ToolProfile == "CODING" {
		cmd = append(cmd, "--sandbox")
	}
	cmd = append(cmd, req.UserMessage)
	return cmd
}

// UseStreamJSON is false until we land per-event parsing — keeps the safe
// raw-text fallback so Codex output is at least readable in chat. Flip to true
// once parser_codex.go ships a parser validated against fixture output.
func (codexAdapter) UseStreamJSON() bool { return false }

func (codexAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseCodexStreamJSON(line, handler)
}

// SetupSystemPrompt is a no-op for Codex pending upstream guidance on how the
// Rust port consumes system instructions in non-interactive mode. Today the
// crewship preamble is appended to UserMessage by orchestrator_run.go for any
// adapter that returns nil here without a CLI flag.
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

func (codexAdapter) SupportsMCP() bool { return false }
