package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The sidecar asks this endpoint about a run it cannot account for locally.
// Every answer here either keeps a live agent working or revokes one, so each
// branch is worth a case of its own.
func TestRunStatus_AnswersWhetherTheRunMayStillAct(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewRunStatusHandler(db, quietLogger())

	// A live attempt of running work.
	seedWorkItem(t, db, seededWork{ID: "wk-live", WorkspaceID: ws, State: "running", Generation: 2})
	seedWorkAttempt(t, db, "wk-live", "run-live", 2)

	// An attempt that has ended, its work item still running (a retry took over).
	seedWorkItem(t, db, seededWork{ID: "wk-retried", WorkspaceID: ws, State: "running", Generation: 3})
	seedWorkAttempt(t, db, "wk-retried", "run-ended", 3)
	if _, err := db.Exec(
		`UPDATE work_attempts SET ended_at = '2026-09-11T00:00:00.000000000Z' WHERE run_id = 'run-ended'`,
	); err != nil {
		t.Fatal(err)
	}

	// An attempt superseded by a later generation. Its own row looks untouched,
	// which is exactly why the generation comparison has to be here.
	seedWorkItem(t, db, seededWork{ID: "wk-superseded", WorkspaceID: ws, State: "running", Generation: 9})
	seedWorkAttempt(t, db, "wk-superseded", "run-old", 4)

	// Work that finished.
	seedWorkItem(t, db, seededWork{ID: "wk-done", WorkspaceID: ws, State: "succeeded", Generation: 1})
	seedWorkAttempt(t, db, "wk-done", "run-done", 1)

	tests := []struct {
		name       string
		runID      string
		wantActive bool
	}{
		{"the current attempt of running work", "run-live", true},
		{"an attempt that has ended", "run-ended", false},
		{"an attempt a later generation replaced", "run-old", false},
		{"an attempt of work that finished", "run-done", false},
		{"a run the ledger has never heard of", "run-ghost", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/internal/runs/"+tc.runID+"/status", nil)
			req.SetPathValue("runId", tc.runID)
			rr := httptest.NewRecorder()
			h.Status(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
			}
			var out runStatusResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Active != tc.wantActive {
				t.Errorf("active = %v, want %v (reason: %q)", out.Active, tc.wantActive, out.Reason)
			}
			if out.RunID != tc.runID {
				t.Errorf("run_id = %q, want %q", out.RunID, tc.runID)
			}
			if !out.Active && out.Reason == "" {
				t.Error("a refusal with no reason leaves an operator nothing to read")
			}
		})
	}
}

// An unknown run is 200 with active=false, NOT 404.
//
// On this surface a 404 is deliberately ambiguous: the router answers an
// unregistered path and a refused caller with the same bytes, so the internal
// surface cannot be mapped. A missing run returned as 404 would therefore be
// read by an old sidecar as "this host lacks the endpoint" and by a revoked one
// as the same thing — and those two demand opposite responses.
func TestRunStatus_UnknownRunIsAnAnswerNotA404(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewRunStatusHandler(db, quietLogger())

	req := httptest.NewRequest("GET", "/api/v1/internal/runs/nope/status", nil)
	req.SetPathValue("runId", "nope")
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Fatal("an unknown run answered 404, which a caller cannot tell from 'this endpoint does not exist' " +
			"or from 'your token was refused' — and those need opposite handling")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

// An unavailable ledger is not an answer. Reporting "inactive" would revoke
// every live run in the crew during a database blip; reporting "active" would
// defeat the check entirely. 503 routes the caller to its own written-down
// policy for an unreachable authority.
func TestRunStatus_AnUnavailableLedgerIsNotAVerdict(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	h := NewRunStatusHandler(db, quietLogger())
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/internal/runs/run-x/status", nil)
	req.SetPathValue("runId", "run-x")
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — an unreachable ledger must not read as a verdict either way", rr.Code)
	}
}

// The endpoint and the memory mutation path must agree about what "current"
// means. Two readings of the same tables would drift, and the direction that
// matters is a sidecar admitting a run the ledger considers finished.
func TestRunStatus_AgreesWithTheMemoryMutationFence(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)

	seedWorkItem(t, db, seededWork{ID: "wk-agree", WorkspaceID: ws, State: "running", Generation: 5, AgentID: "ag-1"})
	seedWorkAttempt(t, db, "wk-agree", "run-agree", 5)

	active, _, err := runIsLiveAttempt(context.Background(), db, "run-agree")
	if err != nil {
		t.Fatalf("run status: %v", err)
	}
	mm := NewMemoryMutationHandler(db, t.TempDir(), t.TempDir(), quietLogger())
	fenceErr := mm.authorizeRun(ws, "ag-1", "run-agree", 5)(context.Background())

	if active != (fenceErr == nil) {
		t.Fatalf("run status says active=%v but the memory fence says %v; the two definitions have drifted",
			active, fenceErr)
	}
}
