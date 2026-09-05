package main

// Acceptance for `crewship issue runs`, driven through the BUILT BINARY.
//
// The project rule ("every API endpoint gets a CLI command, and its
// acceptance test drives the CLI binary") applies here: ListRuns
// (internal/api/issue_handler_runs.go) now attributes a run to an issue via
// EITHER assignments.mission_id (#2256, going forward — including a mention
// dispatch, which has no mission_tasks row at all) OR the legacy
// mission_tasks.assignment_id join. A stub that only ever returned
// mission-task-shaped rows would pass even if the CLI (or the query) quietly
// dropped mention-dispatched or delegation-hop runs — the exact regression
// this branch fixes. So the fixture carries three runs, one from each path,
// distinguishable by id and task text, and the test asserts the CLI printed
// all three and that exactly one request hit the runs route with the
// resolved identifier.
//
// The default table renderer has no ID column (AGENT/TASK/STATUS/STARTED/
// DURATION/RESULT — cmd_issue_extra.go's issueRunsCmd), so this drives
// `--format json` to see the ids the CLI decoded, matching issueRunDTO's
// json tags (internal/api/issue_handler_runs.go).

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

type issueRunsStub struct {
	mu         sync.Mutex
	runsCalls  []string // paths the runs route was hit with
	runsStatus int
	runsBody   string
}

func (s *issueRunsStub) start(t *testing.T) *httptest.Server {
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/runs":
			s.mu.Lock()
			s.runsCalls = append(s.runsCalls, r.URL.Path)
			status := s.runsStatus
			body := s.runsBody
			s.mu.Unlock()
			if status == 0 {
				status = http.StatusOK
			}
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

func (s *issueRunsStub) calls(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.runsCalls...)
}

func runIssueRunsCLI(t *testing.T, serverURL string, args ...string) (string, error) {
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

// Three runs shaped like issueRunDTO (internal/api/issue_handler_runs.go):
// one reached only through the legacy mission_tasks join (mission-task run),
// one via a.mission_id from a mention dispatch (no mission_tasks row at
// all), and one via a.mission_id from a delegation hop.
const issueRunsFixture = `[
  {"id":"asg_delegation_1","status":"completed","agent_name":"Riley","task":"delegation-hop: fix retry backoff",
   "started_at":"2026-08-31T10:00:00Z","ended_at":"2026-08-31T10:05:00Z","duration_ms":300000,
   "result_summary":"Backoff fixed via delegated hop"},
  {"id":"asg_mention_1","status":"completed","agent_name":"Casey","task":"mention-dispatch: check token expiry",
   "started_at":"2026-08-31T09:00:00Z","ended_at":"2026-08-31T09:02:00Z","duration_ms":120000,
   "result_summary":"Investigated via @mention dispatch"},
  {"id":"asg_missiontask_1","status":"completed","agent_name":"Jordan","task":"mission-task: refresh token handling",
   "started_at":"2026-08-30T09:10:00Z","ended_at":"2026-08-30T09:20:00Z","duration_ms":600000,
   "result_summary":"Done via mission task"}
]`

func TestAcceptance_IssueRuns_ListsEveryAttributedRun(t *testing.T) {
	stub := &issueRunsStub{runsStatus: http.StatusOK, runsBody: issueRunsFixture}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "runs", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue runs: %v\noutput: %s", err, out)
	}

	// Exactly one request hit the runs route, with the resolved identifier
	// (not e.g. the raw issue id iss_1).
	calls := stub.calls(t)
	if len(calls) != 1 || calls[0] != "/api/v1/crews/crew_1/issues/BE-42/runs" {
		t.Errorf("runs calls = %v, want exactly one call to /api/v1/crews/crew_1/issues/BE-42/runs", calls)
	}

	// All three attribution paths made it to stdout: legacy mission_tasks
	// join, a.mission_id from a mention dispatch, and a.mission_id from a
	// delegation hop.
	for _, id := range []string{"asg_delegation_1", "asg_mention_1", "asg_missiontask_1"} {
		if !strings.Contains(out, id) {
			t.Errorf("output missing run id %q:\n%s", id, out)
		}
	}
}

// issueRunsFixtureWithAttribution mirrors issueRunDTO's #2313 addition:
// mission_id and source (task | mention | delegation), one row per
// attribution path.
const issueRunsFixtureWithAttribution = `[
  {"id":"asg_delegation_1","status":"completed","agent_name":"Riley","task":"delegation-hop: fix retry backoff",
   "started_at":"2026-08-31T10:00:00Z","ended_at":"2026-08-31T10:05:00Z","duration_ms":300000,
   "result_summary":"Backoff fixed via delegated hop","mission_id":"mission_1","source":"delegation"},
  {"id":"asg_mention_1","status":"completed","agent_name":"Casey","task":"mention-dispatch: check token expiry",
   "started_at":"2026-08-31T09:00:00Z","ended_at":"2026-08-31T09:02:00Z","duration_ms":120000,
   "result_summary":"Investigated via @mention dispatch","mission_id":"mission_1","source":"mention"},
  {"id":"asg_missiontask_1","status":"completed","agent_name":"Jordan","task":"mission-task: refresh token handling",
   "started_at":"2026-08-30T09:10:00Z","ended_at":"2026-08-30T09:20:00Z","duration_ms":600000,
   "result_summary":"Done via mission task","mission_id":"mission_1","source":"task"}
]`

// TestAcceptance_IssueRuns_JSON_ShowsMissionIDAndSource is the #2313-item-3
// regression: ListRuns has always carried mission_id and source, but the
// CLI's own `runs` struct (issueRunsCmd) stopped at status/agent_name/task/
// started_at/duration_ms and silently dropped both — a client watching the
// runs via the CLI could not tell WHY a run was attributed to the issue.
func TestAcceptance_IssueRuns_JSON_ShowsMissionIDAndSource(t *testing.T) {
	stub := &issueRunsStub{runsStatus: http.StatusOK, runsBody: issueRunsFixtureWithAttribution}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "runs", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue runs: %v\noutput: %s", err, out)
	}

	for _, want := range []string{`"mission_id": "mission_1"`, `"source": "delegation"`, `"source": "mention"`, `"source": "task"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %s:\n%s", want, out)
		}
	}
}

// TestAcceptance_IssueRuns_Table_HasSourceColumn is the table half of the
// same regression: the dashboard-parity table (AGENT/TASK/STATUS/STARTED/
// DURATION/RESULT) never surfaced attribution at all.
func TestAcceptance_IssueRuns_Table_HasSourceColumn(t *testing.T) {
	stub := &issueRunsStub{runsStatus: http.StatusOK, runsBody: issueRunsFixtureWithAttribution}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "runs", "BE-42")
	if err != nil {
		t.Fatalf("issue runs: %v\noutput: %s", err, out)
	}

	if !strings.Contains(out, "SOURCE") {
		t.Errorf("table output missing a SOURCE column header:\n%s", out)
	}
	for _, want := range []string{"delegation", "mention", "task"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing source value %q:\n%s", want, out)
		}
	}
}

// issueRunsFixtureWithHardStop mirrors issueRunDTO's #2365 (work package
// B7b) addition: hard_stop_result / hard_stop_at, present on a run Tier 2
// actually reached, absent (omitempty) on one that was never hard-stopped.
const issueRunsFixtureWithHardStop = `[
  {"id":"asg_hard_stopped","status":"CANCELLED","agent_name":"Riley","task":"hard-stopped run",
   "started_at":"2026-09-04T10:00:00Z","ended_at":"2026-09-04T10:00:05Z","duration_ms":5000,
   "hard_stop_result":"TERMINATED_TERM","hard_stop_at":"2026-09-04T10:00:05Z"},
  {"id":"asg_never_stopped","status":"completed","agent_name":"Casey","task":"ordinary run",
   "started_at":"2026-09-04T09:00:00Z","ended_at":"2026-09-04T09:02:00Z","duration_ms":120000}
]`

// TestAcceptance_IssueRuns_JSON_ShowsHardStopResult is the #2365 regression:
// a live check must be able to read what a Tier 2 hard stop did
// (`hard_stop_result`/`hard_stop_at`) from `issue runs -f json` alone,
// without reading the journal — the same live-readable contract mission_id
// and source got in #2313, and outcome got in #2358.
func TestAcceptance_IssueRuns_JSON_ShowsHardStopResult(t *testing.T) {
	stub := &issueRunsStub{runsStatus: http.StatusOK, runsBody: issueRunsFixtureWithHardStop}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "runs", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue runs: %v\noutput: %s", err, out)
	}

	if !strings.Contains(out, `"hard_stop_result": "TERMINATED_TERM"`) {
		t.Errorf("json output missing hard_stop_result for the hard-stopped run:\n%s", out)
	}
	if !strings.Contains(out, `"hard_stop_at": "2026-09-04T10:00:05Z"`) {
		t.Errorf("json output missing hard_stop_at for the hard-stopped run:\n%s", out)
	}
	if strings.Contains(out, `"id": "asg_never_stopped"`) {
		idx := strings.Index(out, `"id": "asg_never_stopped"`)
		nextIdx := strings.Index(out[idx:], "}")
		if nextIdx == -1 {
			nextIdx = len(out) - idx
		}
		if strings.Contains(out[idx:idx+nextIdx], "hard_stop") {
			t.Errorf("run never hard-stopped carries a hard_stop_* key, want omitempty to drop it:\n%s", out[idx:idx+nextIdx])
		}
	}
}

func TestAcceptance_IssueRuns_NonOKReportedAsError(t *testing.T) {
	stub := &issueRunsStub{runsStatus: http.StatusInternalServerError, runsBody: `{"error":"boom"}`}
	srv := stub.start(t)

	out, err := runIssueRunsCLI(t, srv.URL, "issue", "runs", "BE-42")
	if err == nil {
		t.Fatalf("issue runs: want a non-zero exit for a non-2xx response, got none\noutput: %s", out)
	}
	if exitCodeOf(t, err) == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
}
