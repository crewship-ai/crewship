package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

func TestPageStorageMaintenanceAuthorityAndIntegrity(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	directory := t.TempDir()
	h.SetProjectStore(&pages.ProjectStore{Directory: directory})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	compact := func(role, body string) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", "/", ws, user, role, body)
		w := httptest.NewRecorder()
		h.CompactPageProjects(w, r)
		return w
	}
	if w := compact("MEMBER", `{}`); w.Code != 403 {
		t.Fatalf("non-admin retention %d", w.Code)
	}
	if w := compact("OWNER", `{"discard_history":true}`); w.Code != 400 || !strings.Contains(w.Body.String(), "confirm=true") {
		t.Fatalf("unconfirmed history deletion %d", w.Code)
	}
	if w := compact("OWNER", `{"discard_history":false,"confirm":false}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	verify := func(workspace string) *httptest.ResponseRecorder {
		r := pagesRequest(t, "GET", "/", workspace, user, "OWNER", "")
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		h.VerifyProjectStorage(w, r)
		return w
	}
	w := verify(ws)
	var report struct {
		CheckedObjects int                 `json:"checked_git_objects"`
		Healthy        bool                `json:"healthy"`
		Failures       []map[string]string `json:"failures"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 || !report.Healthy || report.CheckedObjects == 0 {
		t.Fatalf("healthy storage: %s %v", w.Body.String(), err)
	}
	if w := verify("foreign"); w.Code != 404 {
		t.Fatalf("cross-workspace integrity read %d", w.Code)
	}
	files, err := filepath.Glob(filepath.Join(directory, "*", "*.yaml"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if err := os.WriteFile(files[0], []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	w = verify(ws)
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || report.Healthy || len(report.Failures) == 0 {
		t.Fatalf("corruption missed: %s %v", w.Body.String(), err)
	}
}
