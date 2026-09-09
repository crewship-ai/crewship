package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

func TestPageProjectAgentWorkflowIdentityPolicyAndCAS(t *testing.T) {
	h, ws, crew, agent := pageInternalSaveFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&testPageBuilder{}, &pagebuild.Store{Directory: t.TempDir()})
	h.pageRuntimeOrigin = "https://pages.example.net"
	h.pageStudioOrigin = "https://studio.example.com"
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level='full' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	create := `{"workspace_id":"` + ws + `","crew_id":"` + crew + `","agent_id":"` + agent + `","name":"Health","panels":[{"id":"p1","schema":"status.v1","owner":"crew/eng-pages","producer":"agent/lead-pages","sla_seconds":30,"span":4}]}`
	w := httptest.NewRecorder()
	h.InternalSave(w, httptest.NewRequest("POST", "/", strings.NewReader(create)))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	call := func(operation string, revision int64, fields map[string]any, boundWorkspace string) *httptest.ResponseRecorder {
		body := map[string]any{"workspace_id": ws, "crew_id": crew, "agent_id": agent, "operation": operation, "slug": "health", "expected_revision": revision}
		for k, v := range fields {
			body[k] = v
		}
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/", bytes.NewReader(data))
		r = r.WithContext(crewBoundCtx1222(boundWorkspace, crew))
		w := httptest.NewRecorder()
		h.InternalProject(w, r)
		return w
	}
	if w := call("init", 0, nil, "another-workspace"); w.Code != 403 {
		t.Fatalf("cross tenant %d", w.Code)
	}
	if w := call("init", 0, map[string]any{"agent_id": "another-agent"}, ws); w.Code != 400 {
		t.Fatalf("foreign agent %d", w.Code)
	}
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level='guided' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	h.policyResolver.Invalidate(crew)
	if w := call("init", 0, nil, ws); w.Code != 403 {
		t.Fatalf("held %d", w.Code)
	}
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level='full' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	h.policyResolver.Invalidate(crew)
	if w := call("init", 0, nil, ws); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("read", 0, nil, ws); w.Code != 200 || !strings.Contains(w.Body.String(), "runAction") || !strings.Contains(w.Body.String(), "/pages/health") {
		t.Fatalf("authoring contract missing: %s", w.Body.String())
	}
	if w := call("read", 0, map[string]any{"target_crew_slug": "eng-pages"}, ws); w.Code != 403 {
		t.Fatalf("ordinary crew delegation allowed: %s", w.Body.String())
	}
	change := map[string]any{"files": []pages.ProjectFile{{Path: "src/notes.txt", Encoding: "utf8", Content: "from agent"}}}
	if w := call("save", 0, change, ws); w.Code != 409 {
		t.Fatalf("stale %d", w.Code)
	}
	if w := call("save", 1, change, ws); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("read", 0, map[string]any{"path": "src/notes.txt"}, ws); w.Code != 200 || !strings.Contains(w.Body.String(), "from agent") {
		t.Fatal(w.Body.String())
	}
	if w := call("save", 2, map[string]any{"delete_paths": []string{"package.json"}}, ws); w.Code != 422 {
		t.Fatalf("required file removed: %d %s", w.Code, w.Body.String())
	}
	if w := call("publish", 2, nil, ws); w.Code != 400 {
		t.Fatalf("agent could publish %d", w.Code)
	}
	w = call("build", 2, nil, ws)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var job pageBuildRecord
	json.Unmarshal(w.Body.Bytes(), &job)
	if !waitForBackgroundWork(5e9) {
		t.Fatal("build timeout")
	}
	if w := call("status", 0, nil, ws); w.Code != 200 || strings.Contains(w.Body.String(), "javascript") {
		t.Fatalf("status leaked artifact: %d %s", w.Code, w.Body.String())
	}
	if w := call("check", 2, map[string]any{"build_id": job.ID}, ws); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var userNull bool
	var author string
	if err := h.db.QueryRow(`SELECT actor_user_id IS NULL,actor_json FROM page_project_revisions WHERE revision=2`).Scan(&userNull, &author); err != nil {
		t.Fatal(err)
	}
	if !userNull || !strings.Contains(author, agent) || !strings.Contains(author, crew) {
		t.Fatalf("agent pretends to be a user: %s", author)
	}
	var publications int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_project_publications`).Scan(&publications); err != nil || publications != 0 {
		t.Fatal("agent changed publications", err)
	}
	if _, err := h.db.Exec(`UPDATE pages SET owner_crew_id=NULL,owner_user_id=(SELECT id FROM users LIMIT 1) WHERE slug='health'`); err != nil {
		t.Fatal(err)
	}
	if w := call("read", 0, nil, ws); w.Code != 403 {
		t.Fatalf("lost owner gate: %d", w.Code)
	}
}
