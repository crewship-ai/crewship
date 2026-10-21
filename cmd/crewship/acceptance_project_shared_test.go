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

func TestAcceptanceGuestSharedProjectDetailsCLI(t *testing.T) {
	version := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		args     []string
		method   string
		conflict bool
	}{
		{"read json", []string{"project", "shared", "p1", "--format", "json"}, "GET", false},
		{"read yaml", []string{"project", "shared", "p1", "--format", "yaml"}, "GET", false},
		{"read columns", []string{"project", "shared", "p1"}, "GET", false},
		{"update", []string{"project", "shared", "update", "p1", "--name", "Client name", "--description", "Client brief", "--version", version, "--format", "yaml"}, "PUT", false},
		{"conflict not retried", []string{"project", "shared", "update", "p1", "--name", "Client name", "--description", "Client brief", "--version", version}, "PUT", true},
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
				if r.URL.Path != "/api/v1/workspaces/ws_test/restricted-projects/p1" || r.Method != tc.method {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				mu.Lock()
				calls++
				mu.Unlock()
				if r.Method == "PUT" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 3 || body["name"] != "Client name" || body["description"] != "Client brief" || body["version"] != version {
						t.Errorf("incorrect reviewed update: %#v", body)
					}
				}
				if tc.conflict {
					w.WriteHeader(409)
					_, _ = io.WriteString(w, `{"error":"Project details changed"}`)
					return
				}
				_ = json.NewEncoder(w).Encode(sharedProjectDetails{ID: "p1", Name: "Client name", Description: "Client brief", Version: version, CanManage: true})
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", tc.args...)
			if (err != nil) != tc.conflict {
				t.Fatalf("err=%v output=%s", err, out)
			}
			mu.Lock()
			count := calls
			mu.Unlock()
			if count != 1 {
				t.Fatalf("requests=%d; update must not fetch a fresh version or retry: %s", count, out)
			}
			if tc.conflict {
				if !strings.Contains(out, "Project details changed") {
					t.Fatal(out)
				}
				return
			}
			if tc.name == "read columns" {
				for _, column := range []string{"PROJECT", "MANAGE", "VERSION", "DESCRIPTION", "Client name"} {
					if !strings.Contains(out, column) {
						t.Fatalf("missing column %q: %s", column, out)
					}
				}
				return
			}
			var result map[string]any
			if strings.Contains(tc.name, "json") {
				err = json.Unmarshal([]byte(out), &result)
			} else {
				err = yaml.Unmarshal([]byte(out), &result)
			}
			if err != nil || result["can_manage"] != true || result["name"] != "Client name" || result["version"] != version {
				t.Fatalf("machine response: %#v, %v, %s", result, err, out)
			}
		})
	}
}
