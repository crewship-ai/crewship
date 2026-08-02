package pipeline

// The two wake paths that registered nothing (#1662, defect 4).
//
// Both `type: script` steps and prewarm call EnsureCrewRuntime directly and
// never reach RunAgent, which is where refreshActivity and the stats
// registration live. A container woken by either was tracked by nothing at
// all — no TTL, so the reaper never saw it; no stats, so the dashboard tile
// stayed empty. It ran until crewshipd restarted or an operator stopped it by
// hand.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

func wakeTestOrchestrator() *orchestrator.Orchestrator {
	return orchestrator.New(&orchCovContainer{}, nil, slog.Default())
}

func TestPrewarmCrew_RegistersTheContainerWithTheReaper(t *testing.T) {
	cp := newCountingProvider()
	orch := wakeTestOrchestrator()
	r := &OrchestratorRunner{
		container: cp,
		orch:      orch,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, crewID, _ string) (provider.CrewConfig, error) {
			return provider.CrewConfig{ID: crewID, Slug: "growth", TTLHours: 6}, nil
		},
	}

	if err := r.PrewarmCrew(context.Background(), "crew_x", "ws_1"); err != nil {
		t.Fatalf("PrewarmCrew: %v", err)
	}

	act, ok := orch.CrewActivity("crew_x")
	if !ok {
		t.Fatal("prewarm left the crew untracked — the container it started can never be reaped")
	}
	if act.ContainerID != "ctr_crew_x" {
		t.Errorf("tracked containerID = %q, want ctr_crew_x", act.ContainerID)
	}
	if act.TTLHours != 6 {
		t.Errorf("tracked TTL = %d, want 6 (the crew's resolved config)", act.TTLHours)
	}
}

func TestPrewarmCrew_RegistersTheContainerForStats(t *testing.T) {
	cp := newCountingProvider()
	orch := wakeTestOrchestrator()
	var gotContainer, gotCrew, gotWS string
	orch.SetStatsRegisterCallback(func(containerID, crewID, workspaceID string) {
		gotContainer, gotCrew, gotWS = containerID, crewID, workspaceID
	})
	r := &OrchestratorRunner{
		container: cp,
		orch:      orch,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, crewID, _ string) (provider.CrewConfig, error) {
			return provider.CrewConfig{ID: crewID}, nil
		},
	}

	if err := r.PrewarmCrew(context.Background(), "crew_x", "ws_1"); err != nil {
		t.Fatalf("PrewarmCrew: %v", err)
	}
	if gotContainer != "ctr_crew_x" || gotCrew != "crew_x" || gotWS != "ws_1" {
		t.Errorf("stats registration = (%q,%q,%q), want (ctr_crew_x, crew_x, ws_1)", gotContainer, gotCrew, gotWS)
	}
}

func TestPrewarmCrew_NilOrchestratorStillWarms(t *testing.T) {
	// Bare/test runners construct OrchestratorRunner without an orchestrator.
	// Registration is best-effort; it must not turn a working prewarm into a
	// panic.
	cp := newCountingProvider()
	r := &OrchestratorRunner{container: cp, logger: slog.Default()}
	if err := r.PrewarmCrew(context.Background(), "crew_x", "ws_1"); err != nil {
		t.Fatalf("PrewarmCrew with a nil orchestrator: %v", err)
	}
	if cp.starts != 1 {
		t.Errorf("starts = %d, want 1", cp.starts)
	}
}

func TestRunScript_RegistersTheContainerWithTheReaper(t *testing.T) {
	// The assertion is on the TTL, not merely on the crew being present.
	// The hold this path also takes creates the tracking entry by itself, so
	// "is it tracked?" would stay green with the registration deleted — the
	// TTL is the one thing only the registration delivers, and without it the
	// reaper skips the crew forever (ttl <= 0 means never stop).
	c := &scriptCovContainer{inspectCode: 0}
	orch := wakeTestOrchestrator()
	r := &OrchestratorRunner{
		container: c,
		orch:      orch,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, crewID, _ string) (provider.CrewConfig, error) {
			return provider.CrewConfig{ID: crewID, TTLHours: 6}, nil
		},
	}

	_, err := r.RunScript(context.Background(), ScriptRunRequest{
		AuthorCrewID: "crew_1", WorkspaceID: "ws_1",
		Interpreter: "python3", Path: "/crew/shared/s.py",
	})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	act, ok := orch.CrewActivity("crew_1")
	if !ok {
		t.Fatal("a script step left the crew untracked — the container it woke can never be reaped")
	}
	if act.TTLHours != 6 {
		t.Errorf("registered TTL = %d, want 6 (the crew's resolved config)", act.TTLHours)
	}
	if act.ContainerID == "" {
		t.Error("script step registered the crew with no container id — nothing to stop")
	}
	if act.LastActivity.IsZero() {
		t.Error("script step registered the crew without an activity timestamp")
	}
}

func TestRunScript_HoldsTheContainerOpenWhileTheScriptRuns(t *testing.T) {
	// The script execs inside the crew container. A TTL sweep landing
	// mid-script would stop the container out from under a running step, and
	// the step would surface as an unattributable exec failure. The hold must
	// be live for the duration and dropped afterwards.
	orch := wakeTestOrchestrator()
	held := make(chan int, 1)
	c := &scriptCovContainer{inspectCode: 0}
	c.onExec = func() {
		act, _ := orch.CrewActivity("crew_1")
		held <- act.Holds
	}
	r := &OrchestratorRunner{container: c, orch: orch, logger: slog.Default()}

	if _, err := r.RunScript(context.Background(), ScriptRunRequest{
		AuthorCrewID: "crew_1", WorkspaceID: "ws_1",
		Interpreter: "bash", Path: "/crew/shared/s.sh",
	}); err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	select {
	case n := <-held:
		if n < 1 {
			t.Errorf("holds during the script exec = %d, want at least 1", n)
		}
	case <-time.After(time.Second):
		t.Fatal("script exec never observed")
	}

	act, _ := orch.CrewActivity("crew_1")
	if act.Holds != 0 {
		t.Errorf("holds after the script returned = %d, want 0", act.Holds)
	}
}

func TestRunScript_NilOrchestratorStillRuns(t *testing.T) {
	c := &scriptCovContainer{inspectCode: 0}
	r := &OrchestratorRunner{container: c, logger: slog.Default()}
	if _, err := r.RunScript(context.Background(), ScriptRunRequest{
		AuthorCrewID: "crew_1", Interpreter: "bash", Path: "/crew/shared/s.sh",
	}); err != nil {
		t.Fatalf("RunScript with a nil orchestrator: %v", err)
	}
}
