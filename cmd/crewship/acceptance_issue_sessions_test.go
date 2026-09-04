package main

// Acceptance for `crewship issue sessions`, driven through the BUILT BINARY.
//
// The project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") applies here too: ListSessions
// (internal/api/issue_sessions.go, PRD-ISSUES-AND-ROUTINES-2026 §9.2, work
// package B1 — #2332) is a new endpoint, and the CLI's own `sessions` struct
// (issueSessionsCmd, cmd_issue_extra.go) has its own JSON tags that could
// drift from issueAgentSessionDTO's independently of any unit test on
// either side. This drives the real binary against a stub server shaped
// like the real route, matching acceptance_issue_runs_test.go's pattern.

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

type issueSessionsStub struct {
	mu            sync.Mutex
	sessionsCalls []string
	sessionsBody  string
}

func (s *issueSessionsStub) start(t *testing.T) *httptest.Server {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/sessions":
			s.mu.Lock()
			s.sessionsCalls = append(s.sessionsCalls, r.URL.Path)
			body := s.sessionsBody
			s.mu.Unlock()
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *issueSessionsStub) calls(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sessionsCalls...)
}

func runIssueSessionsCLI(t *testing.T, serverURL string, args ...string) (string, error) {
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

// issueSessionsFixture is shaped like issueAgentSessionDTO
// (internal/api/issue_sessions.go): one session with an agent_version
// (the agent has config history), one without (agent_version omitted —
// nil, never edited since creation).
const issueSessionsFixture = `[
  {"id":"sess_1","mission_id":"mission_1","agent_id":"agent_1","agent_name":"Backend Dev",
   "state":"pending","last_consumed_seq":0,"agent_version":3,
   "created_at":"2026-09-04T10:00:00Z","updated_at":"2026-09-04T10:00:00Z"},
  {"id":"sess_2","mission_id":"mission_1","agent_id":"agent_2","agent_name":"QA Bot",
   "state":"pending","last_consumed_seq":0,
   "created_at":"2026-09-04T09:00:00Z","updated_at":"2026-09-04T09:00:00Z"}
]`

func TestAcceptance_IssueSessions_ListsEverySession(t *testing.T) {
	stub := &issueSessionsStub{sessionsBody: issueSessionsFixture}
	srv := stub.start(t)

	out, err := runIssueSessionsCLI(t, srv.URL, "issue", "sessions", "BE-42")
	if err != nil {
		t.Fatalf("issue sessions: %v\noutput: %s", err, out)
	}

	calls := stub.calls(t)
	if len(calls) != 1 || calls[0] != "/api/v1/crews/crew_1/issues/BE-42/sessions" {
		t.Errorf("sessions calls = %v, want exactly one call to /api/v1/crews/crew_1/issues/BE-42/sessions", calls)
	}

	for _, want := range []string{"Backend Dev", "QA Bot", "pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAcceptance_IssueSessions_JSON_ShowsAgentVersion(t *testing.T) {
	stub := &issueSessionsStub{sessionsBody: issueSessionsFixture}
	srv := stub.start(t)

	out, err := runIssueSessionsCLI(t, srv.URL, "issue", "sessions", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue sessions: %v\noutput: %s", err, out)
	}
	for _, want := range []string{`"agent_version": 3`, `"agent_version": null`, `"sess_1"`, `"sess_2"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %s:\n%s", want, out)
		}
	}
}

func TestAcceptance_IssueSessions_EmptyList(t *testing.T) {
	stub := &issueSessionsStub{sessionsBody: `[]`}
	srv := stub.start(t)

	out, err := runIssueSessionsCLI(t, srv.URL, "issue", "sessions", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue sessions: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want the empty JSON array", out)
	}
}
