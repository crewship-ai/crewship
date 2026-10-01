package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

// A dispatch a mission tick starts is admitted by the quiet window before
// its goroutine runs: counted as busy at once, so a window closing while the
// goroutine is being scheduled cannot hold over it.
func TestAdmitDispatchCountsBeforeTheGoroutineRuns(t *testing.T) {
	c := quiesce.Default()
	wr, ok := c.Enter(context.Background())
	if !ok {
		t.Fatal("writer refused with no window")
	}
	begun := make(chan error, 1)
	go func() {
		w, err := c.Begin(context.Background(), quiesce.Options{
			BusyWait: 20 * time.Millisecond, Poll: time.Millisecond,
			HoldCap: time.Minute, DrainTimeout: time.Minute,
		})
		if err == nil {
			w.Release()
		}
		begun <- err
	}()
	for !c.Holding() {
		time.Sleep(time.Millisecond) // wait for "closing"; no assertion depends on it
	}
	admit := admitDispatch(context.WithoutCancel(wr.Context()))
	if c.Runs() != 1 {
		t.Fatalf("Runs = %d right after admitDispatch, want 1", c.Runs())
	}
	wr.Leave()
	if err := <-begun; !errors.Is(err, quiesce.ErrBusy) {
		t.Fatalf("Begin: %v, want ErrBusy (the admitted dispatch keeps the window from holding)", err)
	}
	_, done := admit()
	done()
	if c.Runs() != 0 {
		t.Fatalf("Runs = %d after done, want 0", c.Runs())
	}
}

// Outside a tick (no writer inside) and with a window open, the dispatch
// waits for the release before it starts.
func TestAdmitDispatchWaitsOutAnOpenWindow(t *testing.T) {
	c := quiesce.Default()
	w, err := c.Begin(context.Background(), quiesce.Options{BusyWait: time.Second, HoldCap: time.Minute, DrainTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	admit := admitDispatch(context.Background())
	got := make(chan func(), 1)
	go func() {
		_, done := admit()
		got <- done
	}()
	select {
	case <-got:
		w.Release()
		t.Fatal("a dispatch started while the window was held")
	case <-time.After(20 * time.Millisecond):
	}
	w.Release()
	(<-got)()
}

func TestMissionTimeoutCleanupWaitsForQuietWindow(t *testing.T) {
	t.Chdir(t.TempDir())
	db := setupTestDB(t)
	workspace, crew, lead, agent := seedTestData(t, db)
	mission := createTestMission(t, db, workspace, crew, lead)
	insertTask(t, db, "awaiting-timeout", mission, &agent, "Awaiting approval", "AWAITING_APPROVAL", 1, nil)
	engine := newTestEngine(t, db)
	state := makeMissionState(mission, crew, "dev-crew", lead, workspace, "timeout-barrier")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	window, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer window.Release()
	done := make(chan struct{})
	go func() { engine.runMissionLoop(ctx, state); close(done) }()
	select {
	case <-done:
		t.Fatal("timeout cleanup wrote through a held backup window")
	case <-time.After(100 * time.Millisecond):
	}
	var taskStatus string
	if err = db.QueryRow(`SELECT status FROM mission_tasks WHERE id='awaiting-timeout'`).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "AWAITING_APPROVAL" || getMissionStatus(t, db, mission) != "IN_PROGRESS" {
		t.Fatalf("timeout changed protected rows while window held: task=%s mission=%s", taskStatus, getMissionStatus(t, db, mission))
	}
	progressPath := filepath.Join("data", "crews", "dev-crew", "missions", "timeout-barrier", "progress.jsonl")
	if _, err := os.Stat(progressPath); !os.IsNotExist(err) {
		t.Fatalf("timeout progress was written during held window: %v", err)
	}
	window.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout cleanup did not resume after release")
	}
	if err = db.QueryRow(`SELECT status FROM mission_tasks WHERE id='awaiting-timeout'`).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	progress, err := os.ReadFile(progressPath)
	if err != nil || !strings.Contains(string(progress), "mission_timeout") {
		t.Fatalf("missing progress after release: %v", err)
	}
	if taskStatus != "FAILED" || getMissionStatus(t, db, mission) != "FAILED" {
		t.Fatalf("timeout failed to finalize after release: task=%s mission=%s", taskStatus, getMissionStatus(t, db, mission))
	}
}
