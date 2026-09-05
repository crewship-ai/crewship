package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Acceptance for PRD §18 scenario 15, second half (B15, #2389): acting on a
// NEEDS_HUMAN card through the CLI binary resumes the run, writes a receipt,
// and updates the same card. Real api.NewRouter (its own AssignmentHandler
// is the dispatcher, exactly as in production), real migrated DB, the
// crewship binary as the client.

const inboxActAcceptanceWorkspaceID = "cinboxactws00000000001"

func startInboxActAcceptanceServer(t *testing.T) (cfgPath, cardID, missionID string, db *sql.DB) {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	db = dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	const ws = inboxActAcceptanceWorkspaceID
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'InboxAct', 'inbox-act-ws')`, ws)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('iba-owner', 'owner@iba-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('ibam-owner', ?, 'iba-owner', 'OWNER')`, ws)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix, network_mode, container_memory_mb, container_cpus)
		VALUES ('iba-crew', ?, 'Crew', 'iba-crew', 'IBA', 'free', 4096, 2.0)`, ws)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('iba-agent', ?, 'iba-crew', 'Riley', 'riley', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, ws)
	missionID = "iba-mission"
	mustExec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, number, identifier,
		priority, sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, 'iba-crew', 'iba-agent', 'trace-iba', 'Pick a bucket', 'IN_PROGRESS', 1, 'IBA-1', 'medium', 0, 'issue',
		datetime('now'), datetime('now'))`, missionID, ws)
	// The session that asked, parked in awaiting_input after a NEEDS_HUMAN
	// run; the assignment that reported it; the card B6 raised for it.
	mustExec(`INSERT INTO chats (id, workspace_id, agent_id, title, created_at, updated_at)
		VALUES (?, ?, 'iba-agent', 'Pick a bucket', datetime('now'), datetime('now'))`, missionID, ws)
	mustExec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, agent_version, created_at, updated_at)
		VALUES ('iba-sess', ?, ?, 'iba-agent', 'awaiting_input', 0, 2, datetime('now'), datetime('now'))`, ws, missionID)
	mustExec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id, outcome)
		VALUES ('iba-run-1', ?, ?, 'iba-agent', 'iba-agent', 'pick a bucket', 'COMPLETED', 1, datetime('now'), ?, 'iba-sess', 'NEEDS_HUMAN')`, ws, missionID, missionID)
	if err := inbox.WriteThreaded(context.Background(), db, logger, inbox.Item{
		WorkspaceID: ws, Kind: inbox.KindRunNeedsHuman, SourceID: "iba-run-1", TargetRole: "MANAGER",
		Title: "Riley needs your input on IBA-1", BodyMD: "staging or prod bucket?", SenderType: "agent", SenderID: "riley",
		Priority: "high", Blocking: true, ThreadKey: "issue:" + ws + ":" + missionID, AttentionClass: inbox.AttentionInput,
		Payload: map[string]any{"who_can_act": []string{"role:MANAGER"}, "context": map[string]any{"issue": "IBA-1", "run": "iba-run-1"}},
		Actions: []inbox.Action{{ID: "answer", Label: "Answer"}, {ID: "take_over", Label: "Take over"}, {ID: "dismiss", Label: "Dismiss"}},
	}); err != nil {
		t.Fatalf("raise card: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM inbox_items WHERE kind = ? AND source_id = 'iba-run-1'`, inbox.KindRunNeedsHuman).Scan(&cardID); err != nil {
		t.Fatalf("card id: %v", err)
	}

	const ownerToken = "crewship_cli_ibaowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-iba-owner', 'iba-owner', 't', ?, datetime('now'))`,
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
	return cfgPath, cardID, missionID, db
}

func runInboxActCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_InboxAct_AnswerResumesRun_ReceiptOnSameCard(t *testing.T) {
	cfgPath, cardID, missionID, db := startInboxActAcceptanceServer(t)

	out, err := runInboxActCLI(t, cfgPath, "inbox", "act", cardID, "answer", "--input", "Use the staging bucket.")
	if err != nil {
		t.Fatalf("inbox act answer: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Answered "+cardID) || !strings.Contains(out, "dispatched") {
		t.Fatalf("answer output:\n%s", out)
	}

	// The run resumed: a new assignment on the same session, for the same
	// agent, fed by a delivery of the person's comment.
	var newRun, newRunSession string
	if err := db.QueryRow(`SELECT id, session_id FROM assignments WHERE mission_id = ? AND id != 'iba-run-1'`, missionID).Scan(&newRun, &newRunSession); err != nil {
		t.Fatalf("no new run after answer: %v", err)
	}
	if newRunSession != "iba-sess" {
		t.Fatalf("new run session = %q, want iba-sess", newRunSession)
	}
	var delState, delRun string
	if err := db.QueryRow(`SELECT state, COALESCE(assignment_id,'') FROM mission_comment_mentions WHERE mission_id = ? AND agent_id = 'iba-agent'`, missionID).Scan(&delState, &delRun); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	// claimed → consumed happens when the run's goroutine starts its exec,
	// which the HTTP response does not wait for; either state proves the
	// delivery is bound to the resumed run.
	if (delState != "claimed" && delState != "consumed") || delRun != newRun {
		t.Fatalf("delivery = (%s, %s), want (claimed|consumed, %s)", delState, delRun, newRun)
	}
	// The session's awaiting_input → active flip happens in the resumed
	// run's goroutine after the HTTP response, so it is not asserted here
	// (it raced in CI); the handler test waits for dispatches and covers
	// it. The new run being bound to the session above is what proves the
	// run resumed the session that asked.

	// The receipt, on the issue's event log and visible through the CLI.
	out, err = runInboxActCLI(t, cfgPath, "issue", "events", "IBA-1", "--after-seq", "0", "--format", "json")
	if err != nil {
		t.Fatalf("issue events: %v\n%s", err, out)
	}
	if !strings.Contains(out, "inbox_acted") || !strings.Contains(out, "agent_version 2") {
		t.Fatalf("receipt not on the event log:\n%s", out)
	}

	// The same card, resolved in place with the receipt — and still the only one.
	out, err = runInboxActCLI(t, cfgPath, "inbox", "get", cardID, "--format", "json")
	if err != nil {
		t.Fatalf("inbox get: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"resolved"`) || !strings.Contains(out, `"receipt"`) || !strings.Contains(out, newRun) {
		t.Fatalf("card after answer:\n%s", out)
	}
	var cards int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ?`, inbox.KindRunNeedsHuman).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("cards = %d, want 1", cards)
	}

	// Acting twice is refused.
	if out, err := runInboxActCLI(t, cfgPath, "inbox", "act", cardID, "dismiss"); err == nil || !strings.Contains(out, "already acted") {
		t.Fatalf("second act should fail with 'already acted': err=%v\n%s", err, out)
	}
}

func TestAcceptance_InboxAct_TakeOver_SettlesSession(t *testing.T) {
	cfgPath, cardID, _, db := startInboxActAcceptanceServer(t)
	out, err := runInboxActCLI(t, cfgPath, "inbox", "act", cardID, "take_over")
	if err != nil {
		t.Fatalf("inbox act take_over: %v\n%s", err, out)
	}
	var sessState, cardState, action string
	if err := db.QueryRow(`SELECT state FROM issue_agent_sessions WHERE id = 'iba-sess'`).Scan(&sessState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state, resolved_action FROM inbox_items WHERE id = ?`, cardID).Scan(&cardState, &action); err != nil {
		t.Fatal(err)
	}
	if sessState != "idle" || cardState != "resolved" || action != "take_over" {
		t.Fatalf("session=%s card=(%s,%s)", sessState, cardState, action)
	}
}
