//go:build linux

package orchestrator

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type missingSignalContainer struct {
	stopProcessContainer
	missing string
}

func (c missingSignalContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	cfg.Cmd = append([]string(nil), cfg.Cmd...)
	for i := range cfg.Cmd {
		cfg.Cmd[i] = strings.ReplaceAll(cfg.Cmd[i], "/bin/kill", c.missing)
	}
	return c.stopProcessContainer.Exec(ctx, cfg)
}

func TestDirectProbeMissingKillNeverConfirmsAbsence(t *testing.T) {
	state := newMemState()
	owner := New(stopProcessContainer{}, state, slog.Default())
	run, _, _ := startDirectRun(t, owner, state, "a", "a", true)
	broken := New(missingSignalContainer{missing: filepath.Join(t.TempDir(), "missing-kill")}, state, slog.Default())
	loc := RunLocation{ContainerID: run.ContainerID, AgentSlug: run.AgentSlug, RunID: run.ID}
	if stopped, err := broken.StopRunAt(t.Context(), loc); stopped || err == nil {
		t.Fatalf("missing signal tool confirmed absence: stopped=%v err=%v", stopped, err)
	}
	if alive, err := broken.RunIsAliveAt(t.Context(), loc); !alive && err == nil {
		t.Fatal("missing probe tool reported a live process absent")
	}
	if alive, err := owner.RunIsAliveAt(t.Context(), loc); !alive || err != nil {
		t.Fatalf("control process no longer alive: %v %v", alive, err)
	}
}

func TestStopAgentRecoversPersistedRunAfterRestart(t *testing.T) {
	state := newMemState()
	o := New(stopProcessContainer{}, state, slog.Default())
	a, _, done := startDirectRun(t, o, state, "a", "a", true)
	b, _, _ := startDirectRun(t, o, state, "b", "b", true)
	restarted := New(stopProcessContainer{}, state, slog.Default())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := restarted.StopAgent(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("confirmed stop before process exit")
	}
	if runStatus(t, state, a.ID) != "cancelled" {
		t.Fatal("recovered stop did not persist cancellation")
	}
	if alive, err := restarted.RunIsAliveAt(ctx, RunLocation{ContainerID: b.ContainerID, AgentSlug: b.AgentSlug, RunID: b.ID}); !alive || err != nil {
		t.Fatalf("other agent affected: %v %v", alive, err)
	}
}
