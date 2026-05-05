package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// geminiAdapter wires Google's `gemini` CLI (@google/gemini-cli npm package,
// 0.40.1 as of 2026-05). Auth is GEMINI_API_KEY (canonical, AI Studio path)
// or GOOGLE_API_KEY (Vertex AI path); the CLI accepts either, and our env
// builder mirrors the value into both.
//
// Canonical headless form per geminicli.com/docs/cli/headless/:
//
//	gemini -p <message> --output-format stream-json -m <model>
//
// The `--system-instruction` flag we used pre-refactor is NOT documented in
// the public headless reference and may not exist on current builds. The
// portable way to inject a system prompt for gemini is to prepend it to the
// user message — orchestrator_run.go already prepends crewshipSystemPreamble
// to req.SystemPrompt and req.SystemPrompt to req.UserMessage when no other
// transport is available. That happens upstream, so here we just pass the
// finalised UserMessage through.
//
// Stream-JSON event types (per the headless docs):
//   - init        — session/model bootstrap
//   - message     — assistant text deltas
//   - tool_use    — tool invocation
//   - tool_result — tool response
//   - error
//   - result      — terminal usage + duration
type geminiAdapter struct{}

func (geminiAdapter) Name() string { return "GEMINI_CLI" }

func (geminiAdapter) BuildCommand(req AgentRunRequest) []string {
	// Fold the crewship preamble + agent SystemPrompt into the prompt body
	// since gemini-cli's headless docs do not expose a system-instruction
	// flag. Use a clear "[SYSTEM]" delimiter so a future model that does
	// honour role boundaries via prompt structure can still locate the
	// preamble. The message ordering is system-then-user, mirroring how
	// Anthropic's --system-prompt flag is rendered.
	prompt := req.UserMessage
	if sys := crewshipSystemPreamble + req.SystemPrompt; sys != "" {
		prompt = "[SYSTEM]\n" + sys + "\n\n[USER]\n" + req.UserMessage
	}
	cmd := []string{"gemini", "-p", prompt, "--output-format", "stream-json"}
	if req.LLMModel != "" {
		cmd = append(cmd, "-m", req.LLMModel)
	}
	return cmd
}

// UseStreamJSON returns true: gemini-cli emits NDJSON in stream-json mode.
// parseGeminiStreamJSON consumes it.
func (geminiAdapter) UseStreamJSON() bool { return true }

func (geminiAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseGeminiStreamJSON(line, handler)
}

// SetupSystemPrompt drops canonical memory files including GEMINI.md (which
// Gemini CLI auto-discovers per upstream docs). Combined with the [SYSTEM]/
// [USER] prepend in BuildCommand, Gemini agents have turn-1 context AND
// turn-2+ persistent memory parity with the rest of the adapters.
func (geminiAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("gemini adapter setup system prompt: %w", err)
	}
	if err := writeAgentSkills(ctx, container, containerID, workDir, req.Skills, logger); err != nil {
		logger.Warn("gemini adapter write agent skills failed", "error", err)
	}
	return nil
}

// SupportsMCP returns true: gemini-cli auto-discovers MCP servers from
// .gemini/settings.json regardless of headless / TTY mode (the headless docs
// explicitly preserve MCP behaviour).
func (geminiAdapter) SupportsMCP() bool { return true }

func (geminiAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeMCPGemini(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("gemini adapter write MCP config: %w", err)
	}
	return nil
}
