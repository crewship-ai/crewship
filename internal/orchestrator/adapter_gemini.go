package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// geminiAdapter wires Google's `gemini` CLI (@google/gemini-cli npm package).
// Auth via GOOGLE_API_KEY (or GEMINI_API_KEY — both accepted upstream). The
// `--output-format stream-json` flag from PR #10883 is what makes this adapter
// viable for parity with Claude Code; older gemini-cli versions only emitted
// raw text.
type geminiAdapter struct{}

func (geminiAdapter) Name() string { return "GEMINI_CLI" }

func (geminiAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"gemini"}
	systemPrompt := crewshipSystemPreamble + req.SystemPrompt
	if systemPrompt != "" {
		cmd = append(cmd, "--system-instruction", systemPrompt)
	}
	cmd = append(cmd, "-p", req.UserMessage)
	return cmd
}

// UseStreamJSON returns false until per-event parsing lands in parser_gemini.go.
// Flipping to true requires (a) updating BuildCommand to pass
// `--output-format stream-json` and (b) shipping a parser validated against
// fixture output captured from a real `gemini -p` invocation.
func (geminiAdapter) UseStreamJSON() bool { return false }

func (geminiAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseGeminiStreamJSON(line, handler)
}

// SetupSystemPrompt is a no-op for Gemini: the prompt is passed via
// --system-instruction on the command line.
func (geminiAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

func (geminiAdapter) SupportsMCP() bool { return false }
