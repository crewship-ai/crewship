package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestAcceptance_PublicPageUnlockAnonymousCapacity(t *testing.T) {
	for _, tc := range []struct {
		status, exit   int
		body, expected string
	}{
		{200, 0, `{"slug":"owned-public-page"}`, "owned-public-page"},
		{401, cli.ExitAuth, `{"error":"that link and password do not match"}`, "that link and password do not match"},
		{429, cli.ExitRateLimited, `{"error":"public page password checks are busy; retry shortly"}`, "public page password checks are busy"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				raw, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/api/v1/public/pages/owned-synthetic-token/unlock" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Workspace-ID") != "" || string(raw) != `{"password":"owned-synthetic-password"}` {
					t.Errorf("unexpected anonymous request method=%s path=%s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status == 429 {
					w.Header().Set("Retry-After", "1")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			dir := t.TempDir()
			cfg := filepath.Join(dir, "cli.yaml")
			input := filepath.Join(dir, "unlock.json")
			if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: owned-should-not-be-sent\nworkspace: owned-no-workspace\nformat: json\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, []byte(`{"password":"owned-synthetic-password"}`), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := runFeedbackCLI(t, cfg, "api", "request", "POST", "/api/v1/public/pages/owned-synthetic-token/unlock", "--anonymous", "--input", input, "--yes")
			code := 0
			if err != nil {
				e, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = e.ExitCode()
			}
			if code != tc.exit || !strings.Contains(out, tc.expected) || calls.Load() != 1 {
				t.Fatalf("exit=%d want%d requests%d output=%s", code, tc.exit, calls.Load(), out)
			}
		})
	}
}
