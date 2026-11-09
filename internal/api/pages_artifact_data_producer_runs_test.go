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
	finish := `{"base_version":1,"state":"succeeded"}`
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
	if w := f.do(t, f.owner, "finish", "sales", first.ID, "", `{"base_version":1,"state":"succeeded"}`); w.Code != 200 {
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
	for _, tc := range []struct {
		name       string
		user       string
		wantReason bool
	}{{"owner", f.owner, true}, {"reader", f.reader, false}} {
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
			return f.do(t, f.reader, "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded"}`)
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
			return producerRunRequest(t, f.h, f.ws, f.owner, "second-board", "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded"}`)
		}, []int{404}},
		{"other workspace read", func() *httptest.ResponseRecorder {
			return producerRunRequest(t, f.h, "other-ws", f.owner, "three-screens", "list", "", "", "?version=1", "")
		}, []int{404}},
		{"other workspace finish", func() *httptest.ResponseRecorder {
			return producerRunRequest(t, f.h, "other-ws", f.owner, "three-screens", "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded"}`)
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
	if w := f.do(t, editor, "finish", "sales", run.ID, "", `{"base_version":1,"state":"succeeded"}`); w.Code != 404 {
		t.Fatalf("revoked editor finished a run: %d %s", w.Code, w.Body)
	}
	if w := f.do(t, editor, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"after-revoke"}`); w.Code != 404 {
		t.Fatalf("revoked editor started a run: %d %s", w.Code, w.Body)
	}
}

func TestDashboardProducerRunRetentionIsBounded(t *testing.T) {
	f := newProducerRunFixture(t)
	first := decodeRun(t, f.do(t, f.owner, "start", "sales", "", "", `{"base_version":1,"idempotency_key":"k-success"}`))
	if w := f.do(t, f.owner, "finish", "sales", first.ID, "", `{"base_version":1,"state":"succeeded"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for i := 0; i < artifacts.MaxDataRunsKept+5; i++ {
		if w := f.do(t, f.owner, "start", "sales", "", "", fmt.Sprintf(`{"base_version":1,"idempotency_key":"k-%d"}`, i)); w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
	}
	var kept, success int
	if err := f.h.db.QueryRow(`SELECT count(*), sum(state='succeeded') FROM artifact_data_runs WHERE workspace_id=? AND collection='sales'`, f.ws).Scan(&kept, &success); err != nil {
		t.Fatal(err)
	}
	if kept != artifacts.MaxDataRunsKept+1 || success != 1 {
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
