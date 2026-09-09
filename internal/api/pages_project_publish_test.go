package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

func TestPageProjectPublicationAtomicCASRollbackAndReach(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&testPageBuilder{}, &pagebuild.Store{Directory: t.TempDir()})
	h.pageRuntimeOrigin = "https://pages.example.net"
	h.pageStudioOrigin = "https://studio.example.com"
	pagesCreate(t, h, ws, user, "health")
	makeBuild := func(revision int) *pageBuildRecord {
		w := projectPut(t, h, ws, user, "OWNER", "health", revision-1, projectTestSource())
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		body, _ := json.Marshal(map[string]any{"expected_revision": revision})
		w = buildRequest(t, h, "POST", "/", ws, user, "OWNER", string(body))
		if w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		var job pageBuildRecord
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if !waitForBackgroundWork(5e9) {
			t.Fatal("build did not finish")
		}
		return &job
	}
	call := func(actor, role string, request pageProjectPublishRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(request)
		r := pagesRequest(t, "POST", "/", ws, actor, role, string(body))
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		h.PublishProject(w, r)
		return w
	}
	first := makeBuild(1)
	zero := int64(0)
	req := pageProjectPublishRequest{BuildID: first.ID, ExpectedRevision: 1, ExpectedPublication: &zero, ReviewedCode: true}
	if w := call("other", "MEMBER", req); w.Code != 403 {
		t.Fatalf("unauthorized publication %d", w.Code)
	}
	missing := req
	missing.ReviewedCode = false
	if w := call(user, "OWNER", missing); w.Code != 400 {
		t.Fatalf("unreviewed %d", w.Code)
	}
	if w := call(user, "OWNER", req); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(user, "OWNER", req); w.Code != 200 {
		t.Fatalf("idempotent retry %d", w.Code)
	}
	second := makeBuild(2)
	req.BuildID = second.ID
	req.ExpectedRevision = 2
	if w := call(user, "OWNER", req); w.Code != 409 {
		t.Fatalf("stale publication %d", w.Code)
	}
	one := int64(1)
	req.ExpectedPublication = &one
	if w := call(user, "OWNER", req); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	two := int64(2)
	rollback := pageProjectPublishRequest{ExpectedPublication: &two, RollbackVersion: 1, ReviewedCode: true}
	if w := call(user, "OWNER", rollback); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var live, source int64
	if err := h.db.QueryRow(`SELECT version FROM page_project_live`).Scan(&live); err != nil || live != 3 {
		t.Fatal("publication pointer", err)
	}
	if err := h.db.QueryRow(`SELECT revision FROM page_project_drafts`).Scan(&source); err != nil || source != 2 {
		t.Fatal("rollback changed source draft", err)
	}
	wRetry := call(user, "OWNER", req)
	var receipt struct {
		Version     int64 `json:"version"`
		LiveVersion int64 `json:"live_version"`
		Published   bool  `json:"published"`
		IsCurrent   bool  `json:"is_current"`
		Replayed    bool  `json:"replayed"`
	}
	if err := json.Unmarshal(wRetry.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if wRetry.Code != 200 || receipt.Version != 2 || receipt.LiveVersion != 3 || !receipt.Published || receipt.IsCurrent || !receipt.Replayed {
		t.Fatalf("ambiguous stale receipt: %s", wRetry.Body.String())
	}
	rec, err := h.loadPage(context.Background(), ws, "health")
	if err != nil || !rec.HasApplication || rec.PublicationVersion != 3 {
		t.Fatalf("detail omitted live state: %+v %v", rec, err)
	}
	testApplicationHistory(t, h, ws, user, 3)
	// Rendering does not require an enabled build worker.
	h.builds = nil
	r := pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.PageApplication(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var app struct {
		Publication pagePublication     `json:"publication"`
		Artifact    *pagebuild.Artifact `json:"artifact"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &app); err != nil || app.Artifact == nil || app.Publication.BuildID != first.ID {
		t.Fatalf("wrong rollback artifact %v", err)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing application ETag")
	}
	r = pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.PageApplication(w, r)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("conditional artifact delivery: %d", w.Code)
	}
	r = pagesRequest(t, "GET", "/", ws, "unauthorized-viewer", "MEMBER", "")
	r.SetPathValue("slug", "health")
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.PageApplication(w, r)
	if w.Code != 404 {
		t.Fatalf("conditional delivery bypassed authorization: %d", w.Code)
	}
	r = pagesRequest(t, "GET", "/", "other-workspace", user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w = httptest.NewRecorder()
	h.PageApplication(w, r)
	if w.Code != 404 {
		t.Fatal("cross workspace application")
	}
	testApplicationDevelopmentOrigin(t, h, ws, user)
	// Withdrawal retains the version counter and prevents stale retries reactivating code.
	withdraw := func(actor, role string, version int64) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"expected_publication": version})
		r := pagesRequest(t, "POST", "/", ws, actor, role, string(body))
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		h.UnpublishProject(w, r)
		return w
	}
	if w := withdraw("other", "MEMBER", 3); w.Code != 403 {
		t.Fatalf("unauthorized withdrawal: %d", w.Code)
	}
	if w := withdraw(user, "OWNER", 2); w.Code != 409 {
		t.Fatalf("stale withdrawal: %d", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := withdraw(user, "OWNER", 3); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	r = pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.PageApplication(w, r)
	var withdrawn map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &withdrawn); err != nil || w.Code != 200 || withdrawn["publication"] != nil || withdrawn["publication_version"] != float64(3) || withdrawn["artifact"] != nil {
		t.Fatalf("withdrawal still executes: %s", w.Body.String())
	}
	if w := call(user, "OWNER", rollback); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var active bool
	if err := h.db.QueryRow(`SELECT published FROM page_project_live`).Scan(&active); err != nil || active {
		t.Fatal("old retry republished withdrawn code", err)
	}
	three := int64(3)
	rollback.ExpectedPublication = &three
	if w := call(user, "OWNER", rollback); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := withdraw(user, "OWNER", 3); w.Code != 409 {
		t.Fatalf("old withdrawal removed new publication: %d", w.Code)
	}
	r = pagesRequest(t, "GET", "/?before=4", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w = httptest.NewRecorder()
	h.PublicationHistory(w, r)
	var history struct {
		Publications []struct {
			Version   int64  `json:"version"`
			Withdrawn string `json:"withdrawn_at"`
		} `json:"publications"`
		Version   int64 `json:"publication_version"`
		Published bool  `json:"published"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || len(history.Publications) != 3 || history.Publications[0].Version != 3 || history.Publications[0].Withdrawn == "" || history.Version != 4 || !history.Published {
		t.Fatalf("history lost receipt: %s", w.Body.String())
	}

}

func testApplicationDevelopmentOrigin(t *testing.T, h *PageHandler, ws, user string) {
	t.Helper()
	original := h.pageRuntimeOrigin
	defer func() { h.pageRuntimeOrigin = original; h.pageRuntimeDevelopmentSameOrigin = false }()
	request := func(actor string) *httptest.ResponseRecorder {
		role := "MEMBER"
		if actor == user {
			role = "OWNER"
		}
		r := pagesRequest(t, "GET", "/", ws, actor, role, "")
		r.SetPathValue("slug", "health")
		h.pageRuntimeOrigin = "http://" + r.Host
		w := httptest.NewRecorder()
		h.PageApplication(w, r)
		return w
	}
	if w := request(user); w.Code != 503 {
		t.Fatalf("default same-origin application = %d", w.Code)
	}
	h.pageRuntimeDevelopmentSameOrigin = true
	w := request(user)
	if w.Code != 200 {
		t.Fatalf("development application = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["development_same_origin"] != true {
		t.Fatalf("development flag missing: %v", err)
	}
	if w := request("unauthorized-viewer"); w.Code != 404 {
		t.Fatalf("development bypassed reach gate: %d", w.Code)
	}
	h.pageRuntimeDevelopmentSameOrigin = false
	if w := request(user); w.Code != 503 {
		t.Fatalf("disabled development mode = %d", w.Code)
	}
}
