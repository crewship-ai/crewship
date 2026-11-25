package fileserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent can write its crew's output directory. A link in it, to another
// crew's file or to any host file, must never be listed or served.
func TestFileServerRefusesLinksOutOfTheCrewDirectory(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-secret.txt")
	for path, body := range map[string]string{
		filepath.Join(base, "crewA", "mine.txt"):   "mine",
		filepath.Join(base, "crewB", "secret.txt"): "crew B only",
		outside: "host only",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"other-crew": "../crewB/secret.txt", "other-dir": "../crewB", "host-abs": outside, "host-dir": filepath.Dir(outside)} {
		if err := os.Symlink(target, filepath.Join(base, "crewA", name)); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	s := NewServer(base)
	mux.HandleFunc("GET /files/{id}", s.HandleFileList)
	mux.HandleFunc("GET /files/{id}/{path...}", s.HandleFileDownload)
	for _, p := range []string{"other-crew", "other-dir/secret.txt", "host-abs", "host-dir/host-secret.txt"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/files/crewA/"+p, nil))
		if strings.Contains(rec.Body.String(), "only") {
			t.Errorf("download of %s served a file from outside the crew directory", p)
		}
	}
	for _, dir := range []string{"other-dir", "host-dir"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/files/crewA?path="+dir, nil))
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("listing %s showed files from outside the crew directory", dir)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/files/crewA/mine.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "mine" {
		t.Errorf("own file: %d %q", rec.Code, rec.Body.String())
	}
}
