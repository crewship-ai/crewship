package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

// newFileServer builds a Server backed by a localfs storage rooted at a
// temp dir — the same root the docker provider binds as OutputBasePath,
// so a storage key of "crews/<id>/shared/..." maps to the container's
// /crew/shared bind source.
func newFileServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-routes-files-shared-32"
	dir := t.TempDir()
	cfg.Storage.BasePath = dir
	logger := logging.New("error", "json", nil)
	stor, _ := localfs.New(dir)
	s := New(cfg, logger, &Deps{Storage: stor, DB: openTestDB(t)})
	t.Cleanup(s.StopBackground)
	s.startedAt = time.Now()
	return s, dir
}

// TestHandleFileSave_SharedRoutesToCrewBindTree is the #849 bug regression
// guard: a bundled crew file (dest "shared/...") must land in the crew's
// /crew/shared bind source (host: <BasePath>/crews/<id>/shared/...), NOT
// the /output tree — otherwise the container never sees it and a script
// step fails with "No such file or directory".
func TestHandleFileSave_SharedRoutesToCrewBindTree(t *testing.T) {
	s, dir := newFileServer(t)

	req := httptest.NewRequest("PUT", "/crews/crewX/files/save?path=shared/scripts/parse.py",
		strings.NewReader("print('hi')\n"))
	req.SetPathValue("id", "crewX")
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("save shared file: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// Must exist at the /crew bind source, where EnsureCrewRuntime mounts it.
	want := filepath.Join(dir, "crews", "crewX", "shared", "scripts", "parse.py")
	b, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("bundled file not at crew bind path %s: %v", want, err)
	}
	if string(b) != "print('hi')\n" {
		t.Errorf("content = %q", b)
	}
	// It must NOT have leaked into the /output tree (<id>/shared/...).
	if _, err := os.Stat(filepath.Join(dir, "crewX", "shared", "scripts", "parse.py")); err == nil {
		t.Errorf("shared file wrongly written to the /output tree")
	}
}

// TestHandleFileSave_LegacyOutputTree: a non-shared path keyed by crewID
// still writes to the /output tree (backward compatibility for agent
// output files).
func TestHandleFileSave_LegacyOutputTree(t *testing.T) {
	s, dir := newFileServer(t)

	req := httptest.NewRequest("PUT", "/crews/crewX/files/save?path=crewX/report.txt",
		strings.NewReader("data"))
	req.SetPathValue("id", "crewX")
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("save output file: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "crewX", "report.txt")); err != nil {
		t.Fatalf("legacy output file not written: %v", err)
	}
}

// TestHandleFileSave_RejectsTraversal keeps the traversal guard.
func TestHandleFileSave_RejectsTraversal(t *testing.T) {
	s, _ := newFileServer(t)
	for _, p := range []string{"shared/../../etc/passwd", "/etc/passwd", "../evil"} {
		req := httptest.NewRequest("PUT", "/crews/crewX/files/save?path="+p, strings.NewReader("x"))
		req.SetPathValue("id", "crewX")
		rec := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("path %q should be rejected, got 200", p)
		}
	}
}

// TestHandleFileDownload_SharedRoundTrip: a saved shared file is
// retrievable via download (same routing).
func TestHandleFileDownload_SharedRoundTrip(t *testing.T) {
	s, _ := newFileServer(t)

	save := httptest.NewRequest("PUT", "/crews/crewX/files/save?path=shared/data/x.txt",
		strings.NewReader("payload"))
	save.SetPathValue("id", "crewX")
	sr := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(sr, save)
	if sr.Code != http.StatusOK {
		t.Fatalf("save: %d %s", sr.Code, sr.Body.String())
	}

	dl := httptest.NewRequest("GET", "/crews/crewX/files/download?path=shared/data/x.txt", nil)
	dl.SetPathValue("id", "crewX")
	dr := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(dr, dl)
	if dr.Code != http.StatusOK {
		t.Fatalf("download shared file: status = %d", dr.Code)
	}
	if got := dr.Body.String(); got != "payload" {
		t.Errorf("download body = %q, want payload", got)
	}
}

// TestResolveCrewFileKey_RejectsUnsafeCrewID pins the CodeRabbit Major on
// this PR: r.PathValue("id") can decode to a value carrying a slash or
// dot-dot (an encoded-slash URL), and filepath.Join("crews", crewID, ...)
// would collapse the key out of the crews/ prefix — worst case into
// ANOTHER crew's shared tree. The crew id must be a single clean path
// component before it is joined into a storage key.
func TestResolveCrewFileKey_RejectsUnsafeCrewID(t *testing.T) {
	for _, id := range []string{
		"../elsewhere",
		"..",
		"a/b",
		"crews/other",
		`a\b`,
		".",
		"",
		"../crews/victim",
	} {
		t.Run(id, func(t *testing.T) {
			if key, ok := resolveCrewFileKey(id, "shared/x.py"); ok {
				t.Fatalf("crewID %q must be rejected, got key %q", id, key)
			}
		})
	}
	// Sane CUID-shaped id keeps working.
	if _, ok := resolveCrewFileKey("cmr9bella0046b7ac37ed", "shared/x.py"); !ok {
		t.Fatal("valid crew id rejected")
	}
}
