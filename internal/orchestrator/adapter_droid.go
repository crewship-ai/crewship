package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// droidAdapter wires Factory's `droid exec` headless mode (install:
// `curl -fsSL https://app.factory.ai/cli | sh`). First-class as of the
// MCP-everywhere wave: stream-json output, FACTORY_API_KEY auth, and
// .factory/mcp.json MCP servers.
//
// Tiered autonomy via --auto: low (read-only), medium (file edits), high
// (fully autonomous). MINIMAL/CONSULTATIVE tool profiles downgrade to low
// because those signal "agent should not mutate"; everything else stays at
// medium — the API normalises empty ToolProfile to "CODING" before this point,
// so production traffic is overwhelmingly medium.
//
// Known headless gotcha: `droid exec --mission` fails to spawn its background
// daemon in containers without a TTY (Factory issue #794). We never pass
// --mission so the daemon path is avoided.
type droidAdapter struct{}

func (droidAdapter) Name() string { return "FACTORY_DROID" }

func (droidAdapter) BuildCommand(req AgentRunRequest) []string {
	// Map validated tool_profile (lib/validations.ts: MINIMAL/CODING/FULL)
	// onto Droid's --auto autonomy: low/medium/high. MESSAGING was retired
	// in #261 — the three remaining profiles map straight through.
	autonomy := "medium"
	switch req.ToolProfile {
	case "MINIMAL":
		autonomy = "low"
	case "FULL":
		autonomy = "high"
	}
	cmd := []string{"droid", "exec", "--auto", autonomy, "-o", "stream-json"}
	if req.LLMModel != "" {
		cmd = append(cmd, "--model", req.LLMModel)
	}
	// Droid exec has no --system-prompt flag; same turn-1 strategy as Codex
	// — fold the system prompt + memory into the user message so the first
	// invocation has context, then SetupSystemPrompt drops AGENTS.md +
	// .factory/AGENTS.md for turn-2+ via Droid's discovery.
	prompt := req.UserMessage
	if sys := strings.TrimSpace(crewshipSystemPreamble + req.SystemPrompt); sys != "" {
		prompt = "[SYSTEM]\n" + sys + "\n\n[USER]\n" + req.UserMessage
	}
	// `--` separator: see adapter_codex.go for rationale (user-message
	// dash-prefix safety).
	cmd = append(cmd, "--", prompt)
	return cmd
}

// UseStreamJSON returns true: -o stream-json emits NDJSON events. The schema
// is not formally published by Factory; parser_droid.go does best-effort
// extraction (text/tool/result discriminators) and falls back to raw text
// for unknown event shapes — fixture data needed for tighter parsing.
func (droidAdapter) UseStreamJSON() bool { return true }

func (droidAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseDroidStreamJSON(line, handler)
}

func (droidAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("droid adapter setup system prompt: %w", err)
	}
	if err := writeAgentSkills(ctx, container, containerID, workDir, req.Skills, logger); err != nil {
		logger.Warn("droid adapter write agent skills failed", "error", err)
	}
	return nil
}

// SupportsMCP returns true. Droid auto-discovers .factory/mcp.json at session
// start. Schema is mcpServers map (Anthropic-compatible) with explicit
// "type": "stdio" | "http" required.
func (droidAdapter) SupportsMCP() bool { return true }

func (droidAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeMCPDroid(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("droid adapter write MCP config: %w", err)
	}
	return nil
}
