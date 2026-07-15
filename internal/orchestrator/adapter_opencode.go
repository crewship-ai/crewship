package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// opencodeAdapter wires sst.dev's `opencode` CLI (opencode-ai npm package,
// 1.14.33 as of 2026-05). OpenCode is BYOK across providers: the user picks
// which provider/model to route to via the -m flag (e.g. anthropic/claude-...)
// and the corresponding provider's API key needs to be in the container env.
//
// Canonical non-interactive form per opencode.ai/docs/cli/ is `opencode run
// <message>`. Relevant flags:
//   - --format        : default | json   (NOT --output-format — different name)
//     json emits JSONL — one flat event envelope per line (step_start,
//     text, tool_use, step_finish, error); parseOpenCodeStreamJSON consumes
//     it line-by-line like every other stream-JSON adapter
//   - --model, -m     : "provider/model" — e.g. anthropic/claude-sonnet-4-6
//   - --continue, -c  : resume last session
//   - --session, -s   : resume specific session
//
// Known upstream caveat (anomalyco/opencode#26855): the process can exit
// before emitting the final step_finish envelope. streamOutput synthesizes a
// terminal result in that case so run finalization never hangs on it.
//
// Native MCP support exists upstream via the `mcp` section of opencode.json;
// SetupSystemPrompt drops AGENTS.md (system instructions) into the work
// directory so the agent persona persists across CLI invocations.
type opencodeAdapter struct{}

func (opencodeAdapter) Name() string { return "OPENCODE" }

// PromptViaStdin is false: OpenCode is not confirmed to read its prompt from
// stdin, so the message keeps being passed as an argument unchanged.
func (opencodeAdapter) PromptViaStdin(req AgentRunRequest) bool { return false }

func (opencodeAdapter) BuildCommand(req AgentRunRequest) []string {
	cmd := []string{"opencode", "run", "--format", "json"}
	if req.LLMModel != "" {
		// OpenCode BYOKs across providers, so --model MUST be in
		// "provider/model" form. A bare model (e.g. "claude-sonnet-4-6")
		// is unroutable and opencode dies before its first LLM call with an
		// opaque UnknownError (#1007, reproduced live on dev2). Qualify it
		// with the agent's declared provider when the model doesn't already
		// carry a provider segment. Unknown/empty provider → pass through
		// unchanged (the parser now surfaces opencode's error either way).
		cmd = append(cmd, "--model", qualifyOpenCodeModel(req.LLMProvider, req.LLMModel))
	}
	// Turn-1 parity: OpenCode reads AGENTS.md between invocations but the
	// first user message in a fresh container has no system context. Prepend
	// the system prompt with [SYSTEM]/[USER] delimiters so the model sees
	// preamble + persona + memory on turn 1. Same fix every other non-Claude
	// adapter uses.
	prompt := req.UserMessage
	if sys := strings.TrimSpace(crewshipSystemPreamble + req.SystemPrompt); sys != "" {
		prompt = "[SYSTEM]\n" + sys + "\n\n[USER]\n" + req.UserMessage
	}
	// `--` separator: see adapter_codex.go for rationale.
	cmd = append(cmd, "--", prompt)
	return cmd
}

// UseStreamJSON returns true: --format json emits JSONL and
// parseOpenCodeStreamJSON consumes one event envelope per line.
func (opencodeAdapter) UseStreamJSON() bool { return true }

// openCodeProviderIDs maps Crewship's llm_provider enum to the provider id
// OpenCode expects as the first "provider/model" segment (models.dev / AI-SDK
// naming). Only providers Crewship can assign to an OPENCODE agent are listed;
// anything else falls through to "pass model unchanged" so we never fabricate a
// provider we can't stand behind. OLLAMA is included for completeness but its
// models already arrive as "ollama/…" via localModelPrefix, so the has-slash
// guard in qualifyOpenCodeModel short-circuits before we consult this map.
var openCodeProviderIDs = map[string]string{
	"ANTHROPIC": "anthropic",
	"OPENAI":    "openai",
	"GOOGLE":    "google",
	"OLLAMA":    "ollama",
}

// ModelNameProviderID infers the LLM provider id from a bare model name's
// well-known prefix. This is AUTHORITATIVE over the agent's configured provider
// because the model can be overridden per call — a pipeline step tier override
// or CREWSHIP_SUBAGENT_MODEL can name a bare model belonging to a DIFFERENT
// provider than the agent's static llm_provider. Pairing the override model
// with the static provider would mis-stamp it (e.g. "anthropic/gpt-4o-mini")
// and misroute the run — the exact opaque failure #1007 set out to kill. The
// provider must follow the model. Returns "" when the name reveals nothing.
//
// Exported (originally OpenCode-adapter-private) so callers outside this
// package can derive a paymaster-compatible provider string ("anthropic",
// "openai", "google", "xai") from a bare model slug without duplicating this
// prefix table — see chatbridge/scheduler's #1205 cost-ledger fallback path.
func ModelNameProviderID(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude-"),
		strings.Contains(m, "sonnet"),
		strings.Contains(m, "haiku"),
		strings.Contains(m, "opus"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt-"),
		strings.HasPrefix(m, "chatgpt"),
		strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"):
		return "openai"
	case strings.HasPrefix(m, "gemini-"):
		return "google"
	case strings.HasPrefix(m, "grok-"):
		return "xai"
	}
	return ""
}

// qualifyOpenCodeModel returns model in OpenCode's required "provider/model"
// form. A model that already carries a "/" segment is returned untouched (it is
// either already qualified or an "ollama/…" local model). For a bare model we
// prefer the provider the MODEL NAME implies (correct for cross-provider
// per-call overrides), and fall back to the agent's configured provider only
// when the name reveals nothing. An empty/unknown provider yields the bare
// model unchanged rather than a guess.
func qualifyOpenCodeModel(provider, model string) string {
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	if id := ModelNameProviderID(model); id != "" {
		return id + "/" + model
	}
	if id, ok := openCodeProviderIDs[strings.ToUpper(strings.TrimSpace(provider))]; ok {
		return id + "/" + model
	}
	return model
}

func (opencodeAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseOpenCodeStreamJSON(line, handler)
}

// SetupSystemPrompt drops the canonical memory file set instead of just
// AGENTS.md. OpenCode auto-discovers AGENTS.md primarily, but the unified
// writer keeps memory parity with every other adapter — a Cursor agent
// switching to OpenCode mid-mission sees the same context.
func (opencodeAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeCanonicalMemoryFiles(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("opencode adapter setup system prompt: %w", err)
	}
	if err := writeAgentSkills(ctx, container, containerID, workDir, req.Skills, logger); err != nil {
		logger.Warn("opencode adapter write agent skills failed", "error", err)
	}
	return nil
}

// SupportsMCP returns true. OpenCode reads opencode.json with MCP servers
// under the `mcp` key. Schema differs significantly from Claude Code:
// type:local|remote, command is array, env field is "environment", env-var
// syntax is {env:VAR}. writeMCPOpenCode handles all the translation.
func (opencodeAdapter) SupportsMCP() bool { return true }

func (opencodeAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	if err := writeMCPOpenCode(ctx, container, containerID, req, workDir, logger); err != nil {
		return fmt.Errorf("opencode adapter write MCP config: %w", err)
	}
	return nil
}
