package api

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
	"gopkg.in/yaml.v3"
)

func projectTestSource() *pages.SourceProject {
	return &pages.SourceProject{Format: pages.SourceProjectFormat, Runtime: pages.SourceProjectRuntime, Files: []pages.ProjectFile{
		{Path: "package.json", Encoding: "utf8", Content: "{}\n"},
		{Path: "pnpm-lock.yaml", Encoding: "utf8", Content: "lockfileVersion: '9.0'\n"},
		{Path: "index.html", Encoding: "utf8", Content: "<div id='root'></div>\n"},
		{Path: "src/main.tsx", Encoding: "utf8", Content: "// Stav MySQL — živá data\n"},
		{Path: "public/logo.bin", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte{0, 255, 4})},
	}}
}

func projectPut(t *testing.T, h *PageHandler, ws, user, role, slug string, rev int, p *pages.SourceProject) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(map[string]any{"expected_revision": rev, "project": p})
	if err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "PUT", "/api/v1/pages/"+slug+"/project", ws, user, role, string(b))
	r.SetPathValue("slug", slug)
	w := httptest.NewRecorder()
	h.PutProject(w, r)
	return w
}

func TestPageProjectRoundTripAndDraftIsolation(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	p := projectTestSource()
	saved := projectPut(t, h, ws, user, "OWNER", "health", 0, p)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	if again := projectPut(t, h, ws, user, "OWNER", "health", 0, p); again.Code != 409 {
		t.Fatalf("stale save: %d", again.Code)
	}
	bundle, _ := pagesExport(t, h, ws, user, "health")
	if bundle["format"] != pages.TransferV2 {
		t.Fatal("source omitted from export")
	}
	// Round trip the actual HTTP export through a single YAML file representation.
	y, err := yaml.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := pages.ParseProjectTransfer(strings.NewReader(string(y)))
	if err != nil {
		t.Fatal(err)
	}
	pagesSeedRoutine(t, h, ws, "check-mysql")
	transfer.Page.Panels[0].Actions = []pages.PanelAction{{ID: "check", Kind: pages.ActionCall, Label: "Check", Routine: "check-mysql"}}
	transfer.Slug = "health-copy"
	raw, err := json.Marshal(transfer)
	if err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "POST", "/api/v1/pages/import", ws, user, "OWNER", string(raw))
	w := httptest.NewRecorder()
	h.Import(w, r)
	if w.Code != 201 {
		t.Fatalf("import %d: %s", w.Code, w.Body.String())
	}
	get := pagesRequest(t, "GET", "/api/v1/pages/health-copy/project", ws, user, "OWNER", "")
	get.SetPathValue("slug", "health-copy")
	out := httptest.NewRecorder()
	h.GetProject(out, get)
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	var draft pageProjectDraft
	if err := json.Unmarshal(out.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	want, _ := p.Digest()
	if draft.Digest != want || draft.Revision != 1 {
		t.Fatal("source or revision lost")
	}
	if len(draft.Definition.Spec.Panels[0].Actions) != 1 {
		t.Fatal("draft action lost")
	}
	rec, err := h.loadPage(r.Context(), ws, "health-copy")
	if err != nil {
		t.Fatal(err)
	}
	active, ok := h.currentDocument(httptest.NewRecorder(), rec)
	if !ok {
		t.Fatal("invalid active document")
	}
	if len(active.Spec.Panels[0].Actions) != 0 {
		t.Fatal("import activated an action")
	}
	again, _ := pagesExport(t, h, ws, user, "health-copy")
	by, err := yaml.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := pages.ParseProjectTransfer(strings.NewReader(string(by)))
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := copy.Project.Digest()
	if digest != want || len(copy.Page.Panels[0].Actions) != 1 {
		t.Fatal("export lost draft content")
	}
}

func TestPageProjectRBACAndConcurrentSave(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	p := projectTestSource()
	// A member without ownership or a write grant must not access sources.
	for _, method := range []string{"GET", "PUT"} {
		r := pagesRequest(t, method, "/api/v1/pages/health/project", ws, "other-user", "MEMBER", `{}`)
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		if method == "GET" {
			h.GetProject(w, r)
		} else {
			h.PutProject(w, r)
		}
		if w.Code != 403 {
			t.Fatalf("%s status %d", method, w.Code)
		}
	}
	other := pagesRequest(t, "GET", "/api/v1/pages/health/project", "other-workspace", user, "OWNER", "")
	other.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.GetProject(w, other)
	if w.Code != 404 {
		t.Fatalf("cross-workspace status %d", w.Code)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := projectPut(t, h, ws, user, "OWNER", "health", 0, p)
			if response.Code != 200 && response.Code != 409 {
				t.Errorf("save HTTP %d: %s", response.Code, response.Body.String())
			}
			codes <- response.Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent save outcomes %v", counts)
	}
}

func TestPageProjectImportMissingBindingIsAtomic(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	bundle, _ := pagesExport(t, h, ws, user, "health")
	b, _ := json.Marshal(bundle)
	var req pages.TransferImport
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatal(err)
	}
	req.Slug = "bad-copy"
	req.Page.Panels[0].Actions = []pages.PanelAction{{ID: "run", Kind: pages.ActionCall, Label: "Run", Routine: "missing-routine"}}
	b, _ = json.Marshal(req)
	r := pagesRequest(t, "POST", "/api/v1/pages/import", ws, user, "OWNER", string(b))
	w := httptest.NewRecorder()
	h.Import(w, r)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "missing-routine") {
		t.Fatalf("bad import %d %s", w.Code, w.Body.String())
	}
	if n := pagesCountIn(t, h, `SELECT count(*) FROM pages WHERE slug='bad-copy'`); n != 0 {
		t.Fatal("partial import")
	}
	// Remapping the action's routine resolves the same dependency as producers.
	pagesSeedRoutine(t, h, ws, "local-check")
	req.Bind = map[string]string{"routine/missing-routine": "routine/local-check"}
	b, _ = json.Marshal(req)
	r = pagesRequest(t, "POST", "/api/v1/pages/import", ws, user, "OWNER", string(b))
	w = httptest.NewRecorder()
	h.Import(w, r)
	if w.Code != 201 {
		t.Fatalf("bound import %d %s", w.Code, w.Body.String())
	}
}

func TestPageProjectStrictSaveAndDisabledStore(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 503 {
		t.Fatalf("disabled status %d", w.Code)
	}
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	p, _ := json.Marshal(projectTestSource())
	for _, body := range []string{`{"expected_revision":0,"expected_revision":1,"project":` + string(p) + `}`, `{"expected_revision":0,"project":` + string(p) + `,"grant":"admin"}`} {
		r := pagesRequest(t, "PUT", "/api/v1/pages/health/project", ws, user, "OWNER", body)
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		h.PutProject(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid save %d %s", w.Code, w.Body.String())
		}
	}
}

func TestPageProjectDefinitionUpdateIsDraftOnly(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r := pagesRequest(t, "PUT", "/api/v1/pages/health/project", ws, user, "OWNER", "")
	rec, err := h.loadPage(r.Context(), ws, "health")
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := h.currentDocument(httptest.NewRecorder(), rec)
	if !ok {
		t.Fatal("missing definition")
	}
	original := doc.Metadata.Name
	doc.Metadata.Name = "Updated draft"
	raw, err := json.Marshal(map[string]any{"expected_revision": 1, "project": projectTestSource(), "definition": doc})
	if err != nil {
		t.Fatal(err)
	}
	r = pagesRequest(t, "PUT", "/api/v1/pages/health/project", ws, user, "OWNER", string(raw))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.PutProject(w, r)
	if w.Code != 200 {
		t.Fatalf("save %d: %s", w.Code, w.Body.String())
	}
	draft, err := h.loadProject(r, rec)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 2 || draft.Definition.Metadata.Name != "Updated draft" {
		t.Fatal("draft update lost")
	}
	rec, err = h.loadPage(r.Context(), ws, "health")
	if err != nil {
		t.Fatal(err)
	}
	live, ok := h.currentDocument(httptest.NewRecorder(), rec)
	if !ok || live.Metadata.Name != original {
		t.Fatal("draft changed live page")
	}
	if n := pagesCountIn(t, h, `SELECT count(*) FROM page_project_revisions`); n != 2 {
		t.Fatalf("audit revisions %d", n)
	}
	h.SetProjectStore(nil)
	r = pagesRequest(t, "GET", "/api/v1/pages/health/export", ws, user, "OWNER", "")
	r.SetPathValue("slug", "health")
	w = httptest.NewRecorder()
	h.Export(w, r)
	if w.Code != 503 {
		t.Fatalf("disabled source export: %d", w.Code)
	}
}
