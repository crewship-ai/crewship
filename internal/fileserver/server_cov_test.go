package fileserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func listRequest(crewID, subPath string) *http.Request {
	url := "/crews/" + crewID + "/files"
	if subPath != "" {
		url += "?path=" + subPath
	}
	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("id", crewID)
	return req
}

func downloadRequest(crewID, path string) *http.Request {
	req := httptest.NewRequest("GET", "/crews/"+crewID+"/files/"+path, nil)
	req.SetPathValue("id", crewID)
	req.SetPathValue("path", path)
	return req
}

// TestHandleFileList_SubPath lists a nested directory via ?path= and
// asserts the returned entries carry crew-relative paths.
func TestHandleFileList_SubPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "crew-1", "reports")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "q1.csv"), []byte("a,b"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := NewServer(dir)
	w := httptest.NewRecorder()
	s.HandleFileList(w, listRequest("crew-1", "reports"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		CrewID string     `json:"crew_id"`
		Files  []FileInfo `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CrewID != "crew-1" {
		t.Errorf("crew_id = %q", resp.CrewID)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(resp.Files))
	}
	if resp.Files[0].Name != "q1.csv" || resp.Files[0].IsDir {
		t.Errorf("entry = %+v", resp.Files[0])
	}
	if resp.Files[0].Path != filepath.Join("reports", "q1.csv") {
		t.Errorf("path = %q, want reports/q1.csv", resp.Files[0].Path)
	}
	if resp.Files[0].Size != 3 {
		t.Errorf("size = %d, want 3", resp.Files[0].Size)
	}
}

// TestHandleFileList_SymlinkEscapeForbidden pins the V-09 containment
// re-check: a symlinked subdir pointing outside the crew base must be
// refused even though the lexical path looks contained.
func TestHandleFileList_SymlinkEscapeForbidden(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "outputs")
	crew := filepath.Join(base, "crew-1")
	outside := filepath.Join(root, "secrets")
	if err := os.MkdirAll(crew, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(crew, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	s := NewServer(base)
	w := httptest.NewRecorder()
	s.HandleFileList(w, listRequest("crew-1", "link"))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (symlink escape)", w.Code)
	}
}

// TestHandleFileList_MissingSubdirReturnsEmpty pins the EvalSymlinks
// error path for a path that doesn't exist inside an existing crew dir:
// graceful empty listing, not an error.
func TestHandleFileList_MissingSubdirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "crew-1"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := NewServer(dir)
	w := httptest.NewRecorder()
	s.HandleFileList(w, listRequest("crew-1", "nope"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Files []FileInfo `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Files) != 0 {
		t.Errorf("files = %v, want empty", resp.Files)
	}
}

// TestHandleFileList_FileAsDirIs500 pins the non-ENOENT ReadDir error
// branch: pointing ?path= at a regular file resolves via symlink checks
// but fails ReadDir with ENOTDIR → 500.
func TestHandleFileList_FileAsDirIs500(t *testing.T) {
	dir := t.TempDir()
	crew := filepath.Join(dir, "crew-1")
	if err := os.MkdirAll(crew, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(crew, "plain.txt"), []byte("x"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := NewServer(dir)
	w := httptest.NewRecorder()
	s.HandleFileList(w, listRequest("crew-1", "plain.txt"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (ReadDir on a file)", w.Code)
	}
}

// TestHandleFileDownload_TraversalForbidden pins the lexical containment
// check on the download route.
func TestHandleFileDownload_TraversalForbidden(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "crew-1"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := NewServer(dir)
	w := httptest.NewRecorder()
	s.HandleFileDownload(w, downloadRequest("crew-1", "../../etc/passwd"))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestHandleFileDownload_SymlinkEscapeForbidden pins V-09 on downloads: a
// symlink inside the crew dir pointing at a file outside must be refused.
func TestHandleFileDownload_SymlinkEscapeForbidden(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "outputs")
	crew := filepath.Join(base, "crew-1")
	if err := os.MkdirAll(crew, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(crew, "leak.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	s := NewServer(base)
	w := httptest.NewRecorder()
	s.HandleFileDownload(w, downloadRequest("crew-1", "leak.txt"))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (symlink escape)", w.Code)
	}
	if w.Body.String() == "topsecret" {
		t.Error("secret file content leaked through symlink")
	}
}

// TestHandleFileDownload_MissingCrewIs404 pins the realBase EvalSymlinks
// error path (crew directory absent).
func TestHandleFileDownload_MissingCrewIs404(t *testing.T) {
	s := NewServer(t.TempDir())
	w := httptest.NewRecorder()
	s.HandleFileDownload(w, downloadRequest("ghost-crew", "file.txt"))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleFileDownload_HeaderInjectionSanitized pins the
// Content-Disposition sanitizer end-to-end: quotes/control characters in
// the filename are replaced before the header is emitted.
func TestHandleFileDownload_HeaderInjectionSanitized(t *testing.T) {
	dir := t.TempDir()
	crew := filepath.Join(dir, "crew-1")
	if err := os.MkdirAll(crew, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	evil := `bad"name.txt`
	if err := os.WriteFile(filepath.Join(crew, evil), []byte("data"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := NewServer(dir)
	w := httptest.NewRecorder()
	s.HandleFileDownload(w, downloadRequest("crew-1", evil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	cd := w.Header().Get("Content-Disposition")
	want := `attachment; filename="bad_name.txt"`
	if cd != want {
		t.Errorf("Content-Disposition = %q, want %q", cd, want)
	}
	if w.Header().Get("Content-Length") != "4" {
		t.Errorf("Content-Length = %q, want 4", w.Header().Get("Content-Length"))
	}
}

// TestDetectMIME_RemainingTypes covers the extensions the existing test
// table skips.
func TestDetectMIME_RemainingTypes(t *testing.T) {
	cases := map[string]string{
		"notes.txt":  "text/plain",
		"page.HTML":  "text/html",
		"pic.jpg":    "image/jpeg",
		"pic.JPEG":   "image/jpeg",
		"diagram.svg": "image/svg+xml",
	}
	for name, want := range cases {
		if got := detectMIME(name); got != want {
			t.Errorf("detectMIME(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestSanitizeFilename covers the rune filter and the empty fallback.
func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{`report.pdf`, `report.pdf`},
		{`a"b\c.txt`, `a_b_c.txt`},
		{"ctl\x01\x1fchars\x7f.md", "ctl__chars_.md"},
		{"", "download"},
		{"\"\\", "__"},
		{"příloha.pdf", "příloha.pdf"}, // non-ASCII printable kept
	}
	for _, tc := range cases {
		if got := sanitizeFilename(tc.in); got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHandleFileDownload_PermissionDeniedIs500 pins the non-ENOENT open
// error branch: an unreadable file is a 500, not a 404.
func TestHandleFileDownload_PermissionDeniedIs500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 000 still readable")
	}
	dir := t.TempDir()
	crew := filepath.Join(dir, "crew-1")
	if err := os.MkdirAll(crew, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	locked := filepath.Join(crew, "locked.txt")
	if err := os.WriteFile(locked, []byte("x"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o640) })

	s := NewServer(dir)
	w := httptest.NewRecorder()
	s.HandleFileDownload(w, downloadRequest("crew-1", "locked.txt"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (EACCES is not a 404)", w.Code)
	}
}
