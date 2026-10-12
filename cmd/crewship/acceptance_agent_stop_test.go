package main

// Acceptance for `crewship agent stop`, driven through the BUILT BINARY
// (#2879). The stop route answers success for an agent that was already
// idle, and a stable `code` when it cannot confirm the agent is stopped;
// the CLI's printed line and exit code are the contract agents read.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func agentStopStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	const id = "agent_1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_, _ = w.Write([]byte(`[{"id":"` + id + `","slug":"viktor","crew_id":"crew_1"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/"+id+"/stop":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_AgentStop_Outcomes(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantExit bool
		want     []string
		reject   []string
	}{
		{
			name:   "stopped",
			status: 200, body: `{"id":"agent_1","status":"STOPPED","outcome":"stopped"}`,
			want:   []string{"Agent viktor stopped."},
			reject: []string{"was not running"},
		},
		{
			name:   "already_stopped",
			status: 200, body: `{"id":"agent_1","status":"STOPPED","outcome":"already_stopped"}`,
			want:   []string{"Agent viktor was not running; nothing to stop."},
			reject: []string{"Agent viktor stopped."},
		},
		{
			name:   "server_without_outcome",
			status: 200, body: `{"id":"agent_1","status":"STOPPED"}`,
			want: []string{"Agent viktor stopped."},
		},
		{
			name:   "stop_not_confirmed",
			status: 502, body: `{"error":"runtime stop not confirmed","code":"stop_not_confirmed"}`,
			wantExit: true,
			want:     []string{"runtime stop not confirmed", "may still be running", "crewship agent stop viktor"},
		},
		{
			name:   "runtime_unavailable",
			status: 502, body: `{"error":"runtime stop unavailable","code":"runtime_unavailable"}`,
			wantExit: true,
			want:     []string{"runtime stop unavailable", "could not be reached", "crewship doctor"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := agentStopStub(t, tc.status, tc.body)
			out, err := runIssueStopCLI(t, srv.URL, "agent", "stop", "viktor")
			if tc.wantExit {
				if err == nil || exitCodeOf(t, err) == 0 {
					t.Fatalf("want non-zero exit, got %v\noutput: %s", err, out)
				}
			} else if err != nil {
				t.Fatalf("agent stop: %v\noutput: %s", err, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output %q missing %q", out, w)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(out, r) {
					t.Errorf("output %q must not contain %q", out, r)
				}
			}
		})
	}
}
