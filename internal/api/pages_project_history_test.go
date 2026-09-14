package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

func TestPageProjectHistoryRestoreCASAndIsolation(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	first := projectTestSource()
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, first); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	changed := projectTestSource()
	changed.Files[3].Content = "// changed\n"
	if w := projectPut(t, h, ws, user, "OWNER", "health", 1, changed); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	call := func(method, workspace, actor, role, body string) *httptest.ResponseRecorder {
		r := pagesRequest(t, method, "/", workspace, actor, role, body)
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		if method == "GET" {
			h.ProjectHistory(w, r)
		} else {
			h.RestoreProject(w, r)
		}
		return w
	}
	for _, method := range []string{"GET", "POST"} {
		if w := call(method, ws, "other", "MEMBER", `{"revision":1,"expected_revision":2}`); w.Code != 403 {
			t.Fatalf("reader %s: %d", method, w.Code)
		}
		if w := call(method, "other-workspace", user, "OWNER", `{"revision":1,"expected_revision":2}`); w.Code != 404 {
			t.Fatalf("workspace %s: %d", method, w.Code)
		}
	}
	w := call("GET", ws, user, "OWNER", "")
	var history struct {
		Revisions []pageProjectRevision `json:"revisions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history.Revisions) != 2 {
		t.Fatalf("history %s %v", w.Body.String(), err)
	}
	if !pages.ValidProjectCommit(history.Revisions[0].GitCommit) || !history.Revisions[0].Restorable {
		t.Fatal("missing Git checkpoint")
	}
	if w := call("POST", ws, user, "OWNER", `{"revision":1,"expected_revision":1}`); w.Code != 409 {
		t.Fatalf("stale restore %d", w.Code)
	}
	if w := call("POST", ws, user, "OWNER", `{"revision":1,"expected_revision":2}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r := pagesRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w = httptest.NewRecorder()
	h.GetProject(w, r)
	var draft pageProjectDraft
	if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	digest, _ := first.Digest()
	if draft.Revision != 3 || draft.Digest != digest || draft.GitCommit == history.Revisions[1].GitCommit {
		t.Fatal("restore did not append a new checkpoint")
	}
	var versions int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_versions`).Scan(&versions); err != nil || versions != 1 {
		t.Fatal("source restore changed the live Page")
	}
}
