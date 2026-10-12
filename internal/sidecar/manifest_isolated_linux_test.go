//go:build linux && sidecar_isolated

package sidecar

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func isolatedManifestFixture(t *testing.T) *Server {
	t.Helper()
	if os.Getenv("CREWSHIP_MANIFEST_ISOLATED_TEST") != "1" || os.Geteuid() != 1002 {
		t.Fatal("requires owned isolated container and sidecar UID")
	}
	if err := os.RemoveAll(manifestPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(manifestPath), 0700); _ = os.RemoveAll(manifestPath) })
	return &Server{}
}

func manifestRequest(s *Server, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/manifest", strings.NewReader(body))
	if method == http.MethodGet {
		s.handleGetManifest(w, r)
	} else {
		s.handleUpdateManifest(w, r)
	}
	return w
}

func TestIsolatedManifestMergesIdempotentlyAndNeverLosesConcurrentUpdates(t *testing.T) {
	s := isolatedManifestFixture(t)
	w := manifestRequest(s, http.MethodGet, "")
	var initial CrewManifest
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.Version != 1 {
		t.Fatalf("missing manifest default: %d %s", w.Code, w.Body.String())
	}
	patch := `{"packages":{"apt":["curl"],"npm":["tool"],"pip":["parser"]},"credentials":[{"name":"synthetic","agent":"writer","type":"token"}],"setup_commands":["echo synthetic"]}`
	for i := 0; i < 2; i++ {
		if got := manifestRequest(s, http.MethodPost, patch); got.Code != 200 {
			t.Fatalf("patch refused: %d %s", got.Code, got.Body.String())
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"packages":{"apt":["package-%d"]},"setup_commands":["echo %d"]}`, i, i)
			if got := manifestRequest(s, http.MethodPost, body); got.Code != 200 {
				t.Errorf("concurrent update refused: %d", got.Code)
			}
		}(i)
	}
	wg.Wait()
	got, err := readManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages.Apt) != 21 || len(got.Packages.Npm) != 1 || len(got.Packages.Pip) != 1 || len(got.Credentials) != 1 || len(got.SetupCommands) != 21 {
		t.Fatalf("lost or duplicated additions: %+v", got)
	}
	if got.Credentials[0].Name != "synthetic" || got.Credentials[0].Agent != "writer" {
		t.Fatalf("credential metadata changed: %+v", got.Credentials)
	}
	entries, err := filepath.Glob("/crew/.manifest.tmp*")
	if err != nil || len(entries) != 0 {
		t.Fatalf("atomic update leaked temporary manifests: %v %v", entries, err)
	}
}

func TestIsolatedManifestStorageRefusalsPreserveExistingBytes(t *testing.T) {
	for _, mode := range []string{"malformed JSON", "unreadable file", "directory", "read-only directory"} {
		t.Run(mode, func(t *testing.T) {
			s := isolatedManifestFixture(t)
			original := []byte(`{"version":7,"setup_commands":["keep"]}`)
			if mode == "malformed JSON" {
				original = []byte("not-json")
			}
			if mode == "directory" {
				if err := os.Mkdir(manifestPath, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(manifestPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "unreadable file" {
				if err := os.Chmod(manifestPath, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(manifestPath, 0600)
			}
			if mode == "read-only directory" {
				if err := os.Chmod("/crew", 0500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod("/crew", 0700)
			}
			if w := manifestRequest(s, http.MethodPost, `{"setup_commands":["new"]}`); w.Code != 500 {
				t.Fatalf("storage refusal reported success: %d %s", w.Code, w.Body.String())
			}
			if mode != "read-only directory" {
				if w := manifestRequest(s, http.MethodGet, ""); w.Code != 500 {
					t.Fatalf("invalid stored manifest returned success: %d", w.Code)
				}
			}
			if mode == "unreadable file" {
				if err := os.Chmod(manifestPath, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "directory" {
				raw, err := os.ReadFile(manifestPath)
				if err != nil || string(raw) != string(original) {
					t.Fatalf("refused update changed original: %q %v", raw, err)
				}
			}
		})
	}
}

func TestIsolatedManifestRejectsInvalidInputAndCleansRenameFailure(t *testing.T) {
	s := isolatedManifestFixture(t)
	if w := manifestRequest(s, http.MethodPost, "not-json"); w.Code != 400 {
		t.Fatalf("invalid JSON accepted: %d", w.Code)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("invalid request published file: %v", err)
	}
	if err := os.Mkdir(manifestPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(&CrewManifest{Version: 1}); err == nil {
		t.Fatal("directory replaced by manifest")
	}
	entries, err := filepath.Glob("/crew/.manifest.tmp*")
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed rename leaked temporary file: %v %v", entries, err)
	}
}
