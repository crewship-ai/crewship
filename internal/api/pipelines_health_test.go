package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ODCH-RT-07: the Health table's figures come from one endpoint — last 20
// outcomes, success over the finished ones, and a rolling window of runs and
// spend — instead of the newest 200 workspace runs and a calendar month.
func TestGetHealth(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineWithVersions(t, h, wsID, "pln-h1", "busy", 1)
	seedPipelineWithVersions(t, h, wsID, "pln-h2", "quiet", 1)
	seedPipelineWithVersions(t, h, wsID, "pln-h3", "never", 1)

	now := time.Now().UTC()
	run := func(id, pipelineID, slug, status, outcome, mode string, ago time.Duration, cost float64) {
		t.Helper()
		ts := now.Add(-ago).Format(time.RFC3339Nano)
		if _, err := h.db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, outcome, mode, started_at, cost_usd, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`, id, wsID, pipelineID, slug, status, outcome, mode, ts, cost, ts, ts); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	day := 24 * time.Hour
	run("r1", "pln-h1", "busy", "completed", "", "run", 40*day, 1.00) // outside the 30 d window, inside the last 20
	run("r2", "pln-h1", "busy", "completed", "", "run", 5*day, 0.50)
	run("r3", "pln-h1", "busy", "failed", "", "run", 4*day, 0.25)
	run("r4", "pln-h1", "busy", "completed", "FAILED", "run", 3*day, 0.25) // finished, but its result failed
	run("r5", "pln-h1", "busy", "completed", "", "run", 2*day, 0.50)
	run("r6", "pln-h1", "busy", "cancelled", "", "run", 1*day, 0.10)
	run("r7", "pln-h1", "busy", "running", "", "run", time.Hour, 0)
	run("r8", "pln-h1", "busy", "completed", "", "dry_run", time.Hour, 9) // not a real run
	run("r9", "pln-h2", "quiet", "completed", "", "run", 2*day, 0.10)

	get := func(query string) (int, routineHealthResponse) {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/x"+query, nil), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.GetHealth(rr, req)
		var resp routineHealthResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		return rr.Code, resp
	}

	code, resp := get("")
	if code != http.StatusOK || resp.WindowDays != 30 || len(resp.Routines) != 2 {
		t.Fatalf("health = %d %+v", code, resp)
	}
	by := map[string]routineHealthRow{}
	for _, r := range resp.Routines {
		by[r.Slug] = r
	}
	busy := by["busy"]
	want := []string{"completed", "completed", "failed", "failed", "completed", "stopped", "running"}
	if len(busy.LastOutcomes) != len(want) {
		t.Fatalf("busy outcomes = %v, want %v", busy.LastOutcomes, want)
	}
	for i := range want {
		if busy.LastOutcomes[i] != want[i] {
			t.Fatalf("busy outcomes = %v, want %v", busy.LastOutcomes, want)
		}
	}
	// 5 finished (the stopped and the running one are not), 3 of them completed.
	if busy.Finished != 5 || busy.Succeeded != 3 || busy.SuccessPct == nil || *busy.SuccessPct != 60 {
		t.Fatalf("busy success = %+v", busy)
	}
	// The window drops the 40-day-old run and the dry run.
	if busy.Runs != 6 || busy.SpendUSD != 1.6 {
		t.Fatalf("busy window = %d runs, $%v; want 6 runs, $1.6", busy.Runs, busy.SpendUSD)
	}
	if by["quiet"].Runs != 1 || by["quiet"].SuccessPct == nil || *by["quiet"].SuccessPct != 100 {
		t.Fatalf("quiet = %+v", by["quiet"])
	}
	if _, ok := by["never"]; ok {
		t.Error("a routine with no run must not be listed")
	}

	// A narrower window changes the figures, not the outcomes.
	_, narrow := get("?days=3")
	for _, r := range narrow.Routines {
		if r.Slug == "busy" && (r.Runs != 3 || len(r.LastOutcomes) != len(want)) {
			t.Fatalf("busy in 3 days = %+v", r)
		}
	}

	for _, bad := range []string{"?days=0", "?days=367", "?days=abc"} {
		if code, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
}

func TestGetHealth_Last20Only(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineWithVersions(t, h, wsID, "pln-h1", "busy", 1)
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		ts := now.Add(-time.Duration(25-i) * time.Hour).Format(time.RFC3339Nano)
		status := "completed"
		if i < 5 { // the five oldest fail and must fall out of the last 20
			status = "failed"
		}
		if _, err := h.db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, created_at, updated_at)
			VALUES (?, ?, 'pln-h1', 'busy', ?, 'run', ?, ?, ?)`, "run_"+string(rune('a'+i)), wsID, status, ts, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetHealth(rr, req)
	var resp routineHealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil || len(resp.Routines) != 1 {
		t.Fatalf("health: %v %s", err, rr.Body.String())
	}
	row := resp.Routines[0]
	if len(row.LastOutcomes) != 20 || row.Finished != 20 || row.SuccessPct == nil || *row.SuccessPct != 100 || row.Runs != 25 {
		t.Fatalf("row = %+v", row)
	}
}

func TestGetHealth_NoRunStore_503(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetHealth(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}
