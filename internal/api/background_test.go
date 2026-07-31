package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// TestBackgroundWork_DrainWaitsForAnOutstandingHandlerWrite is the
// teeth test for background.go, and the regression test for #1596.
//
// The defect it pins: a handler returns 200 while the work it spawned
// is still in flight, so the owning test's teardown races that work —
// deleting the storage dir out from under it, or closing the database
// it is about to query. The symptom is a DIFFERENT test family failing
// each run, self-healing on re-run.
//
// Making that deterministic rather than hoping to catch the race: the
// test takes the lessons flock itself before driving the handler, so
// the hook's goroutine is provably parked inside WriteCrewLesson and
// cannot possibly have finished. A timer releases the lock 300ms
// later. Then the single assertion that matters:
//
//	waitForBackgroundWork returned true  =>  the lesson file EXISTS
//
// with no polling between the two. Before the hook registers its
// goroutine, the wait returns in microseconds — long before the timer
// fires, so the file cannot be there and the test is red. After it
// registers, the wait cannot return until the write is done, so the
// file is always there.
//
// Neither direction can be faked by a neighbour: other tests' work can
// only make the wait take LONGER (never shorter), so a red here is
// always this hook's, and a green always means the drain actually
// waited.
func TestBackgroundWork_DrainWaitsForAnOutstandingHandlerWrite(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	storagePath := storageDir(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-drain", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, identifier, status, created_at, updated_at)
		VALUES ('m_drain', ?, ?, ?, 'trace_drain', 'Drain the background work', 'ENG-1596', 'REVIEW', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	// Park the hook's write. WriteCrewLesson does its MkdirAll before
	// taking the lock, so pre-creating the tree here only removes a
	// step it would have taken anyway — the flock is what blocks.
	memDir := filepath.Join(storagePath, "crews", crewID, "shared", ".memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	lessonsPath := filepath.Join(memDir, "lessons.md")
	lk := memory.NewFileLock(lessonsPath + ".lock")
	if err := lk.Lock(); err != nil {
		t.Fatalf("take lessons lock: %v", err)
	}
	unlocked := make(chan struct{})
	time.AfterFunc(300*time.Millisecond, func() {
		_ = lk.Unlock()
		close(unlocked)
	})
	t.Cleanup(func() { <-unlocked })

	handler := NewMissionHandler(db, nil, nil, logger)
	handler.SetStoragePath(storagePath)

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m_drain", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m_drain")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	if !waitForBackgroundWork(30 * time.Second) {
		t.Fatal("waitForBackgroundWork timed out: the mission-outcome hook never finished")
	}

	// No polling, deliberately. Polling here would let the test pass
	// on a drain that returns early, which is the bug.
	if _, err := os.Stat(lessonsPath); err != nil {
		t.Fatalf("drain returned before the handler's detached write landed: stat %s: %v\n"+
			"The mission-outcome hook's goroutine is not registered with beginBackgroundWork, "+
			"so a test's teardown can delete this directory (or close the DB) while the write is in flight — #1596.",
			lessonsPath, err)
	}
}

// TestBackgroundWork_BeginIsIdempotentPerGoroutine pins the Done-once
// contract. A finish func called twice would drive the WaitGroup
// counter negative and panic the whole test binary — a defence worth
// having explicitly, since the call sites all `defer finish()` and a
// future site that also calls it on an early return would otherwise
// take the suite down rather than fail one test.
func TestBackgroundWork_BeginIsIdempotentPerGoroutine(t *testing.T) {
	finish := beginBackgroundWork()
	if waitForBackgroundWork(50 * time.Millisecond) {
		t.Fatal("wait reported drained while work was registered")
	}
	finish()
	finish()
	if !waitForBackgroundWork(5 * time.Second) {
		t.Fatal("wait did not drain after finish()")
	}
}
