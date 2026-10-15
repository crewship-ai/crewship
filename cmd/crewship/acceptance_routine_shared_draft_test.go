package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptanceGuestRoutineDraftCLI(t *testing.T) {
	snapshot := `{"draft_id":"d1","revision":3,"base_revision":2,"name":"Client draft","description":"Reviewed notes","definition":{"steps":[],"name":"Candidate"},"can_edit":true}`
	for _, method := range []string{"GET", "PUT"} {
		for _, format := range []string{"table", "json", "yaml"} {
			t.Run(method+"-"+format, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/workspaces" {
						_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
						return
					}
					calls.Add(1)
					if r.Method != method || r.URL.Path != "/api/v1/workspaces/ws_test/restricted-routine-library/shared/draft" {
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
						return
					}
					if method == "PUT" {
						var body map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 6 || string(body["revision"]) != "3" || string(body["base_revision"]) != "2" || string(body["draft_id"]) != `"d1"` || !strings.Contains(string(body["definition"]), "Candidate") {
							t.Errorf("snapshot changed or read-only controls forwarded: %+v %v", body, err)
						}
					}
					_, _ = io.WriteString(w, snapshot)
				}))
				defer server.Close()
				args := []string{"routine", "shared-draft", "shared", "--format", format}
				if method == "PUT" {
					args = []string{"routine", "shared-draft", "save", "shared", "--file", "-", "--format", format}
				}
				out, err := runGuestCLI(t, server.URL, snapshot, args...)
				if err != nil || calls.Load() != 1 {
					t.Fatalf("calls=%d err=%v output=%s", calls.Load(), err, out)
				}
				if format == "table" {
					for _, column := range []string{"NAME", "DRAFT", "REVISION", "BASE", "EDIT", "Client draft", "d1", "true"} {
						if !strings.Contains(out, column) {
							t.Fatalf("missing %q: %s", column, out)
						}
					}
				} else {
					var body map[string]any
					if format == "json" {
						err = json.Unmarshal([]byte(out), &body)
					} else {
						err = yaml.Unmarshal([]byte(out), &body)
					}
					if err != nil || body["draft_id"] != "d1" || body["definition"].(map[string]any)["name"] != "Candidate" || body["can_edit"] != true {
						t.Fatalf("incomplete snapshot: %+v %v", body, err)
					}
				}
			})
		}
	}
	for _, input := range []string{snapshot, strings.Replace(snapshot, `"revision":3,`, "", 1), snapshot + `{}`, strings.Replace(snapshot, `"can_edit":true`, `"skip_test_gate":true`, 1)} {
		t.Run("conflict-or-invalid-"+input, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				calls.Add(1)
				w.WriteHeader(409)
				_, _ = io.WriteString(w, `{"error":"Draft changed"}`)
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, input, "routine", "shared-draft", "save", "shared", "--file", "-")
			wantCalls := int64(0)
			if input == snapshot {
				wantCalls = 1
			}
			if err == nil || calls.Load() != wantCalls {
				t.Fatalf("invalid/conflicting snapshot retried: calls=%d err=%v output=%s", calls.Load(), err, out)
			}
		})
	}
}
