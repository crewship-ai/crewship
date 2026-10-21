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

func TestAcceptanceGuestIssueDeletionCLI(t *testing.T) {
	version := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		args     []string
		method   string
		conflict bool
	}{
		{"preview columns", []string{"issue", "shared-delete-preview", "CLIENT-1"}, "GET", false},
		{"preview json", []string{"issue", "shared-delete-preview", "CLIENT-1", "--format", "json"}, "GET", false},
		{"preview yaml", []string{"issue", "shared-delete-preview", "CLIENT-1", "--format", "yaml"}, "GET", false},
		{"delete json", []string{"issue", "shared-delete", "CLIENT-1", "--project", "p1", "--version", version, "--confirm", "--format", "json"}, "DELETE", false},
		{"delete yaml", []string{"issue", "shared-delete", "CLIENT-1", "--project", "p1", "--version", version, "--confirm", "--format", "yaml"}, "DELETE", false},
		{"conflict not retried", []string{"issue", "shared-delete", "CLIENT-1", "--project", "p1", "--version", version, "--confirm"}, "DELETE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				path := "/api/v1/workspaces/ws_test/restricted-issues/CLIENT-1"
				if tc.method == "GET" {
					path += "/deletion"
				}
				if r.Method != tc.method || r.URL.Path != path {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				mu.Lock()
				calls++
				mu.Unlock()
				if tc.method == "DELETE" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 3 || body["project_id"] != "p1" || body["version"] != version || body["confirm"] != true {
						t.Errorf("wrong reviewed deletion %#v", body)
					}
					if tc.conflict {
						w.WriteHeader(409)
						_, _ = io.WriteString(w, `{"error":"Issue changed"}`)
						return
					}
					w.WriteHeader(204)
					return
				}
				_ = json.NewEncoder(w).Encode(sharedIssueDeletion{ID: "i1", Identifier: "CLIENT-1", Title: "Client request", ProjectID: "p1", Status: "BACKLOG", Version: version, CanDelete: true})
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", tc.args...)
			if (err != nil) != tc.conflict {
				t.Fatalf("error=%v output=%s", err, out)
			}
			mu.Lock()
			count := calls
			mu.Unlock()
			if count != 1 {
				t.Fatalf("%d requests: must not fetch a replacement version or retry: %s", count, out)
			}
			if tc.conflict {
				if !strings.Contains(out, "Issue changed") {
					t.Fatal(out)
				}
				return
			}
			if tc.name == "preview columns" {
				for _, value := range []string{"ISSUE", "PROJECT", "STATUS", "DELETE", "VERSION", "CLIENT-1", "BACKLOG", version} {
					if !strings.Contains(out, value) {
						t.Fatalf("missing %q: %s", value, out)
					}
				}
				return
			}
			var body map[string]any
			if strings.HasSuffix(tc.name, "json") {
				err = json.Unmarshal([]byte(out), &body)
			} else {
				err = yaml.Unmarshal([]byte(out), &body)
			}
			if err != nil {
				t.Fatal(err, out)
			}
			if tc.method == "DELETE" {
				if body["deleted"] != true {
					t.Fatal(body)
				}
			} else if body["can_delete"] != true || body["project_id"] != "p1" || body["version"] != version {
				t.Fatal(body)
			}
		})
	}
	t.Run("confirmation required before HTTP", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/workspaces" {
				_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
				return
			}
			calls++
			w.WriteHeader(500)
		}))
		defer server.Close()
		out, err := runGuestCLI(t, server.URL, "", "issue", "shared-delete", "CLIENT-1", "--project", "p1", "--version", version)
		if err == nil || !strings.Contains(out, "--confirm") || calls != 0 {
			t.Fatalf("err=%v calls=%d out=%s", err, calls, out)
		}
	})
}
