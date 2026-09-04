package main

// Acceptance for `crewship issue get`, driven through the BUILT BINARY.
//
// #2313 item 2: the API's issueResponse carries `owner` and `delegate`
// (#2297, A10 — the typed projection of missions.owner_user_id /
// delegate_agent_id, independent of the legacy assignee_type/assignee_id
// pair) but the CLI's issueItem struct (cmd_issue.go) stopped at
// assignee_*, so `issue get -f json` silently dropped both fields and the
// table view never showed them at all. This test drives the real binary
// against a stub server returning owner+delegate and asserts both surfaces:
// the JSON output round-trips the fields verbatim, and the table prints an
// "Owner" row and a "Delegate" row.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const issueGetFixtureWithOwnerDelegate = `{
	"id": "iss_1",
	"crew_id": "crew_1",
	"crew_slug": "eng",
	"identifier": "BE-42",
	"title": "Refresh token handling",
	"status": "IN_PROGRESS",
	"priority": "medium",
	"mission_type": "STANDARD",
	"owner": {"id": "user_1", "name": "Pavel Srba"},
	"delegate": {"id": "agent_1", "name": "Riley"},
	"created_at": "2026-08-30T09:00:00Z",
	"updated_at": "2026-08-30T09:00:00Z"
}`

func startIssueGetStub(t *testing.T, issueBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/BE-42":
			_, _ = w.Write([]byte(issueBody))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/crew_1/issues/BE-42/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runIssueGetCLI(t *testing.T, serverURL string, args ...string) (string, error) {
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

func TestAcceptance_IssueGet_JSON_ShowsOwnerAndDelegate(t *testing.T) {
	srv := startIssueGetStub(t, issueGetFixtureWithOwnerDelegate)

	out, err := runIssueGetCLI(t, srv.URL, "issue", "get", "BE-42", "--format", "json")
	if err != nil {
		t.Fatalf("issue get: %v\noutput: %s", err, out)
	}

	for _, want := range []string{`"owner"`, `"Pavel Srba"`, `"delegate"`, `"Riley"`, `"user_1"`, `"agent_1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %s:\n%s", want, out)
		}
	}
}

func TestAcceptance_IssueGet_Table_ShowsOwnerAndDelegateRows(t *testing.T) {
	srv := startIssueGetStub(t, issueGetFixtureWithOwnerDelegate)

	out, err := runIssueGetCLI(t, srv.URL, "issue", "get", "BE-42")
	if err != nil {
		t.Fatalf("issue get: %v\noutput: %s", err, out)
	}

	if !strings.Contains(out, "Owner") || !strings.Contains(out, "Pavel Srba") {
		t.Errorf("table output missing an Owner row with the resolved name:\n%s", out)
	}
	if !strings.Contains(out, "Delegate") || !strings.Contains(out, "Riley") {
		t.Errorf("table output missing a Delegate row with the resolved name:\n%s", out)
	}
}

func TestAcceptance_IssueGet_Table_OwnerDelegateAbsent_ShowDash(t *testing.T) {
	fixture := `{
		"id": "iss_2",
		"crew_id": "crew_1",
		"crew_slug": "eng",
		"identifier": "BE-42",
		"title": "No owner or delegate yet",
		"status": "BACKLOG",
		"priority": "none",
		"mission_type": "STANDARD",
		"created_at": "2026-08-30T09:00:00Z",
		"updated_at": "2026-08-30T09:00:00Z"
	}`
	srv := startIssueGetStub(t, fixture)

	out, err := runIssueGetCLI(t, srv.URL, "issue", "get", "BE-42")
	if err != nil {
		t.Fatalf("issue get: %v\noutput: %s", err, out)
	}

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Owner") && !strings.HasSuffix(trimmed, "-") {
			t.Errorf("Owner row = %q, want it to end in '-' when no owner is set", trimmed)
		}
		if strings.HasPrefix(trimmed, "Delegate") && !strings.HasSuffix(trimmed, "-") {
			t.Errorf("Delegate row = %q, want it to end in '-' when no delegate is set", trimmed)
		}
	}
}
