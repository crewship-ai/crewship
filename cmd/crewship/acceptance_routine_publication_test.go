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
