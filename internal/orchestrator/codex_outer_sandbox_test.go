package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
)

// A process-global environment variable must never grant outer-isolation consent.
func TestCodexGlobalSandboxOverrideIsNotAdapterAuthority(t *testing.T) {
	t.Setenv("CREWSHIP_CODEX_SANDBOX_MODE", "danger-full-access")
	for _, profile := range []string{"CODING", "STANDARD", "MINIMAL"} {
		cmd := (codexAdapter{}).BuildCommand(AgentRunRequest{ToolProfile: profile})
		i := slices.Index(cmd, "--sandbox")
		if i < 0 || i+1 >= len(cmd) {
			t.Fatal("missing sandbox mode")
		}
		want := "workspace-write"
		if profile == "MINIMAL" {
			want = "read-only"
		}
		if cmd[i+1] != want {
			t.Errorf("%s: global env granted %s, want %s without per-run admission", profile, cmd[i+1], want)
		}
	}
}

func TestCodexOuterSandboxRequiresStoredConsentBeforeRuntime(t *testing.T) {
	for _, adapter := range []string{"CODEX_CLI", "CLAUDE_CODE", "OPENCODE"} {
		t.Run(adapter, func(t *testing.T) {
			t.Setenv("CREWSHIP_CODEX_SANDBOX_MODE", "danger-full-access")
			o := New(execStartedProbeContainer{}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			called := 0
			o.SetCodexSandboxPolicyResolver(func(context.Context, string, string) (CodexSandboxPolicy, error) {
				called++
				return CodexSandboxPolicy{}, nil
			})
			req := AgentRunRequest{CLIAdapter: adapter, ToolProfile: "CODING", WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", AgentSlug: "fixture", RunID: "outer-test"}
			err := o.admitCodexSandbox(context.Background(), &req, nil)
			if adapter == "CODEX_CLI" {
				if err == nil || !strings.Contains(err.Error(), "has not opted") || called != 1 {
					t.Fatalf("legacy global env bypass: %v calls%d", err, called)
				}
			} else if err != nil || called != 0 {
				t.Fatalf("other adapter changed: %v calls%d", err, called)
			}
			if req.codexOuterSandbox || req.managedLaunch != nil {
				t.Fatal("unqualified request gained sandbox authority")
			}
		})
	}
}

func TestCodexOuterSandboxMinimalNeverOptsOut(t *testing.T) {
	t.Setenv("CREWSHIP_CODEX_SANDBOX_MODE", "danger-full-access")
	o := New(execStartedProbeContainer{}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetCodexSandboxPolicyResolver(func(context.Context, string, string) (CodexSandboxPolicy, error) {
		t.Fatal("MINIMAL requested outer isolation")
		return CodexSandboxPolicy{}, nil
	})
	req := AgentRunRequest{CLIAdapter: "CODEX_CLI", ToolProfile: "MINIMAL"}
	if err := o.admitCodexSandbox(context.Background(), &req, nil); err != nil {
		t.Fatal(err)
	}
	if req.codexOuterSandbox {
		t.Fatal("MINIMAL gained outer isolation")
	}
}

type codexOuterContainer struct {
	launchContainer
	outerChecks int
	failAfter   int
}

func (c *codexOuterContainer) AttestCodexOuterSandbox(context.Context, string, string, managedlaunch.Descriptor) error {
	c.outerChecks++
	if c.failAfter > 0 && c.outerChecks >= c.failAfter {
		return errors.New("fixture outer runtime drift")
	}
	return nil
}

type codexOuterAudit struct {
	events []provider.CodexSandboxEvidence
	fail   bool
}

func (a *codexOuterAudit) RecordAudit(context.Context, string, string, string, string, string, map[string]any) {
}
func (a *codexOuterAudit) RecordCodexSandboxPreflight(_ context.Context, e provider.CodexSandboxEvidence) error {
	if a.fail {
		return errors.New("fixture durable audit failure")
	}
	a.events = append(a.events, e)
	return nil
}

func TestCodexOuterSandboxCommonRunRechecksConsentAndRuntime(t *testing.T) {
	for _, which := range []string{"accepted", "revoked", "runtime-drift", "audit-failed"} {
		t.Run(which, func(t *testing.T) {
			c := &codexOuterContainer{}
			if which == "runtime-drift" {
				c.failAfter = 2
			}
			state := newLockedMemState()
			o := New(c, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
			audit := &codexOuterAudit{fail: which == "audit-failed"}
			o.SetAuditLog(audit)
			calls := 0
			o.SetCodexSandboxPolicyResolver(func(_ context.Context, ws, crew string) (CodexSandboxPolicy, error) {
				if ws != "ws" || crew != "outer-crew" {
					t.Fatal("policy lost tenant scope")
				}
				calls++
				return CodexSandboxPolicy{Enabled: which != "revoked" || calls == 1, Launch: launchDescriptor()}, nil
			})
			use := provider.NewRuntimeUse("outer-crew", "c1", func() {})
			defer use.Release()
			req := AgentRunRequest{RuntimeUse: use, WorkspaceID: "ws", CrewID: "outer-crew", AgentID: "agent", AgentSlug: "fixture", RunID: "outer-" + which, ContainerID: "c1", CLIAdapter: "CODEX_CLI", ToolProfile: "CODING", UserMessage: "offline", TimeoutSecs: 2}
			err := o.RunAgent(context.Background(), req, nil)
			launches := 0
			for _, cfg := range c.commands {
				if len(cfg.Cmd) > 2 && cfg.Cmd[1] == "--managed-launch" {
					launches++
					raw, _ := base64.RawURLEncoding.DecodeString(cfg.Cmd[2])
					var d managedlaunch.Descriptor
					if json.Unmarshal(raw, &d) != nil || !d.OuterSandbox || d.RunID != req.RunID || d.Path != launchDescriptor().Path {
						t.Fatal("unqualified or additional probe launch")
					}
					if !strings.Contains(strings.Join(cfg.Cmd, " "), "danger-full-access") {
						t.Fatal("outer mode not selected")
					}
				}
			}
			if which == "accepted" {
				if err != nil || launches != 1 || c.outerChecks != 2 || calls != 2 {
					t.Fatalf("admission err%v launches%d checks%d policy%d", err, launches, c.outerChecks, calls)
				}
				if len(audit.events) != 1 || audit.events[0].RunID != req.RunID || audit.events[0].AgentID != req.AgentID || audit.events[0].Status != "admitted" {
					t.Fatal("sandbox audit attribution missing")
				}
			} else {
				if err == nil || launches != 0 {
					t.Fatalf("failed evidence reached exec: %v launches%d", err, launches)
				}
				raw, _ := state.Get(context.Background(), "agent_runs", req.RunID)
				var saved RunState
				if json.Unmarshal(raw, &saved) != nil || saved.Status != "error" {
					t.Fatal("refusal left run nonterminal")
				}
			}
		})
	}
}

func TestCodexOuterEnvironmentKeepsMiseConstantsAndRejectsCollision(t *testing.T) {
	d := launchDescriptor()
	d.OuterSandbox = true
	req := AgentRunRequest{RunID: "outer-environment", managedLaunch: d, codexOuterSandbox: true}
	cfg, err := managedExecConfig(req, []string{"codex", "exec"}, nil, "/")
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Join(cfg.Env, "\n")
	if !strings.Contains(values, "MISE_DATA_DIR=/opt/mise/data") || !strings.Contains(values, "MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml") {
		t.Fatal("mise shim runtime metadata missing")
	}
	if _, err := managedExecConfig(req, []string{"codex", "exec"}, []string{"MISE_DATA_DIR=/crew/untrusted"}, "/"); err == nil {
		t.Fatal("runtime metadata collision accepted")
	}
}

func TestCodexOuterKnownOptInFailuresAreAuditedWithoutAuthority(t *testing.T) {
	for _, which := range []string{"authority-error", "missing-use", "missing-descriptor", "invalid-descriptor", "audit-error"} {
		t.Run(which, func(t *testing.T) {
			c := &codexOuterContainer{}
			o := New(c, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			audit := &codexOuterAudit{fail: which == "audit-error"}
			o.SetAuditLog(audit)
			o.SetCodexSandboxPolicyResolver(func(context.Context, string, string) (CodexSandboxPolicy, error) {
				policy := CodexSandboxPolicy{Enabled: true, Launch: launchDescriptor()}
				switch which {
				case "authority-error", "audit-error":
					return policy, errors.New("fixture missing immutable authority")
				case "missing-descriptor":
					policy.Launch = nil
				case "invalid-descriptor":
					policy.Launch.SHA256 = "invalid"
				}
				return policy, nil
			})
			req := AgentRunRequest{WorkspaceID: "ws", CrewID: "crew", ContainerID: "c1", AgentID: "agent", AgentSlug: "fixture", RunID: "outer-reject-" + which, CLIAdapter: "CODEX_CLI", ToolProfile: "CODING"}
			var use *provider.RuntimeUse
			if which != "missing-use" {
				use = provider.NewRuntimeUse("crew", "c1", func() {})
				defer use.Release()
			}
			err := o.admitCodexSandbox(context.Background(), &req, use)
			if err == nil || req.codexOuterSandbox || req.managedLaunch != nil || len(c.commands) != 0 {
				t.Fatalf("rejected admission gained authority/process: %v", err)
			}
			if which == "audit-error" {
				if !strings.Contains(err.Error(), "durable audit failure") {
					t.Fatal("audit failure was hidden")
				}
			} else {
				if len(audit.events) != 1 || audit.events[0].Status != "denied" || audit.events[0].RunID != req.RunID || audit.events[0].AgentID != req.AgentID {
					t.Fatal("opt-in refusal not attributed in audit")
				}
			}
		})
	}
}

func TestCodexOuterStopBeforeRunStateCannotCreateProcess(t *testing.T) {
	c := &codexOuterContainer{}
	state := newLockedMemState()
	o := New(c, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetCodexSandboxPolicyResolver(func(context.Context, string, string) (CodexSandboxPolicy, error) {
		return CodexSandboxPolicy{Enabled: true, Launch: launchDescriptor()}, nil
	})
	req := AgentRunRequest{WorkspaceID: "ws", CrewID: "crew", ContainerID: "c1", AgentID: "agent", AgentSlug: "fixture", RunID: "outer-preparing-stop", CLIAdapter: "CODEX_CLI", ToolProfile: "CODING"}
	ctx, finish, err := o.trackAgentRun(context.Background(), &req)
	if err != nil {
		t.Fatal(err)
	}
	use := provider.NewRuntimeUse("crew", "c1", func() {})
	defer use.Release()
	if err := o.admitCodexSandbox(ctx, &req, use); err != nil {
		t.Fatal(err)
	}
	location := RunLocation{ContainerID: "c1", AgentSlug: "fixture", RunID: req.RunID}
	if !o.knownManagedRunLocation(location) {
		t.Fatal("nonpilot outer run lost managed identity before state persistence")
	}
	go func() { <-ctx.Done(); finish() }()
	bounded, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.StopAgent(bounded, req.AgentID); err != nil {
		t.Fatal(err)
	}
	if err := req.ExecGate(context.Background()); err == nil {
		t.Fatal("stopped preparation may still create a process")
	}
	if len(c.commands) != 0 {
		t.Fatal("admission/stop spawned an extra probe")
	}
}

func TestCodexOuterRecoveredLocationRetainsManagedProbe(t *testing.T) {
	state := newLockedMemState()
	o := New(nil, state, slog.Default())
	d := launchDescriptor()
	d.OuterSandbox = true
	saved := RunState{ID: "outer-recovered", AgentID: "agent", AgentSlug: "fixture", ContainerID: "c1", ManagedLaunch: d}
	raw, _ := json.Marshal(saved)
	if err := state.Set(context.Background(), "agent_runs", saved.ID, raw); err != nil {
		t.Fatal(err)
	}
	// No agentRuns entry or pilot selector survives this simulated recovery.
	location := RunLocation{ContainerID: "c1", AgentSlug: "fixture", RunID: saved.ID}
	probe, err := o.managedRunProbe(context.Background(), location, true)
	if err != nil || !strings.Contains(probe, "echo UNKNOWN; exit;") || strings.Contains(probe, "tmux") {
		t.Fatalf("recovered outer run fell back to legacy absence: %v %s", err, probe)
	}
}
