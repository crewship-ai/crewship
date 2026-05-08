package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"

	_ "modernc.org/sqlite"
)

// pipelineSmokeSchema is the minimum subset of the v78 schema the
// HTTP handlers need to operate. We mirror it here rather than
// running the full migrate package — keeps the HTTP test fast and
// independent of unrelated migrations.
const pipelineSmokeSchema = `
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    execution_tiers_json TEXT NOT NULL DEFAULT '{}'
);
INSERT INTO workspaces (id) VALUES ('ws_smoke');

CREATE TABLE users (id TEXT PRIMARY KEY);
CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
INSERT INTO crews (id, workspace_id) VALUES ('crew_a', 'ws_smoke'), ('crew_b', 'ws_smoke');
CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT NOT NULL);
INSERT INTO agents (id, crew_id) VALUES ('agent_lead', 'crew_a');

CREATE TABLE pipelines (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug                     TEXT NOT NULL,
    name                     TEXT NOT NULL,
    description              TEXT,
    dsl_version              TEXT NOT NULL DEFAULT '1.0',
    definition_json          TEXT NOT NULL,
    definition_hash          TEXT NOT NULL,
    ephemeral                INTEGER NOT NULL DEFAULT 0,
    workspace_visible        INTEGER NOT NULL DEFAULT 1,
    invocation_count         INTEGER NOT NULL DEFAULT 0,
    last_invoked_at          TEXT,
    last_invocation_status   TEXT,
    author_crew_id           TEXT REFERENCES crews(id) ON DELETE SET NULL,
    author_agent_id          TEXT REFERENCES agents(id) ON DELETE SET NULL,
    author_user_id           TEXT REFERENCES users(id) ON DELETE SET NULL,
    author_chat_id           TEXT,
    author_run_id            TEXT,
    authored_via             TEXT NOT NULL DEFAULT 'agent_tool_call',
    imported_from_url        TEXT,
    last_test_run_at         TEXT,
    last_test_run_passed     INTEGER NOT NULL DEFAULT 0,
    execution_tier_json      TEXT,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT,
    UNIQUE (workspace_id, slug)
);

CREATE TABLE pipeline_runs (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pipeline_id         TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    pipeline_slug       TEXT NOT NULL,
    pipeline_version    INTEGER,
    status              TEXT NOT NULL,
    mode                TEXT NOT NULL DEFAULT 'run',
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    current_step_id     TEXT,
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',
    output              TEXT,
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT,
    failed_at_step      TEXT,
    error_fingerprint   TEXT,
    invoking_crew_id    TEXT,
    invoking_agent_id   TEXT,
    invoking_user_id    TEXT,
    triggered_via       TEXT NOT NULL DEFAULT 'manual',
    triggered_by_id     TEXT,
    idempotency_key     TEXT,
    inputs_json         TEXT NOT NULL DEFAULT '{}',
    concurrency_key     TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    priority TEXT NOT NULL DEFAULT 'normal',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);
`

func openSmokeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// :memory: is per-connection — pin the pool to one connection so
	// schemata stays visible across queries.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), pipelineSmokeSchema); err != nil {
		_ = db.Close()
		t.Fatalf("schema: %v", err)
	}
	// Seed every authenticated user the suite uses through withAuthCtx
	// so author_user_id can satisfy the FK in pipelines / pipeline_runs.
	// Without this, the happy-path tests only work because SQLite skips
	// the FK enforcement; turning foreign_keys=ON would silently flip
	// these to "constraint failed".
	for _, uid := range []string{"user_1", "user_42", "admin_user", "u"} {
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO users(id) VALUES(?)`, uid); err != nil {
			_ = db.Close()
			t.Fatalf("seed user %q: %v", uid, err)
		}
	}
	return db
}

// stubRunner implements pipeline.AgentRunner deterministically. The
// HTTP smoke tests don't care about real LLM output — they only
// need to verify the handler correctly forwards inputs to the
// runner and returns the runner's output as JSON.
type stubRunner struct {
	output string
	calls  int
}

func (s *stubRunner) RunStep(_ context.Context, _ pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	s.calls++
	return pipeline.AgentStepResult{Output: s.output, CostUSD: 0.001, DurationMs: 5}, nil
}

// withWorkspaceCtx injects the workspace_id into the request context
// so handlers calling WorkspaceIDFromContext find it. Mirrors the
// authMw.RequireWorkspace middleware used in production.
func withWorkspaceCtx(req *http.Request, workspaceID string) *http.Request {
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, workspaceID)
	return req.WithContext(ctx)
}

// seedSmokePipeline inserts a single passable pipeline directly so
// list/get/run tests don't need to round-trip through Save (which
// would require a wired runner for the test_run gate).
func seedSmokePipeline(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	def := `{"name":"` + slug + `","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES (?, 'ws_smoke', ?, ?, ?, ?, 0, 1, 'crew_a', 'agent_lead', 'agent_tool_call', ?, 1, ?, ?)`,
		"pln_test_"+slug, slug, slug, def, "hash_"+slug, now, now, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestPipelinesAPI_List_HappyPath(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "first")
	seedSmokePipeline(t, db, "second")

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/workspaces/ws_smoke/pipelines", nil), "ws_smoke")
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	var out []pipelineResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("count: got %d, want 2", len(out))
	}
	for _, p := range out {
		if p.Definition != nil {
			t.Errorf("list response should NOT include definition (slug=%s)", p.Slug)
		}
		if p.AuthorCrewID != "crew_a" {
			t.Errorf("author: got %q, want crew_a", p.AuthorCrewID)
		}
	}
}

func TestPipelinesAPI_Get_IncludesDefinition(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/workspaces/ws_smoke/pipelines/demo", nil), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d", w.Code)
	}
	var out pipelineResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Slug != "demo" {
		t.Errorf("slug: got %q", out.Slug)
	}
	if len(out.Definition) == 0 {
		t.Error("get response should include definition")
	}
}

func TestPipelinesAPI_Get_NotFound(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "ghost")
	w := httptest.NewRecorder()
	h.Get(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestPipelinesAPI_Run_Returns503WithoutRunner(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.Run(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (runner not wired), got %d", w.Code)
	}
}

func TestPipelinesAPI_Run_HappyPath(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	runner := &stubRunner{output: "hello from stub"}
	h := NewPipelineHandler(db, slog.Default(), runner, nil)

	body := bytes.NewReader([]byte(`{"inputs":{}}`))
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", body), "ws_smoke")
	req.SetPathValue("slug", "demo")
	req.Header.Set("X-Crewship-Invoking-Crew", "crew_b")
	req.ContentLength = int64(body.Len())
	w := httptest.NewRecorder()
	h.Run(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var res pipeline.RunResult
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: got %q (err=%q)", res.Status, res.ErrorMessage)
	}
	if res.Output != "hello from stub" {
		t.Errorf("output: got %q", res.Output)
	}
	if runner.calls != 1 {
		t.Errorf("runner calls: got %d", runner.calls)
	}
}

func TestPipelinesAPI_DryRun_NoRunnerInvocation(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	runner := &stubRunner{output: "should-not-be-seen"}
	h := NewPipelineHandler(db, slog.Default(), runner, nil)

	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(`{"inputs":{}}`)), "ws_smoke")
	req.SetPathValue("slug", "demo")
	req.ContentLength = int64(len(`{"inputs":{}}`))
	w := httptest.NewRecorder()
	h.DryRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if runner.calls != 0 {
		t.Errorf("dry_run should NOT invoke runner; got %d calls", runner.calls)
	}
}

func TestPipelinesAPI_Delete_SoftDeletes(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "doomed")
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	req := withWorkspaceCtx(httptest.NewRequest("DELETE", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "doomed")
	w := httptest.NewRecorder()
	h.Delete(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
	// After delete, list should be empty.
	listReq := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), "ws_smoke")
	listW := httptest.NewRecorder()
	h.List(listW, listReq)
	body, _ := io.ReadAll(listW.Body)
	if !bytes.Contains(body, []byte("[]")) {
		t.Errorf("list after delete should be empty array, got %s", body)
	}
}

func TestPipelinesAPI_TestRun_RejectsBadDSL(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	runner := &stubRunner{output: "x"}
	h := NewPipelineHandler(db, slog.Default(), runner, nil)

	body := []byte(`{"definition":{"name":"BAD NAME WITH SPACES","steps":[]},"author_crew_id":"crew_a"}`)
	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), "ws_smoke")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	h.TestRun(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for invalid DSL, got %d body=%s", w.Code, w.Body.String())
	}
	if runner.calls != 0 {
		t.Errorf("invalid DSL should not reach runner; got %d calls", runner.calls)
	}
}

// withAuthCtx fills user + role into the request context so handlers
// hitting RoleFromContext / UserFromContext find what authedMw would
// have placed there in production. Routine save tests need this since
// the handler enforces RBAC + uses user.ID for author_user_id.
func withAuthCtx(req *http.Request, userID, role string) *http.Request {
	ctx := req.Context()
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	ctx = context.WithValue(ctx, ctxRole, role)
	return req.WithContext(ctx)
}

func TestPipelinesAPI_Save_RequiresManagerRole(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := []byte(`{
		"slug":"new-routine","name":"new","description":"x",
		"definition":{"name":"new-routine","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]},
		"last_test_run_passed":true,
		"last_test_run_at":"` + now + `"}`)

	for _, role := range []string{"VIEWER", "MEMBER"} {
		req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), "ws_smoke")
		req = withAuthCtx(req, "user_1", role)
		req.ContentLength = int64(len(body))
		w := httptest.NewRecorder()
		h.Save(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("role %q: expected 403, got %d (%s)", role, w.Code, w.Body.String())
		}
	}
}

func TestPipelinesAPI_Save_HappyPathManager(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := []byte(`{
		"slug":"manager-saved","name":"by manager","description":"...",
		"definition":{"name":"manager-saved","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]},
		"author_crew_id":"crew_a",
		"last_test_run_passed":true,
		"last_test_run_at":"` + now + `"}`)

	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), "ws_smoke")
	req = withAuthCtx(req, "user_42", "MANAGER")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	h.Save(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var out pipelineResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Slug != "manager-saved" {
		t.Errorf("slug: got %q", out.Slug)
	}
	// Authorship recorded as user-API path
	if out.AuthorUserID != "user_42" {
		t.Errorf("author_user_id: got %q, want user_42", out.AuthorUserID)
	}
	if out.AuthoredVia != "user_api" {
		t.Errorf("authored_via: got %q, want user_api", out.AuthoredVia)
	}
}

func TestPipelinesAPI_Save_SkipTestGateForbiddenForManager(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)

	body := []byte(`{
		"slug":"skip-attempt","name":"x","description":"y",
		"definition":{"name":"skip-attempt","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]},
		"author_crew_id":"crew_a",
		"skip_test_gate":true}`)

	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), "ws_smoke")
	req = withAuthCtx(req, "user_42", "MANAGER")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	h.Save(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("MANAGER + skip_test_gate must be 403, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "OWNER or ADMIN role") {
		t.Errorf("expected role-required message, got %s", w.Body.String())
	}
}

func TestPipelinesAPI_Save_SkipTestGateAllowedForAdmin(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)

	body := []byte(`{
		"slug":"admin-skip","name":"x","description":"y",
		"definition":{"name":"admin-skip","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]},
		"author_crew_id":"crew_a",
		"skip_test_gate":true}`)

	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), "ws_smoke")
	req = withAuthCtx(req, "admin_user", "ADMIN")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	h.Save(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("ADMIN + skip_test_gate should succeed, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestPipelinesAPI_Save_RejectsBadDSL(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	h := NewPipelineHandler(db, slog.Default(), nil, nil)

	body := []byte(`{
		"slug":"bad","name":"x","description":"",
		"definition":{"name":"bad","steps":[{"id":"a","type":"unsupported_type"}]}}`)

	req := withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(body)), "ws_smoke")
	req = withAuthCtx(req, "u", "ADMIN")
	req.ContentLength = int64(len(body))
	w := httptest.NewRecorder()
	h.Save(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for unsupported step type, got %d", w.Code)
	}
}

// ListRunRecords tests — the v83 column-typed projection endpoint.

func TestPipelinesAPI_ListRunRecords_503WithoutStore(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	// Note: SetRunStore NOT called — the handler should 503.
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "demo")
	w := httptest.NewRecorder()
	h.ListRunRecords(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (runStore not wired), got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "runs (journal-backed)") {
		t.Errorf("expected fallback hint pointing at /runs, got body: %s", w.Body.String())
	}
}

func TestPipelinesAPI_ListRunRecords_HappyPath(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	store := pipeline.NewRunStore(db)
	now := time.Now().UTC()
	// Seed three runs: one running, one completed, one failed.
	for _, r := range []*pipeline.RunRecord{
		{ID: "run_a", WorkspaceID: "ws_smoke", PipelineID: "pln_test_demo", PipelineSlug: "demo",
			Status: pipeline.RunStatusRunning, StartedAt: now.Add(-1 * time.Minute)},
		{ID: "run_b", WorkspaceID: "ws_smoke", PipelineID: "pln_test_demo", PipelineSlug: "demo",
			Status: pipeline.RunStatusCompleted, StartedAt: now.Add(-2 * time.Minute), Output: "ok"},
		{ID: "run_c", WorkspaceID: "ws_smoke", PipelineID: "pln_test_demo", PipelineSlug: "demo",
			Status: pipeline.RunStatusFailed, StartedAt: now.Add(-3 * time.Minute), ErrorMessage: "boom"},
	} {
		if err := store.Insert(context.Background(), r); err != nil {
			t.Fatalf("seed run %s: %v", r.ID, err)
		}
	}

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	h.SetRunStore(store)
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "demo")
	rec := httptest.NewRecorder()
	h.ListRunRecords(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 records, got %d", len(out))
	}
	// Newest first.
	wantOrder := []string{"run_a", "run_b", "run_c"}
	for i, w := range wantOrder {
		if out[i]["id"] != w {
			t.Errorf("[%d] expected %s, got %v", i, w, out[i]["id"])
		}
	}
	// Status enum is column-typed so values are exact.
	statuses := []string{}
	for _, r := range out {
		statuses = append(statuses, r["status"].(string))
	}
	wantStatuses := []string{"running", "completed", "failed"}
	for i, ws := range wantStatuses {
		if statuses[i] != ws {
			t.Errorf("status[%d]: got %q want %q", i, statuses[i], ws)
		}
	}
}

func TestPipelinesAPI_ListRunRecords_FilterByStatus(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedSmokePipeline(t, db, "demo")
	store := pipeline.NewRunStore(db)
	now := time.Now().UTC()
	for _, r := range []*pipeline.RunRecord{
		{ID: "r1", WorkspaceID: "ws_smoke", PipelineID: "pln_test_demo", PipelineSlug: "demo",
			Status: pipeline.RunStatusRunning, StartedAt: now},
		{ID: "r2", WorkspaceID: "ws_smoke", PipelineID: "pln_test_demo", PipelineSlug: "demo",
			Status: pipeline.RunStatusCompleted, StartedAt: now},
		{ID: "r3", WorkspaceID: "ws_smoke", PipelineID: "pln_test_demo", PipelineSlug: "demo",
			Status: pipeline.RunStatusCompleted, StartedAt: now},
	} {
		if err := store.Insert(context.Background(), r); err != nil {
			t.Fatalf("seed run %s: %v", r.ID, err)
		}
	}
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	h.SetRunStore(store)
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x?status=completed", nil), "ws_smoke")
	req.SetPathValue("slug", "demo")
	// httptest.NewRequest doesn't parse the URL query string by default
	// for our path; explicitly set RawQuery so r.URL.Query() works in
	// the handler.
	req.URL.RawQuery = "status=completed"
	rec := httptest.NewRecorder()
	h.ListRunRecords(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var out []map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 2 {
		t.Errorf("expected 2 completed runs, got %d", len(out))
	}
	for _, r := range out {
		if r["status"] != "completed" {
			t.Errorf("filter leak: got status=%v", r["status"])
		}
	}
}

func TestPipelinesAPI_ListRunRecords_NotFoundOnUnknownSlug(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	store := pipeline.NewRunStore(db)
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	h.SetRunStore(store)
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), "ws_smoke")
	req.SetPathValue("slug", "ghost")
	w := httptest.NewRecorder()
	h.ListRunRecords(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
