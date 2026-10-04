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
	if err == nil || !strings.Contains(err.Error(), "dynamický ELF") || !strings.Contains(err.Error(), "A2") {
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
					if json.Unmarshal(raw, &actual) != nil || actual.Version != d.Version || actual.Artifact != d.Artifact {
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
