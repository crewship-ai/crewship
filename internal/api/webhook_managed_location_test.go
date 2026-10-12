package api

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

type retainedLocationState struct {
	provider.StateProvider
	raw   []byte
	reads int
}

func (s *retainedLocationState) Get(context.Context, string, string) ([]byte, error) {
	s.reads++
	return s.raw, nil
}

type missingManagedPIDContainer struct {
	verticalContainer
	path        string
	inspections int
}

func (c *missingManagedPIDContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	cmd := exec.CommandContext(ctx, cfg.Cmd[0], cfg.Cmd[1:]...)
	cmd.Env = append(os.Environ(), "PATH="+c.path)
	raw, err := cmd.Output()
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(string(raw)))}, err
}
func (c *missingManagedPIDContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	c.inspections++
	return &provider.ContainerStatus{State: "running"}, nil
}

// Substitute admission/execution, while preserving the real gate, durable mode
// validation, Stop/Alive, and shell probes. The orchestrator unit regression
// separately proves capture from the tracked production RunAgent invocation.
type admittedLocationRunner struct {
	*orchestrator.Orchestrator
	state   *retainedLocationState
	managed bool
}

func (r *admittedLocationRunner) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, handler orchestrator.EventHandler) error {
	saved := orchestrator.RunState{ID: req.RunID, ContainerID: req.ContainerID, AgentSlug: req.AgentSlug}
	if r.managed {
		saved.ManagedLaunch = &managedlaunch.Descriptor{}
	}
	r.state.raw, _ = json.Marshal(saved)
	if err := req.ExecGate(ctx); err != nil {
		return err
	}
	if handler != nil {
		handler(orchestrator.AgentEvent{Type: "text", Content: "started"})
	}
	return orchestrator.ErrDetachedStillRunning
}
func (r *admittedLocationRunner) RetainManagedRunLocation(ctx context.Context, loc orchestrator.RunLocation) (orchestrator.RunLocation, error) {
	loc.Managed = r.managed // stand-in for process-local admitted ownership
	return r.Orchestrator.RetainManagedRunLocation(ctx, loc)
}

func TestWebhookScheduledManagedLocationSurvivesReturn(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		for _, managed := range []bool{false, true} {
			name := "webhook-legacy"
			if scheduled {
				name = "scheduled-legacy"
			}
			if managed {
				name = strings.TrimSuffix(name, "legacy") + "managed"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "") // selector disabled, durable admission wins
				rig := newVerticalRig(t)
				state := &retainedLocationState{}
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
				container := &missingManagedPIDContainer{path: dir + ":/usr/bin:/bin"}
				runner := &admittedLocationRunner{Orchestrator: orchestrator.New(container, state, quietLogger()), state: state, managed: managed}
				rig.router.webhookHandler.orch = runner
				workID := ""
				if scheduled {
					workID = acceptScheduledInRig(t, rig).WorkID
				} else {
					workID = rig.deliver(verticalBody).WorkID
				}
				claimed, err := rig.store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "managed-location-test", Limits: work.DefaultLimits(), WorkID: workID})
				if err != nil {
					t.Fatal(err)
				}
				item, err := rig.store.Get(t.Context(), workID)
				if err != nil {
					t.Fatal(err)
				}
				base := NewWebhookRuntime(rig.router.webhookHandler)
				var runtime dispatch.Runtime = base
				if scheduled {
					runtime = NewScheduledRuntime(base, nil, 4096, 2)
				}
				if err := rig.store.MarkStarting(t.Context(), workID, claimed.RunID, claimed.Generation, "agent-run:"+claimed.RunID); err != nil {
					t.Fatal(err)
				}
				assignment := dispatch.Assignment{Item: item, RunID: claimed.RunID, Generation: claimed.Generation}
				err = runtime.Run(t.Context(), assignment, nil)
				if err != orchestrator.ErrDetachedStillRunning {
					t.Fatalf("run: %v", err)
				}
				launch, ok := base.loadLaunch(assignment.RunID)
				if !ok || launch.location.Managed != managed {
					t.Fatalf("retained location: %+v", launch)
				}
				if !managed && state.reads != 0 {
					t.Fatalf("legacy creation admission read durable state: %d", state.reads)
				}
				pid := filepath.Join("/tmp", "crewship-direct-"+assignment.RunID+".pid")
				if _, err := os.Stat(pid); !os.IsNotExist(err) {
					t.Fatalf("fixture must have no PID file: %v", err)
				}
				// Switch to a fresh orchestrator with no in-process ownership after return.
				rig.router.webhookHandler.orch = orchestrator.New(container, state, quietLogger())
				locator := base.Locator(assignment)
				alive, aliveErr := runtime.Alive(t.Context(), locator)
				stopped, stopErr := runtime.Stop(t.Context(), locator)
				if managed {
					if alive || stopped || aliveErr == nil || stopErr == nil || !strings.Contains(aliveErr.Error(), "UNKNOWN") || !strings.Contains(stopErr.Error(), "UNKNOWN") {
						t.Fatalf("missing managed PID: alive=%v/%v stopped=%v/%v", alive, aliveErr, stopped, stopErr)
					}
				} else {
					if alive || aliveErr != nil || !stopped || stopErr != nil {
						t.Fatalf("legacy: alive=%v/%v stopped=%v/%v", alive, aliveErr, stopped, stopErr)
					}
					if state.reads != 2 {
						t.Fatalf("legacy liveness/stop durable reads: %d, want 2", state.reads)
					}
				}
				if container.inspections != 0 {
					t.Fatalf("unexpected Docker inspections: %d", container.inspections)
				}
			})
		}
	}
}
