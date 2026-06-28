package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedRunLogEntry inserts one journal row tied to a run's trace_id so the
// RunLogs projection has something to read. ts is explicit so tests can
// assert ordering deterministically.
func seedRunLogEntry(t *testing.T, db *sql.DB, id, wsID, traceID, ts, severity, summary string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO journal_entries
		    (id, workspace_id, ts, entry_type, severity, actor_type, actor_id,
		     summary, payload, refs, trace_id, priority)
		VALUES (?, ?, ?, 'pipeline.step.completed', ?, 'system', 'sys',
		        ?, '{}', '{}', ?, 'normal')`,
		id, wsID, ts, severity, summary, traceID); err != nil {
		t.Fatalf("seed run log entry %s: %v", id, err)
	}
}

// TestRunLogs_MissingRunID_Returns400 — empty path value short-circuits
// before any DB work, same guard as GetRun.
func TestRunLogs_MissingRunID_Returns400(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs//logs", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.RunLogs(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestRunLogs_UnknownRun_Returns404 — a run id absent from this workspace
// (unknown or foreign) is masked as 404, never an empty 200, so existence
// in another tenant doesn't leak.
func TestRunLogs_UnknownRun_Returns404(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_nope/logs", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_nope")
	rr := httptest.NewRecorder()
	h.RunLogs(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestRunLogs_ReturnsEntriesOldestFirst — the console reads top-to-bottom,
// so the handler must reverse journal.List's newest-first ordering and
// project ts/level/message from each entry.
func TestRunLogs_ReturnsEntriesOldestFirst(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "pl-logs", "logs-pipe")
	seedRunRow(t, db, wsID, "pl-logs", "logs-pipe", "prn_logs", "completed")

	// Older first, newer second — by ts.
	seedRunLogEntry(t, db, "jl-1", wsID, "prn_logs", "2026-01-01T00:00:01Z", "info", "first line")
	seedRunLogEntry(t, db, "jl-2", wsID, "prn_logs", "2026-01-01T00:00:02Z", "error", "second line")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_logs/logs", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_logs")
	rr := httptest.NewRecorder()
	h.RunLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got []runLogEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2; body=%s", len(got), rr.Body.String())
	}
	if got[0].Message != "first line" || got[1].Message != "second line" {
		t.Fatalf("order wrong: %+v", got)
	}
	if got[0].Level != "info" || got[1].Level != "error" {
		t.Fatalf("levels wrong: %+v", got)
	}
}

// TestRunLogs_MatchesPayloadRunID — pipeline runs tag the run id in the
// payload (payload.run_id), not the trace_id column. The handler must match
// those entries too, else the Activity Logs tab shows "no output" for every
// pipeline run despite a full timeline.
func TestRunLogs_MatchesPayloadRunID(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "pl-pl", "pl-pipe")
	seedRunRow(t, db, wsID, "pl-pl", "pl-pipe", "prn_pl", "running")

	// Entry tagged the pipeline way: empty trace_id, run id only in payload.
	if _, err := db.Exec(`
		INSERT INTO journal_entries
		    (id, workspace_id, ts, entry_type, severity, actor_type, actor_id,
		     summary, payload, refs, trace_id, priority)
		VALUES ('jp-1', ?, '2026-01-01T00:00:01Z', 'pipeline.run.started', 'info',
		        'orchestrator', 'prn_pl', 'Pipeline started',
		        '{"run_id":"prn_pl","pipeline_slug":"pl-pipe"}', '{}', '', 'normal')`,
		wsID); err != nil {
		t.Fatalf("seed payload entry: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_pl/logs", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_pl")
	rr := httptest.NewRecorder()
	h.RunLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got []runLogEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if len(got) != 1 || got[0].Message != "Pipeline started" {
		t.Fatalf("payload-run_id entry not matched: %+v", got)
	}
}

// TestRunLogs_EmptyWhenNoEntries — a run that exists but produced no
// journal rows returns an empty array (not null, not 404).
func TestRunLogs_EmptyWhenNoEntries(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "pl-empty", "empty-pipe")
	seedRunRow(t, db, wsID, "pl-empty", "empty-pipe", "prn_empty", "completed")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_empty/logs", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_empty")
	rr := httptest.NewRecorder()
	h.RunLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != "[]\n" && got != "[]" {
		t.Fatalf("body = %q, want empty array", got)
	}
}
