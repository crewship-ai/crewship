package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

// CLIAdapter is the per-CLI strategy for command building, container-side prompt
// setup, and stdout stream parsing. Each supported coding-agent CLI implements
// this interface; orchestrator_run.go and exec_stream.go dispatch through the
// registry below instead of switching on the adapter string in five places.
//
// All methods are stateless — adapters are singletons stored in adapterRegistry.
// New adapters are added by implementing this interface and registering an
// instance in init().
type CLIAdapter interface {
	// Name returns the canonical CLI adapter identifier (e.g. "CLAUDE_CODE").
	// Stored on AgentRunRequest.CLIAdapter and exposed on Agent.cli_adapter.
	Name() string

	// BuildCommand returns the argv slice the orchestrator will Exec inside
	// the container. The first element is the binary name. Implementations
	// must be pure functions of req — no I/O, no globals.
	BuildCommand(req AgentRunRequest) []string

	// UseStreamJSON declares whether ParseStreamLine should be invoked per
	// line of stdout. When false, streamOutput emits each line as a single
	// "text" event without parsing — used as a safe fallback for CLIs whose
	// JSON event schema we have not yet wired up.
	UseStreamJSON() bool

	// ParseStreamLine consumes one stdout line (without trailing newline) and
	// emits zero-or-more AgentEvents to handler. Implementations that have no
	// structured output should keep UseStreamJSON() == false instead of
	// implementing this; streamOutput will not call it.
	ParseStreamLine(line []byte, handler EventHandler)

	// SetupSystemPrompt writes any container-side files needed to convey the
	// crewship system preamble + agent system prompt to this CLI before it
	// runs (e.g. AGENTS.md for OpenCode/Cursor, .cursor/rules for Cursor).
	// CLIs that take the system prompt via a command-line flag (Claude Code,
	// Gemini) return nil here.
	SetupSystemPrompt(
		ctx context.Context,
		container provider.ContainerProvider,
		containerID string,
		req AgentRunRequest,
		workDir string,
		logger *slog.Logger,
	) error

	// SupportsMCP indicates whether this adapter has working MCP server
	// injection wired up. Used by orchestrator_run.go to decide whether to
	// call WriteMCPConfig at all.
	SupportsMCP() bool

	// WriteMCPConfig writes the per-CLI MCP server config file into the
	// container. Each CLI has its own file path + format:
	//   - Claude:   /crew/agents/<slug>/.mcp.json (mcpServers, JSON)
	//   - Codex:    <workdir>/.codex/config.toml ([mcp_servers.X], TOML!)
	//   - Gemini:   <workdir>/.gemini/settings.json (mcpServers, JSON)
	//   - OpenCode: <workdir>/opencode.json (mcp key, type:local/remote)
	//   - Cursor:   <workdir>/.cursor/mcp.json (mcpServers, broken in -p mode
	//               but we write it anyway for parity)
	//   - Droid:    <workdir>/.factory/mcp.json (mcpServers, type:stdio/http)
	//
	// req.MCPServers + req.CrewMCPConfigJSON + req.AgentMCPConfigJSON are the
	// inputs; the writer picks whichever is non-empty in the priority order
	// the original setupMCPConfig used (raw JSON > resolved server list).
	// Adapters that return SupportsMCP()==false should make this a no-op.
	WriteMCPConfig(
		ctx context.Context,
		container provider.ContainerProvider,
		containerID string,
		req AgentRunRequest,
		workDir string,
		logger *slog.Logger,
	) error
}

// adapterRegistry maps the CLIAdapter enum value (as stored on
// AgentRunRequest.CLIAdapter) to the adapter implementation. Lookup goes
// through getAdapter, which falls back to the Claude Code adapter for
// unknown values to preserve historical behaviour from BuildCLICommand's
// default arm.
var adapterRegistry = map[string]CLIAdapter{}

func registerAdapter(a CLIAdapter) {
	adapterRegistry[a.Name()] = a
}

func init() {
	registerAdapter(claudeCodeAdapter{})
	registerAdapter(codexAdapter{})
	registerAdapter(geminiAdapter{})
	registerAdapter(opencodeAdapter{})
	registerAdapter(cursorAdapter{})
	registerAdapter(droidAdapter{})
}

// getAdapter returns the adapter for a CLI identifier, falling back to a
// minimal "unknown" adapter that emits a bare `claude --print <msg>` command.
// This matches the pre-refactor behaviour of BuildCLICommand's default arm
// (which had the same minimal fallback) and is asserted by failover_test.go
// "unknown defaults to claude".
func getAdapter(name string) CLIAdapter {
	if a, ok := adapterRegistry[name]; ok {
		return a
	}
	return unknownAdapter{}
}

// unknownAdapter is the fallback returned by getAdapter for any CLIAdapter
// string we do not recognise. It produces a minimal `claude --print <msg>`
// command (no system prompt, no flags) — enough to be runnable for
// debugging, not enough to be useful in production. Acts as a safety net
// so a malformed agent record cannot crash the orchestrator.
type unknownAdapter struct{}

func (unknownAdapter) Name() string { return "" }

func (unknownAdapter) BuildCommand(req AgentRunRequest) []string {
	return []string{"claude", "--print", req.UserMessage}
}

func (unknownAdapter) UseStreamJSON() bool { return false }

func (unknownAdapter) ParseStreamLine(line []byte, handler EventHandler) {}

func (unknownAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

func (unknownAdapter) SupportsMCP() bool { return false }

func (unknownAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

// writeCanonicalMemoryFiles drops the assembled system prompt + persistent
// agent memory blob into every memory file each CLI auto-discovers. This is
// the user's hard requirement: "memory must be the same for all agents in a
// workspace, no matter which CLI runs them" — without this every Codex /
// Droid / Gemini agent runs with zero memory access by default because their
// CLIs read AGENTS.md / GEMINI.md / .factory/AGENTS.md respectively, none of
// which the orchestrator wrote pre-fix.
//
// The canonical body is the same string that Claude Code receives via its
// --system-prompt flag (crewshipSystemPreamble + req.SystemPrompt, where
// SystemPrompt has already been merged with persistent + crew-shared memory
// upstream in orchestrator_run.go). By writing it to ALL discovery paths
// regardless of which adapter we're serving, an agent swapping its
// cli_adapter from CLAUDE_CODE to CODEX_CLI sees the same context next turn.
//
// Discovery paths covered:
//   - AGENTS.md           — OpenCode, Cursor, Codex, Droid auto-discover this
//   - CLAUDE.md           — Cursor; Claude Code skips it under --bare (we still
//     emit so a user disabling --bare gets parity)
//   - GEMINI.md           — Gemini CLI auto-discovers
//   - .cursor/rules/crewship.md — Cursor priority path, takes precedence
//     over AGENTS.md for that CLI
//   - .factory/AGENTS.md  — Factory Droid alternate path
//
// Each path is written best-effort — a failure on one file does not abort the
// others, because partial parity is still better than none. Errors collect
// into a single returned error so the caller can log details.
func writeCanonicalMemoryFiles(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	body := crewshipSystemPreamble + req.SystemPrompt
	targets := []string{
		"AGENTS.md",
		"CLAUDE.md",
		"GEMINI.md",
		".cursor/rules/crewship.md",
		".factory/AGENTS.md",
	}
	var firstErr error
	for _, t := range targets {
		if err := writeFileViaContainer(ctx, container, containerID, workDir, t, body, logger); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if logger != nil {
				logger.Warn("canonical memory file write failed", "path", t, "error", err)
			}
		}
	}
	return firstErr
}

// writeFileViaContainer is a small helper used by adapters that need to drop
// a single text file (system prompt, config) into the container before the CLI
// runs. The content is base64-encoded over the shell to avoid any quoting or
// heredoc-delimiter problems with arbitrary user-supplied prompts.
//
// SECURITY: chmod 600 is applied unconditionally because all agents in a crew
// share one container and run as the same UID (1001). MCP configs may contain
// literal API tokens (e.g. Codex env-block values that the user typed
// directly). Without 600 perms a sibling agent could `cat` the config and
// exfiltrate the token. Match the pre-existing pattern in setupMCPConfig +
// setupClaudeConfig (exec_mcp.go) which already chmod 600 their writes.
func writeFileViaContainer(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	workDir string,
	relPath string,
	content string,
	logger *slog.Logger,
) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	escapedPath := shellEscape(relPath)
	script := fmt.Sprintf("mkdir -p \"$(dirname %s)\" && echo %s | base64 -d > %s && chmod 600 %s",
		escapedPath, encoded, escapedPath, escapedPath)

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		WorkingDir:  workDir,
		User:        "1001:1001",
	}
	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	if logger != nil {
		logger.Debug("file written into container", "path", relPath)
	}
	return nil
}
