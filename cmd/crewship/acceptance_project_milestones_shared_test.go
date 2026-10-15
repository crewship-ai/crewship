package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptanceGuestProjectMilestoneCLI(t *testing.T) {
	version := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name           string
		args           []string
		method, suffix string
		body           map[string]any
		conflict       bool
	}{
		{"list columns", []string{"project", "shared", "milestones", "p1", "--limit", "2", "--offset", "1"}, "GET", "", nil, false},
		{"read json", []string{"project", "shared", "milestones", "p1", "m1", "--format", "json"}, "GET", "/m1", nil, false},
		{"create yaml", []string{"project", "shared", "milestones", "create", "p1", "--name", "Release", "--description", "Client brief", "--target-date", "2026-11-01", "--format", "yaml"}, "POST", "", map[string]any{"name": "Release", "description": "Client brief", "target_date": "2026-11-01", "status": "active"}, false},
		{"update json", []string{"project", "shared", "milestones", "update", "p1", "m1", "--name", "Release", "--description", "Client brief", "--target-date", "", "--status", "completed", "--position", "2", "--version", version, "--format", "json"}, "PUT", "/m1", map[string]any{"name": "Release", "description": "Client brief", "target_date": "", "status": "completed", "position": float64(2), "version": version}, false},
		{"conflict not retried", []string{"project", "shared", "milestones", "delete", "p1", "m1", "--version", version, "--confirm"}, "DELETE", "/m1", map[string]any{"version": version}, true},
		{"delete yaml", []string{"project", "shared", "milestones", "delete", "p1", "m1", "--version", version, "--confirm", "--format", "yaml"}, "DELETE", "/m1", map[string]any{"version": version}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			item := sharedProjectMilestone{ID: "m1", ProjectID: "p1", Name: "Release", Description: "Client brief", TargetDate: "2026-11-01", Status: "active", Position: 2, Version: version, CanManage: true, CanDelete: true}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				expected := "/api/v1/workspaces/ws_test/restricted-projects/p1/milestones" + tc.suffix
				if r.URL.Path != expected || r.Method != tc.method {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				mu.Lock()
				calls++
				mu.Unlock()
				if tc.name == "list columns" && (r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("offset") != "1") {
					t.Error("pagination missing")
				}
				if tc.body != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body, tc.body) {
						t.Errorf("body=%#v want %#v", body, tc.body)
					}
				} else {
					raw, _ := io.ReadAll(r.Body)
					if len(raw) != 0 {
						t.Errorf("GET sent a body: %q", raw)
					}
				}
				if tc.conflict {
					w.WriteHeader(409)
					_, _ = io.WriteString(w, `{"error":"Milestone changed"}`)
					return
				}
				if tc.method == "DELETE" {
					w.WriteHeader(204)
					return
				}
				if tc.method == "POST" {
					w.WriteHeader(201)
				}
				if tc.name == "list columns" {
					_ = json.NewEncoder(w).Encode([]sharedProjectMilestone{item})
				} else {
					_ = json.NewEncoder(w).Encode(item)
				}
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
				t.Fatalf("request count=%d: hidden refresh or retry", count)
			}
			if tc.conflict {
				if !strings.Contains(out, "Milestone changed") {
					t.Fatal(out)
				}
				return
			}
			if tc.name == "list columns" {
				for _, column := range []string{"MILESTONE", "TARGET", "POSITION", "MANAGE", "DELETE", "VERSION", "Release"} {
					if !strings.Contains(out, column) {
						t.Fatalf("missing %q: %s", column, out)
					}
				}
				return
			}
			var result map[string]any
			if strings.Contains(tc.name, "yaml") {
				err = yaml.Unmarshal([]byte(out), &result)
			} else {
				err = json.Unmarshal([]byte(out), &result)
			}
			if err != nil {
				t.Fatal(err, out)
			}
			if tc.method == "DELETE" {
				if result["deleted"] != true {
					t.Fatal(result)
				}
				return
			}
			if result["project_id"] != "p1" || result["can_manage"] != true || result["version"] != version {
				t.Fatal("machine shape mismatch", result)
			}
		})
	}
	t.Run("delete requires explicit confirmation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unconfirmed deletion reached HTTP: %s", r.URL.Path)
			w.WriteHeader(500)
		}))
		defer server.Close()
		out, err := runGuestCLI(t, server.URL, "", "project", "shared", "milestones", "delete", "p1", "m1", "--version", version)
		if err == nil || !strings.Contains(out, "--confirm") {
			t.Fatal(err, out)
		}
	})
}
