package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserAvatarGetPreservesDestinationOnFailure(t *testing.T) {
	guardCLIState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/user1/avatar" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	setStubCLI(t, server.URL)
	path := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newUserAvatarGetCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"user1", "--output", path})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected authorization failure")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("destination changed: %q %v", data, err)
	}
}

func TestWorkspaceLogoGetDownloadsBytes(t *testing.T) {
	guardCLIState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logo") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("image bytes"))
	}))
	defer server.Close()
	setStubCLI(t, server.URL)
	path := filepath.Join(t.TempDir(), "logo")
	cmd := newWorkspaceLogoGetCommand()
	cmd.SetArgs([]string{"--output", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "image bytes" {
		t.Fatalf("download: %q %v", data, err)
	}
}

func TestUserAvatarGetDownloadsBytesAndRejectsTraversal(t *testing.T) {
	guardCLIState(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/users/user1/avatar" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("avatar bytes"))
	}))
	defer server.Close()
	setStubCLI(t, server.URL)
	path := filepath.Join(t.TempDir(), "avatar")
	cmd := newUserAvatarGetCommand()
	cmd.SetArgs([]string{"user1", "--output", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "avatar bytes" {
		t.Fatalf("download: %q %v", data, err)
	}
	cmd = newUserAvatarGetCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"..", "--output", path})
	if err := cmd.Execute(); err == nil || calls != 1 {
		t.Fatalf("traversal: error=%v calls=%d", err, calls)
	}
}
