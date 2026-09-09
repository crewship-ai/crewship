package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Use literal JSON.stringify-style wire bytes: json.Marshal would hide this
// regression by HTML-escaping the definition before it reaches the server.
func TestN1DraftBrowserBytesTestAndPublish(t *testing.T) {
	for _, value := range []string{"https://example.test/?a=1&b=2", "a > b", "a < b"} {
		t.Run(value, func(t *testing.T) {
			h, user, ws := newPipelineHandlerForCRUDTest(t)
			h.runner = &stubRunner{output: "unused-in-dry-run"}
			call := func(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
				r := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/x", strings.NewReader(body)), ws), user, "MANAGER")
				r.SetPathValue("slug", "browser-draft")
				w := httptest.NewRecorder()
				handler(w, r)
				return w
			}
			def := `{"name":"browser-draft","steps":[{"id":"a","type":"transform","transform":{"input":"` + value + `","expression":"."}}]}`
			w := call(h.SaveDraft, `{"slug":"browser-draft","document":{"slug":"browser-draft","name":"Browser draft","definition":`+def+`}}`)
			if w.Code != 200 {
				t.Fatalf("save: %d %s", w.Code, w.Body)
			}
			var d pipeline.Draft
			if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			w = call(h.TestRun, `{"definition":`+def+`}`)
			if w.Code != 200 {
				t.Fatalf("test: %d %s", w.Code, w.Body)
			}
			var proof struct {
				SaveToken string `json:"save_token"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &proof); err != nil {
				t.Fatal(err)
			}
			if proof.SaveToken == "" {
				t.Fatalf("no proof: %s", w.Body)
			}
			body, err := json.Marshal(map[string]any{"id": d.ID, "revision": d.Revision, "save_token": proof.SaveToken})
			if err != nil {
				t.Fatal(err)
			}
			w = call(h.PublishDraft, string(body))
			if w.Code != 201 {
				t.Fatalf("publish: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestDraftAPIRevisionPublicationAndAuthorization(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	rec := &recordingEmitter{}
	h.emitter = rec
	call := func(handler http.HandlerFunc, role, slug string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/x", bytes.NewReader(raw)), ws), user, role)
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	slug := "draft-api"
	def := json.RawMessage(`{"name":"draft-api","steps":[{"id":"a","type":"transform","transform":{"input":"hello","expression":"."}}]}`)
	d := pipeline.Draft{Slug: slug, Document: json.RawMessage(`{"slug":"draft-api","name":"Draft API","definition":` + string(def) + `,"skip_test_gate":true,"skip_governance_gate":true,"save_token":"untrusted"}`)}
	for _, invalid := range []string{"null", "[]", `"text"`} {
		bad := d
		bad.Document = json.RawMessage(`{"slug":"draft-api","definition":` + invalid + `}`)
		if w := call(h.SaveDraft, "MANAGER", slug, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid draft %s: %d", invalid, w.Code)
		}
	}
	if w := call(h.SaveDraft, "MEMBER", slug, d); w.Code != 403 {
		t.Fatalf("member saved: %d %s", w.Code, w.Body)
	}
	w := call(h.SaveDraft, "MANAGER", slug, d)
	if w.Code != 200 {
		t.Fatalf("draft: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(d.Document, &doc)
	for _, key := range []string{"skip_test_gate", "skip_governance_gate", "save_token"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("persisted %s", key)
		}
	}
	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE workspace_id=? AND slug=?`, ws, slug).Scan(&n)
	if n != 0 {
		t.Fatal("saving draft created live recipe")
	}
	if w = call(h.GetDraft, "MEMBER", slug, nil); w.Code != 403 {
		t.Fatalf("member read draft: %d", w.Code)
	}
	publish := map[string]any{"id": d.ID, "revision": d.Revision}
	if w = call(h.PublishDraft, "MANAGER", slug, publish); w.Code != 422 {
		t.Fatalf("missing proof: %d %s", w.Code, w.Body)
	}
	publish["save_token"] = signSaveToken([]byte(testSaveTokenSecret1371), ws, definitionHashHex(def), user, time.Now())
	if w = call(h.PublishDraft, "MANAGER", slug, publish); w.Code != 201 {
		t.Fatalf("publication: %d %s", w.Code, w.Body)
	}
	if w = call(h.PublishDraft, "MANAGER", slug, publish); w.Code != 409 {
		t.Fatalf("duplicate publication: %d %s", w.Code, w.Body)
	}
	if w = call(h.SaveDraft, "MANAGER", slug, d); w.Code != 409 {
		t.Fatalf("stale editor: %d %s", w.Code, w.Body)
	}
	var versions int
	h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions v JOIN pipelines p ON p.id=v.pipeline_id WHERE p.workspace_id=? AND p.slug=?`, ws, slug).Scan(&versions)
	entry := trustEntry(t, rec, journal.EntryPipelinePublished)
	if entry.ActorID != user || entry.Payload["draft_revision"] != 1 || entry.Refs["pipeline_slug"] != slug {
		t.Fatalf("publication audit: %+v", entry)
	}
	if versions != 1 {
		t.Fatalf("versions=%d", versions)
	}
}

func TestDraftPublicationReturnsScheduleRepairTarget(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	ctx := context.Background()
	now := time.Now()
	def := `{"name":"preset-repair","steps":[{"id":"a","type":"transform","transform":{"input":"hello","expression":"."}}]}`
	p, err := h.store.Save(ctx, pipeline.SaveInput{WorkspaceID: ws, Slug: "preset-repair", Name: "Preset repair", DefinitionJSON: def, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.Exec(`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json) VALUES('repair-plan',?,'Daily report',?,'0 9 * * *','{}')`, ws, p.ID); err != nil {
		t.Fatal(err)
	}
	d, err := h.store.GetDraft(ctx, ws, p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	updated := json.RawMessage(`{"name":"preset-repair","inputs":[{"name":"region","type":"string","required":true}],"steps":[{"id":"a","type":"transform","transform":{"input":"hello","expression":"."}}]}`)
	d.Document, _ = json.Marshal(map[string]any{"slug": p.Slug, "name": p.Name, "definition": updated})
	d.UpdatedBy = user
	d, err = h.store.SaveDraft(ctx, *d)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"id": d.ID, "revision": d.Revision, "save_token": signSaveToken([]byte(testSaveTokenSecret1371), ws, definitionHashHex(updated), user, time.Now())})
	r := withAuthCtx(withWorkspaceCtx(httptest.NewRequest("POST", "/publish", bytes.NewReader(body)), ws), user, "MANAGER")
	r.SetPathValue("slug", p.Slug)
	w := httptest.NewRecorder()
	h.PublishDraft(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("publish %d %s", w.Code, w.Body)
	}
	var response struct {
		Conflict pipeline.ScheduleDraftConflict `json:"schedule_conflict"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Conflict.ScheduleID != "repair-plan" || response.Conflict.Name != "Daily report" || response.Conflict.Reason == "" {
		t.Fatalf("missing repair target: %s", w.Body)
	}
	retained, _ := h.store.GetDraft(ctx, ws, p.Slug)
	if retained.ID != d.ID || retained.Revision != d.Revision {
		t.Fatal("blocked publication lost draft")
	}
}

func TestN3DraftScheduleCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inputs  string
		values  string
		enabled bool
	}{
		{"legacy input", `[{"name":"topic"}]`, `{"topic":"news"}`, true},
		{"disabled schedule", `[{"name":"topic","type":"string","widget":"text","required":true}]`, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, ws := newPipelineHandlerForCRUDTest(t)
			ctx := context.Background()
			now := time.Now()
			def := `{"name":"compat","steps":[]}`
			p, err := h.store.Save(ctx, pipeline.SaveInput{WorkspaceID: ws, Slug: "compat", Name: "Compatibility", DefinitionJSON: def, LastTestRunAt: &now, LastTestRunPassed: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Exec(`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,inputs_json,enabled) VALUES('compat-plan',?,'Daily',?,'0 9 * * *',?,?)`, ws, p.ID, tc.values, tc.enabled); err != nil {
				t.Fatal(err)
			}
			d, err := h.store.GetDraft(ctx, ws, p.Slug)
			if err != nil {
				t.Fatal(err)
			}
			def = `{"name":"compat","inputs":` + tc.inputs + `,"steps":[]}`
			d.Document = json.RawMessage(`{"slug":"compat","definition":` + def + `}`)
			d.UpdatedBy = user
			d, err = h.store.SaveDraft(ctx, *d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.store.Save(ctx, pipeline.SaveInput{WorkspaceID: ws, Slug: p.Slug, Name: p.Name, DefinitionJSON: def, LastTestRunAt: &now, LastTestRunPassed: true, Publication: &pipeline.DraftPublication{ID: d.ID, Revision: d.Revision, Document: string(d.Document)}})
			if err != nil {
				t.Fatalf("compatible publication blocked: %v", err)
			}
		})
	}
}
