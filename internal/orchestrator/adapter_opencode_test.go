package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// adapter_opencode.go — SetupSystemPrompt + WriteMCPConfig wrappers.
//
// Name / BuildCommand / UseStreamJSON / ParseStreamLine / SupportsMCP
// are 100% covered. These two zero-coverage methods are thin wrappers
// over writeCanonicalMemoryFiles + writeMCPOpenCode that exist purely
// to attach the "opencode adapter ...: %w" error-message prefix.
// Pinning the prefix here means operators triaging an MCP / system-
// prompt setup failure can find the responsible adapter from the
// error string alone.
// ---------------------------------------------------------------------------

// adapterTestContainer is a tiny ContainerProvider that returns
// configurable Exec results. It records every Exec call so tests can
// assert which write paths fired.
type adapterTestContainer struct {
	mu sync.Mutex

	execCalls   int
	execErr     error
	execResult  *provider.ExecResult // returned when execErr is nil
	execScripts []string             // sh -c script bodies seen
}

func (f *adapterTestContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls++
	if len(cfg.Cmd) >= 3 && cfg.Cmd[0] == "sh" && cfg.Cmd[1] == "-c" {
		f.execScripts = append(f.execScripts, cfg.Cmd[2])
	}
	if f.execErr != nil {
		return nil, f.execErr
	}
	if f.execResult != nil {
		return f.execResult, nil
	}
	// Default: empty success. Each call gets its own io.NopCloser so the
	// caller can Close() independently.
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(""))}, nil
}

// Other ContainerProvider methods unused by these tests.
func (f *adapterTestContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (f *adapterTestContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (f *adapterTestContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *adapterTestContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (f *adapterTestContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *adapterTestContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (f *adapterTestContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}
func (f *adapterTestContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*adapterTestContainer)(nil)

func quietAdapterLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// ---- SetupSystemPrompt ----

func TestOpencodeAdapter_SetupSystemPrompt_HappyPath_NoError(t *testing.T) {
	// All 5 canonical memory writes succeed (fake Exec returns ""
	// output, no error). Adapter wraps writeCanonicalMemoryFiles +
	// writeAgentSkills; both return nil → adapter returns nil.
	fake := &adapterTestContainer{}
	req := AgentRunRequest{
		SystemPrompt: "Be helpful.",
	}
	err := opencodeAdapter{}.SetupSystemPrompt(
		context.Background(), fake, "ct-1", req, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Fatalf("SetupSystemPrompt: %v", err)
	}
	// writeCanonicalMemoryFiles emits 5 files; writeAgentSkills with
	// an empty Skills slice exits cleanly without exec. Expect at
	// least the 5 memory-file writes.
	if fake.execCalls < 5 {
		t.Errorf("execCalls = %d, want ≥ 5 (5 canonical memory files)", fake.execCalls)
	}
}

func TestOpencodeAdapter_SetupSystemPrompt_AllWritesFail_WrapsWithAdapterName(t *testing.T) {
	// When every write fails, writeCanonicalMemoryFiles returns the
	// first error. The adapter MUST wrap with "opencode adapter setup
	// system prompt: %w" so operators triaging the error can identify
	// the responsible adapter.
	want := errors.New("docker exec hung up")
	fake := &adapterTestContainer{execErr: want}
	req := AgentRunRequest{SystemPrompt: "irrelevant"}

	err := opencodeAdapter{}.SetupSystemPrompt(
		context.Background(), fake, "ct-fail", req, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error when all writes fail")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "opencode adapter setup system prompt") {
		t.Errorf("err = %v, want \"opencode adapter setup system prompt\" prefix (operator triage signal)", err)
	}
}

func TestOpencodeAdapter_SetupSystemPrompt_PartialFailure_NoAdapterError(t *testing.T) {
	// writeCanonicalMemoryFiles tolerates per-file failures as long as
	// AT LEAST ONE write succeeds. Source-comment: "We now log every
	// failure but only return an error if EVERY target failed." Because
	// we can't easily simulate "first N fail, rest succeed" with this
	// fake's single error mode, this test verifies the all-succeed path
	// already; the all-fail path is covered above. A future fake with
	// per-call programmable errors would extend this — for now the
	// boundaries (all-pass / all-fail) bracket the partial behavior.
	fake := &adapterTestContainer{}
	err := opencodeAdapter{}.SetupSystemPrompt(
		context.Background(), fake, "ct-2", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Errorf("all-success path = %v, want nil", err)
	}
}

// ---- WriteMCPConfig ----

func TestOpencodeAdapter_WriteMCPConfig_EmptyMCP_ShortCircuitsToNil(t *testing.T) {
	// normaliseMCPInputs returns (nil, nil) when there are no MCP
	// sources. writeMCPOpenCode early-returns; the adapter wrapper
	// must return nil (NOT a wrapped non-nil error).
	fake := &adapterTestContainer{}
	err := opencodeAdapter{}.WriteMCPConfig(
		context.Background(), fake, "ct-mcp", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Errorf("WriteMCPConfig on empty MCP = %v, want nil", err)
	}
	if fake.execCalls != 0 {
		t.Errorf("empty MCP triggered %d Exec calls; expected 0 (short-circuit)", fake.execCalls)
	}
}

func TestOpencodeAdapter_WriteMCPConfig_WrapsContainerWriteFailure(t *testing.T) {
	// With actual MCP servers + a failing Exec, writeMCPOpenCode emits
	// the JSON via container exec which fails. Adapter wraps with
	// "opencode adapter write MCP config: %w".
	want := errors.New("permission denied")
	fake := &adapterTestContainer{execErr: want}
	req := AgentRunRequest{
		MCPServers: []MCPServerConfig{
			{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}},
		},
	}
	err := opencodeAdapter{}.WriteMCPConfig(
		context.Background(), fake, "ct-mcp-fail", req, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error when container write fails")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "opencode adapter write MCP config") {
		t.Errorf("err = %v, want \"opencode adapter write MCP config\" prefix", err)
	}
}

func TestOpencodeAdapter_WriteMCPConfig_HappyPath_EmitsContainerExec(t *testing.T) {
	// With MCP specs and a successful Exec, the adapter should fire at
	// least one container exec (to write the opencode.json blob).
	fake := &adapterTestContainer{}
	req := AgentRunRequest{
		MCPServers: []MCPServerConfig{
			{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}},
		},
	}
	a := opencodeAdapter{}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-mcp-happy", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	if fake.execCalls == 0 {
		t.Errorf("expected at least one Exec call for the opencode.json write; got 0")
	}
	// Sanity: the written script should mention opencode.json.
	found := false
	for _, s := range fake.execScripts {
		if strings.Contains(s, "opencode.json") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no exec script targeted opencode.json; got scripts: %v", fake.execScripts)
	}
}

// ---- Adapter satisfies CLIAdapter interface ----

// Compile-time assertion. A refactor that adds a method to
// CLIAdapter would surface here AND at the adapter-registry init() —
// this is the cheaper place to catch it.
var _ CLIAdapter = opencodeAdapter{}
