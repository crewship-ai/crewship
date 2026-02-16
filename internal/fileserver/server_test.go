package fileserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleFileList(t *testing.T) {
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "team-1")
	_ = os.MkdirAll(teamDir, 0750)
	_ = os.WriteFile(filepath.Join(teamDir, "report.txt"), []byte("data"), 0640)
	_ = os.MkdirAll(filepath.Join(teamDir, "subdir"), 0750)

	s := NewServer(dir)

	req := httptest.NewRequest("GET", "/teams/team-1/files", nil)
	req.SetPathValue("id", "team-1")
	w := httptest.NewRecorder()

	s.HandleFileList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	files := resp["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestHandleFileListEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(dir)

	req := httptest.NewRequest("GET", "/teams/nonexistent/files", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	s.HandleFileList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleFileDownload(t *testing.T) {
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "team-1")
	_ = os.MkdirAll(teamDir, 0750)
	_ = os.WriteFile(filepath.Join(teamDir, "report.pdf"), []byte("pdf-data"), 0640)

	s := NewServer(dir)

	req := httptest.NewRequest("GET", "/teams/team-1/files/report.pdf", nil)
	req.SetPathValue("id", "team-1")
	req.SetPathValue("path", "report.pdf")
	w := httptest.NewRecorder()

	s.HandleFileDownload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "pdf-data" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("unexpected content-type: %s", w.Header().Get("Content-Type"))
	}
}

func TestHandleFileDownloadNotFound(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "team-1"), 0750)

	s := NewServer(dir)

	req := httptest.NewRequest("GET", "/teams/team-1/files/missing.txt", nil)
	req.SetPathValue("id", "team-1")
	req.SetPathValue("path", "missing.txt")
	w := httptest.NewRecorder()

	s.HandleFileDownload(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleFileListPathTraversal(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "team-1"), 0750)

	s := NewServer(dir)

	req := httptest.NewRequest("GET", "/teams/team-1/files?path=../../etc", nil)
	req.SetPathValue("id", "team-1")
	w := httptest.NewRecorder()

	s.HandleFileList(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestDetectMIME(t *testing.T) {
	tests := map[string]string{
		"file.pdf":  "application/pdf",
		"file.json": "application/json",
		"file.csv":  "text/csv",
		"file.md":   "text/markdown",
		"file.png":  "image/png",
		"file.xyz":  "application/octet-stream",
	}
	for name, expected := range tests {
		if got := detectMIME(name); got != expected {
			t.Errorf("detectMIME(%q) = %q, want %q", name, got, expected)
		}
	}
}
