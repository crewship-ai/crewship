package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// exec_mcp_npx.go — nodeJSLauncher, isNpxCommand, filterNpxServers,
// filterMergedMCPConfigNpx.
//
// These run before sidecar boot to drop MCP stdio servers whose npm/npx
// launcher isn't installed in the agent container — without this gate,
// a missing launcher surfaces as a confusing "tool not found" mid-run.
// ---------------------------------------------------------------------------

// fakeExecContainer is a tiny ContainerProvider whose only purpose is to
// answer the probe command (`command -v npx`/`npm`). availableCommands
// is the set of "yes I have this" launchers; cmds not in the set get an
// empty-string response (the source treats empty as "missing").
type fakeExecContainer struct {
	availableCommands map[string]bool
	execErr           error
	execCalls         int
}

func (f *fakeExecContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	f.execCalls++
	if f.execErr != nil {
		return nil, f.execErr
	}
	// Probe command shape is: ["sh", "-c", "command -v <launcher> >/dev/null 2>&1 && echo ok"].
	// Extract <launcher> from the inner sh -c arg so the test can answer
	// per-launcher.
	var output string
	for _, launcher := range []string{"npx", "npm"} {
		if len(cfg.Cmd) >= 3 && strings.Contains(cfg.Cmd[2], "command -v "+launcher+" ") {
			if f.availableCommands[launcher] {
				output = "ok\n"
			}
			break
		}
	}
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(output))}, nil
}

// The rest of the ContainerProvider surface is unused by the npx filter;
// we satisfy it with zero-value returns.
func (f *fakeExecContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (f *fakeExecContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (f *fakeExecContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *fakeExecContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (f *fakeExecContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *fakeExecContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return true, 0, nil
}
func (f *fakeExecContainer) CrewContainerName(slug string) string { return "crewship-team-" + slug }
func (f *fakeExecContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*fakeExecContainer)(nil)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// ---- nodeJSLauncher ----

func TestNodeJSLauncher_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace-only", "   ", ""},
		{"npx-bare", "npx", "npx"},
		{"npx-with-args", "npx -y @modelcontextprotocol/server-everything", "npx"},
		{"npm-bare", "npm", "npm"},
		{"npm-with-args", "npm exec mcp-server", "npm"},
		{"npx-with-leading-spaces", "  npx foo", "npx"},
		{"node-not-allowed", "node server.js", ""},
		{"python-not-allowed", "python -m server", ""},
		{"path-not-stripped", "/usr/local/bin/npx foo", ""}, // pin: first-token-exact, no basename
		{"empty-after-strip", "    ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeJSLauncher(tc.in); got != tc.want {
				t.Errorf("nodeJSLauncher(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---- isNpxCommand ----

func TestIsNpxCommand_DelegatesToNodeJSLauncher(t *testing.T) {
	// isNpxCommand returns true iff nodeJSLauncher returns non-empty.
	// Pin the delegation contract with both true and false fixtures.
	if !isNpxCommand("npx foo") {
		t.Error("isNpxCommand(\"npx foo\") = false, want true")
	}
	if !isNpxCommand("npm exec bar") {
		t.Error("isNpxCommand(\"npm exec bar\") = false, want true")
	}
	if isNpxCommand("node server.js") {
		t.Error("isNpxCommand(\"node server.js\") = true, want false")
	}
	if isNpxCommand("") {
		t.Error("isNpxCommand(\"\") = true, want false")
	}
}

// ---- filterNpxServers ----

func TestFilterNpxServers_NoNpxServers_NoProbeNoFiltering(t *testing.T) {
	// Short-circuit: if no server uses npx/npm, the probe MUST NOT run —
	// avoids a needless Exec round-trip on every container with HTTP-only
	// MCP servers.
	fake := &fakeExecContainer{}
	in := []MCPServerConfig{
		{Name: "remote-a", Transport: "http", Endpoint: "https://x.example/mcp"},
		{Name: "stdio-non-npx", Transport: "stdio", Command: "python -m server"},
	}
	out := filterNpxServers(context.Background(), fake, "ct-1", in, quietLogger())
	if len(out) != len(in) {
		t.Errorf("expected unchanged slice, got %d → %d", len(in), len(out))
	}
	if fake.execCalls != 0 {
		t.Errorf("probe ran %d times; expected zero (no npx servers means no probe)", fake.execCalls)
	}
}

func TestFilterNpxServers_AllLaunchersAvailable_NoFiltering(t *testing.T) {
	fake := &fakeExecContainer{availableCommands: map[string]bool{"npx": true, "npm": true}}
	in := []MCPServerConfig{
		{Name: "filesystem", Transport: "stdio", Command: "npx -y @modelcontextprotocol/server-filesystem"},
		{Name: "memory", Transport: "stdio", Command: "npm exec mcp-memory"},
	}
	out := filterNpxServers(context.Background(), fake, "abcdef123456abcd", in, quietLogger())
	if len(out) != len(in) {
		t.Errorf("expected nothing filtered (both launchers available), got %d → %d", len(in), len(out))
	}
}

func TestFilterNpxServers_NpxMissing_DropsMatchingServers(t *testing.T) {
	// npx missing, npm available. Filter must drop the npx server only.
	fake := &fakeExecContainer{availableCommands: map[string]bool{"npm": true}}
	in := []MCPServerConfig{
		{Name: "needs-npx", Transport: "stdio", Command: "npx -y server-everything"},
		{Name: "needs-npm", Transport: "stdio", Command: "npm exec mcp-memory"},
		{Name: "http-keep", Transport: "http", Endpoint: "https://x/mcp"},
	}
	out := filterNpxServers(context.Background(), fake, "abcdef123456", in, quietLogger())
	if len(out) != 2 {
		t.Fatalf("expected 2 surviving (npm + http), got %d: %+v", len(out), out)
	}
	for _, s := range out {
		if s.Name == "needs-npx" {
			t.Errorf("npx-requiring server should have been filtered: %+v", s)
		}
	}
}

func TestFilterNpxServers_BothMissing_DropsAllStdio(t *testing.T) {
	fake := &fakeExecContainer{availableCommands: map[string]bool{}}
	in := []MCPServerConfig{
		{Name: "needs-npx", Transport: "stdio", Command: "npx -y server"},
		{Name: "needs-npm", Transport: "stdio", Command: "npm exec mcp"},
		{Name: "http-keep", Transport: "http", Endpoint: "https://x/mcp"},
	}
	out := filterNpxServers(context.Background(), fake, "ct12345678abc", in, quietLogger())
	if len(out) != 1 {
		t.Fatalf("expected 1 (http only), got %d: %+v", len(out), out)
	}
	if out[0].Name != "http-keep" {
		t.Errorf("survivor = %q, want http-keep", out[0].Name)
	}
}

func TestFilterNpxServers_ProbeError_KeepsServers(t *testing.T) {
	// Source: "Exec failure (...) don't drop the server; remove from map
	// so it won't be filtered out." A container that's still booting must
	// not silently lose MCP configuration to a transient probe failure.
	fake := &fakeExecContainer{
		availableCommands: nil,
		execErr:           errors.New("container not ready"),
	}
	in := []MCPServerConfig{
		{Name: "needs-npx", Transport: "stdio", Command: "npx -y server"},
	}
	out := filterNpxServers(context.Background(), fake, "abcdef12345678", in, quietLogger())
	if len(out) != 1 {
		t.Errorf("probe error should keep servers; got %d, want 1", len(out))
	}
}

func TestFilterNpxServers_TruncatesContainerID_ShortContainer(t *testing.T) {
	// Container IDs shorter than 12 chars must not panic on the
	// containerID[:12] slice — source uses min(12, len(containerID)) to
	// clip safely. Pin with a 4-char id.
	fake := &fakeExecContainer{availableCommands: map[string]bool{}}
	in := []MCPServerConfig{
		{Name: "needs-npx", Transport: "stdio", Command: "npx foo"},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("short containerID panicked: %v", r)
		}
	}()
	_ = filterNpxServers(context.Background(), fake, "abcd", in, quietLogger())
}

// ---- filterMergedMCPConfigNpx ----

func TestFilterMergedMCPConfigNpx_EmptyInput_ReturnsUnchanged(t *testing.T) {
	fake := &fakeExecContainer{}
	got, skipped := filterMergedMCPConfigNpx(context.Background(), fake, "ct", "", quietLogger())
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if skipped != nil {
		t.Errorf("skipped = %v, want nil", skipped)
	}
	if fake.execCalls != 0 {
		t.Errorf("probe ran on empty input: %d calls", fake.execCalls)
	}
}

func TestFilterMergedMCPConfigNpx_MalformedJSON_PassesThroughUnchanged(t *testing.T) {
	// Source: a parse failure returns the input verbatim — agents see
	// what they put in; the orchestrator surfaces the parse error
	// downstream rather than silently dropping the config.
	fake := &fakeExecContainer{}
	in := "not-valid-json"
	got, skipped := filterMergedMCPConfigNpx(context.Background(), fake, "ct", in, quietLogger())
	if got != in {
		t.Errorf("malformed input mutated: %q → %q", in, got)
	}
	if skipped != nil {
		t.Errorf("skipped = %v, want nil", skipped)
	}
}

func TestFilterMergedMCPConfigNpx_NothingFiltered_ReturnsOriginalString(t *testing.T) {
	// All launchers available → no filtering → return the EXACT input
	// string (not a re-marshalled equivalent). Pin this so callers can
	// rely on pointer-stable round-trip when no changes are needed.
	fake := &fakeExecContainer{availableCommands: map[string]bool{"npx": true}}
	in := `{"mcpServers":{"keep":{"type":"stdio","command":"npx -y foo"}}}`
	got, skipped := filterMergedMCPConfigNpx(context.Background(), fake, "ct", in, quietLogger())
	if got != in {
		t.Errorf("got %q, want input unchanged %q", got, in)
	}
	if skipped != nil {
		t.Errorf("skipped = %v, want nil", skipped)
	}
}

func TestFilterMergedMCPConfigNpx_DropsMissingLauncherServer(t *testing.T) {
	fake := &fakeExecContainer{availableCommands: map[string]bool{}}
	in := `{"mcpServers":{
		"needs-npx":{"type":"stdio","command":"npx -y foo"},
		"http-keep":{"type":"http","command":"unused"}
	}}`
	got, skipped := filterMergedMCPConfigNpx(context.Background(), fake, "abcdef12abcdef", in, quietLogger())
	if got == in {
		t.Error("expected output to differ from input after filtering")
	}
	// The "needs-npx" key MUST be gone from the re-marshalled JSON.
	var wrapper map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &wrapper); err != nil {
		t.Fatalf("output isn't valid JSON: %v (%q)", err, got)
	}
	if _, present := wrapper["mcpServers"]["needs-npx"]; present {
		t.Errorf("needs-npx still in output: %s", got)
	}
	if _, present := wrapper["mcpServers"]["http-keep"]; !present {
		t.Errorf("http-keep missing from output: %s", got)
	}
	if len(skipped) != 1 || skipped[0] != "needs-npx" {
		t.Errorf("skipped = %v, want [needs-npx]", skipped)
	}
}

func TestFilterMergedMCPConfigNpx_AllDropped_ReturnsEmptyString(t *testing.T) {
	// Source: "if len(w.MCPServers) == 0 { return "", skipped }".
	// Pin that the empty-mcpServers case returns the empty string (not
	// "{}" or some other JSON shape) so the sidecar treats it as
	// "no MCP servers" rather than "empty servers object".
	fake := &fakeExecContainer{availableCommands: map[string]bool{}}
	in := `{"mcpServers":{"a":{"type":"stdio","command":"npx -y foo"}}}`
	got, skipped := filterMergedMCPConfigNpx(context.Background(), fake, "abcdef12abcdef", in, quietLogger())
	if got != "" {
		t.Errorf("got %q, want empty string (no servers left)", got)
	}
	if len(skipped) != 1 || skipped[0] != "a" {
		t.Errorf("skipped = %v, want [a]", skipped)
	}
}

func TestFilterMergedMCPConfigNpx_MalformedEntry_PreservedNotFiltered(t *testing.T) {
	// Source: "Preserve entries that failed to parse — they weren't
	// filtered by npx logic." An entry whose serverEntry shape is bad
	// stays in the output rather than getting silently swept.
	fake := &fakeExecContainer{availableCommands: map[string]bool{}}
	in := `{"mcpServers":{
		"needs-npx":{"type":"stdio","command":"npx -y foo"},
		"weird":"this-is-not-an-object-just-a-string"
	}}`
	got, _ := filterMergedMCPConfigNpx(context.Background(), fake, "abcdef12abcdef", in, quietLogger())
	var wrapper map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &wrapper); err != nil {
		t.Fatalf("output not valid JSON: %v (%q)", err, got)
	}
	if _, ok := wrapper["mcpServers"]["weird"]; !ok {
		t.Errorf("malformed entry was filtered out; should be preserved (only npx-driven removals happen here)")
	}
}
