package main

// Acceptance for `crewship issue checkpoints`, driven through the BUILT
// BINARY. The project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") applies here too: ListCheckpoints
// (internal/api/issue_checkpoints.go, PRD-ISSUES-AND-ROUTINES-2026 §9.5,
// work package B5 — #2345) is a new endpoint, and the CLI's own struct in
// issueCheckpointsCmd (cmd_issue_extra.go) has its own JSON tags that could
// drift from checkpointDTO independently of any unit test on either side.
// Matches acceptance_issue_sessions_test.go's pattern.

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

type issueCheckpointsStub struct {
	mu              sync.Mutex
	calls           []string
	sessionsBody    string
	checkpointsBody string
	agentsBody      string
}

func (s *issueCheckpointsStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/BE-42":
			_, _ = w.Write([]byte(`{
				"id": "iss_1", "crew_id": "crew_1", "identifier": "BE-42",
				"title": "Refresh token handling", "status": "IN_PROGRESS", "priority": "MEDIUM",
				"mission_type": "STANDARD",
				"created_at": "2026-08-30T09:00:00Z", "updated_at": "2026-08-30T09:00:00Z"
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_, _ = w.Write([]byte(s.agentsBody))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/sessions":
			_, _ = w.Write([]byte(s.sessionsBody))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/sessions/sess_1/checkpoints":
			_, _ = w.Write([]byte(s.checkpointsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runIssueCheckpointsCLI(t *testing.T, serverURL string, args ...string) (string, error) {
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

const issueCheckpointsFixtureSessions = `[
  {"id":"sess_1","mission_id":"mission_1","agent_id":"agent_1","agent_name":"Backend Dev",
   "state":"active","last_consumed_seq":12,
   "created_at":"2026-09-04T09:00:00Z","updated_at":"2026-09-04T10:00:00Z"}
]`

const issueCheckpointsFixtureAgents = `[{"id":"agent_1","slug":"backend-dev"}]`

// checkpointDTO-shaped (internal/api/issue_checkpoints.go).
const issueCheckpointsFixture = `[
  {"id":"chk_2","session_id":"sess_1","run_id":"run_2","seq_at_write":12,
   "done":"Added the CLI docs","next_step":"none","confidence":"high","parsed":true,
   "created_at":"2026-09-04T10:00:00Z"},
  {"id":"chk_1","session_id":"sess_1","run_id":"run_1","seq_at_write":5,
   "done":"Implemented the parser and its tests","next_step":"add the CLI docs","confidence":"high","parsed":true,
   "created_at":"2026-09-04T09:00:00Z"}
]`

func TestAcceptance_IssueCheckpoints_ListsNewestFirst(t *testing.T) {
	stub := &issueCheckpointsStub{
		sessionsBody:    issueCheckpointsFixtureSessions,
		checkpointsBody: issueCheckpointsFixture,
		agentsBody:      issueCheckpointsFixtureAgents,
	}
	srv := stub.start(t)

	out, err := runIssueCheckpointsCLI(t, srv.URL, "issue", "checkpoints", "BE-42", "--agent", "backend-dev")
	if err != nil {
		t.Fatalf("issue checkpoints: %v\noutput: %s", err, out)
	}

	for _, want := range []string{"Added the CLI docs", "Implemented the parser", "high", "true"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Newest first: chk_2's DONE text must appear before chk_1's.
	iNewest := strings.Index(out, "Added the CLI docs")
	iOldest := strings.Index(out, "Implemented the parser")
	if iNewest == -1 || iOldest == -1 || iNewest > iOldest {
		t.Errorf("expected the newest checkpoint listed first:\n%s", out)
	}
}

func TestAcceptance_IssueCheckpoints_JSON_ShowsParsedAndConfidence(t *testing.T) {
	stub := &issueCheckpointsStub{
		sessionsBody:    issueCheckpointsFixtureSessions,
		checkpointsBody: issueCheckpointsFixture,
		agentsBody:      issueCheckpointsFixtureAgents,
	}
	srv := stub.start(t)

	out, err := runIssueCheckpointsCLI(t, srv.URL, "issue", "checkpoints", "BE-42", "--agent", "backend-dev", "--format", "json")
	if err != nil {
		t.Fatalf("issue checkpoints: %v\noutput: %s", err, out)
	}
	for _, want := range []string{`"parsed": true`, `"confidence": "high"`, `"chk_1"`, `"chk_2"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %s:\n%s", want, out)
		}
	}
}

func TestAcceptance_IssueCheckpoints_SingleSession_AgentFlagOptional(t *testing.T) {
	stub := &issueCheckpointsStub{
		sessionsBody:    issueCheckpointsFixtureSessions,
		checkpointsBody: issueCheckpointsFixture,
		agentsBody:      issueCheckpointsFixtureAgents,
	}
	srv := stub.start(t)

	// No --agent: exactly one session exists, so it is picked automatically.
	out, err := runIssueCheckpointsCLI(t, srv.URL, "issue", "checkpoints", "BE-42")
	if err != nil {
		t.Fatalf("issue checkpoints: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Added the CLI docs") {
		t.Errorf("output missing checkpoint data:\n%s", out)
	}
}

func TestAcceptance_IssueCheckpoints_NoSessions_ErrorsByName(t *testing.T) {
	stub := &issueCheckpointsStub{sessionsBody: `[]`, agentsBody: issueCheckpointsFixtureAgents}
	srv := stub.start(t)

	out, err := runIssueCheckpointsCLI(t, srv.URL, "issue", "checkpoints", "BE-42")
	if err == nil {
		t.Fatalf("expected a non-zero exit for an issue with no sessions, got success:\n%s", out)
	}
	if !strings.Contains(out, "no agent sessions") {
		t.Errorf("expected an error naming the missing sessions, got:\n%s", out)
	}
}
