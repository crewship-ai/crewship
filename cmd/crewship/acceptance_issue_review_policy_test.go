package main

// Acceptance for the two CLI halves of the issue work contract that had no
// command until now: `issue review-policy` (PUT .../review-policy) and
// `issue result` (GET .../runs/{runId}/result). Real api.NewRouter, real
// migrated DB, the crewship binary as the client — the same contract an
// agent driving the CLI gets, which is the point of the rule that every
// endpoint has a command.

import (
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const issueWorkAcceptanceWorkspaceID = "cissueworkws000000001"

// startIssueWorkAcceptanceServer seeds one crew with one issue in TODO (so
// the review policy is still changeable), one finished run on that issue,
// and one run on a SECOND issue — the row the result endpoint must refuse to
// read across.
func startIssueWorkAcceptanceServer(t *testing.T) (cfgPath string, db *sql.DB) {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	db = dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	const ws = issueWorkAcceptanceWorkspaceID
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'IssueWork', 'issue-work-ws')`, ws)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('iw-owner', 'owner@iw-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('iwm-owner', ?, 'iw-owner', 'OWNER')`, ws)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix, network_mode, container_memory_mb, container_cpus)
		VALUES ('iw-crew', ?, 'Crew', 'iw-crew', 'IW', 'free', 4096, 2.0)`, ws)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('iw-agent', ?, 'iw-crew', 'Riley', 'riley', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, ws)
	seedIssueRow := func(id, identifier string, number int) {
		mustExec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, number, identifier,
			priority, sort_order, mission_type, created_at, updated_at)
			VALUES (?, ?, 'iw-crew', 'iw-agent', ?, 'Work contract', 'TODO', ?, ?, 'medium', 0, 'issue',
			datetime('now'), datetime('now'))`, id, ws, "trace-"+id, number, identifier)
	}
	seedIssueRow("iw-mission", "IW-1", 1)
	seedIssueRow("iw-other", "IW-2", 2)
	// A finished run on IW-1, and one on IW-2 that IW-1 must not be able to read.
	mustExec(`INSERT INTO chats (id, workspace_id, agent_id, title, created_at, updated_at)
		VALUES ('iw-chat', ?, 'iw-agent', 'Work contract', datetime('now'), datetime('now'))`, ws)
	mustExec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, outcome, result_summary)
		VALUES ('iw-run-1', ?, 'iw-chat', 'iw-agent', 'iw-agent', 'do the thing', 'COMPLETED', 1, datetime('now'), 'iw-mission', 'SUCCEEDED', 'Checked every source and listed the three that disagree.')`, ws)
	mustExec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, outcome, result_summary)
		VALUES ('iw-run-other', ?, 'iw-chat', 'iw-agent', 'iw-agent', 'other issue', 'COMPLETED', 1, datetime('now'), 'iw-other', 'SUCCEEDED', 'Belongs to IW-2.')`, ws)

	const ownerToken = "crewship_cli_iwowner000000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-iw-owner', 'iw-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + ws + "\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, db
}

func runIssueWorkCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func issueReviewRequired(t *testing.T, db *sql.DB) (required int, revision int) {
	t.Helper()
	if err := db.QueryRow(`SELECT client_review_required, revision FROM issue_work WHERE mission_id = 'iw-mission'`).
		Scan(&required, &revision); err != nil {
		t.Fatalf("read issue_work: %v", err)
	}
	return required, revision
}

func TestAcceptance_IssueReviewPolicy_ThroughTheCLI(t *testing.T) {
	cfgPath, db := startIssueWorkAcceptanceServer(t)

	// A new issue requires acceptance by default; the command drops it.
	if required, _ := issueReviewRequired(t, db); required != 1 {
		t.Fatalf("seeded client_review_required = %d, want 1", required)
	}
	out, err := runIssueWorkCLI(t, cfgPath, "issue", "review-policy", "IW-1", "--required=false")
	if err != nil {
		t.Fatalf("review-policy --required=false: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no longer needs your acceptance") {
		t.Errorf("output did not say what changed:\n%s", out)
	}
	required, revision := issueReviewRequired(t, db)
	if required != 0 {
		t.Fatalf("client_review_required = %d after --required=false, want 0", required)
	}

	// …and puts it back, carrying the revision the CAS needs without being told.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "review-policy", "IW-1", "--required")
	if err != nil {
		t.Fatalf("review-policy --required: %v\n%s", err, out)
	}
	requiredNow, revisionNow := issueReviewRequired(t, db)
	if requiredNow != 1 {
		t.Fatalf("client_review_required = %d after --required, want 1", requiredNow)
	}
	if revisionNow != revision+1 {
		t.Fatalf("revision = %d, want %d — every accepted change bumps it", revisionNow, revision+1)
	}

	// The flag has no default: a bare call is refused, and changes nothing.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "review-policy", "IW-1")
	if err == nil {
		t.Fatalf("a bare review-policy must be refused, got success:\n%s", out)
	}
	if !strings.Contains(out, "--required") {
		t.Errorf("the refusal must name the flag that resolves it:\n%s", out)
	}
	if r, rev := issueReviewRequired(t, db); r != requiredNow || rev != revisionNow {
		t.Errorf("a refused call still changed the row: (%d,%d) -> (%d,%d)", requiredNow, revisionNow, r, rev)
	}

	// A stale revision is refused rather than silently overwriting a policy
	// somebody changed in between.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "review-policy", "IW-1", "--required=false", "--revision", "0")
	if err == nil {
		t.Fatalf("a stale revision must be refused, got success:\n%s", out)
	}
	if r, _ := issueReviewRequired(t, db); r != 1 {
		t.Errorf("a refused stale call changed the policy to %d", r)
	}
}

func TestAcceptance_IssueResult_ThroughTheCLI(t *testing.T) {
	cfgPath, _ := startIssueWorkAcceptanceServer(t)

	out, err := runIssueWorkCLI(t, cfgPath, "issue", "result", "IW-1", "iw-run-1")
	if err != nil {
		t.Fatalf("issue result: %v\n%s", err, out)
	}
	for _, want := range []string{"iw-run-1", "COMPLETED", "SUCCEEDED", "three that disagree"} {
		if !strings.Contains(out, want) {
			t.Errorf("result output lacks %q:\n%s", want, out)
		}
	}

	// -f json is the shape an agent reads.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "result", "IW-1", "iw-run-1", "-f", "json")
	if err != nil {
		t.Fatalf("issue result -f json: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"result_summary"`) || !strings.Contains(out, `"outcome"`) {
		t.Errorf("json output is not the documented document:\n%s", out)
	}

	// A run that belongs to another issue is not readable through this one.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "result", "IW-1", "iw-run-other")
	if err == nil {
		t.Fatalf("a foreign run must not be readable through IW-1:\n%s", out)
	}
	if strings.Contains(out, "Belongs to IW-2") {
		t.Errorf("the foreign run's result leaked:\n%s", out)
	}
}

// `issue work` is the third command of the same contract and had no test
// that drives the binary either: the handler tests call the router directly,
// so nothing checked that the command sends what the endpoint requires
// (an operation id and a revision it must fill in by itself).
func TestAcceptance_IssueWork_TakeOverThroughTheCLI(t *testing.T) {
	cfgPath, db := startIssueWorkAcceptanceServer(t)

	out, err := runIssueWorkCLI(t, cfgPath, "issue", "work", "IW-1", "--action", "take_over")
	if err != nil {
		t.Fatalf("issue work take_over: %v\n%s", err, out)
	}
	var mode, worker string
	if err := db.QueryRow(`SELECT mode, COALESCE(worker_user_id,'') FROM issue_work WHERE mission_id = 'iw-mission'`).
		Scan(&mode, &worker); err != nil {
		t.Fatalf("read issue_work: %v", err)
	}
	if mode != "human" || worker != "iw-owner" {
		t.Fatalf("after take_over: mode=%q worker=%q, want human/iw-owner", mode, worker)
	}
	// The command defaults the revision from the issue it just read, so a
	// second call is a new operation rather than a stale-revision refusal.
	out, err = runIssueWorkCLI(t, cfgPath, "issue", "work", "IW-1", "--action", "submit", "--note", "Checked the three sources.")
	if err != nil {
		t.Fatalf("issue work submit: %v\n%s", err, out)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = 'iw-mission'`).Scan(&status); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if status != "REVIEW" {
		t.Fatalf("status after submit = %q, want REVIEW", status)
	}
}
