package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstanceBackupUploadStreamsArchiveWithoutWorkspace(t *testing.T) {
	data := []byte("opaque encrypted archive fixture")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/instance/backups/bundles/upload" {
			t.Errorf("unexpected upload route: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("wrong media: %s", r.Header.Get("Content-Type"))
		}
		if r.URL.Query().Has("workspace_id") {
			t.Error("upload requires no workspace")
		}
		received, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(received, data) {
			t.Errorf("archive changed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"path":"/backups/fixture.tar.zst","scope":"instance","size_bytes":32,"format_version":4,"proof_level":1}`)
	}))
	defer server.Close()
	setStubCLI(t, server.URL)
	cliCfg.Workspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	file := filepath.Join(t.TempDir(), "encrypted.tar.zst")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	adminInstanceBackupUploadCmd.SetOut(&output)
	t.Cleanup(func() { adminInstanceBackupUploadCmd.SetOut(nil) })
	if err := adminInstanceBackupUploadCmd.RunE(adminInstanceBackupUploadCmd, []string{file}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("upload requests=%d", calls)
	}
}
