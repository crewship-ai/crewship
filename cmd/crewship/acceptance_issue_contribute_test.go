package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptanceGuestIssueContributionCLI(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		method, path string
		body         map[string]any
		conflict     bool
		columns      []string
	}{
		{"projects", []string{"issue", "shared", "projects"}, "GET", "/restricted-issue-projects", nil, false, []string{"PROJECT", "CONTRIBUTE", "INTAKE READY", "Client project"}},
		{"create", []string{"issue", "shared", "create", "--project", "p1", "--title", "Client request", "--description", "Brief", "--format", "json"}, "POST", "/restricted-issues", map[string]any{"project_id": "p1", "title": "Client request", "description": "Brief"}, false, nil},
		{"comment", []string{"issue", "shared", "comment", "CLIENT-1", "--body", "Client question", "--format", "yaml"}, "POST", "/restricted-issues/CLIENT-1/comments", map[string]any{"body": "Client question"}, false, nil},
		{"comments", []string{"issue", "shared", "comments", "CLIENT-1"}, "GET", "/restricted-issues/CLIENT-1/comments", nil, false, []string{"COMMENT", "AUTHOR", "BODY", "Client question"}},
		{"move", []string{"issue", "shared", "move", "CLIENT-1", "--status", "TODO", "--format", "json"}, "PATCH", "/restricted-issues/CLIENT-1", map[string]any{"status": "TODO"}, false, nil},
		{"intake read", []string{"project", "guest-intake", "p1", "--format", "json"}, "GET", "/projects/p1/guest-intake", nil, false, nil},
		{"intake save", []string{"project", "guest-intake", "p1", "--crew", "crew-2", "--format", "yaml"}, "PUT", "/projects/p1/guest-intake", map[string]any{"crew_id": "crew-2", "revision": float64(7)}, false, nil},
		{"intake conflict is not retried", []string{"project", "guest-intake", "p1", "--crew", "crew-2"}, "PUT", "/projects/p1/guest-intake", map[string]any{"crew_id": "crew-2", "revision": float64(7)}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				path := strings.TrimPrefix(r.URL.Path, "/api/v1/workspaces/ws_test")
				if path == tc.path && r.Method == tc.method {
					calls++
					if tc.body != nil {
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
					}
					if tc.conflict {
						w.WriteHeader(409)
						_, _ = io.WriteString(w, `{"error":"Intake changed"}`)
						return
					}
				} else if !(strings.HasSuffix(path, "/guest-intake") && r.Method == "GET" && tc.method == "PUT") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				switch {
				case strings.HasSuffix(path, "/guest-intake"):
					revision, crew := 7, "crew-1"
					if r.Method == "PUT" {
						revision, crew = 8, "crew-2"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"project_id": "p1", "crew_id": crew, "crew_name": "Intake crew", "revision": revision})
				case strings.HasSuffix(path, "restricted-issue-projects"):
					_, _ = io.WriteString(w, `[{"id":"p1","name":"Client project","can_contribute":true,"intake_ready":true}]`)
				case strings.HasSuffix(path, "/comments"):
					comment := `{"id":"c1","body":"Client question","author":"You","created_at":"2026-10-14T22:00:00Z"}`
					if r.Method == "GET" {
						comment = "[" + comment + "]"
					} else {
						w.WriteHeader(201)
					}
					_, _ = io.WriteString(w, comment)
				default:
					if r.Method == "POST" {
						w.WriteHeader(201)
					}
					_, _ = io.WriteString(w, `{"id":"i1","identifier":"CLIENT-1","title":"Client request","status":"TODO","project_id":"p1","project_name":"Client project","can_read":true,"can_contribute":true}`)
				}
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", tc.args...)
			if (err != nil) != tc.conflict {
				t.Fatalf("err=%v output=%s", err, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Fatalf("request count %d, expected exactly one: %s", calls, out)
			}
			if tc.body != nil {
				a, _ := json.Marshal(body)
				b, _ := json.Marshal(tc.body)
				if string(a) != string(b) {
					t.Fatalf("request body %s want %s", a, b)
				}
			}
			for _, column := range tc.columns {
				if !strings.Contains(out, column) {
					t.Errorf("missing %q in %s", column, out)
				}
			}
			if strings.Contains(tc.name, "save") || tc.name == "comment" {
				var document map[string]any
				if err := yaml.Unmarshal([]byte(out), &document); err != nil {
					t.Fatal(err, out)
				}
				key := "body"
				if strings.Contains(tc.name, "save") {
					key = "crew_id"
				}
				if _, ok := document[key]; !ok {
					t.Fatalf("missing YAML key %s: %s", key, out)
				}
			}
		})
	}
}

func TestAcceptanceGuestIssueContributionRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{
		{"issue", "shared", "create", "--project", "p1"},
		{"issue", "shared", "comment", "..", "--body", "Question"},
		{"issue", "shared", "move", "CLIENT-1", "--status", "DONE"},
		{"project", "guest-intake", ".."},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); calls++; mu.Unlock(); w.WriteHeader(500) }))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", args...)
			if err == nil {
				t.Fatalf("invalid command succeeded: %s", out)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 0 {
				t.Fatalf("invalid command made %d requests", calls)
			}
		})
	}
}
