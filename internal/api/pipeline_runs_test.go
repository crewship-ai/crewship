package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// runsHandlerRig wires a PipelineHandler against the full-migration test
// DB so the production pipeline_runs schema (v83) is in play, not a
// truncated smoke schema. Returns the handler, the underlying DB so
// individual tests can seed rows directly, and the workspace fixtures
// every authenticated request needs.
func runsHandlerRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil)
	return h, db, userID, wsID
}

// seedRunsPipeline inserts a minimal pipelines row so pipeline_runs FK
// constraints are satisfied. Mirrors the column list seedPipelineRow in
// pipeline_schedules_test.go uses (the schedule + run paths only consult
// id/slug/workspace_id from the join).
func seedRunsPipeline(t *testing.T, db *sql.DB, wsID, id, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, '{"name":"x","steps":[]}', 'hash', ?, ?, ?)`,
		id, wsID, slug, slug, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

// seedRunRow inserts a single pipeline_runs row with sensible defaults.
// Tests that care about a particular column override via the optional
// mutate callback so each test stays readable without macro-style setups.
func seedRunRow(t *testing.T, db *sql.DB, wsID, pipelineID, slug, runID, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO pipeline_runs (
		    id, workspace_id, pipeline_id, pipeline_slug,
		    status, mode, started_at,
		    step_outputs_json, cost_usd, duration_ms,
		    triggered_via, inputs_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'run', ?, '{}', 0, 0, 'manual', '{}', ?, ?)`,
		runID, wsID, pipelineID, slug, status, now, now, now); err != nil {
		t.Fatalf("seed pipeline_run: %v", err)
	}
}

// ── GetRun ──────────────────────────────────────────────────────────────

// TestPipelineRuns_GetRun_MissingRunID_Returns400 confirms the handler
// short-circuits before touching the DB when the path value is empty.
// Without this guard a stray request shape could bypass the workspace
// scope check at the SQL level.
func TestPipelineRuns_GetRun_MissingRunID_Returns400(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/", nil),
		userID, wsID, "OWNER",
	)
	// Explicitly clear runId path value (no SetPathValue).
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineRuns_GetRun_UnknownID_Returns404 verifies the
// sql.ErrNoRows path. The lookup is workspace-scoped, so an unknown id
// is indistinguishable from a foreign-workspace id — both must surface
// as 404 to avoid leaking existence.
func TestPipelineRuns_GetRun_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_nope", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_nope")
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineRuns_GetRun_HappyPath_Returns200WithEnrichedRow exercises
// the LEFT JOIN pipelines projection. The handler must surface
// pipeline_name from the join so the trace canvas doesn't have to make
// a second fetch — a regression would silently return the bare slug.
func TestPipelineRuns_GetRun_HappyPath_Returns200WithEnrichedRow(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "pln_a", "ping-hosts")
	seedRunRow(t, db, wsID, "pln_a", "ping-hosts", "prn_1", "completed")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_1", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_1")
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["id"] != "prn_1" {
		t.Errorf("id = %v, want prn_1", resp["id"])
	}
	if resp["workspace_id"] != wsID {
		t.Errorf("workspace_id = %v, want %q", resp["workspace_id"], wsID)
	}
	if resp["pipeline_slug"] != "ping-hosts" {
		t.Errorf("pipeline_slug = %v, want ping-hosts", resp["pipeline_slug"])
	}
	// pipeline_name comes from the LEFT JOIN. A nil here means the join
	// silently dropped — exactly the kind of regression tests like this
	// guard.
	if resp["pipeline_name"] != "ping-hosts" {
		t.Errorf("pipeline_name = %v, want ping-hosts (join broken?)", resp["pipeline_name"])
	}
}

// TestPipelineRuns_GetRun_CrossWorkspace_Returns404 is the tenant
// isolation check. A run owned by workspace A must NEVER surface under
// workspace B's context — even though the row exists, the WHERE clause
// filters it out and the handler responds with 404.
func TestPipelineRuns_GetRun_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsA := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsA, "pln_a", "ours")
	seedRunRow(t, db, wsA, "pln_a", "ours", "prn_secret", "completed")

	// Provision a foreign workspace and try to read wsA's run from it.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+otherWS+"/pipeline-runs/prn_secret", nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("runId", "prn_secret")
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace read leaked: status = %d, want 404; body=%s",
			rr.Code, rr.Body.String())
	}
}

// ── ListWorkspaceRuns ───────────────────────────────────────────────────

// TestPipelineRuns_List_Empty_Returns200WithEmptyArray guards against
// the JSON-null-instead-of-[] bug that would break every UI list
// renderer. The handler emits {rows, count}; rows must be the JSON
// array form even when empty.
func TestPipelineRuns_List_Empty_Returns200WithEmptyArray(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Rows  []map[string]any `json:"rows"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
	if len(resp.Rows) != 0 {
		t.Errorf("rows len = %d, want 0", len(resp.Rows))
	}
	// Belt + suspenders: scan the raw payload for `"rows":null`. The
	// handler explicitly initialises with `make([]map[string]interface{}, 0)`,
	// so a `null` here would be a silent regression after a refactor.
	if strings.Contains(rr.Body.String(), `"rows":null`) {
		t.Errorf("empty rows serialised as null — UI expects []")
	}
}

// TestPipelineRuns_List_HidesOtherWorkspaces is the tenant-isolation
// check for the list endpoint. A row in workspace B must never appear
// in workspace A's feed.
func TestPipelineRuns_List_HidesOtherWorkspaces(t *testing.T) {
	h, db, userID, wsA := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsA, "pln_a", "ours")
	seedRunRow(t, db, wsA, "pln_a", "ours", "prn_ours", "completed")

	// Foreign workspace with its own pipeline + run.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	seedRunsPipeline(t, db, otherWS, "pln_b", "theirs")
	seedRunRow(t, db, otherWS, "pln_b", "theirs", "prn_theirs", "completed")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsA+"/pipeline-runs", nil),
		userID, wsA, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var resp struct {
		Rows []struct {
			ID string `json:"id"`
		} `json:"rows"`
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Fatalf("count = %d, want exactly 1 (own ws only); rows=%v", resp.Count, resp.Rows)
	}
	if resp.Rows[0].ID != "prn_ours" {
		t.Errorf("tenant leak: got %q, want prn_ours", resp.Rows[0].ID)
	}
}

// TestPipelineRuns_List_StatusFilter_RestrictsRows checks the explicit
// status=<value> filter path. Seeded two rows with different statuses;
// the filter must restrict to the matching one.
func TestPipelineRuns_List_StatusFilter_RestrictsRows(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "pln_a", "demo")
	seedRunRow(t, db, wsID, "pln_a", "demo", "prn_running", "running")
	seedRunRow(t, db, wsID, "pln_a", "demo", "prn_failed", "failed")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs?status=failed", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var resp struct {
		Rows []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"rows"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(resp.Rows))
	}
	if resp.Rows[0].Status != "failed" {
		t.Errorf("status filter leaked: got %q, want failed", resp.Rows[0].Status)
	}
}

// TestPipelineRuns_List_StatusActive_BundlesInProgressStatuses verifies
// the `status=active` shortcut documented inline as
// running+queued+paused. A row with status=completed must NOT appear.
func TestPipelineRuns_List_StatusActive_BundlesInProgressStatuses(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedRunsPipeline(t, db, wsID, "pln_a", "demo")
	seedRunRow(t, db, wsID, "pln_a", "demo", "prn_running", "running")
	seedRunRow(t, db, wsID, "pln_a", "demo", "prn_queued", "queued")
	seedRunRow(t, db, wsID, "pln_a", "demo", "prn_paused", "paused")
	seedRunRow(t, db, wsID, "pln_a", "demo", "prn_done", "completed")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs?status=active", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWorkspaceRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 3 {
		t.Errorf("active count = %d, want 3 (running+queued+paused, excluding completed)", resp.Count)
	}
}

// ── ListActiveRuns ──────────────────────────────────────────────────────

// TestPipelineRuns_ListActiveRuns_NoRegistry_Returns200WithEmptyArray
// confirms graceful degradation: when SetRunRegistry hasn't been wired
// the endpoint must NOT 503 — the UI degrades by showing an empty list,
// not an error banner.
func TestPipelineRuns_ListActiveRuns_NoRegistry_Returns200WithEmptyArray(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipelines/runs/active", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListActiveRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := strings.TrimSpace(rr.Body.String())
	if body == "null" {
		t.Errorf("no-registry list serialised as null — UI expects []")
	}
	// Should decode as an array, never an object/error.
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode array: %v; body=%s", err, body)
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0", len(out))
	}
}

// TestPipelineRuns_ListActiveRuns_RegistryEmpty_Returns200WithEmptyArray
// wires a real (but empty) RunRegistry. Same contract as the no-registry
// path: 200 + [], so the UI never has to special-case.
func TestPipelineRuns_ListActiveRuns_RegistryEmpty_Returns200WithEmptyArray(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	h.SetRunRegistry(pipeline.NewRunRegistry())

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipelines/runs/active", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListActiveRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0", len(out))
	}
}

// ── CancelRun ───────────────────────────────────────────────────────────

// TestPipelineRuns_CancelRun_NoRegistry_Returns503 — without a wired
// registry the cancel surface MUST loudly signal unavailability rather
// than nil-deref. 503 is the documented contract.
func TestPipelineRuns_CancelRun_NoRegistry_Returns503(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/prn_1/cancel", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_1")
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// TestPipelineRuns_CancelRun_MissingRunID_Returns400 — even with a
// wired registry, the empty path value must short-circuit to 400 before
// the workspace scope scan.
func TestPipelineRuns_CancelRun_MissingRunID_Returns400(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	h.SetRunRegistry(pipeline.NewRunRegistry())

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs//cancel", nil),
		userID, wsID, "OWNER",
	)
	// No SetPathValue → runId is empty.
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineRuns_CancelRun_UnknownRun_Returns404 — when the runID is
// not in this workspace's Active() snapshot the handler returns 404.
// This guards both "never started" and "cross-workspace runID guess"
// scenarios identically.
func TestPipelineRuns_CancelRun_UnknownRun_Returns404(t *testing.T) {
	h, _, userID, wsID := runsHandlerRig(t)
	h.SetRunRegistry(pipeline.NewRunRegistry())

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipelines/runs/prn_nope/cancel", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("runId", "prn_nope")
	rr := httptest.NewRecorder()
	h.CancelRun(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
