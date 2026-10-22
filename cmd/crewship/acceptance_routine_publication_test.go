package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAcceptanceRoutinePublicationCLI(t *testing.T) {
	for _, action := range []string{"validate", "publish"} {
		for _, status := range []int{200, 409, 422} {
			t.Run(action+http.StatusText(status), func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/workspaces" {
						_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
						return
					}
					calls.Add(1)
					if r.Method != "POST" || r.URL.Path != "/api/v1/workspaces/ws_test/restricted-routine-library/shared/draft/"+action {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 3 || body["draft_id"] != "d1" || body["revision"] != float64(3) || body["base_revision"] != float64(2) {
						t.Errorf("changed reviewed snapshot: %+v %v", body, err)
					}
					w.WriteHeader(status)
					if status != 200 {
						_, _ = io.WriteString(w, `{"error":"Reviewed snapshot refused"}`)
					} else if action == "validate" {
						_, _ = io.WriteString(w, `{"valid":true,"scope":"structure_only","draft_id":"d1","revision":3,"base_revision":2}`)
					} else {
						_, _ = io.WriteString(w, `{"id":"shared","version":4,"publication_revision":3}`)
					}
				}))
				defer server.Close()
				out, err := runGuestCLI(t, server.URL, "", "routine", "draft", action, "shared", "--draft-id", "d1", "--revision", "3", "--base-revision", "2", "--format", "json")
				if calls.Load() != 1 || (err != nil) != (status != 200) {
					t.Fatalf("calls=%d err=%v output=%s", calls.Load(), err, out)
				}
				if status == 200 && action == "validate" && !strings.Contains(out, "structure_only") {
					t.Fatalf("validation must explain its limited scope: %s", out)
				}
			})
		}
	}
}

func TestAcceptanceRoutinePublicationRequiresReviewedReference(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer server.Close()
	for _, flags := range [][]string{
		{"--draft-id", "d1"},
		{"--draft-id", "d1", "--revision", "0", "--base-revision", "2"},
		{"--draft-id", "d1", "--revision", "3", "--base-revision", "-1"},
	} {
		for _, action := range []string{"validate", "publish"} {
			out, err := runGuestCLI(t, server.URL, "", append([]string{"routine", "draft", action, "shared"}, flags...)...)
			if err == nil || calls.Load() != 0 {
				t.Fatalf("incomplete reference reached server: err=%v calls=%d output=%s", err, calls.Load(), out)
			}
		}
	}
}

// A Guest-published routine runs through the existing restricted admission;
// the binary waits on the private receipt and fails on a stopped run.
func TestAcceptancePublishedRoutineRunCLI(t *testing.T) {
	for _, outcome := range []string{"completed", "stopped", "delayed"} {
		t.Run(outcome, func(t *testing.T) {
			var runs atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v1/workspaces":
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
				case r.Method == "POST" && r.URL.Path == "/api/v1/workspaces/ws_test/pipelines/shared/run":
					runs.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("run body: %v", err)
					}
					if inputs, _ := body["inputs"].(map[string]any); inputs["task"] != "report" {
						t.Errorf("inputs not forwarded: %+v", body)
					}
					if outcome == "delayed" {
						w.WriteHeader(422)
						_, _ = io.WriteString(w, `{"error":"Published routines start immediately; delay_seconds is not supported"}`)
						return
					}
					w.WriteHeader(202)
					_, _ = io.WriteString(w, `{"run_id":"job-1","chat_id":"c1","status":"SCHEDULED","restricted":true,"status_url":"/api/v1/workspaces/ws_test/restricted-routine-runs/job-1"}`)
				case r.Method == "GET" && r.URL.Path == "/api/v1/workspaces/ws_test/restricted-routine-runs/job-1":
					if outcome == "stopped" {
						w.WriteHeader(404)
						_, _ = io.WriteString(w, `{"error":"workflow unavailable"}`)
						return
					}
					_, _ = io.WriteString(w, `{"run_id":"job-1","status":"completed","step_outputs":{"first":"PUBLISHED_OUTPUT"},"created_at":"2026-10-22T12:00:00Z"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			args := []string{"routine", "run", "shared", "--inputs", `{"task":"report"}`, "--wait", "--format", "json"}
			if outcome == "delayed" {
				args = append(args, "--delay", "60")
			}
			out, err := runGuestCLI(t, server.URL, "", args...)
			if runs.Load() != 1 {
				t.Fatalf("run requests=%d output=%s", runs.Load(), out)
			}
			if outcome == "completed" {
				if err != nil || !strings.Contains(out, "PUBLISHED_OUTPUT") {
					t.Fatalf("completed run: err=%v output=%s", err, out)
				}
				return
			}
			if err == nil || strings.Contains(out, "PUBLISHED_OUTPUT") {
				t.Fatalf("%s run must fail without a result: output=%s", outcome, out)
			}
		})
	}
}
