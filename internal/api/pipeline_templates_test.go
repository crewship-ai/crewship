package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ODCH-RT-22: routine templates. A template is a definition and a name; using
// one saves a DRAFT, never a live routine.

type tplRig struct {
	t *testing.T
	h *PipelineHandler
}

func newTplRig(t *testing.T) *tplRig {
	db := openSmokeDB(t)
	seedSmokePipelineDef(t, db, "weekly", testResultDef)
	return &tplRig{t: t, h: NewPipelineHandler(db, slog.Default(), nil, nil)}
}

func (r *tplRig) do(method, path, body string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	r.t.Helper()
	req := withWorkspaceCtx(httptest.NewRequest(method, "/api/v1/workspaces/ws_smoke"+path, strings.NewReader(body)), "ws_smoke")
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "user_1"}))
	if i := strings.Index(path, "/pipeline-templates/"); i >= 0 {
		id := strings.TrimPrefix(path[i:], "/pipeline-templates/")
		req.SetPathValue("id", strings.SplitN(id, "/", 2)[0])
	}
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func (r *tplRig) create(body string) routineTemplateResponse {
	r.t.Helper()
	w := r.do("POST", "/pipeline-templates", body, r.h.CreateTemplate)
	if w.Code != http.StatusCreated {
		r.t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var out routineTemplateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func TestRoutineTemplates_Create(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		pre      string
		wantCode int
	}{
		{"from a definition", `{"name":"Weekly scan","definition":` + testResultDef + `}`, "", http.StatusCreated},
		{"from a routine", `{"name":"Copy of weekly","from_routine":"weekly"}`, "", http.StatusCreated},
		{"unknown routine", `{"name":"x","from_routine":"ghost"}`, "", http.StatusNotFound},
		{"both given", `{"name":"x","from_routine":"weekly","definition":` + testResultDef + `}`, "", http.StatusBadRequest},
		{"neither given", `{"name":"x"}`, "", http.StatusBadRequest},
		{"blank name", `{"name":" ","from_routine":"weekly"}`, "", http.StatusBadRequest},
		{"name too long", `{"name":"` + strings.Repeat("n", 81) + `","from_routine":"weekly"}`, "", http.StatusBadRequest},
		{"definition that could not save", `{"name":"x","definition":{"name":"x","steps":[{"id":"a","type":"nope"}]}}`, "", http.StatusUnprocessableEntity},
		{"not json", `{`, "", http.StatusBadRequest},
		{"duplicate name ignoring case", `{"name":"WEEKLY SCAN","from_routine":"weekly"}`, `{"name":"Weekly scan","from_routine":"weekly"}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTplRig(t)
			if tc.pre != "" {
				rig.create(tc.pre)
			}
			if w := rig.do("POST", "/pipeline-templates", tc.body, rig.h.CreateTemplate); w.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

func TestRoutineTemplates_ListGetDeleteUse(t *testing.T) {
	rig := newTplRig(t)
	tpl := rig.create(`{"name":"Weekly scan","description":"Compare prices","category":"research","from_routine":"weekly"}`)
	if tpl.StepCount != 1 || tpl.Definition == nil {
		t.Fatalf("created = %+v", tpl)
	}

	// The list carries no definition; get does.
	w := rig.do("GET", "/pipeline-templates", "", rig.h.ListTemplates)
	var list []routineTemplateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Definition != nil || list[0].Name != "Weekly scan" || list[0].Category != "research" {
		t.Fatalf("list = %v %s", err, w.Body.String())
	}
	if w = rig.do("GET", "/pipeline-templates/"+tpl.ID, "", rig.h.GetTemplate); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"definition"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	if w = rig.do("GET", "/pipeline-templates/nope", "", rig.h.GetTemplate); w.Code != http.StatusNotFound {
		t.Fatalf("get unknown: %d", w.Code)
	}

	// Using it saves a draft, not a routine.
	w = rig.do("POST", "/pipeline-templates/"+tpl.ID+"/use", `{"slug":"price-watch","name":"Price watch","author_crew_id":"crew_a"}`, rig.h.UseTemplate)
	if w.Code != http.StatusCreated {
		t.Fatalf("use: %d %s", w.Code, w.Body.String())
	}
	var draft pipeline.Draft
	if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil || draft.Slug != "price-watch" || draft.Revision != 1 {
		t.Fatalf("draft = %v %+v", err, draft)
	}
	var doc struct {
		Name         string          `json:"name"`
		AuthorCrewID string          `json:"author_crew_id"`
		Definition   json.RawMessage `json:"definition"`
	}
	if err := json.Unmarshal(draft.Document, &doc); err != nil || doc.Name != "Price watch" || doc.AuthorCrewID != "crew_a" || !strings.Contains(string(doc.Definition), "tr-routine") {
		t.Fatalf("draft document = %v %s", err, draft.Document)
	}
	var n int
	_ = rig.h.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE slug='price-watch'`).Scan(&n)
	if n != 0 {
		t.Fatal("using a template must not create a live routine")
	}

	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"slug taken by a draft", `{"slug":"price-watch"}`, http.StatusConflict},
		{"slug taken by a routine", `{"slug":"weekly"}`, http.StatusConflict},
		{"bad slug", `{"slug":"Not A Slug"}`, http.StatusBadRequest},
		{"no slug", `{}`, http.StatusBadRequest},
		{"foreign crew", `{"slug":"another","author_crew_id":"crew_zzz"}`, http.StatusUnprocessableEntity},
	} {
		if w := rig.do("POST", "/pipeline-templates/"+tpl.ID+"/use", tc.body, rig.h.UseTemplate); w.Code != tc.want {
			t.Errorf("%s: %d, want %d: %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
	if w := rig.do("POST", "/pipeline-templates/nope/use", `{"slug":"x1"}`, rig.h.UseTemplate); w.Code != http.StatusNotFound {
		t.Errorf("use unknown: %d", w.Code)
	}

	if w := rig.do("DELETE", "/pipeline-templates/"+tpl.ID, "", rig.h.DeleteTemplate); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := rig.do("DELETE", "/pipeline-templates/"+tpl.ID, "", rig.h.DeleteTemplate); w.Code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", w.Code)
	}
}
