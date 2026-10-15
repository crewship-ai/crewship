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

func TestAcceptanceRoutineDraftReviewCLI(t *testing.T) {
	snapshot := `{"draft_id":"d1","revision":3,"base_revision":2,"name":"Shared","description":"Notes","definition":{"steps":[]},"can_edit":true}`
	receipt := `{"id":"review1","routine_id":"r1","draft_id":"d1","revision":3,"base_revision":2,"state":"pending","published_version":0}`
	for _, action := range []string{"request-review", "review-status", "review-status-id"} {
		for _, format := range []string{"table", "json", "yaml"} {
			t.Run(action+"-"+format, func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/workspaces" {
						_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
						return
					}
					calls.Add(1)
					method, path := "GET", "/api/v1/workspaces/ws_test/restricted-routine-library/shared/draft/reviews"
					if action == "request-review" {
						method = "POST"
					}
					if action == "review-status-id" {
						path += "/review1"
					}
					if r.Method != method || r.URL.Path != path {
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
						return
					}
					if method == "POST" {
						var body map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 3 || string(body["draft_id"]) != `"d1"` || string(body["revision"]) != "3" || string(body["base_revision"]) != "2" {
							t.Errorf("unexpected review body %+v %v", body, err)
						}
						w.WriteHeader(202)
					}
					_, _ = io.WriteString(w, receipt)
				}))
				defer server.Close()
				args := []string{"routine", "shared-draft", "review-status", "shared", "--format", format}
				if action == "request-review" {
					args = []string{"routine", "shared-draft", action, "shared", "--file", "-", "--format", format}
				}
				if action == "review-status-id" {
					args = append(args, "review1")
				}
				out, err := runGuestCLI(t, server.URL, snapshot, args...)
				if err != nil || calls.Load() != 1 {
					t.Fatalf("calls=%d err=%v output=%s", calls.Load(), err, out)
				}
				if format == "table" {
					for _, cell := range []string{"REVIEW", "DRAFT", "REVISION", "BASE", "STATE", "PUBLISHED VERSION", "review1", "d1", "pending"} {
						if !strings.Contains(out, cell) {
							t.Errorf("missing table cell %q in %s", cell, out)
						}
					}
				} else {
					var body map[string]any
					if format == "json" {
						err = json.Unmarshal([]byte(out), &body)
					} else {
						err = yaml.Unmarshal([]byte(out), &body)
					}
					if err != nil || body["id"] != "review1" || body["state"] != "pending" || body["draft_id"] != "d1" {
						t.Fatalf("invalid receipt %s %v", out, err)
					}
				}
			})
		}
	}
	for _, input := range []string{snapshot, strings.Replace(snapshot, `"revision":3,`, "", 1), snapshot + `{}`, strings.Replace(snapshot, `"can_edit":true`, `"requested_by":"owner"`, 1), strings.Replace(snapshot, `"revision":3`, `"revision":0`, 1)} {
		t.Run("conflict-invalid-"+input, func(t *testing.T) {
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
			out, err := runGuestCLI(t, server.URL, input, "routine", "shared-draft", "request-review", "shared", "--file", "-")
			want := int64(0)
			if input == snapshot {
				want = 1
			}
			if err == nil || calls.Load() != want {
				t.Fatalf("invalid/conflicting request retried calls=%d err=%v out=%s", calls.Load(), err, out)
			}
		})
	}
	for _, body := range []string{"null", strings.Replace(receipt, `"state":"pending"`, `"state":"published"`, 1)} {
		t.Run("status-"+body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/workspaces" {
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					return
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", "routine", "shared-draft", "review-status", "shared", "--format", "json")
			if body == "null" {
				if err != nil || strings.TrimSpace(out) != "null" {
					t.Fatalf("missing latest receipt: %s %v", out, err)
				}
			} else if err == nil {
				t.Fatalf("false publication accepted: %s", out)
			}
		})
	}
}
