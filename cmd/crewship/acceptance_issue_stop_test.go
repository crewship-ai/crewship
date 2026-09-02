package main

// Acceptance for `crewship issue stop`, driven through the BUILT BINARY.
//
// The project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") applies here as much as anywhere:
// Stop's contract changed from an instant cancel to a cooperative one (a
// live run's current step finishes; no further step starts), and the one
// place that promise is user-visible is the CLI's success line. A stubbed
// unit test that asserts against the command's Go code, not the printed
// text, would not catch a help string or success message that quietly
// reverted to the old ("cancels running tasks" / "Stopped %s") wording.
//
// This test asserts three things: the request actually reaches the stop
// route (not a sibling issue endpoint), the success output carries the new
// cooperative wording, and a non-2xx response from the server is reported
// as a CLI error (non-zero exit, error text on output) rather than being
// swallowed.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type issueStopStub struct {
	mu         sync.Mutex
	stopCalls  []string // paths the stop route was hit with
	stopStatus int
}

func (s *issueStopStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/BE-42":
			_, _ = w.Write([]byte(`{
				"id": "iss_1",
				"crew_id": "crew_1",
				"identifier": "BE-42",
				"title": "Refresh token handling",
				"status": "IN_PROGRESS",
				"priority": "MEDIUM",
				"mission_type": "STANDARD",
				"created_at": "2026-08-30T09:00:00Z",
				"updated_at": "2026-08-30T09:00:00Z"
			}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/stop":
			s.mu.Lock()
			s.stopCalls = append(s.stopCalls, r.URL.Path)
			status := s.stopStatus
			s.mu.Unlock()
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			if status >= 200 && status < 300 {
				_, _ = w.Write([]byte(`{"status":"CANCELLED","identifier":"BE-42"}`))
			} else {
				_, _ = w.Write([]byte(`{"error":"issue must be IN_PROGRESS or REVIEW to stop"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *issueStopStub) calls(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stopCalls...)
}

func runIssueStopCLI(t *testing.T, serverURL string, args ...string) (string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_IssueStop_CooperativeStopContract(t *testing.T) {
	stub := &issueStopStub{stopStatus: http.StatusOK}
	srv := stub.start(t)

	out, err := runIssueStopCLI(t, srv.URL, "issue", "stop", "BE-42")
	if err != nil {
		t.Fatalf("issue stop: %v\noutput: %s", err, out)
	}

	// (a) the request actually reached the stop route, not merely the
	// issue-lookup GET that precedes it.
	calls := stub.calls(t)
	if len(calls) != 1 || calls[0] != "/api/v1/crews/crew_1/issues/BE-42/stop" {
		t.Errorf("stop calls = %v, want exactly one call to the stop route", calls)
	}

	// (b) the success line describes the cooperative contract, not the old
	// instant-cancel wording ("Stopped %s" / "cancels running tasks").
	if !strings.Contains(out, "Stop requested for BE-42") {
		t.Errorf("output = %q, want it to say a stop was requested", out)
	}
	if !strings.Contains(out, "current step will finish") {
		t.Errorf("output = %q, want it to describe the current step finishing", out)
	}
	if !strings.Contains(out, "no further step will start") {
		t.Errorf("output = %q, want it to describe that no further step starts", out)
	}
	if strings.Contains(out, "Stopped BE-42") {
		t.Errorf("output = %q, still carries the old instant-cancel wording", out)
	}
}

func TestAcceptance_IssueStop_NonOKReportedAsError(t *testing.T) {
	stub := &issueStopStub{stopStatus: http.StatusBadRequest}
	srv := stub.start(t)

	out, err := runIssueStopCLI(t, srv.URL, "issue", "stop", "BE-42")
	if err == nil {
		t.Fatalf("issue stop: want a non-zero exit for a non-2xx response, got none\noutput: %s", out)
	}
	if exitCodeOf(t, err) == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(out, "IN_PROGRESS or REVIEW") {
		t.Errorf("output = %q, want the server's error detail surfaced", out)
	}
}
