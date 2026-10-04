package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// Selecting a pilot must never silently use the legacy launch when the
// authoritative build descriptor or runtime attestation is unavailable.
func TestManagedLaunchPilotMissingEvidenceRefusesRun(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	o := New(execStartedProbeContainer{}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := o.RunAgent(context.Background(), AgentRunRequest{
		RunID: "managed-missing", CrewID: "pilot-crew", AgentID: "a1", AgentSlug: "agent-1",
		ContainerID: "c1", CLIAdapter: "CODEX_CLI", UserMessage: "offline fixture", TimeoutSecs: 1,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "managed launch") {
		t.Fatalf("pilot without build evidence reached legacy execution: %v", err)
	}
}

func TestManagedLaunchClaudeExplainsA2Refusal(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	o := New(execStartedProbeContainer{}, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := AgentRunRequest{CrewID: "pilot-crew", CLIAdapter: "CLAUDE_CODE"}
	err := o.admitManagedLaunch(context.Background(), &req, nil)
	if err == nil || !strings.Contains(err.Error(), "dynamic ELF") || !strings.Contains(err.Error(), "A2") {
		t.Fatalf("unsupported Claude error=%v", err)
	}
}

func launchDescriptor() *managedlaunch.Descriptor {
	return &managedlaunch.Descriptor{Artifact: managedlaunch.Artifact{Path: "/opt/native/codex", SHA256: strings.Repeat("b", 64), Format: "static_elf"}, ImageID: "sha256:" + strings.Repeat("a", 64), RevisionID: "r1", LockSHA256: strings.Repeat("c", 64), Binary: "codex", Version: "0.160.0"}
}

type launchContainer struct {
	execStartedProbeContainer
	commands     []provider.ExecConfig
	attestations int
	denyAfter    int
}

func (c *launchContainer) AttestManagedLaunch(context.Context, string, managedlaunch.Descriptor) error {
	c.attestations++
	if c.denyAfter > 0 && c.attestations >= c.denyAfter {
		return errors.New("managed launch: changed runtime")
	}
	return nil
}
func (c *launchContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.commands = append(c.commands, cfg)
	if len(cfg.Cmd) == 1 && cfg.Cmd[0] == "sh" && cfg.Stdin != nil {
		return &provider.ExecResult{ExecID: "setup-exec", Reader: io.NopCloser(strings.NewReader(preflightDoneMarker + "\n"))}, nil
	}
	return c.execStartedProbeContainer.Exec(ctx, cfg)
}

func TestManagedLaunchUsesCommonRunAdmissionAndDirectExec(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	for _, entry := range []string{"chat", "assignment"} {
		t.Run(entry, func(t *testing.T) {
			c := &launchContainer{}
			state := newLockedMemState()
			o := New(c, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
			d := launchDescriptor()
			o.SetManagedLaunchResolver(func(context.Context, string, string, string) (*managedlaunch.Descriptor, error) {
				copy := *d
				return &copy, nil
			})
			use := provider.NewRuntimeUse("pilot-crew", "c1", func() {})
			defer use.Release()
			req := AgentRunRequest{RuntimeUse: use, RunID: "managed-direct", CrewID: "pilot-crew", WorkspaceID: "w1", AgentID: "a1", AgentSlug: "agent-1", ContainerID: "c1", CLIAdapter: "CODEX_CLI", UserMessage: "offline", TimeoutSecs: 1}
			var err error
			if entry == "assignment" {
				err = o.RunAgentForAssignment(context.Background(), req, nil)
			} else {
				err = o.RunAgent(context.Background(), req, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			launched := 0
			for _, cfg := range c.commands {
				if len(cfg.Cmd) > 1 && cfg.Cmd[1] == "--managed-launch" {
					launched++
					if cfg.Cmd[0] != managedlaunch.LauncherPath {
						t.Fatal(cfg.Cmd)
					}
					raw, _ := base64.RawURLEncoding.DecodeString(cfg.Cmd[2])
					var actual managedlaunch.Descriptor
					if json.Unmarshal(raw, &actual) != nil || actual.Version != d.Version || actual.Artifact != d.Artifact || actual.RunID != req.RunID || !strings.Contains(directRunProbe(actual.RunID, false), managedlaunch.DirectRunPIDFile(actual.RunID)) {
						t.Fatal("launcher lost admission evidence")
					}
				}
				if strings.Contains(strings.Join(cfg.Cmd, " "), "tmux new-session") {
					t.Fatal("managed run used tmux")
				}
			}
			if launched != 1 || c.attestations != 2 {
				t.Fatalf("launches=%d attestations=%d", launched, c.attestations)
			}
			raw, _ := state.Get(context.Background(), "agent_runs", req.RunID)
			var saved RunState
			json.Unmarshal(raw, &saved)
			if saved.ManagedLaunch == nil || saved.ManagedLaunch.Version != d.Version {
				t.Fatal("run provenance lost lock version")
			}
		})
	}
}

func TestManagedLaunchRechecksBeforeCreationGate(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	c := &launchContainer{denyAfter: 2}
	o := New(c, newLockedMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetManagedLaunchResolver(func(context.Context, string, string, string) (*managedlaunch.Descriptor, error) {
		return launchDescriptor(), nil
	})
	use := provider.NewRuntimeUse("pilot-crew", "c1", func() {})
	defer use.Release()
	gate := false
	err := o.RunAgent(context.Background(), AgentRunRequest{RuntimeUse: use, RunID: "managed-drift", CrewID: "pilot-crew", WorkspaceID: "w1", AgentID: "a1", AgentSlug: "agent-1", ContainerID: "c1", CLIAdapter: "CODEX_CLI", UserMessage: "offline", ExecGate: func(context.Context) error { gate = true; return nil }}, nil)
	if err == nil || gate {
		t.Fatalf("drift reached creation gate: %v %v", err, gate)
	}
	for _, cfg := range c.commands {
		if len(cfg.Cmd) > 1 && cfg.Cmd[1] == "--managed-launch" {
			t.Fatal("drift launched CLI")
		}
	}
}

type managedPersistFailure struct{ *lockedMemState }

func (s managedPersistFailure) Set(ctx context.Context, bucket, key string, value []byte) error {
	if bucket == "agent_runs" {
		return errors.New("fixture persistence unavailable")
	}
	return s.lockedMemState.Set(ctx, bucket, key, value)
}

func TestManagedLaunchPersistenceFailurePreventsExec(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	c := &launchContainer{}
	o := New(c, managedPersistFailure{newLockedMemState()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o.SetManagedLaunchResolver(func(context.Context, string, string, string) (*managedlaunch.Descriptor, error) {
		return launchDescriptor(), nil
	})
	use := provider.NewRuntimeUse("pilot-crew", "c1", func() {})
	defer use.Release()
	gate := false
	err := o.RunAgent(context.Background(), AgentRunRequest{RuntimeUse: use, RunID: "managed-persist", CrewID: "pilot-crew", WorkspaceID: "w1", AgentID: "a1", AgentSlug: "agent-1", ContainerID: "c1", CLIAdapter: "CODEX_CLI", ExecGate: func(context.Context) error { gate = true; return nil }}, nil)
	if err == nil || !strings.Contains(err.Error(), "persistence failed") || gate || len(c.commands) != 0 {
		t.Fatalf("persistence failure executed: err=%v gate=%v commands=%v", err, gate, c.commands)
	}
}

func TestManagedRunProbePreservesDurableScopeAndMissingState(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	state := newLockedMemState()
	o := New(&launchContainer{}, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	location := RunLocation{ContainerID: "c1", AgentSlug: "agent-1", RunID: "managed-probe", Managed: true}
	if _, err := o.managedRunProbe(context.Background(), location, false); err == nil {
		t.Fatal("known managed attempt fell back after lost durable state")
	}
	for _, wrong := range []string{"container", "agent", "run", "valid"} {
		run := RunState{ID: location.RunID, ContainerID: location.ContainerID, AgentSlug: location.AgentSlug, ManagedLaunch: launchDescriptor()}
		switch wrong {
		case "container":
			run.ContainerID = "other"
		case "agent":
			run.AgentSlug = "other"
		case "run":
			run.ID = "other"
		}
		raw, _ := json.Marshal(run)
		if err := state.Set(context.Background(), "agent_runs", location.RunID, raw); err != nil {
			t.Fatal(err)
		}
		probe, err := o.managedRunProbe(context.Background(), location, true)
		if wrong == "valid" {
			if err != nil || !strings.HasSuffix(probe, "echo UNKNOWN; exit; ") {
				t.Fatalf("durable managed probe=%q %v", probe, err)
			}
		} else if err == nil {
			t.Fatalf("confused %s scope accepted", wrong)
		}
	}
}

func TestManagedRunProbeAfterPilotSelectorDisabled(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "")
	state := newLockedMemState()
	location := RunLocation{ContainerID: "c1", AgentSlug: "agent-1", RunID: "managed-recovery", Managed: true}
	raw, _ := json.Marshal(RunState{ID: location.RunID, ContainerID: location.ContainerID, AgentSlug: location.AgentSlug, ManagedLaunch: launchDescriptor()})
	if err := state.Set(context.Background(), "agent_runs", location.RunID, raw); err != nil {
		t.Fatal(err)
	}
	restarted := New(&launchContainer{}, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	probe, err := restarted.managedRunProbe(context.Background(), location, false)
	if err != nil || !strings.HasSuffix(probe, "echo UNKNOWN; exit; ") {
		t.Fatalf("disabled selector bypassed durable managed marker: %q %v", probe, err)
	}
}

// Rejected preparation must not leave durable running occupancy for a CLI
// that never reached the creation gate or obtained a process identity.
func TestManagedLaunchPreExecFailureIsTerminal(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	for _, tc := range []struct{ name, slug, prompt, wantError string }{
		{"oversized-argument", "agent-1", strings.Repeat("x", maxArgStrLen), "argument exceeds execve limit"},
		{"invalid-slug", "../agent", "offline", "invalid agent slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := &launchContainer{}
			state := newLockedMemState()
			o := New(c, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
			o.SetManagedLaunchResolver(func(context.Context, string, string, string) (*managedlaunch.Descriptor, error) {
				return launchDescriptor(), nil
			})
			use := provider.NewRuntimeUse("pilot-crew", "c1", func() {})
			defer use.Release()
			gate := false
			req := AgentRunRequest{RuntimeUse: use, RunID: "managed-preexec-" + tc.name, CrewID: "pilot-crew", WorkspaceID: "w1", AgentID: "a1", AgentSlug: tc.slug, ContainerID: "c1", CLIAdapter: "CODEX_CLI", UserMessage: tc.prompt, ExecGate: func(context.Context) error { gate = true; return nil }}
			err := o.RunAgent(ctx, req, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("unexpected preparation result: %v", err)
			}
			if gate {
				t.Fatal("rejected preparation reached workload creation gate")
			}
			for _, cfg := range c.commands {
				if len(cfg.Cmd) > 1 && cfg.Cmd[1] == "--managed-launch" || strings.Contains(strings.Join(cfg.Cmd, " "), "tmux new-session") {
					t.Fatal("rejected preparation executed workload")
				}
			}
			raw, err := state.Get(ctx, "agent_runs", req.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var saved RunState
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.ManagedLaunch == nil || saved.ID != req.RunID || saved.ContainerID != req.ContainerID {
				t.Fatal("missing persisted managed run evidence")
			}
			if saved.Status != "error" {
				t.Fatalf("never-started managed run remains nonterminal: status=%q", saved.Status)
			}
		})
	}
}

type legacyProbeState struct {
	*lockedMemState
	reads int
}

func (s *legacyProbeState) Get(context.Context, string, string) ([]byte, error) {
	s.reads++
	return nil, errors.New("unavailable legacy state")
}

type legacyProbeContainer struct {
	execStartedProbeContainer
	inspections int
	commands    []provider.ExecConfig
}

func (c *legacyProbeContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	c.inspections++
	return &provider.ContainerStatus{State: "running"}, nil
}
func (c *legacyProbeContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.commands = append(c.commands, cfg)
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT\n"))}, nil
}

func TestManagedLaunchLeavesNonpilotProbeBehaviorUnchanged(t *testing.T) {
	t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "pilot-crew")
	state := &legacyProbeState{lockedMemState: newLockedMemState()}
	c := &legacyProbeContainer{}
	o := New(c, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
	location := RunLocation{ContainerID: "legacy-container", AgentSlug: "legacy-agent", RunID: "legacy-run"}
	if alive, err := o.RunIsAliveAt(context.Background(), location); err != nil || alive {
		t.Fatalf("legacy liveness: %v %v", alive, err)
	}
	if gone, err := o.StopRunAt(context.Background(), location); err != nil || !gone {
		t.Fatalf("legacy stop: %v %v", gone, err)
	}
	if state.reads != 0 || c.inspections != 0 || len(c.commands) != 2 {
		t.Fatalf("legacy acquired state/inspect work: reads=%d inspections=%d execs=%d", state.reads, c.inspections, len(c.commands))
	}
	for i, cfg := range c.commands {
		script := cfg.Cmd[2]
		if !strings.HasPrefix(script, directRunProbe(location.RunID, i == 1)) || strings.Contains(script, "group_separator") {
			t.Fatal("legacy probe changed", script)
		}
	}
}
