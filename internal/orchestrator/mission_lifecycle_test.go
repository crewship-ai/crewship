package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------------------------------------------------------------------------
// mission.go — lifecycle (StartMission / StopMission / Shutdown) and the
// ParseHandoff public wrapper. Existing tests in mission_test.go cover
// ResolveReadyTasks and the rest of the scheduler; this file fills in the
// surrounding lifecycle gates that runMissionLoop relies on.
// ---------------------------------------------------------------------------

func newLifecycleHub(t *testing.T) *ws.Hub {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	return ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
}

func newLifecycleEngine(t *testing.T, db *sql.DB) *MissionEngine {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	return NewMissionEngine(db, nil, newLifecycleHub(t), logger)
}

// ---- ParseHandoff (public wrapper) ----

// parseHandoff itself is exhaustively tested in handoff_test.go. The
// exported ParseHandoff wrapper just delegates, so a single sanity check
// is enough to lock that the public surface keeps that delegation.
func TestParseHandoff_PublicWrapperDelegatesToInternal(t *testing.T) {
	input := `Some output
---HANDOFF---
summary: did the thing
confidence: high
artifacts: report.md
---END HANDOFF---`
	pub := ParseHandoff(input)
	priv := parseHandoff(input)
	if pub != priv {
		t.Errorf("ParseHandoff(%q) = %+v, parseHandoff = %+v — wrapper must delegate", input, pub, priv)
	}
	if !pub.Parsed || pub.Summary != "did the thing" || pub.Confidence != "high" {
		t.Errorf("public wrapper returned %+v, want fully-populated parsed handoff", pub)
	}
}

func TestParseHandoff_PublicWrapper_NoBlock(t *testing.T) {
	pub := ParseHandoff("no markers here")
	if pub.Parsed {
		t.Errorf("ParseHandoff on no-block input must return Parsed=false; got %+v", pub)
	}
}

// ---- StopMission ----

func TestStopMission_NoopWhenNotActive(t *testing.T) {
	// StopMission on a mission ID that was never started must be a silent
	// no-op (no panic, no mutation). The toolbar's "stop" button can fire
	// after the orchestrator already cleaned the mission up.
	e := newLifecycleEngine(t, setupTestDB(t))
	e.StopMission("mission-never-started") // must not panic
	if len(e.active) != 0 {
		t.Errorf("active map non-empty after no-op stop: %v", e.active)
	}
}

func TestStopMission_CancelsAndRemovesActive(t *testing.T) {
	e := newLifecycleEngine(t, setupTestDB(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelled := false
	e.mu.Lock()
	e.active["m1"] = &missionState{
		ID: "m1", WorkspaceID: "ws", CrewSlug: "c",
		cancel: func() { cancelled = true; cancel() },
	}
	e.mu.Unlock()
	_ = ctx

	e.StopMission("m1")
	if !cancelled {
		t.Error("StopMission did not invoke the mission's cancel func")
	}
	e.mu.Lock()
	_, stillActive := e.active["m1"]
	e.mu.Unlock()
	if stillActive {
		t.Error("StopMission did not delete the entry from active map")
	}
}

// ---- Shutdown ----

func TestShutdown_CancelsAllAndSetsStopping(t *testing.T) {
	e := newLifecycleEngine(t, setupTestDB(t))

	var cancelled int
	cancelFn := func() { cancelled++ }

	e.mu.Lock()
	e.active["m1"] = &missionState{ID: "m1", cancel: cancelFn}
	e.active["m2"] = &missionState{ID: "m2", cancel: cancelFn}
	e.active["m3"] = &missionState{ID: "m3", cancel: cancelFn}
	e.mu.Unlock()

	e.Shutdown()

	if cancelled != 3 {
		t.Errorf("Shutdown cancelled %d missions, want 3", cancelled)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.active) != 0 {
		t.Errorf("active map after Shutdown = %v, want empty", e.active)
	}
	if !e.stopping {
		t.Error("Shutdown did not set stopping=true")
	}
}

func TestShutdown_RejectsSubsequentStartMission(t *testing.T) {
	// Post-Shutdown, StartMission must reject with "shutting down" rather
	// than silently leak a new goroutine (the engine would not be there
	// to drive it after the parent process is exiting).
	e := newLifecycleEngine(t, setupTestDB(t))
	e.Shutdown()
	err := e.StartMission(context.Background(), "irrelevant")
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("StartMission post-Shutdown = %v, want \"shutting down\" error", err)
	}
}

// ---- StartMission gates ----

func TestStartMission_ConcurrentStartIsIdempotentNoop(t *testing.T) {
	// Pre-seed the active map as if a concurrent start beat us. The TOCTOU
	// guard in StartMission checks for this exact sentinel — and since W5
	// (boot re-attach) it resolves the race as a successful no-op: the
	// desired state (a live loop) already holds, so no error and, crucially,
	// no second loop replacing the existing state.
	e := newLifecycleEngine(t, setupTestDB(t))
	existing := &missionState{ID: "mission-busy"}
	e.mu.Lock()
	e.active["mission-busy"] = existing
	e.mu.Unlock()

	if err := e.StartMission(context.Background(), "mission-busy"); err != nil {
		t.Errorf("StartMission with active mission = %v, want nil (idempotent no-op)", err)
	}
	e.mu.Lock()
	got := e.active["mission-busy"]
	e.mu.Unlock()
	if got != existing {
		t.Error("idempotent StartMission replaced the existing active mission state")
	}
}

func TestStartMission_NotFound_CleansUpSentinel(t *testing.T) {
	// When the mission row doesn't exist, StartMission inserts a sentinel
	// into the active map BEFORE the DB lookup. The error path must remove
	// it so a later (correct) StartMission for the same ID can succeed.
	db := setupTestDB(t)
	e := newLifecycleEngine(t, db)

	err := e.StartMission(context.Background(), "ghost-mission")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("StartMission on missing mission = %v, want \"not found\"", err)
	}
	e.mu.Lock()
	_, leaked := e.active["ghost-mission"]
	e.mu.Unlock()
	if leaked {
		t.Error("ghost-mission sentinel was not cleaned from active map after not-found error")
	}
}

func TestStartMission_HappyPath_RegistersActiveAndCancelsCleanly(t *testing.T) {
	// Verify the happy path: with a valid mission row, StartMission
	// populates the active map with the resolved title/crew/lead, spawns
	// the loop goroutine, and StopMission cancels it within the window
	// before the loop's first 3-second tick. We don't assert the loop
	// did any work — that's covered by mission_e2e_test.go.
	db := setupTestDB(t)
	wsID, crewID, leadID, _ := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	e := newLifecycleEngine(t, db)

	if err := e.StartMission(context.Background(), missionID); err != nil {
		t.Fatalf("StartMission: %v", err)
	}

	e.mu.Lock()
	ms, ok := e.active[missionID]
	e.mu.Unlock()
	if !ok {
		t.Fatal("active map missing mission after StartMission")
	}
	if ms.Title != "Test Mission" {
		t.Errorf("active mission Title = %q, want \"Test Mission\"", ms.Title)
	}
	if ms.CrewSlug != "dev-crew" {
		t.Errorf("active mission CrewSlug = %q, want dev-crew", ms.CrewSlug)
	}
	if ms.cancel == nil {
		t.Error("active mission has nil cancel — Shutdown/StopMission would panic")
	}

	// StopMission cancels the loop and removes the entry. Loop deletes
	// itself again in its defer; the second delete is a no-op.
	e.StopMission(missionID)

	// Give the loop goroutine a beat to exit on ctx cancel.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		_, stillActive := e.active[missionID]
		e.mu.Unlock()
		if !stillActive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.mu.Lock()
	_, stillActive := e.active[missionID]
	e.mu.Unlock()
	if stillActive {
		t.Error("mission still in active map 1s after StopMission")
	}
}

// ---- getMissionStatus ----

func TestGetMissionStatus_ReadsCurrentStatus(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, leadID, _ := seedTestData(t, db)
	missionID := createTestMission(t, db, wsID, crewID, leadID)

	e := newLifecycleEngine(t, db)
	got, err := e.getMissionStatus(context.Background(), missionID)
	if err != nil {
		t.Fatalf("getMissionStatus: %v", err)
	}
	if got != "IN_PROGRESS" {
		t.Errorf("status = %q, want IN_PROGRESS", got)
	}

	// Mutate and re-read — the read must reflect the new value (i.e.
	// no caching is hiding the DB).
	if _, err := db.Exec(`UPDATE missions SET status = 'PAUSED' WHERE id = ?`, missionID); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = e.getMissionStatus(context.Background(), missionID)
	if err != nil {
		t.Fatalf("getMissionStatus (after update): %v", err)
	}
	if got != "PAUSED" {
		t.Errorf("status after update = %q, want PAUSED", got)
	}
}

func TestGetMissionStatus_UnknownMission_ReturnsErrNoRows(t *testing.T) {
	e := newLifecycleEngine(t, setupTestDB(t))
	_, err := e.getMissionStatus(context.Background(), "missing-mission")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, want sql.ErrNoRows", err)
	}
}
