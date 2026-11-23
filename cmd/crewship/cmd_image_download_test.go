package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestUserAvatarGetRejectsDotSegmentBeforeRequest(t *testing.T) {
	guardCLIState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("request sent: %s", r.URL.Path) }))
	defer server.Close()
	setStubCLI(t, server.URL)
	cmd := newUserAvatarGetCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"..", "--output", filepath.Join(t.TempDir(), "a")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid resource identifier") {
		t.Fatalf("dot segment: %v", err)
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

func TestImageDownloadPreservesDestinationForIncompleteBody(t *testing.T) {
	commands := []struct {
		name       string
		newCommand func() *cobra.Command
		args       []string
	}{
		{name: "avatar", newCommand: newUserAvatarGetCommand, args: []string{"user1"}},
		{name: "logo", newCommand: newWorkspaceLogoGetCommand},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			for _, oversized := range []bool{true, false} {
				failure := "truncated"
				if oversized {
					failure = "oversized"
				}
				t.Run(failure, func(t *testing.T) {
					guardCLIState(t)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if oversized {
							_, _ = w.Write(bytes.Repeat([]byte("x"), maxImageDownload+1))
							return
						}
						w.Header().Set("Content-Length", "100")
						_, _ = io.WriteString(w, "incomplete")
					}))
					t.Cleanup(server.Close)
					setStubCLI(t, server.URL)
					dir := t.TempDir()
					output := filepath.Join(dir, "image.png")
					if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
						t.Fatal(err)
					}
					cmd := command.newCommand()
					cmd.SilenceErrors, cmd.SilenceUsage = true, true
					cmd.SetArgs(append(append([]string{}, command.args...), "--output", output))
					err := cmd.Execute()
					if oversized && (err == nil || !strings.Contains(err.Error(), "exceeds 2 MiB")) {
						t.Fatalf("expected size limit error, got %v", err)
					}
					if !oversized && !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("expected interrupted response error, got %v", err)
					}
					data, err := os.ReadFile(output)
					if err != nil || string(data) != "existing" {
						t.Fatalf("destination changed: %q %v", data, err)
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 1 {
						t.Fatalf("temporary output left behind: %v (%v)", entries, err)
					}
				})
			}
		})
	}
}
