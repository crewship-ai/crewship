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
	// call setupMCPConfig at all. Currently true only for CLAUDE_CODE and
	// OPENCODE; the other CLIs either lack upstream MCP support or have a
	// different config shape we have not implemented yet.
	SupportsMCP() bool
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

// writeFileViaContainer is a small helper used by adapters that need to drop
// a single text file (system prompt, config) into the container before the CLI
// runs. The content is base64-encoded over the shell to avoid any quoting or
// heredoc-delimiter problems with arbitrary user-supplied prompts.
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
	script := fmt.Sprintf("mkdir -p \"$(dirname %s)\" && echo %s | base64 -d > %s",
		shellEscape(relPath), encoded, shellEscape(relPath))

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
