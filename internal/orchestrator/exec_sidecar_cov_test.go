package orchestrator

// Coverage tests for exec_sidecar.go: PreRunInstallPackages, checkSidecar,
// startSidecar, credTypeToProvider. Uses covContainer, a scripted
// ContainerProvider fake that records every Exec call and lets each test
// route replies / exit codes per script.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covContainer is a scripted ContainerProvider fake shared by the *_cov_test
// files. route (optional) inspects each ExecConfig and may return a reply;
// returning (nil, nil) falls through to a default empty-success result.
// inspect (optional) keys exit codes by ExecID.
type covContainer struct {
	mu      sync.Mutex
	calls   []provider.ExecConfig
	route   func(cfg provider.ExecConfig) (*provider.ExecResult, error)
	inspect func(execID string) (bool, int, error)
}

func (c *covContainer) snapshotCalls() []provider.ExecConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]provider.ExecConfig, len(c.calls))
	copy(out, c.calls)
	return out
}

func (c *covContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "cov-container", nil
}
func (c *covContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *covContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *covContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *covContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, cfg)
	c.mu.Unlock()
	if c.route != nil {
		res, err := c.route(cfg)
		if res != nil || err != nil {
			return res, err
		}
	}
	return &provider.ExecResult{ExecID: "cov-default", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (c *covContainer) ExecInspect(_ context.Context, execID string) (bool, int, error) {
	if c.inspect != nil {
		return c.inspect(execID)
	}
	return false, 0, nil
}
func (c *covContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *covContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *covContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*covContainer)(nil)

func covScript(cfg provider.ExecConfig) string { return strings.Join(cfg.Cmd, " ") }

func covQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func covResult(execID, body string) *provider.ExecResult {
	return &provider.ExecResult{ExecID: execID, Reader: io.NopCloser(strings.NewReader(body))}
}

// ---- PreRunInstallPackages ----

func TestPreRunInstallPackages_EmptyIsNoop(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	if err := PreRunInstallPackages(context.Background(), c, "ctr1", nil, covQuietLogger()); err != nil {
		t.Fatalf("expected nil error for empty package list, got %v", err)
	}
	if len(c.snapshotCalls()) != 0 {
		t.Errorf("expected zero execs for empty package list, got %d", len(c.snapshotCalls()))
	}
}

func TestPreRunInstallPackages_RejectsUnsafeNames(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	err := PreRunInstallPackages(context.Background(), c, "ctr1", []string{"curl", "evil;rm -rf /"}, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "invalid package name") {
		t.Fatalf("expected invalid package name error, got %v", err)
	}
	if len(c.snapshotCalls()) != 0 {
		t.Errorf("no exec must run when validation fails, got %d calls", len(c.snapshotCalls()))
	}
}

func TestPreRunInstallPackages_ExecError(t *testing.T) {
	t.Parallel()
	c := &covContainer{route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
		return nil, errors.New("docker down")
	}}
	err := PreRunInstallPackages(context.Background(), c, "ctr1", []string{"git"}, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "pre-run install") {
		t.Fatalf("expected wrapped exec error, got %v", err)
	}
}

func TestPreRunInstallPackages_Success(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	if err := PreRunInstallPackages(context.Background(), c, "ctr-123456789012", []string{"git", "jq"}, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := c.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 exec, got %d", len(calls))
	}
	if calls[0].User != "0:0" {
		t.Errorf("apt install must run as root, got user %q", calls[0].User)
	}
	script := covScript(calls[0])
	if !strings.Contains(script, "apt-get install -y -qq git jq") {
		t.Errorf("install script missing packages: %q", script)
	}
}

// ---- checkSidecar ----

func TestCheckSidecar_NilProvider(t *testing.T) {
	t.Parallel()
	if h := checkSidecar(context.Background(), nil, "ctr1"); h != nil {
		t.Errorf("nil provider must yield nil health, got %+v", h)
	}
}

func TestCheckSidecar_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		execErr  error
		wantNil  bool
		wantMode string
	}{
		{name: "exec error", execErr: errors.New("boom"), wantNil: true},
		{name: "invalid json", body: "not json", wantNil: true},
		{name: "status not ok", body: `{"status":"starting","network_mode":"free"}`, wantNil: true},
		{name: "healthy free", body: `{"status":"ok","network_mode":"free"}`, wantMode: "free"},
		{name: "healthy restricted", body: `{"status":"ok","network_mode":"restricted"}`, wantMode: "restricted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &covContainer{route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
				if tc.execErr != nil {
					return nil, tc.execErr
				}
				return covResult("health", tc.body), nil
			}}
			h := checkSidecar(context.Background(), c, "ctr1")
			if tc.wantNil {
				if h != nil {
					t.Fatalf("expected nil health, got %+v", h)
				}
				return
			}
			if h == nil {
				t.Fatal("expected non-nil health")
			}
			if h.NetworkMode != tc.wantMode {
				t.Errorf("network mode = %q, want %q", h.NetworkMode, tc.wantMode)
			}
		})
	}
}

// ---- credTypeToProvider ----

func TestCredTypeToProvider_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		env  string
		want string
	}{
		{"ANTHROPIC_API_KEY", "ANTHROPIC"},
		{"OPENAI_API_KEY", "OPENAI"},
		{"GOOGLE_API_KEY", "GOOGLE"},
		{"GEMINI_API_KEY", "GOOGLE"},
		{"CURSOR_API_KEY", "CURSOR"},
		{"FACTORY_API_KEY", "FACTORY"},
		{"RANDOM_TOKEN", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := credTypeToProvider(Credential{EnvVarName: tc.env}); got != tc.want {
			t.Errorf("credTypeToProvider(%q) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

// ---- startSidecar ----

// covSidecarInputRE extracts the base64 stdin payload from the launch script:
// echo '<b64>' | base64 -d | crewship-sidecar --addr ...
var covSidecarInputRE = regexp.MustCompile(`echo '([A-Za-z0-9+/=]+)' \| base64 -d \| crewship-sidecar`)

func covDecodeSidecarInput(t *testing.T, script string) map[string]any {
	t.Helper()
	m := covSidecarInputRE.FindStringSubmatch(script)
	if len(m) != 2 {
		t.Fatalf("sidecar launch script did not match payload pattern: %q", script)
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatalf("decode sidecar payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal sidecar payload: %v", err)
	}
	return out
}

func TestStartSidecar_SuccessPayloadShape(t *testing.T) {
	t.Parallel()
	c := &covContainer{route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		script := covScript(cfg)
		if strings.Contains(script, "crewship-sidecar --addr") {
			return covResult("sidecar-start", ""), nil
		}
		return nil, nil
	}}
	creds := []Credential{
		{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-test", Priority: 2},
		{ID: "c2", EnvVarName: "SOMETHING_ELSE", PlainValue: "ignored"}, // unmapped → skipped
	}
	memCfg := &SidecarMemoryConfig{
		Enabled: true, BasePath: "/crew/agents/bob/.memory",
		AgentSlug: "bob", AgentRole: "lead", CrewMemoryPath: "/crew/shared/.memory",
	}
	ipcCfg := &SidecarIPCConfig{BaseURL: "http://host:9000", Token: "tok", AgentID: "a1", WorkspaceID: "ws1"}
	members := []SidecarCrewMember{{ID: "m1", Slug: "eva", Name: "Eva"}}
	policy := &SidecarNetworkPolicy{Mode: "restricted", AllowedDomains: []string{"api.github.com"}}
	servers := []MCPServerConfig{
		{Name: "remote", Transport: "streamable-http", Endpoint: "https://mcp.example.com"},
		{Name: "local", Transport: "stdio", Command: "npx"},
	}

	err := startSidecar(context.Background(), c, "container-abcdef123456", creds, memCfg, ipcCfg, members, policy, servers, covQuietLogger())
	if err != nil {
		t.Fatalf("startSidecar: %v", err)
	}

	calls := c.snapshotCalls()
	// Expect: memory perms prep (root) + sidecar launch (1002).
	if len(calls) != 2 {
		t.Fatalf("expected 2 execs (prep + launch), got %d", len(calls))
	}
	prep, launch := calls[0], calls[1]
	if prep.User != "0:0" {
		t.Errorf("memory prep must run as root, got %q", prep.User)
	}
	prepScript := covScript(prep)
	if !strings.Contains(prepScript, "/crew/agents/bob/.memory") || !strings.Contains(prepScript, "/crew/shared/.memory") {
		t.Errorf("prep script missing memory paths: %q", prepScript)
	}
	if !strings.Contains(prepScript, "chown -R 1001:1002") {
		t.Errorf("prep script must chown to 1001:1002: %q", prepScript)
	}
	if launch.User != "1002:1002" {
		t.Errorf("sidecar must run as UID 1002, got %q", launch.User)
	}

	input := covDecodeSidecarInput(t, covScript(launch))
	credsOut, _ := input["credentials"].([]any)
	if len(credsOut) != 1 {
		t.Fatalf("expected 1 mapped credential, got %v", input["credentials"])
	}
	c0 := credsOut[0].(map[string]any)
	if c0["provider"] != "ANTHROPIC" || c0["token"] != "sk-test" {
		t.Errorf("credential mapping wrong: %v", c0)
	}
	np, _ := input["network_policy"].(map[string]any)
	if np["mode"] != "restricted" {
		t.Errorf("network policy not forwarded: %v", input["network_policy"])
	}
	mcps, _ := input["mcp_servers"].([]any)
	if len(mcps) != 1 {
		t.Fatalf("only streamable-http MCP servers must reach the sidecar, got %v", input["mcp_servers"])
	}
	if mcps[0].(map[string]any)["name"] != "remote" {
		t.Errorf("wrong MCP server forwarded: %v", mcps[0])
	}
	cm, _ := input["crew_members"].([]any)
	if len(cm) != 1 || cm[0].(map[string]any)["slug"] != "eva" {
		t.Errorf("crew members not forwarded: %v", input["crew_members"])
	}
	ipc, _ := input["ipc"].(map[string]any)
	if ipc["base_url"] != "http://host:9000" {
		t.Errorf("ipc config not forwarded: %v", input["ipc"])
	}
}

func TestStartSidecar_MemoryPrepFailuresAreNonFatal(t *testing.T) {
	t.Parallel()
	// Case 1: the prep exec itself errors — sidecar still starts.
	c1 := &covContainer{route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if cfg.User == "0:0" {
			return nil, errors.New("prep exploded")
		}
		return covResult("sidecar-start", ""), nil
	}}
	mem := &SidecarMemoryConfig{Enabled: true, BasePath: "/crew/agents/x/.memory"}
	if err := startSidecar(context.Background(), c1, "ctr-123456789012", nil, mem, nil, nil, nil, nil, covQuietLogger()); err != nil {
		t.Fatalf("prep exec error must not fail startSidecar: %v", err)
	}

	// Case 2: prep exits non-zero — logged, sidecar still starts.
	c2 := &covContainer{
		route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			if cfg.User == "0:0" {
				return covResult("prep-exec", "permission denied"), nil
			}
			return covResult("sidecar-start", ""), nil
		},
		inspect: func(execID string) (bool, int, error) {
			if execID == "prep-exec" {
				return false, 1, nil
			}
			return false, 0, nil
		},
	}
	if err := startSidecar(context.Background(), c2, "ctr-123456789012", nil, mem, nil, nil, nil, nil, covQuietLogger()); err != nil {
		t.Fatalf("prep non-zero exit must not fail startSidecar: %v", err)
	}
}

func TestStartSidecar_ExecError(t *testing.T) {
	t.Parallel()
	c := &covContainer{route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
		return nil, errors.New("no such container")
	}}
	err := startSidecar(context.Background(), c, "ctr1", nil, nil, nil, nil, nil, nil, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "start sidecar") {
		t.Fatalf("expected start sidecar error, got %v", err)
	}
}

func TestStartSidecar_InspectError(t *testing.T) {
	t.Parallel()
	c := &covContainer{
		inspect: func(_ string) (bool, int, error) { return false, 0, errors.New("inspect broken") },
	}
	err := startSidecar(context.Background(), c, "ctr1", nil, nil, nil, nil, nil, nil, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "inspect sidecar exec") {
		t.Fatalf("expected inspect error, got %v", err)
	}
}

func TestStartSidecar_HealthCheckFailure(t *testing.T) {
	t.Parallel()
	c := &covContainer{
		route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
			return covResult("sidecar-start", "sidecar health check failed"), nil
		},
		inspect: func(_ string) (bool, int, error) { return false, 1, nil },
	}
	err := startSidecar(context.Background(), c, "ctr-123456789012", nil, nil, nil, nil, nil, nil, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "sidecar health check failed (exit 1)") {
		t.Fatalf("expected health check failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "sidecar health check failed") {
		t.Errorf("error must carry the captured output: %v", err)
	}
}
