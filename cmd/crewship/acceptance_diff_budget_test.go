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

func TestAcceptance_DiffBudgetRefusals(t *testing.T) {
	for _, command := range []struct {
		name, path string
		args       []string
	}{
		{"versions", "/api/v1/workspaces/cownednightdiffworkspace1/pipelines/owned-routine/diff", []string{"routine", "diff", "owned-routine", "--from", "1", "--to", "2"}},
		{"proposal", "/api/v1/consolidate/proposed/owned-proposal/diff", []string{"consolidate", "proposed", "diff", "owned-proposal"}},
		{"approve-preview", "/api/v1/consolidate/proposed/owned-proposal/diff", []string{"consolidate", "proposed", "approve", "owned-proposal", "--diff", "--yes"}},
	} {
		for _, tc := range []struct {
			status, exit int
			message      string
		}{
			{413, cli.ExitGeneric, "diff is too large; view the inputs separately"},
			{429, cli.ExitRateLimited, "diff service is busy; retry shortly"},
		} {
			t.Run(fmt.Sprintf("%s/%d", command.name, tc.status), func(t *testing.T) {
				var calls, posts atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method == "POST" {
						posts.Add(1)
					}
					if r.Method != "GET" || r.URL.Path != command.path || r.Header.Get("Authorization") != "Bearer owned-cli-token" || r.URL.Query().Get("workspace_id") != "cownednightdiffworkspace1" {
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					}
					if command.name == "versions" && (r.URL.Query().Get("from") != "1" || r.URL.Query().Get("to") != "2") {
						t.Error("version query changed")
					}
					w.Header().Set("Content-Type", "application/json")
					if tc.status == 429 {
						w.Header().Set("Retry-After", "1")
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, fmt.Sprintf(`{"error":%q}`, tc.message))
				}))
				defer srv.Close()
				cfg := filepath.Join(t.TempDir(), "cli.yaml")
				if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: owned-cli-token\nworkspace: cownednightdiffworkspace1\nformat: json\n"), 0600); err != nil {
					t.Fatal(err)
				}
				out, err := runFeedbackCLI(t, cfg, command.args...)
				exit := 0
				if err != nil {
					failure, ok := err.(*exec.ExitError)
					if !ok {
						t.Fatal(err)
					}
					exit = failure.ExitCode()
				}
				if exit != tc.exit || !strings.Contains(out, tc.message) || calls.Load() != 1 || posts.Load() != 0 {
					t.Fatalf("exit%d want%d calls%d posts%d output%s", exit, tc.exit, calls.Load(), posts.Load(), out)
				}
			})
		}
	}
}

func TestAcceptance_RoutineRollbackKeepsSuccessWhenDiffRefused(t *testing.T) {
	for _, status := range []int{413, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var versions, posts, diffs atomic.Int32
			prefix := "/api/v1/workspaces/cownednightdiffworkspace1/pipelines/owned-routine"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer owned-cli-token" || r.URL.Query().Get("workspace_id") != "cownednightdiffworkspace1" {
					t.Error("rollback request identity changed")
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == prefix+"/versions":
					versions.Add(1)
					_, _ = io.WriteString(w, `[{"version":2,"is_head":true},{"version":1}]`)
				case r.Method == "POST" && r.URL.Path == prefix+"/rollback":
					posts.Add(1)
					raw, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
					if !strings.Contains(string(raw), `"target_version":1`) {
						t.Error("rollback target changed")
					}
					_, _ = io.WriteString(w, `{"version":1}`)
				case r.Method == "GET" && r.URL.Path == prefix+"/diff":
					diffs.Add(1)
					if r.URL.Query().Get("from") != "1" || r.URL.Query().Get("to") != "2" {
						t.Error("rollback diff direction changed")
					}
					if status == 429 {
						w.Header().Set("Retry-After", "1")
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":"diff preview refused"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			cfg := filepath.Join(t.TempDir(), "cli.yaml")
			if e := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: owned-cli-token\nworkspace: cownednightdiffworkspace1\nformat: json\n"), 0600); e != nil {
				t.Fatal(e)
			}
			out, e := runFeedbackCLI(t, cfg, "routine", "rollback", "owned-routine", "--to", "1")
			if e != nil || !strings.Contains(out, "Rolled back owned-routine to v1.") || versions.Load() != 1 || posts.Load() != 1 || diffs.Load() != 1 {
				t.Fatalf("rollback success changed: versions%d posts%d diffs%d error%v output%s", versions.Load(), posts.Load(), diffs.Load(), e, out)
			}
		})
	}
}
