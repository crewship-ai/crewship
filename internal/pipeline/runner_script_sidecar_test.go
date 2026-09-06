package pipeline

// The LIVENESS half of #1473.
//
// #1473 gave routine `script` steps the sidecar proxy env so they MEET the crew
// egress fence instead of running around it. It did not give them a sidecar to
// meet: crewship-sidecar is started from the agent-run path alone
// (Orchestrator.ensureSidecar, which needs an *AgentRunRequest), while a script
// step only ever goes through crewstart.StartResolved — which brings the crew's
// declared SERVICE sidecars up and nothing else.
//
// So on a crew no agent run has warmed, every script step that touches the
// network dies on the proxy that is not there:
//
//	fatal: unable to access ... Failed to connect to 127.0.0.1 port 9119
//	after 0 ms: Couldn't connect to server
//
// It is deterministic on a fresh seed — the seeded ci-nightly-triage routine
// runs its `probe` script step BEFORE its `triage` agent step, so the probe
// fails on every fresh install and raises a false alert — and it self-heals the
// moment anything runs an agent in that crew, which is why it reads as flaky.

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

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// sidecarScriptContainer is a ContainerProvider fake that models one crew
// container: it answers the in-container sidecar /health probe with a
// scripted body (empty = nothing listening on 9119, i.e. a cold crew) and
// records every exec — argv AND stdin, because the sidecar launch script rides
// stdin rather than argv (see startSidecar's /proc/<pid>/cmdline note).
type sidecarScriptContainer struct {
	mu     sync.Mutex
	health string
	calls  []string
}

func (c *sidecarScriptContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-sidecar-test", nil
}
func (c *sidecarScriptContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *sidecarScriptContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *sidecarScriptContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}

func (c *sidecarScriptContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")
	stdin := ""
	if cfg.Stdin != nil {
		b, _ := io.ReadAll(cfg.Stdin)
		stdin = string(b)
	}
	c.mu.Lock()
	c.calls = append(c.calls, joined+"\n"+stdin)
	c.mu.Unlock()
	if strings.Contains(joined, "127.0.0.1:9119/health") {
		return &provider.ExecResult{ExecID: "exec-health", Reader: io.NopCloser(strings.NewReader(c.health))}, nil
	}
	return &provider.ExecResult{ExecID: "exec-aux", Reader: io.NopCloser(strings.NewReader("out"))}, nil
}

func (c *sidecarScriptContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (c *sidecarScriptContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *sidecarScriptContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *sidecarScriptContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*sidecarScriptContainer)(nil)

func (c *sidecarScriptContainer) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// sidecarLaunchRE matches the one line of the launch script that actually
// starts the proxy, and captures the base64 boot payload it is fed on stdin.
var sidecarLaunchRE = regexp.MustCompile(`echo '([A-Za-z0-9+/=]+)' \| base64 -d \| crewship-sidecar`)

// indexOfSidecarLaunch / indexOfScriptExec locate the two calls the ordering
// assertion is about: the sidecar must be up BEFORE the step's argv runs, not
// merely at some point during the request.
func indexOfSidecarLaunch(calls []string) int {
	for i, c := range calls {
		if sidecarLaunchRE.MatchString(c) {
			return i
		}
	}
	return -1
}

func indexOfScriptExec(calls []string) int {
	for i, c := range calls {
		if strings.Contains(c, "crewship-script") {
			return i
		}
	}
	return -1
}

// sidecarBootPolicy decodes the network policy out of the launch payload — the
// load-bearing half for a script step. A sidecar started with the wrong (or an
// empty) policy would turn today's hard, visible failure into an open egress
// path, which is strictly worse than the bug being fixed.
func sidecarBootPolicy(t *testing.T, call string) map[string]any {
	t.Helper()
	m := sidecarLaunchRE.FindStringSubmatch(call)
	if m == nil {
		t.Fatalf("no sidecar launch in call: %q", call)
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatalf("decode sidecar boot payload: %v", err)
	}
	var payload struct {
		NetworkPolicy map[string]any `json:"network_policy"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse sidecar boot payload: %v", err)
	}
	return payload.NetworkPolicy
}

// newSidecarScriptRunner wires a real Orchestrator (sidecar enabled, as
// internal/server does) over the fake container, plus a crew-runtime resolver
// that stands in for internal/api's BuildCrewRuntimeConfig.
func newSidecarScriptRunner(c provider.ContainerProvider, cfg provider.CrewConfig) *OrchestratorRunner {
	o := orchestrator.New(c, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetSidecarEnabled(true)
	return &OrchestratorRunner{
		container: c,
		orch:      o,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		crewRuntime: func(_ context.Context, crewID, _ string) (provider.CrewConfig, error) {
			out := cfg
			out.ID = crewID
			return out, nil
		},
	}
}

// A script step must not depend on an agent run having happened first. This is
// the bug, stated as a test: cold crew in, running sidecar out, before the
// step's own argv is ever handed to the container.
func TestRunScript_EnsuresTheCrewSidecarBeforeTheStepExecs(t *testing.T) {
	restricted := provider.CrewConfig{
		Slug: "ops", NetworkMode: "restricted", AllowedDomains: []string{"api.github.com"},
	}
	free := provider.CrewConfig{Slug: "ops", NetworkMode: "free"}

	tests := []struct {
		name string
		crew provider.CrewConfig
		// health is what the in-container /health probe answers. "" models a
		// crew container no agent run has ever warmed.
		health    string
		wantStart bool
		wantMode  string
	}{
		{
			name:      "cold crew — nothing is listening on 9119",
			crew:      restricted,
			health:    "",
			wantStart: true,
			wantMode:  "restricted",
		},
		{
			name:      "healthy sidecar on the crew's own policy is REUSED",
			crew:      restricted,
			health:    `{"status":"ok","network_mode":"restricted","domains_hash":"` + orchestrator.DomainsHash([]string{"api.github.com"}) + `"}`,
			wantStart: false,
		},
		{
			name:      "sidecar running a stale policy is replaced",
			crew:      restricted,
			health:    `{"status":"ok","network_mode":"free"}`,
			wantStart: true,
			wantMode:  "restricted",
		},
		{
			name:      "free-mode crew, cold container",
			crew:      free,
			health:    "",
			wantStart: true,
			wantMode:  "free",
		},
		{
			name:      "free-mode crew, healthy sidecar is REUSED",
			crew:      free,
			health:    `{"status":"ok","network_mode":"free"}`,
			wantStart: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &sidecarScriptContainer{health: tc.health}
			r := newSidecarScriptRunner(c, tc.crew)

			if _, err := r.RunScript(context.Background(), ScriptRunRequest{
				WorkspaceID: "ws_1", AuthorCrewID: "crew_1",
				Interpreter: "python3", Path: "/crew/shared/probe.py",
			}); err != nil {
				t.Fatalf("RunScript: %v", err)
			}

			calls := c.snapshot()
			launch := indexOfSidecarLaunch(calls)
			script := indexOfScriptExec(calls)
			if script < 0 {
				t.Fatalf("the step's own argv never ran; calls: %v", calls)
			}
			if !tc.wantStart {
				if launch >= 0 {
					t.Fatalf("a healthy sidecar on the crew's policy was restarted — "+
						"reuse is what checkSidecar/sidecarNeedsRestart exist for; call: %q", calls[launch])
				}
				return
			}
			if launch < 0 {
				t.Fatalf("no crewship-sidecar was started, so the step's HTTP_PROXY "+
					"points at nothing and every network call fails with "+
					"\"Couldn't connect to 127.0.0.1 port 9119\"; calls: %v", calls)
			}
			if launch > script {
				t.Errorf("the sidecar started AFTER the step execed (launch=%d, script=%d) — "+
					"the step's network calls are already gone by then", launch, script)
			}
			policy := sidecarBootPolicy(t, calls[launch])
			if got, _ := policy["mode"].(string); got != tc.wantMode {
				t.Errorf("sidecar boot network policy mode = %q, want %q — starting a "+
					"sidecar with the wrong egress policy trades a hard failure for an "+
					"open fence", got, tc.wantMode)
			}
			if tc.wantMode == "restricted" {
				domains, _ := policy["allowed_domains"].([]any)
				var got []string
				for _, d := range domains {
					s, _ := d.(string)
					got = append(got, s)
				}
				if len(got) != 1 || got[0] != "api.github.com" {
					t.Errorf("restricted sidecar booted with allowed_domains %v, want the "+
						"crew's [api.github.com]", got)
				}
			}
		})
	}
}

// A sidecar that cannot be brought up must fail the step loudly. The proxy env
// is injected unconditionally (#1473), so continuing would run the script
// against a dead proxy and surface as an unattributable "connection refused"
// from inside the user's own script.
func TestRunScript_SidecarStartFailureFailsTheStep(t *testing.T) {
	c := &sidecarFailContainer{sidecarScriptContainer: sidecarScriptContainer{}}
	r := newSidecarScriptRunner(c, provider.CrewConfig{Slug: "ops", NetworkMode: "free"})

	_, err := r.RunScript(context.Background(), ScriptRunRequest{
		WorkspaceID: "ws_1", AuthorCrewID: "crew_1",
		Interpreter: "python3", Path: "/crew/shared/probe.py",
	})
	if err == nil {
		t.Fatal("a crew whose sidecar will not start must fail the step, not run it " +
			"against a proxy that is not there")
	}
	if !strings.Contains(err.Error(), "sidecar") {
		t.Errorf("the error must name the sidecar so the failure is attributable, got: %v", err)
	}
	if indexOfScriptExec(c.snapshot()) >= 0 {
		t.Error("the step's argv ran anyway after the sidecar failed to start")
	}
}

// sidecarFailContainer makes only the sidecar launch fail; every other exec
// still works, so the test isolates the sidecar path from a dead container.
type sidecarFailContainer struct {
	sidecarScriptContainer
}

func (c *sidecarFailContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	res, err := c.sidecarScriptContainer.Exec(ctx, cfg)
	if err != nil {
		return res, err
	}
	calls := c.snapshot()
	if len(calls) > 0 && sidecarLaunchRE.MatchString(calls[len(calls)-1]) {
		return nil, errors.New("docker: exec create failed")
	}
	return res, nil
}

// Two script steps of the same routine can run concurrently against one crew.
// The agent path serializes the whole check→decide→pkill→start sequence on a
// per-container lock (#1220) precisely because two callers that sample the same
// "not running" state both start a sidecar, one killing the other's. The crew
// path must ride the same lock, not a second one.
func TestRunScript_ConcurrentStepsStartOneSidecar(t *testing.T) {
	c := &countingSidecarContainer{}
	r := newSidecarScriptRunner(c, provider.CrewConfig{Slug: "ops", NetworkMode: "free"})

	const steps = 6
	var wg sync.WaitGroup
	errs := make([]error, steps)
	for i := 0; i < steps; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.RunScript(context.Background(), ScriptRunRequest{
				WorkspaceID: "ws_1", AuthorCrewID: "crew_1",
				Interpreter: "python3", Path: "/crew/shared/probe.py",
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if got := c.launches(); got != 1 {
		t.Errorf("%d concurrent script steps launched the sidecar %d times, want 1 — "+
			"a second launch kills the first one's proxy mid-step", steps, got)
	}
}

// countingSidecarContainer models the real thing more closely than the
// scripted fake: once a sidecar has been launched, /health starts answering.
type countingSidecarContainer struct {
	mu      sync.Mutex
	started int
}

func (c *countingSidecarContainer) launches() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

func (c *countingSidecarContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "container-counting", nil
}
func (c *countingSidecarContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (c *countingSidecarContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (c *countingSidecarContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *countingSidecarContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")
	stdin := ""
	if cfg.Stdin != nil {
		b, _ := io.ReadAll(cfg.Stdin)
		stdin = string(b)
	}
	if strings.Contains(joined, "127.0.0.1:9119/health") {
		c.mu.Lock()
		body := ""
		if c.started > 0 {
			body = `{"status":"ok","network_mode":"free"}`
		}
		c.mu.Unlock()
		return &provider.ExecResult{ExecID: "exec-health", Reader: io.NopCloser(strings.NewReader(body))}, nil
	}
	if sidecarLaunchRE.MatchString(stdin) {
		c.mu.Lock()
		c.started++
		c.mu.Unlock()
	}
	return &provider.ExecResult{ExecID: "exec-aux", Reader: io.NopCloser(strings.NewReader("out"))}, nil
}
func (c *countingSidecarContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (c *countingSidecarContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (c *countingSidecarContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (c *countingSidecarContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*countingSidecarContainer)(nil)
