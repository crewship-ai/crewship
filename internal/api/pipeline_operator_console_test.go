package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Routines operator console (#2560), WP-A: additive fields on existing
// responses — `draft` on list + detail, `files` on detail, `failure` on run
// detail, `effective_version` / `version_pinned` on schedule rows.

func decodeJSONMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

func decodeJSONList(t *testing.T, body string) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return l
}

// ── draft ───────────────────────────────────────────────────────────────

func TestPipelineDraft_OnListAndDetail(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedPipelineRowDef(t, h.db, ws, "pl_drafted", "drafted", agentlessProbeDef)
	seedPipelineRowDef(t, h.db, ws, "pl_plain", "plain", agentlessProbeDef)
	execOrFatal(t, h.db, `INSERT INTO pipeline_drafts (id, workspace_id, slug, revision, base_pipeline_id, base_revision, document_json, updated_by, created_at, updated_at)
		VALUES ('drf_1', ?, 'drafted', 2, 'pl_drafted', 1, '{"slug":"drafted"}', 'usr_editor', '2026-09-15T10:00:00Z', '2026-09-15T10:31:00Z')`, ws)
	// A draft for the same slug in another workspace must not leak in.
	execOrFatal(t, h.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_draft_other', 'Other', 'ws-draft-other')`)
	execOrFatal(t, h.db, `INSERT INTO pipeline_drafts (id, workspace_id, slug, revision, base_pipeline_id, base_revision, document_json, updated_by, created_at, updated_at)
		VALUES ('drf_foreign', 'ws_draft_other', 'plain', 9, '', 0, '{"slug":"plain"}', 'usr_x', '2026-09-15T10:00:00Z', '2026-09-15T10:00:00Z')`)

	wantDraft := map[string]any{"id": "drf_1", "revision": float64(2), "updated_at": "2026-09-15T10:31:00Z", "updated_by": "usr_editor"}

	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), ws), user, "MANAGER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	bySlug := map[string]map[string]any{}
	for _, row := range decodeJSONList(t, rr.Body.String()) {
		bySlug[row["slug"].(string)] = row
	}
	if got := bySlug["drafted"]["draft"]; !reflect.DeepEqual(got, wantDraft) {
		t.Errorf("list draft = %#v, want %#v", got, wantDraft)
	}
	if _, ok := bySlug["plain"]["draft"]; ok {
		t.Errorf("list: plain routine must carry no draft key: %#v", bySlug["plain"])
	}

	for slug, want := range map[string]map[string]any{"drafted": wantDraft, "plain": nil} {
		req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), ws), user, "MANAGER")
		req.SetPathValue("slug", slug)
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != 200 {
			t.Fatalf("get %s: %d %s", slug, rr.Code, rr.Body)
		}
		got := decodeJSONMap(t, rr.Body.String())
		if want == nil {
			if _, ok := got["draft"]; ok {
				t.Errorf("get %s: unexpected draft %#v", slug, got["draft"])
			}
			continue
		}
		if !reflect.DeepEqual(got["draft"], want) {
			t.Errorf("get %s: draft = %#v, want %#v", slug, got["draft"], want)
		}
	}
}

// ── files ───────────────────────────────────────────────────────────────

type fakeCrewFiles struct {
	files     []provider.FileInfo
	content   map[string][]byte
	listErr   error
	readErr   error
	listCalls int
	readCalls []string
}

func (f *fakeCrewFiles) ListShared(_ context.Context, _ string) ([]provider.FileInfo, error) {
	f.listCalls++
	return f.files, f.listErr
}

func (f *fakeCrewFiles) ReadShared(_ context.Context, _ string, rel string, limit int64) ([]byte, error) {
	f.readCalls = append(f.readCalls, rel)
	if f.readErr != nil {
		return nil, f.readErr
	}
	b := f.content[rel]
	if int64(len(b)) > limit {
		b = b[:limit]
	}
	return b, nil
}

const filesRoutineDef = `{"name":"ledger","steps":[
	{"id":"post","type":"script","script":{"path":"scripts/ledger-post.go","args":["--rules","/crew/shared/config/rules.yaml"]}},
	{"id":"cleanup","type":"script","script":{"path":"scripts/cleanup.py"}}]}`

func seedFilesRoutine(t *testing.T, h *PipelineHandler, ws, id, slug, def string) {
	t.Helper()
	seedCrewRow(t, h.db, "crew_"+id, ws, "Finance", "finance-"+id)
	seedPipelineRowDef(t, h.db, ws, id, slug, def)
	execOrFatal(t, h.db, `UPDATE pipelines SET author_crew_id = ? WHERE id = ?`, "crew_"+id, id)
}

func getRoutineFiles(t *testing.T, h *PipelineHandler, user, ws, slug string) []map[string]any {
	t.Helper()
	req := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("GET", "/x", nil), ws), user, "MANAGER")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != 200 {
		t.Fatalf("get: %d %s", rr.Code, rr.Body)
	}
	got := decodeJSONMap(t, rr.Body.String())
	raw, ok := got["files"]
	if !ok {
		t.Fatalf("detail has no files member: %s", rr.Body)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("files is %T, want array (never null): %s", raw, rr.Body)
	}
	out := make([]map[string]any, 0, len(list))
	for _, f := range list {
		out = append(out, f.(map[string]any))
	}
	return out
}

func TestPipelineFiles_OnDetail(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedFilesRoutine(t, h, ws, "pl_files", "ledger", filesRoutineDef)
	mod := time.Date(2026, 9, 15, 9, 30, 0, 0, time.UTC)
	fake := &fakeCrewFiles{
		files: []provider.FileInfo{
			{Path: "crews/crew_pl_files/shared/scripts", Name: "scripts", IsDir: true},
			{Path: "crews/crew_pl_files/shared/scripts/ledger-post.go", Name: "ledger-post.go", Size: 4120, ModTime: mod},
			{Path: "crews/crew_pl_files/shared/config/rules.yaml", Name: "rules.yaml", Size: 88, ModTime: mod},
		},
		content: map[string][]byte{
			"scripts/ledger-post.go": []byte("// Posts one invoice to the ERP ledger\n// and retries once.\npackage main\n"),
			"config/rules.yaml":      []byte("# Matching rules\nrules: []\n"),
		},
	}
	h.SetCrewFileReader(fake)

	files := getRoutineFiles(t, h, user, ws, "ledger")
	if len(files) != 3 {
		t.Fatalf("files = %d rows, want 3: %#v", len(files), files)
	}
	want := []map[string]any{
		{"path": "scripts/ledger-post.go", "language": "go", "interpreter": "go run", "step_ids": []any{"post"},
			"description": "Posts one invoice to the ERP ledger and retries once.", "size_bytes": float64(4120), "updated_at": "2026-09-15T09:30:00Z", "present": true, "status": "present"},
		{"path": "scripts/cleanup.py", "language": "py", "interpreter": "python3", "step_ids": []any{"cleanup"},
			"description": "", "present": false, "status": "missing"},
		{"path": "config/rules.yaml", "language": "yaml", "interpreter": "", "step_ids": []any{"post"},
			"description": "Matching rules", "size_bytes": float64(88), "updated_at": "2026-09-15T09:30:00Z", "present": true, "status": "present"},
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files mismatch\n got: %#v\nwant: %#v", files, want)
	}
	if fake.listCalls != 1 {
		t.Errorf("list calls = %d, want exactly one per detail request", fake.listCalls)
	}
	if !reflect.DeepEqual(fake.readCalls, []string{"scripts/ledger-post.go", "config/rules.yaml"}) {
		t.Errorf("read calls = %v; a missing file must not be read", fake.readCalls)
	}
}

func TestPipelineFiles_NeverFailTheDetail(t *testing.T) {
	cases := []struct {
		name   string
		reader crewFileReader
	}{
		{"listing fails", &fakeCrewFiles{listErr: errors.New("crewshipd down")}},
		{"reader not wired", nil},
		{"read fails after listing", &fakeCrewFiles{
			files:   []provider.FileInfo{{Path: "crews/crew_pl_nf/shared/scripts/ledger-post.go", Size: 1, ModTime: time.Now()}},
			readErr: errors.New("container unavailable"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, user, ws := newPipelineHandlerForCRUDTest(t)
			seedFilesRoutine(t, h, ws, "pl_nf", "ledger", filesRoutineDef)
			if tc.reader != nil {
				h.SetCrewFileReader(tc.reader)
			}
			files := getRoutineFiles(t, h, user, ws, "ledger")
			if len(files) != 3 {
				t.Fatalf("files = %#v", files)
			}
			for _, f := range files {
				if f["description"] != "" {
					t.Errorf("%s: description must be empty when unreadable: %q", f["path"], f["description"])
				}
				if _, ok := f["size_bytes"]; ok && f["present"] != true {
					t.Errorf("%s: size without presence: %#v", f["path"], f)
				}
			}
			if tc.name != "read fails after listing" && files[0]["present"] != false {
				t.Errorf("present = %v, want false: %#v", files[0]["present"], files[0])
			}
			// The distinction the Files card relies on: a share that could not
			// be listed leaves every row "unverified"; a listed share marks the
			// paths it lacks "missing" and the ones it has "present" even when
			// the header read failed afterwards.
			wantStatus := map[string]string{
				"listing fails":            "unverified",
				"reader not wired":         "unverified",
				"read fails after listing": "present",
			}[tc.name]
			if files[0]["status"] != wantStatus {
				t.Errorf("status = %v, want %q: %#v", files[0]["status"], wantStatus, files[0])
			}
			if tc.name == "read fails after listing" && files[1]["status"] != "missing" {
				t.Errorf("unlisted path status = %v, want missing: %#v", files[1]["status"], files[1])
			}
		})
	}
}

func TestPipelineFiles_EmptyWhenNothingDeclared(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedFilesRoutine(t, h, ws, "pl_none", "plain", agentlessProbeDef)
	fake := &fakeCrewFiles{}
	h.SetCrewFileReader(fake)
	if files := getRoutineFiles(t, h, user, ws, "plain"); len(files) != 0 {
		t.Fatalf("files = %#v, want []", files)
	}
	if fake.listCalls != 0 {
		t.Fatalf("a routine with no files must not list the share")
	}
}

func TestPipelineFiles_IOIsCapped(t *testing.T) {
	var steps []string
	var infos []provider.FileInfo
	for i := 0; i < pipelineFilesMaxIO+5; i++ {
		p := "scripts/s" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".sh"
		steps = append(steps, `{"id":"s`+string(rune('a'+i%26))+string(rune('a'+i/26))+`","type":"script","script":{"path":"`+p+`"}}`)
		infos = append(infos, provider.FileInfo{Path: "crews/crew_pl_cap/shared/" + p, Size: 1, ModTime: time.Now()})
	}
	def := `{"name":"many","steps":[` + strings.Join(steps, ",") + `]}`
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	seedFilesRoutine(t, h, ws, "pl_cap", "many", def)
	fake := &fakeCrewFiles{files: infos}
	h.SetCrewFileReader(fake)
	files := getRoutineFiles(t, h, user, ws, "many")
	if len(files) != pipelineFilesMaxIO+5 {
		t.Fatalf("rows = %d", len(files))
	}
	if len(fake.readCalls) != pipelineFilesMaxIO {
		t.Fatalf("read calls = %d, want the cap %d", len(fake.readCalls), pipelineFilesMaxIO)
	}
	if files[pipelineFilesMaxIO]["present"] != false || files[pipelineFilesMaxIO]["status"] != "unverified" {
		t.Fatalf("rows past the cap must stay present:false and unverified, got %#v", files[pipelineFilesMaxIO])
	}
}

// The production reader speaks crewshipd's IPC: the same list + download
// endpoints the Files panel and `routine export --scripts` go through.
func TestPipelineFiles_IPCReader(t *testing.T) {
	mod := time.Date(2026, 9, 15, 9, 30, 0, 0, time.UTC)
	big := strings.Repeat("y", 20000)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /crews/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "crew_a" || r.URL.Query().Get("recursive") != "true" || r.URL.Query().Get("subdir") != "shared" {
			http.Error(w, "unexpected query "+r.URL.String(), 400)
			return
		}
		writeJSON(w, 200, map[string]any{"crew_id": "crew_a", "files": []provider.FileInfo{{Path: "crews/crew_a/shared/scripts/x.py", Name: "x.py", Size: 7, ModTime: mod}}})
	})
	mux.HandleFunc("GET /crews/{id}/files/download", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("path") {
		case "shared/scripts/x.py":
			_, _ = w.Write([]byte(big))
		default:
			http.Error(w, "file not found", 404)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	reader := &ipcCrewFileReader{client: srv.Client(), base: srv.URL}

	files, err := reader.ListShared(context.Background(), "crew_a")
	if err != nil || len(files) != 1 || files[0].Path != "crews/crew_a/shared/scripts/x.py" || files[0].Size != 7 || !files[0].ModTime.Equal(mod) {
		t.Fatalf("list: %v %#v", err, files)
	}
	b, err := reader.ReadShared(context.Background(), "crew_a", "scripts/x.py", 8)
	if err != nil || string(b) != "yyyyyyyy" {
		t.Fatalf("read: %v %q (limit must bound the body)", err, b)
	}
	if _, err := reader.ReadShared(context.Background(), "crew_a", "scripts/missing.py", 8); err == nil {
		t.Fatal("a 404 must surface as an error, not empty content")
	}
	if _, err := reader.ListShared(context.Background(), "crew_b"); err == nil {
		t.Fatal("a non-200 listing must be an error")
	}
}

// ── failure ─────────────────────────────────────────────────────────────

const failureRunDef = `{"name":"invoices","steps":[
	{"id":"extract","type":"transform","transform":{"input":"{}","expression":"."}},
	{"id":"verify","name":"Check the extraction","type":"agent_run","agent_slug":"worker","prompt":"x",
	 "outcomes":{"grader_agent_slug":"checker","max_iterations":3,"criteria":[{"name":"total_equals_lines","rule":"totals match"}]}},
	{"id":"decide","type":"transform","transform":{"input":"{}","expression":"."}},
	{"id":"post","type":"transform","transform":{"input":"{}","expression":"."}},
	{"id":"notify","type":"transform","transform":{"input":"{}","expression":"."}}
]}`

func getRunDetail(t *testing.T, h *PipelineHandler, user, ws, runID string) map[string]any {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/pipeline-runs/"+runID, nil), user, ws, "OWNER")
	req.SetPathValue("runId", runID)
	rr := httptest.NewRecorder()
	h.GetRun(rr, req)
	if rr.Code != 200 {
		t.Fatalf("get run: %d %s", rr.Code, rr.Body)
	}
	return decodeJSONMap(t, rr.Body.String())
}

func TestRunFailure_OnRunDetail(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "pl_f", "invoices")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	msg := "step failed after exhausting tiers: outcomes failed: outcomes failed: total_equals_lines"

	seedRunRow(t, db, ws, "pl_f", "invoices", "prn_failed", "failed")
	execOrFatal(t, db, `UPDATE pipeline_runs SET error_message = ?, failed_at_step = 'verify', executed_definition_json = ? WHERE id = 'prn_failed'`, msg, failureRunDef)
	execOrFatal(t, db, `INSERT INTO pipeline_run_step_outputs (run_id, step_id, output, updated_at) VALUES ('prn_failed', 'extract', '{}', ?)`, now)
	execOrFatal(t, db, `INSERT INTO pipeline_step_executions (id, run_id, parent_execution_id, step_id, execution_path, attempt, kind, status, started_at)
		VALUES ('exec_1', 'prn_failed', NULL, 'extract', '/extract', 1, 'transform', 'completed', ?),
		       ('exec_2', 'prn_failed', NULL, 'verify', '/verify', 1, 'agent_run', 'failed', ?),
		       ('exec_3', 'prn_failed', 'exec_2', 'agent', '/verify/agent', 1, 'agent_attempt', 'failed', ?),
		       ('exec_4', 'prn_failed', 'exec_2', 'decide', '/verify/decide', 1, 'transform', 'completed', ?)`, now, now, now, now)

	got := getRunDetail(t, h, user, ws, "prn_failed")
	want := map[string]any{
		"kind": "checker_rejected", "step_id": "verify", "step_name": "Check the extraction",
		"summary":       "The checker rejected the result after exhausting the allowed model tiers.",
		"kept_step_ids": []any{"extract"}, "not_done_step_ids": []any{"decide", "post", "notify"},
	}
	if !reflect.DeepEqual(got["failure"], want) {
		t.Errorf("failure = %#v\nwant %#v", got["failure"], want)
	}
	if got["error_message"] != msg || got["failed_at_step"] != "verify" {
		t.Errorf("raw fields must stay unchanged: %v / %v", got["error_message"], got["failed_at_step"])
	}

	// A completed run carries no failure member at all.
	seedRunRow(t, db, ws, "pl_f", "invoices", "prn_ok", "completed")
	if got := getRunDetail(t, h, user, ws, "prn_ok"); got["failure"] != nil {
		t.Errorf("completed run: failure = %#v, want absent", got["failure"])
	}
	if _, ok := getRunDetail(t, h, user, ws, "prn_ok")["failure"]; ok {
		t.Errorf("completed run must not carry a failure key")
	}

	// Interrupted by a restart: no message, the current step is where it stopped.
	seedRunRow(t, db, ws, "pl_f", "invoices", "prn_int", "interrupted")
	execOrFatal(t, db, `UPDATE pipeline_runs SET current_step_id = 'post', executed_definition_json = ? WHERE id = 'prn_int'`, failureRunDef)
	f := getRunDetail(t, h, user, ws, "prn_int")["failure"].(map[string]any)
	if f["kind"] != "unknown" || f["step_id"] != "post" || f["summary"] != "The run was interrupted before it finished." {
		t.Errorf("interrupted: %#v", f)
	}
	if !reflect.DeepEqual(f["not_done_step_ids"], []any{"notify"}) {
		t.Errorf("interrupted not_done = %#v", f["not_done_step_ids"])
	}

	// Outcome FAILED on a run whose status is not `failed` still classifies.
	seedRunRow(t, db, ws, "pl_f", "invoices", "prn_outcome", "completed")
	execOrFatal(t, db, `UPDATE pipeline_runs SET outcome = 'FAILED', error_message = 'http step "post" got HTTP 502 (success codes: [200])', failed_at_step = 'post' WHERE id = 'prn_outcome'`)
	f = getRunDetail(t, h, user, ws, "prn_outcome")["failure"].(map[string]any)
	if f["kind"] != "http_status" || f["step_name"] != "post" {
		t.Errorf("outcome FAILED: %#v", f)
	}
	if kept, ok := f["kept_step_ids"].([]any); !ok || len(kept) != 0 {
		t.Errorf("no definition archived: kept must be [] not %#v", f["kept_step_ids"])
	}
}

// ── schedules ───────────────────────────────────────────────────────────

func TestScheduleEffectiveVersion_OnListAndCreate(t *testing.T) {
	h, db, user, ws := scheduleHandlerRig(t)
	seedPipelineRow(t, db, ws, "pl_s", "nightly")
	execOrFatal(t, db, `UPDATE pipelines SET head_version = 3 WHERE id = 'pl_s'`)
	seedPipelineRow(t, db, ws, "pl_gone", "gone")
	store := pipeline.NewScheduleStore(db)
	ctx := context.Background()
	pinned := 2
	for _, in := range []pipeline.SaveScheduleInput{
		{WorkspaceID: ws, Name: "latest", TargetPipelineID: "pl_s", CronExpr: "0 2 * * *", Timezone: "UTC", Enabled: true},
		{WorkspaceID: ws, Name: "pinned", TargetPipelineID: "pl_s", TargetPipelineVersion: &pinned, CronExpr: "0 3 * * *", Timezone: "UTC", Enabled: true},
		{WorkspaceID: ws, Name: "orphan", TargetPipelineID: "pl_gone", CronExpr: "0 4 * * *", Timezone: "UTC", Enabled: true},
	} {
		if _, err := store.Save(ctx, in); err != nil {
			t.Fatalf("save %s: %v", in.Name, err)
		}
	}
	execOrFatal(t, db, `UPDATE pipelines SET deleted_at = ? WHERE id = 'pl_gone'`, time.Now().UTC().Format(time.RFC3339))

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/pipeline-schedules", nil), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.ListSchedules(rr, req)
	if rr.Code != 200 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	byName := map[string]map[string]any{}
	for _, row := range decodeJSONList(t, rr.Body.String()) {
		byName[row["name"].(string)] = row
	}
	check := func(name string, wantEffective any, wantPinned bool) {
		t.Helper()
		row, ok := byName[name]
		if !ok {
			t.Fatalf("schedule %q missing from list", name)
		}
		eff, present := row["effective_version"]
		if !present {
			t.Errorf("%s: effective_version key must always be present", name)
		}
		if !reflect.DeepEqual(eff, wantEffective) {
			t.Errorf("%s: effective_version = %#v, want %#v", name, eff, wantEffective)
		}
		if row["version_pinned"] != wantPinned {
			t.Errorf("%s: version_pinned = %#v, want %v", name, row["version_pinned"], wantPinned)
		}
	}
	check("latest", float64(3), false)
	check("pinned", float64(2), true)
	check("orphan", nil, false)

	// Single-row responses carry the same fields.
	body := `{"target_pipeline_slug":"nightly","cron_expr":"*/5 * * * *"}`
	req = withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+ws+"/pipeline-schedules", strings.NewReader(body)), user, ws, "OWNER")
	rr = httptest.NewRecorder()
	h.CreateSchedule(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	created := decodeJSONMap(t, rr.Body.String())
	if created["effective_version"] != float64(3) || created["version_pinned"] != false {
		t.Errorf("create: %v / %v", created["effective_version"], created["version_pinned"])
	}
}

func TestRunFailure_RedactsLegacyDiagnostics(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "pl_secret", "secret-failure")
	seedRunRow(t, db, ws, "pl_secret", "secret-failure", "prn_secret", "failed")
	secrets := []string{"sk-proj-exampleSecret1234567890", "opaqueToken123456789", "example-password-123"}
	msg := "unknown script failure: " + secrets[0] + " Bearer " + secrets[1] + " PASSWORD=" + secrets[2]
	execOrFatal(t, db, `UPDATE pipeline_runs SET error_message = ? WHERE id = 'prn_secret'`, msg)
	got := getRunDetail(t, h, user, ws, "prn_secret")
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(data), secret) {
			t.Errorf("API leaked diagnostic: %s", data)
		}
	}
	f := got["failure"].(map[string]any)
	if f["summary"] != "The run did not finish." {
		t.Errorf("summary: %v", f)
	}
}

func TestRunLists_RedactLegacyDiagnostics(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	h.SetRunStore(pipeline.NewRunStore(db))
	seedRunsPipeline(t, db, ws, "pl_legacy_secret", "legacy-secret")
	seedRunRow(t, db, ws, "pl_legacy_secret", "legacy-secret", "prn_legacy_secret", "failed")
	testPEM := func(kind string) string {
		border := strings.Repeat("-", 5)
		return border + "BEGIN " + kind + border + "\nexample-key-material\n" + border + "END " + kind + border
	}
	// Cover the shared scrubber's credential families at both HTTP boundaries,
	// including multiline keys and a token crossing the list's length cap.
	cases := []string{
		"sk-ant-exampleSecret1234567890",
		"sk-or-exampleSecret12345678901234567890",
		"sk-proj-exampleSecret1234567890",
		"sk-svcacct-exampleSecret1234567890",
		"sk-exampleSecret12345678901234567890",
		"AIzaSy" + strings.Repeat("a", 33),
		"cur_" + strings.Repeat("a", 24),
		"fact_" + strings.Repeat("a", 24),
		"factory_" + strings.Repeat("a", 24),
		"xai-" + strings.Repeat("a", 24),
		"gsk_" + strings.Repeat("a", 24),
		"ghp_exampleSecret1234567890",
		"gho_exampleSecret1234567890",
		"ghs_exampleSecret1234567890",
		"ghr_exampleSecret1234567890",
		"github_pat_exampleSecret1234567890",
		"glpat-" + strings.Repeat("a", 24),
		"xoxb-" + strings.Repeat("a", 24),
		"AKIA1234567890ABCDEF",
		"Bearer exampleSecret1234567890",
		`{"password":"` + strings.Repeat("p", 24) + `"}`, "PASSWORD=" + strings.Repeat("p", 24),
		testPEM("PRIVATE KEY"),
		testPEM("OPENSSH PRIVATE KEY"),
		strings.Repeat("x", 180) + "sk-proj-exampleSecret123456789012345678901234567890",
	}
	for i, diagnostic := range cases {
		execOrFatal(t, db, `UPDATE pipeline_runs SET error_message = ? WHERE id = 'prn_legacy_secret'`, diagnostic)
		for _, endpoint := range []string{"workspace", "routine"} {
			req := withWorkspaceUser(httptest.NewRequest("GET", "/x?limit=50", nil), user, ws, "OWNER")
			req.SetPathValue("slug", "legacy-secret")
			rr := httptest.NewRecorder()
			if endpoint == "workspace" {
				h.ListWorkspaceRuns(rr, req)
			} else {
				h.ListRunRecords(rr, req)
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("%s status %d: %s", endpoint, rr.Code, rr.Body)
			}
			body := rr.Body.String()
			if !strings.Contains(body, "prn_legacy_secret") || !strings.Contains(body, "[REDACTED") {
				t.Errorf("case %d %s did not return redacted run: %s", i, endpoint, body)
			}
			if strings.Contains(body, "exampleSecret") || strings.Contains(body, "example-password") || strings.Contains(body, "example-key-material") {
				t.Errorf("case %d %s leaked credential text: %s", i, endpoint, body)
			}
		}
	}
}
