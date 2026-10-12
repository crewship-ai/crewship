package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type retainedManagedProbeState struct {
	*lockedMemState
	reads int
}

func (s *retainedManagedProbeState) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	s.reads++
	return s.lockedMemState.Get(ctx, bucket, key)
}

type missingPIDProbeContainer struct {
	legacyProbeContainer
	path string
}

func (c *missingPIDProbeContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.commands = append(c.commands, cfg)
	cmd := exec.CommandContext(ctx, cfg.Cmd[0], cfg.Cmd[1:]...)
	cmd.Env = append(os.Environ(), "PATH="+c.path)
	raw, err := cmd.Output()
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(string(raw)))}, err
}

func TestManagedDetachedHomeWrappersRetainAdmission(t *testing.T) {
	for _, managed := range []bool{true, false} {
		name := "legacy"
		if managed {
			name = "managed"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("CREWSHIP_MANAGED_LAUNCH_CREWS", "")
			state := &retainedManagedProbeState{lockedMemState: newLockedMemState()}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			container := &missingPIDProbeContainer{path: dir + ":/usr/bin:/bin"}
			o := New(container, state, slog.New(slog.NewTextHandler(io.Discard, nil)))
			runID := "detached-home-" + name
			req := AgentRunRequest{ContainerID: "c1", AgentSlug: "agent-1", RunID: runID}
			if managed {
				req.managedLaunch = launchDescriptor()
			}
			raw, _ := json.Marshal(RunState{ID: runID, ContainerID: req.ContainerID, AgentSlug: req.AgentSlug, ManagedLaunch: req.managedLaunch})
			if err := state.Set(t.Context(), "agent_runs", runID, raw); err != nil {
				t.Fatal(err)
			}
			o.detached[runID] = &detachedHold{runID: runID, containerID: req.ContainerID, agentSlug: req.AgentSlug, req: req}
			retainRunHome(req.ContainerID, req.AgentSlug, runID)
			defer releaseRunHome(req.ContainerID, req.AgentSlug, runID)
			if _, err := os.Stat("/tmp/crewship-direct-" + runID + ".pid"); !os.IsNotExist(err) {
				t.Fatalf("missing PID fixture: %v", err)
			}
			alive, aliveErr := o.RunIsAlive(t.Context(), runID)
			stopped, stopErr := o.StopRun(t.Context(), runID)
			if managed {
				if alive || stopped || aliveErr == nil || stopErr == nil || !strings.Contains(aliveErr.Error(), "UNKNOWN") || !strings.Contains(stopErr.Error(), "UNKNOWN") {
					t.Fatalf("managed HOME probe: alive=%v/%v stopped=%v/%v", alive, aliveErr, stopped, stopErr)
				}
				// Corrupt persisted identity must still fail closed despite a known hold.
				if err := state.Set(t.Context(), "agent_runs", runID, []byte("{}")); err != nil {
					t.Fatal(err)
				}
				if stopped, err := o.StopRun(t.Context(), runID); stopped || err == nil {
					t.Fatalf("missing durable identity: %v %v", stopped, err)
				}
			} else {
				if alive || aliveErr != nil || !stopped || stopErr != nil {
					t.Fatalf("legacy HOME probe: alive=%v/%v stopped=%v/%v", alive, aliveErr, stopped, stopErr)
				}
				if state.reads != 2 || container.inspections != 0 {
					t.Fatalf("legacy durable reads/runtime inspection: %d %d", state.reads, container.inspections)
				}
			}
		})
	}
}
