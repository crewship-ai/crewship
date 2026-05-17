package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

// fakeTrigger captures the (workspace, crew, slug) arguments the
// caller passes and counts invocations. Drop-in replacement for a
// real *consolidate.PostRunTrigger via the postRunTriggerHook
// interface — no consolidator / Summarizer plumbing needed.
type fakeTrigger struct {
	calls    atomic.Int32
	lastWS   atomic.Value
	lastCrew atomic.Value
	lastSlug atomic.Value
}

func (f *fakeTrigger) OnRunCompleted(_ context.Context, workspaceID, crewID, crewSlug string) bool {
	f.calls.Add(1)
	f.lastWS.Store(workspaceID)
	f.lastCrew.Store(crewID)
	f.lastSlug.Store(crewSlug)
	return true
}

func (f *fakeTrigger) lastWorkspace() string {
	if v := f.lastWS.Load(); v != nil {
		return v.(string)
	}
	return ""
}

func (f *fakeTrigger) lastCrewID() string {
	if v := f.lastCrew.Load(); v != nil {
		return v.(string)
	}
	return ""
}

func (f *fakeTrigger) lastCrewSlug() string {
	if v := f.lastSlug.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// TestUpdateRun_FiresPostRunTriggerOnCompletion asserts the post-run
// hook fires exactly once when a run flips to COMPLETED, carrying
// the workspace + the agent's crew_id + crew slug. The hook is the
// sleep-time pattern from PRD §8.1: agent finishes → consolidator
// wakes opportunistically without waiting for the 6h cron.
func TestUpdateRun_FiresPostRunTriggerOnCompletion(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Seed a crew so the agent has somewhere to belong + so the
	// agent → crew SQL JOIN resolves to a real slug.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_x', ?, 'Crew X', 'crew-x')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES ('a1', ?, 'crew_x', 'Bot', 'bot', 'RUNNING')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	seedRunFixture(t, db, "run1", "a1", wsID, "", "USER", "")

	h := NewInternalHandler(db, "test-token", logger)
	h.SetHub(ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests))
	_ = wireTestJournalForHandler(t, db, h)
	trig := &fakeTrigger{}
	h.SetPostRunTrigger(trig)

	body := strings.NewReader(`{"status":"COMPLETED","exit_code":0}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run1", body)
	req.SetPathValue("runId", "run1")
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := trig.calls.Load(); got != 1 {
		t.Errorf("trigger fire count = %d, want 1", got)
	}
	if trig.lastWorkspace() != wsID {
		t.Errorf("trigger workspace = %q, want %q", trig.lastWorkspace(), wsID)
	}
	if trig.lastCrewID() != "crew_x" || trig.lastCrewSlug() != "crew-x" {
		t.Errorf("trigger crew = (%q, %q), want (crew_x, crew-x)",
			trig.lastCrewID(), trig.lastCrewSlug())
	}
}

// TestUpdateRun_DoesNotFireOnNonCompletedTerminal asserts the trigger
// only fires on COMPLETED — failed / cancelled / timeout runs don't
// produce stable signal for consolidation, and firing on them would
// just produce noisy proposals operators would reject.
func TestUpdateRun_DoesNotFireOnFailure(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_y', ?, 'Crew Y', 'crew-y')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES ('a2', ?, 'crew_y', 'Bot', 'bot', 'RUNNING')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	seedRunFixture(t, db, "run2", "a2", wsID, "", "USER", "")

	h := NewInternalHandler(db, "test-token", logger)
	h.SetHub(ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests))
	_ = wireTestJournalForHandler(t, db, h)
	trig := &fakeTrigger{}
	h.SetPostRunTrigger(trig)

	body := strings.NewReader(`{"status":"FAILED","exit_code":1,"error_message":"boom"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run2", body)
	req.SetPathValue("runId", "run2")
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := trig.calls.Load(); got != 0 {
		t.Errorf("FAILED run must not fire trigger; got %d calls", got)
	}
}

// TestUpdateRun_NoTriggerWired_StillSucceeds asserts the wire is
// optional: with no trigger attached (nil hook) UpdateRun behaves
// exactly like the pre-PRD §8.1 path. Defends against a deployment
// that hasn't enabled the consolidator yet — UpdateRun must not
// regress.
func TestUpdateRun_NoTriggerWired_StillSucceeds(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_z', ?, 'Crew Z', 'crew-z')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, status) VALUES ('a3', ?, 'crew_z', 'Bot', 'bot', 'RUNNING')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	seedRunFixture(t, db, "run3", "a3", wsID, "", "USER", "")

	h := NewInternalHandler(db, "test-token", logger)
	h.SetHub(ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests))
	_ = wireTestJournalForHandler(t, db, h)
	// Deliberately no SetPostRunTrigger — h.postRunTrigger stays nil.

	body := strings.NewReader(`{"status":"COMPLETED","exit_code":0}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run3", body)
	req.SetPathValue("runId", "run3")
	rr := httptest.NewRecorder()
	h.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("nil trigger must not break UpdateRun; status=%d body=%s", rr.Code, rr.Body.String())
	}
}
