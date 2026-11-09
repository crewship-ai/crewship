package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// producerRunRequest drives the run handlers for one Page as one user in one workspace.
func producerRunRequest(t *testing.T, h *PageHandler, ws, user, slug, op, collection, run, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/pages/" + slug + "/artifact/data-runs" + query
	method := "GET"
	switch op {
	case "start":
		path, method = "/api/v1/pages/"+slug+"/artifact/data/"+collection+"/runs", "POST"
	case "finish":
		path, method = "/api/v1/pages/"+slug+"/artifact/data/"+collection+"/runs/"+run, "POST"
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	token := "crewship_cli_producer_" + user
	mustExec(t, h.db, `INSERT OR IGNORE INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,?, ?,datetime('now'))`, "producer-token-"+user, user, "producer", sha256Hex(token))
	req.Header.Set("Authorization", "Bearer "+token)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	req = req.WithContext(ctx)
	req.SetPathValue("slug", slug)
	req.SetPathValue("collection", collection)
	req.SetPathValue("run", run)
	w := httptest.NewRecorder()
	switch op {
	case "start":
		h.StartArtifactDataRun(w, req)
	case "finish":
		h.FinishArtifactDataRun(w, req)
	default:
		h.ListArtifactDataRuns(w, req)
	}
	return w
}

type producerRunFixture struct {
	h      *PageHandler
	ws     string
	owner  string
	reader string
	page   string
	call   func(string, string, string, string) *httptest.ResponseRecorder
}

func newProducerRunFixture(t *testing.T) producerRunFixture {
	t.Helper()
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales", "costs"), "{")); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatalf("share: %d %s", w.Code, w.Body)
	}
	f := producerRunFixture{h: h, ws: ws, owner: owner, reader: "producer-reader", call: call}
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'producer-reader@example.test')`, f.reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, f.reader)
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&f.page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, f.page, f.reader, owner)
	return f
}

func (f producerRunFixture) do(t *testing.T, user, op, collection, run, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	return producerRunRequest(t, f.h, f.ws, user, "three-screens", op, collection, run, query, body)
}

func decodeRun(t *testing.T, w *httptest.ResponseRecorder) artifactDataRunResponse {
	t.Helper()
	var run artifactDataRunResponse
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v %s", err, w.Body)
	}
	return run
}

func TestDashboardProducerRunLifecycleAndIdempotentReplay(t *testing.T) {
	f := newProducerRunFixture(t)
	start := `{"base_version":1,"idempotency_key":"nightly-2026-11-09"}`
	w := f.do(t, f.owner, "start", "sales", "", "", start)
	if w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	run := decodeRun(t, w)
	if run.State != "running" || run.StartedBy != "You" || !strings.HasPrefix(run.ID, "run_") || strings.Contains(w.Body.String(), f.owner) {
		t.Fatalf("started run: %s", w.Body)
	}
	replay := f.do(t, f.owner, "start", "sales", "", "", start)
	if replay.Code != 200 || decodeRun(t, replay).ID != run.ID {
		t.Fatalf("replayed start: %d %s", replay.Code, replay.Body)
	}
	w = f.do(t, f.owner, "list", "", "", "?version=1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"running"`) || strings.Contains(w.Body.String(), "last_success_at") {
		t.Fatalf("running summary: %d %s", w.Code, w.Body)
	}
	finish := `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`
	if w := f.do(t, f.owner, "finish", "sales", run.ID, "", finish); w.Code != 200 || decodeRun(t, w).FinishedAt == "" {
		t.Fatalf("finish: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", run.ID, "", finish); w.Code != 200 {
		t.Fatalf("replayed finish: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", run.ID, "", `{"base_version":1,"state":"failed","reason":"late"}`); w.Code != 409 {
		t.Fatalf("conflicting finish: %d %s", w.Code, w.Body)
	}
	var summary artifactDataRunsResponse
	w = f.do(t, f.owner, "list", "", "", "?version=1", "")
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil || len(summary.Runs) != 1 || summary.Runs[0].LastSuccessAt == "" || summary.Runs[0].Latest.State != "succeeded" {
		t.Fatalf("succeeded summary: %v %s", err, w.Body)
	}
	var codeVersion, dataRows int64
	if err := f.h.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE workspace_id=?`, f.ws).Scan(&codeVersion); err != nil || codeVersion != 1 {
		t.Fatalf("run changed code version: %d %v", codeVersion, err)
	}
	if err := f.h.db.QueryRow(`SELECT count(*) FROM artifact_data_documents WHERE workspace_id=?`, f.ws).Scan(&dataRows); err != nil || dataRows != 0 {
		t.Fatalf("run wrote data: %d %v", dataRows, err)
	}
}

func TestDashboardProducerRunFailureKeepsLastSuccessAndHidesReasonFromReaders(t *testing.T) {
	f := newProducerRunFixture(t)
	first := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"one"}`))
	if w := f.do(t, f.owner, "finish", "sales", first.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 200 {
		t.Fatalf("first finish: %d %s", w.Code, w.Body)
	}
	second := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"two"}`))
	reason := "upstream 502 from https://bot:hunter2@warehouse.example/api Authorization: Bearer abc.def token=crewship_cli_secretvalue\x07"
	body, _ := json.Marshal(map[string]any{"base_version": 1, "state": "failed", "reason": reason})
	w := f.do(t, f.owner, "finish", "sales", second.ID, "", string(body))
	failed := decodeRun(t, w)
	if w.Code != 200 || failed.State != "failed" || !strings.Contains(failed.Reason, "upstream 502") {
		t.Fatalf("failed finish: %d %s", w.Code, w.Body)
	}
	for _, secret := range []string{"hunter2", "abc.def", "secretvalue", "\x07"} {
		if strings.Contains(failed.Reason, secret) {
			t.Fatalf("reason kept %q: %q", secret, failed.Reason)
		}
	}
	// A workspace ADMIN without a grant reads through the audited administrator
	// read, which never makes them an owner or editor, so the reason stays hidden.
	admin := "producer-admin"
	mustExec(t, f.h.db, `INSERT INTO users(id,email) VALUES(?,'producer-admin@example.test')`, admin)
	mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'ADMIN')`, generateCUID(), f.ws, admin)
	for _, tc := range []struct {
		name       string
		user       string
		wantReason bool
	}{{"owner", f.owner, true}, {"reader", f.reader, false}, {"workspace admin", admin, false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.do(t, tc.user, "list", "", "", "?version=1", "")
			var got artifactDataRunsResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || len(got.Runs) != 1 {
				t.Fatalf("list: %d %v %s", w.Code, err, w.Body)
			}
			s := got.Runs[0]
			if s.Latest.State != "failed" || s.LastSuccessAt == "" || s.LastAttemptAt != second.StartedAt {
				t.Fatalf("summary: %+v", s)
			}
			if (s.Latest.Reason != "") != tc.wantReason || strings.Contains(w.Body.String(), f.owner) {
				t.Fatalf("reason visibility: %s", w.Body)
			}
		})
	}
}

func TestDashboardProducerRunAuthority(t *testing.T) {
	f := newProducerRunFixture(t)
	run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"owner"}`))
	mustExec(t, f.h.db, `INSERT INTO workspaces(id,name,slug) VALUES('other-ws','Other','other-ws')`)
	mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'OWNER')`, generateCUID(), "other-ws", f.owner)
	if w := f.call(f.owner, "POST", "artifacts", `{"slug":"second-board","name":"Second",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatalf("second page: %d %s", w.Code, w.Body)
	}
	cases := []struct {
		name   string
		do     func() *httptest.ResponseRecorder
		status []int
	}{
		{"reader start", func() *httptest.ResponseRecorder {
			return f.do(t, f.reader, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"reader"}`)
		}, []int{403}},
		{"reader finish", func() *httptest.ResponseRecorder {
			return f.do(t, f.reader, "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`)
		}, []int{403}},
		{"undeclared collection", func() *httptest.ResponseRecorder {
			return f.do(t, f.owner, "start", "secret", "", "", `{"base_version":1,"idempotency_key":"undeclared"}`)
		}, []int{404}},
		{"stale base version", func() *httptest.ResponseRecorder {
			return f.do(t, f.owner, "start", "sales", "", "", `{"base_version":2,"idempotency_key":"stale"}`)
		}, []int{404, 409}},
		{"spoofed producer field", func() *httptest.ResponseRecorder {
			return f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"spoof","started_by":"agent:x"}`)
		}, []int{400}},
		{"invalid key", func() *httptest.ResponseRecorder {
			return f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"has space"}`)
		}, []int{400}},
		{"succeeded with reason", func() *httptest.ResponseRecorder {
			return f.do(t, f.owner, "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded","reason":"x"}`)
		}, []int{400}},
		{"run from another page", func() *httptest.ResponseRecorder {
			return producerRunRequest(t, f.h, f.ws, f.owner, "second-board", "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`)
		}, []int{404}},
		{"other workspace read", func() *httptest.ResponseRecorder {
			return producerRunRequest(t, f.h, "other-ws", f.owner, "three-screens", "list", "", "", "?version=1", "")
		}, []int{404}},
		{"other workspace finish", func() *httptest.ResponseRecorder {
			return producerRunRequest(t, f.h, "other-ws", f.owner, "three-screens", "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`)
		}, []int{404}},
		{"stranger read", func() *httptest.ResponseRecorder {
			mustExec(t, f.h.db, `INSERT OR IGNORE INTO users(id,email) VALUES('stranger','stranger@example.test')`)
			mustExec(t, f.h.db, `INSERT OR IGNORE INTO workspace_members(id,workspace_id,user_id,role) VALUES('stranger-member',?,'stranger','MEMBER')`, f.ws)
			return f.do(t, "stranger", "list", "", "", "?version=1", "")
		}, []int{404}},
		{"revoked reader grant", func() *httptest.ResponseRecorder {
			mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, f.page, f.reader)
			return f.do(t, f.reader, "list", "", "", "?version=1", "")
		}, []int{404}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.do()
			ok := false
			for _, s := range tc.status {
				ok = ok || w.Code == s
			}
			if !ok {
				t.Fatalf("status %d, want %v: %s", w.Code, tc.status, w.Body)
			}
		})
	}
	var state string
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_data_runs WHERE id=?`, run.ID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("denied requests changed the run: %q %v", state, err)
	}
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "false")
	if w := f.do(t, f.reader, "list", "", "", "?version=1", ""); w.Code != 404 {
		t.Fatalf("disabled: %d %s", w.Code, w.Body)
	}
}

func TestDashboardProducerRunEditorWritesAndKeyBelongsToItsProducer(t *testing.T) {
	f := newProducerRunFixture(t)
	editor := "producer-editor"
	mustExec(t, f.h.db, `INSERT INTO users(id,email) VALUES(?,'producer-editor@example.test')`, editor)
	mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.ws, editor)
	mustExec(t, f.h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'write',?,datetime('now'))`, f.page, editor, f.owner)
	// The editor holds exactly the batch authority, so starting a run matches a batch write.
	batch := dashboardRequest(t, f.h, f.ws, editor, "POST", "sales", "", `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":1}}]}`)
	start := f.do(t, editor, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"shared-key"}`)
	if batch.Code != 200 || start.Code != 201 {
		t.Fatalf("editor share: batch %d %s, start %d %s", batch.Code, batch.Body, start.Code, start.Body)
	}
	if w := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"shared-key"}`); w.Code != 409 {
		t.Fatalf("another producer took over the key: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "list", "", "", "?version=1", ""); !strings.Contains(w.Body.String(), `"started_by":"Contributor"`) || strings.Contains(w.Body.String(), editor) {
		t.Fatalf("producer projection: %s", w.Body)
	}
	// A revoked Edit share loses the run exactly like it loses batch writes.
	run := decodeRun(t, start)
	mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, f.page, editor)
	if w := f.do(t, editor, "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 404 {
		t.Fatalf("revoked editor finished a run: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, editor, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"after-revoke"}`); w.Code != 404 {
		t.Fatalf("revoked editor started a run: %d %s", w.Code, w.Body)
	}
}

func TestDashboardProducerRunRetentionIsBounded(t *testing.T) {
	f := newProducerRunFixture(t)
	first := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"k-success"}`))
	if w := f.do(t, f.owner, "finish", "sales", first.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for i := 0; i < artifacts.DefaultDataRunsKept+5; i++ {
		w := f.do(t, f.owner, "start", "sales", "", "", fmt.Sprintf(`{"base_version":1,"idempotency_key":"k-%d"}`, i))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
		if w := f.do(t, f.owner, "finish", "sales", decodeRun(t, w).ID, "", `{"base_version":1,"state":"failed"}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	var kept, success int
	if err := f.h.db.QueryRow(`SELECT count(*), sum(state='succeeded') FROM artifact_data_runs WHERE workspace_id=? AND collection='sales'`, f.ws).Scan(&kept, &success); err != nil {
		t.Fatal(err)
	}
	if kept != artifacts.DefaultDataRunsKept+1 || success != 1 {
		t.Fatalf("retention kept %d runs, %d successes", kept, success)
	}
	var summary artifactDataRunsResponse
	w := f.do(t, f.owner, "list", "", "", "?version=1", "")
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil || len(summary.Runs) != 1 || summary.Runs[0].LastSuccessAt != decodeRunFinished(t, f, first.ID) {
		t.Fatalf("last success lost: %v %s", err, w.Body)
	}
}

func decodeRunFinished(t *testing.T, f producerRunFixture, id string) string {
	t.Helper()
	var at string
	if err := f.h.db.QueryRow(`SELECT finished_at FROM artifact_data_runs WHERE id=?`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// O04: a success is bound to the data generation the producer wrote or
// verified, by the producer that started the run; the finisher is recorded.
func TestDashboardProducerRunSuccessIsBoundToDataGeneration(t *testing.T) {
	batch := func(t *testing.T, f producerRunFixture, user string, ifVersion int) int64 {
		t.Helper()
		w := dashboardRequest(t, f.h, f.ws, user, "POST", "sales", "", fmt.Sprintf(`{"base_version":1,"documents":[{"id":"row","ifVersion":%d,"value":{"amount":%d}}]}`, ifVersion, ifVersion+1))
		var got struct {
			Generation *int64 `json:"generation"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Generation == nil {
			t.Fatalf("batch has no generation: %d %s", w.Code, w.Body)
		}
		return *got.Generation
	}
	finish := func(f producerRunFixture, user, id, body string) *httptest.ResponseRecorder {
		return f.do(t, user, "finish", "sales", id, "", body)
	}
	t.Run("success needs the generation it wrote", func(t *testing.T) {
		f := newProducerRunFixture(t)
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g1"}`))
		if run.StartGeneration != 0 {
			t.Fatalf("start generation: %+v", run)
		}
		if w := finish(f, f.owner, run.ID, `{"base_version":1,"state":"succeeded"}`); w.Code != 400 {
			t.Fatalf("success without generation: %d %s", w.Code, w.Body)
		}
		if w := finish(f, f.owner, run.ID, `{"base_version":1,"state":"succeeded","data_generation":0}`); w.Code != 409 {
			t.Fatalf("success without a data change or unchanged claim: %d %s", w.Code, w.Body)
		}
		g := batch(t, f, f.owner, 0)
		w := finish(f, f.owner, run.ID, fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g))
		done := decodeRun(t, w)
		if w.Code != 200 || done.DataGeneration == nil || *done.DataGeneration != g || done.FinishedBy != "You" {
			t.Fatalf("bound success: %d %s", w.Code, w.Body)
		}
		if w := finish(f, f.owner, run.ID, fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g)); w.Code != 200 {
			t.Fatalf("idempotent finish: %d %s", w.Code, w.Body)
		}
	})
	t.Run("stale finish after another write", func(t *testing.T) {
		f := newProducerRunFixture(t)
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g2"}`))
		g := batch(t, f, f.owner, 0)
		batch(t, f, f.owner, 1) // a later write the producer did not confirm
		if w := finish(f, f.owner, run.ID, fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g)); w.Code != 409 {
			t.Fatalf("stale finish accepted: %d %s", w.Code, w.Body)
		}
		var state string
		if err := f.h.db.QueryRow(`SELECT state FROM artifact_data_runs WHERE id=?`, run.ID).Scan(&state); err != nil || state != "running" {
			t.Fatalf("stale finish changed the run: %q %v", state, err)
		}
	})
	t.Run("unchanged success must be explicit and verified", func(t *testing.T) {
		f := newProducerRunFixture(t)
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g3"}`))
		if w := finish(f, f.owner, run.ID, `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 200 {
			t.Fatalf("verified unchanged: %d %s", w.Code, w.Body)
		}
		other := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g4"}`))
		g := batch(t, f, f.owner, 0)
		if w := finish(f, f.owner, other.ID, fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d,"unchanged":true}`, g)); w.Code != 409 {
			t.Fatalf("unchanged claim over changed data: %d %s", w.Code, w.Body)
		}
	})
	t.Run("a foreign writer cannot mark another producer's run succeeded", func(t *testing.T) {
		f := newProducerRunFixture(t)
		editor := "producer-editor"
		mustExec(t, f.h.db, `INSERT INTO users(id,email) VALUES(?,'producer-editor@example.test')`, editor)
		mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.ws, editor)
		mustExec(t, f.h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'write',?,datetime('now'))`, f.page, editor, f.owner)
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g5"}`))
		g := batch(t, f, f.owner, 0)
		if w := finish(f, editor, run.ID, fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g)); w.Code != 403 {
			t.Fatalf("foreign success: %d %s", w.Code, w.Body)
		}
		w := finish(f, editor, run.ID, `{"base_version":1,"state":"failed","reason":"abandoned"}`)
		if w.Code != 200 || decodeRun(t, w).FinishedBy != "You" {
			t.Fatalf("foreign abandon: %d %s", w.Code, w.Body)
		}
		var finisher string
		if err := f.h.db.QueryRow(`SELECT finished_by FROM artifact_data_runs WHERE id=?`, run.ID).Scan(&finisher); err != nil || (finisher != editor && finisher != "user:"+editor) {
			t.Fatalf("finisher not recorded: %q %v", finisher, err)
		}
		if w := f.do(t, f.owner, "list", "", "", "?version=1", ""); !strings.Contains(w.Body.String(), `"finished_by":"Contributor"`) || strings.Contains(w.Body.String(), editor) {
			t.Fatalf("finisher projection: %s", w.Body)
		}
	})
	t.Run("failed partial refresh is reported as partial", func(t *testing.T) {
		f := newProducerRunFixture(t)
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g6"}`))
		batch(t, f, f.owner, 0)
		if w := finish(f, f.owner, run.ID, `{"base_version":1,"state":"failed","reason":"second page timed out"}`); w.Code != 200 || !decodeRun(t, w).Partial {
			t.Fatalf("partial failure: %d %s", w.Code, w.Body)
		}
		w := f.do(t, f.reader, "list", "", "", "?version=1", "")
		if !strings.Contains(w.Body.String(), `"partial":true`) || strings.Contains(w.Body.String(), "last_success_at") {
			t.Fatalf("reader summary of partial failure: %s", w.Body)
		}
	})
	t.Run("retry after failure", func(t *testing.T) {
		f := newProducerRunFixture(t)
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g7"}`))
		if w := finish(f, f.owner, run.ID, `{"base_version":1,"state":"failed","reason":"x"}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		if w := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g7"}`); w.Code != 200 || decodeRun(t, w).State != "failed" {
			t.Fatalf("same key replays the failed run: %d %s", w.Code, w.Body)
		}
		retry := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"g7-retry"}`)
		if retry.Code != 201 {
			t.Fatalf("retry with a new key: %d %s", retry.Code, retry.Body)
		}
		g := batch(t, f, f.owner, 0)
		if w := finish(f, f.owner, decodeRun(t, retry).ID, fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g)); w.Code != 200 {
			t.Fatalf("retry success: %d %s", w.Code, w.Body)
		}
	})
}

// O05: retention never removes a running run, concurrent active runs are
// bounded at admission, and an idempotency key expires only on terminal runs.
func TestDashboardProducerRunRetentionNeverPrunesActiveRuns(t *testing.T) {
	f := newProducerRunFixture(t)
	active := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"long-running"}`))
	for i := 0; i < artifacts.DefaultDataRunsKept+5; i++ {
		// Failed without a data change: no overlap with the still-running run.
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", fmt.Sprintf(`{"base_version":1,"idempotency_key":"short-%d"}`, i)))
		if w := f.do(t, f.owner, "finish", "sales", run.ID, "", `{"base_version":1,"state":"failed"}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	var running, terminal int
	if err := f.h.db.QueryRow(`SELECT sum(state='running'), sum(state!='running') FROM artifact_data_runs WHERE workspace_id=?`, f.ws).Scan(&running, &terminal); err != nil || running != 1 || terminal != artifacts.DefaultDataRunsKept {
		t.Fatalf("retention kept %d running and %d terminal (%v)", running, terminal, err)
	}
	if w := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"long-running"}`); w.Code != 200 || decodeRun(t, w).ID != active.ID {
		t.Fatalf("active key lost: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", active.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 200 {
		t.Fatalf("active run could not finish: %d %s", w.Code, w.Body)
	}
	// A terminal run's key expires after the TTL; a running run's key never does.
	newest := "short-" + fmt.Sprint(artifacts.DefaultDataRunsKept+4)
	mustExec(t, f.h.db, `UPDATE artifact_data_runs SET started_at='2000-01-01T00:00:00Z', finished_at='2000-01-01T00:00:01Z' WHERE idempotency_key=?`, newest)
	var expired string
	if err := f.h.db.QueryRow(`SELECT id FROM artifact_data_runs WHERE idempotency_key=?`, newest).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	reused := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"`+newest+`"}`)
	if reused.Code != 201 || decodeRun(t, reused).ID == expired {
		t.Fatalf("expired terminal key not reusable: %d %s", reused.Code, reused.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", decodeRun(t, reused).ID, "", `{"base_version":1,"state":"failed"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", active.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 200 {
		t.Fatalf("active run could not finish: %d %s", w.Code, w.Body)
	}
	// Admission bounds active runs; finished runs free a slot.
	var busy []string
	for i := 0; i < artifacts.DefaultDataRunsActive; i++ {
		w := f.do(t, f.owner, "start", "sales", "", "", fmt.Sprintf(`{"base_version":1,"idempotency_key":"busy-%d"}`, i))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
		busy = append(busy, decodeRun(t, w).ID)
	}
	w := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"one-too-many"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "artifact_data_run_busy") {
		t.Fatalf("active admission not bounded: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"busy-0"}`); w.Code != 200 {
		t.Fatalf("replay of an admitted key while busy: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", busy[0], "", `{"base_version":1,"state":"failed"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"one-too-many"}`); w.Code != 201 {
		t.Fatalf("finished run did not free a slot: %d %s", w.Code, w.Body)
	}
}

// Review round 1: overlapping runs must not let a success hide another run's
// failed or still-running refresh.
func TestDashboardProducerRunSuccessRefusedWhileAnotherRunOverlaps(t *testing.T) {
	f := newProducerRunFixture(t)
	write := func(ifVersion int) int64 {
		w := dashboardRequest(t, f.h, f.ws, f.owner, "POST", "sales", "", fmt.Sprintf(`{"base_version":1,"documents":[{"id":"row","ifVersion":%d,"value":{"amount":%d}}]}`, ifVersion, ifVersion))
		var got struct{ Generation int64 }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("batch: %d %s", w.Code, w.Body)
		}
		return got.Generation
	}
	r2 := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"producer-b"}`))
	write(0)
	r1 := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"producer-a"}`))
	g := write(1)
	success := fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g)
	if w := f.do(t, f.owner, "finish", "sales", r1.ID, "", success); w.Code != 409 || !strings.Contains(w.Body.String(), "artifact_data_run_overlap") {
		t.Fatalf("success while another run is running: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", r2.ID, "", `{"base_version":1,"state":"failed","reason":"b died"}`); w.Code != 200 || !decodeRun(t, w).Partial {
		t.Fatalf("overlapping failure: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", r1.ID, "", success); w.Code != 409 || !strings.Contains(w.Body.String(), "artifact_data_run_overlap") {
		t.Fatalf("success over a partial failure that ended during this run: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", r1.ID, "", `{"base_version":1,"state":"failed"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	// A run started after the failure ended may succeed and becomes the latest.
	r3 := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"producer-a-retry"}`))
	g = write(2)
	if w := f.do(t, f.owner, "finish", "sales", r3.ID, "", fmt.Sprintf(`{"base_version":1,"state":"succeeded","data_generation":%d}`, g)); w.Code != 200 {
		t.Fatalf("retry after the overlap ended: %d %s", w.Code, w.Body)
	}
}

// Review round 2: a run whose producer died must be findable and abandonable
// by another writer using only what the API returns.
func TestDashboardProducerRunLostRunIsFindableAndAbandonable(t *testing.T) {
	f := newProducerRunFixture(t)
	editor := "producer-editor"
	mustExec(t, f.h.db, `INSERT INTO users(id,email) VALUES(?,'producer-editor@example.test')`, editor)
	mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.ws, editor)
	mustExec(t, f.h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'write',?,datetime('now'))`, f.page, editor, f.owner)
	lost := decodeRun(t, f.do(t, editor, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"crashed-job"}`))
	mine := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"owner-job"}`))
	w := f.do(t, f.owner, "finish", "sales", mine.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`)
	var refused struct {
		Code     string                    `json:"code"`
		Blocking []artifactDataRunResponse `json:"blocking_runs"`
	}
	if w.Code != 409 || json.Unmarshal(w.Body.Bytes(), &refused) != nil || refused.Code != "artifact_data_run_overlap" || len(refused.Blocking) != 1 || refused.Blocking[0].ID != lost.ID || refused.Blocking[0].StartedBy != "Contributor" || strings.Contains(w.Body.String(), editor) {
		t.Fatalf("overlap does not name the blocking run: %d %s", w.Code, w.Body)
	}
	// The newest run is the owner's, yet the list still shows every running run to writers.
	list := f.do(t, f.owner, "list", "", "", "?version=1", "")
	var summary artifactDataRunsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &summary); err != nil || len(summary.Runs) != 1 || len(summary.Runs[0].Active) != 2 {
		t.Fatalf("running runs not listed: %v %s", err, list.Body)
	}
	if w := f.do(t, f.reader, "list", "", "", "?version=1", ""); strings.Contains(w.Body.String(), "active_runs") {
		t.Fatalf("reader sees run ids of active runs: %s", w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", refused.Blocking[0].ID, "", `{"base_version":1,"state":"failed","reason":"producer crashed"}`); w.Code != 200 {
		t.Fatalf("owner could not abandon the lost run: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, f.owner, "finish", "sales", mine.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0,"unchanged":true}`); w.Code != 200 {
		t.Fatalf("success after abandoning: %d %s", w.Code, w.Body)
	}
	// A replayed success must repeat the same unchanged claim.
	if w := f.do(t, f.owner, "finish", "sales", mine.ID, "", `{"base_version":1,"state":"succeeded","data_generation":0}`); w.Code != 409 {
		t.Fatalf("replay with a different unchanged claim: %d %s", w.Code, w.Body)
	}
}

// Review round 2: retention must not drop the failed partial run that blocks
// a still-running run's success.
func TestDashboardProducerRunRetentionKeepsOverlapEvidence(t *testing.T) {
	f := newProducerRunFixture(t)
	long := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"long"}`))
	partial := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"partial"}`))
	dashboardRequest(t, f.h, f.ws, f.owner, "POST", "sales", "", `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":1}}]}`)
	if w := f.do(t, f.owner, "finish", "sales", partial.ID, "", `{"base_version":1,"state":"failed"}`); w.Code != 200 || !decodeRun(t, w).Partial {
		t.Fatal(w.Code, w.Body)
	}
	for i := 0; i < artifacts.DefaultDataRunsKept+3; i++ {
		run := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", fmt.Sprintf(`{"base_version":1,"idempotency_key":"short-%d"}`, i)))
		if w := f.do(t, f.owner, "finish", "sales", run.ID, "", `{"base_version":1,"state":"failed"}`); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := f.do(t, f.owner, "finish", "sales", long.ID, "", `{"base_version":1,"state":"succeeded","data_generation":1}`); w.Code != 409 {
		t.Fatalf("pruning removed the overlap evidence: %d %s", w.Code, w.Body)
	}
}
