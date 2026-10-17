package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptanceGuestIssueSearchCLI(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v1/workspaces/ws_test/restricted-issues" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				q := r.URL.Query()
				if q.Get("q") != "Žlutý & %_" || q.Get("project_id") != "p1" || q.Get("limit") != "1" || q.Get("offset") != "1" {
					t.Errorf("wrong search query %v", q)
				}
				_, _ = io.WriteString(w, `[{"id":"i1","identifier":"CLIENT-1","title":"Žlutý & %_","status":"TODO","project_id":"p1","project_name":"Client","can_read":false}]`)
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", "issue", "shared", "--search", "  Žlutý & %_  ", "--project", "p1", "--limit", "1", "--offset", "1", "--format", format)
			if err != nil {
				t.Fatalf("search: %v %s", err, out)
			}
			if calls.Load() != 1 {
				t.Fatalf("%d collection calls; expected one search without retries", calls.Load())
			}
			if format == "table" {
				for _, value := range []string{"ISSUE", "TITLE", "STATUS", "PROJECT", "CLIENT-1", "Žlutý & %_", "Client"} {
					if !strings.Contains(out, value) {
						t.Fatalf("table missing %q: %s", value, out)
					}
				}
				return
			}
			var rows []sharedIssue
			if format == "json" {
				err = json.Unmarshal([]byte(out), &rows)
			} else {
				err = yaml.Unmarshal([]byte(out), &rows)
			}
			if err != nil || len(rows) != 1 || rows[0].Identifier != "CLIENT-1" || rows[0].Title != "Žlutý & %_" || rows[0].CanRead {
				t.Fatalf("wrong projection: %+v %v %s", rows, err, out)
			}
		})
	}
}

func TestAcceptanceGuestIssueSearchRejectsInvalidOptionsLocally(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "shared", "CLIENT-1", "--search", "client"},
		{"issue", "shared", "CLIENT-1", "--search", ""},
		{"issue", "shared", "--search", strings.Repeat("x", 257)},
	} {
		t.Run(strings.Join(args[:3], " "), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", args...)
			if err == nil || !strings.Contains(out, "search") {
				t.Fatalf("invalid search accepted: %v %s", err, out)
			}
			if calls.Load() != 0 {
				t.Fatalf("invalid options sent %d HTTP requests", calls.Load())
			}
		})
	}
}

func TestAcceptanceGuestIssueSearchDoesNotRetryDeniedAudience(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
			return
		}
		calls.Add(1)
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":"Issues unavailable"}`)
	}))
	defer server.Close()
	out, err := runGuestCLI(t, server.URL, "", "issue", "shared", "--search", "client", "--project", "removed-project")
	if err == nil || !strings.Contains(out, "Issues unavailable") || calls.Load() != 1 {
		t.Fatalf("denied search: %v calls=%d %s", err, calls.Load(), out)
	}
}

func TestAcceptanceGuestIssueSearchRateLimitGuidance(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workspaces" {
			io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
			return
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":"Shared issue searches are temporarily limited. Try again in 3s."}`)
			return
		}
		io.WriteString(w, `[]`)
	}))
	defer server.Close()
	out, err := runGuestCLI(t, server.URL, "", "issue", "shared", "--search", "client")
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 6 || !strings.Contains(out, "Try again in 3s.") {
		t.Fatalf("wrong retry guidance/exit: %v %s", err, out)
	}
	if calls.Load() != 1 {
		t.Fatalf("CLI automatically retried429: %d", calls.Load())
	}
	if strings.Contains(out, "fake-token") {
		t.Fatal("token leaked in retry guidance")
	}
	out, err = runGuestCLI(t, server.URL, "", "issue", "shared", "--search", "client")
	if err != nil || calls.Load() != 2 {
		t.Fatalf("explicit retry after server recovery: %v %s calls%d", err, out, calls.Load())
	}
}
