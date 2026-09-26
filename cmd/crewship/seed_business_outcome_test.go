package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestBusinessVerifierRejectsCompletedRunWithFailedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"success", `{"status":"completed","outcome":"SUCCEEDED"}`, false},
		{"missing handoff", `{"status":"completed","outcome":"FAILED","error_message":"no outcome reported"}`, true},
		{"missing outcome", `{"status":"completed"}`, true},
		{"contradictory error", `{"status":"completed","outcome":"SUCCEEDED","error_message":"write failed"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/workspaces/"+covWSCli7+"/pipeline-runs/run-1" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			err := verifyBusinessOutcome(cli.NewClient(s.URL, "test-token", covWSCli7), "run-1")
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}
