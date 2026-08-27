package main

// `crewship inspect` always printed "tool calls: 0" (and undercounted
// "errors:"), no matter how many tools the run actually invoked.
//
// The footer counters in printInspectTable (cmd_inspect.go) recognize a
// journal entry as a tool call when entry_type == "tool_call" or has the
// prefix "tool.". Neither is ever emitted: journal entries for a run's
// individual tool invocations are written as run.agent_span
// (internal/journal/types.go EntryRunAgentSpan, emitted by
// internal/pipeline/agent_span_emit.go), which matches neither check, so
// toolCalls stays 0 forever. The same span also records a failed tool call
// at severity "warn" (not "error" — internal/pipeline/agent_span_emit.go
// only escalates a run.agent_span to journal.SeverityWarn, never
// SeverityError, so a failed tool call never trips the severity=="error"
// branch the errors: footer counts either).
//
// This test drives the REAL api.Router over a migrated SQLite DB (per
// CLAUDE.md: testutil.MigratedSQLDB, not a hand-written fixture schema —
// TestInspectRunE_TableTimeline's existing stub fixture uses entry_type
// "tool_call", which happens to match the buggy code and would keep
// passing whether or not the count is right).
import (
	"database/sql"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const (
	inspectToolCallsWorkspaceID = "citoolcallsws00000001"
	inspectToolCallsCrewID      = "citoolcallscrew0000001"
	inspectToolCallsAgentID     = "citoolcallsagent000001"
	// "r_" namespace — cli.IsPipelineRunID rejects a "run_"/"prn_" id
	// before inspect ever reaches the journal.
	inspectToolCallsRunID = "r_toolcalls_1"
)

// setupInspectToolCallsServer builds a real router over a migrated SQLite
// DB, seeds one workspace/owner/crew/agent, and journals a run.started +
// three run.agent_span entries (two ok, one error) + run.completed for
// inspectToolCallsRunID. Points cliCfg at the resulting httptest server.
func setupInspectToolCallsServer(t *testing.T) *sql.DB {
	t.Helper()

	db := testutil.MigratedSQLDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}

	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'ToolCalls', 'toolcalls-ws')`, inspectToolCallsWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('itc-owner', 'owner@itc.example', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('itc-owner-m', ?, 'itc-owner', 'OWNER')`,
		inspectToolCallsWorkspaceID)

	ownerToken := "crewship_cli_itcowner000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-itc-owner', 'itc-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES (?, ?, 'Engineering', 'engineering', 'free')`,
		inspectToolCallsCrewID, inspectToolCallsWorkspaceID)
	mustExec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status)
	          VALUES (?, ?, ?, 'Viktor', 'viktor', 'LEAD', 'IDLE')`,
		inspectToolCallsAgentID, inspectToolCallsCrewID, inspectToolCallsWorkspaceID)

	insertJournal := func(id, kind, severity string, ts time.Time, payload string) {
		t.Helper()
		_, err := db.Exec(`
			INSERT INTO journal_entries
				(id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, ?, ?, ?, ?, 'normal', 'orchestrator', ?, 'itc', ?, '{}', ?)`,
			id, inspectToolCallsWorkspaceID, inspectToolCallsAgentID, ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			kind, severity, inspectToolCallsRunID, payload, inspectToolCallsRunID)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	now := time.Now().UTC()
	insertJournal("itc_started", "run.started", "info", now.Add(-3*time.Minute), `{"trigger_type":"USER"}`)
	// Three tool calls: two succeed, one fails. Mirrors the real shape
	// internal/pipeline/agent_span_emit.go writes (kind/name/status in the
	// payload; severity warn, not error, when status is "error").
	insertJournal("itc_span1", "run.agent_span", "info", now.Add(-2*time.Minute),
		`{"run_id":"`+inspectToolCallsRunID+`","step_id":"s1","seq":1,"kind":"bash","name":"Bash","status":"ok"}`)
	insertJournal("itc_span2", "run.agent_span", "info", now.Add(-90*time.Second),
		`{"run_id":"`+inspectToolCallsRunID+`","step_id":"s1","seq":2,"kind":"edit","name":"Edit","status":"ok"}`)
	insertJournal("itc_span3", "run.agent_span", "warn", now.Add(-60*time.Second),
		`{"run_id":"`+inspectToolCallsRunID+`","step_id":"s1","seq":3,"kind":"bash","name":"Bash","status":"error"}`)
	insertJournal("itc_completed", "run.completed", "info", now.Add(-1*time.Minute), `{"exit_code":0}`)

	r, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	cliCfg = &cli.CLIConfig{
		Token:     ownerToken,
		Workspace: inspectToolCallsWorkspaceID,
		Server:    srv.URL,
	}
	return db
}

// TestInspectRunE_CountsToolCallsFromAgentSpans is the regression test for
// the always-0 tool calls footer. It must fail on main (toolCalls stays 0)
// and pass once printInspectTable recognizes run.agent_span entries.
func TestInspectRunE_CountsToolCallsFromAgentSpans(t *testing.T) {
	saveCLIState(t)
	setupInspectToolCallsServer(t)

	out := stripANSI(covCaptureStdoutCli9(t, func() {
		if err := inspectCmd.RunE(inspectCmd, []string{inspectToolCallsRunID}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	}))

	if !strings.Contains(out, "tool calls:") {
		t.Fatalf("footer missing tool calls stat:\n%s", out)
	}
	if !strings.Contains(out, "tool calls: 3") {
		t.Errorf("expected 3 tool calls (three run.agent_span entries), got:\n%s", out)
	}
	// One of the three spans carries status:"error"; the errors: footer
	// should count it even though its journal severity is "warn" (that's
	// how agent_span failures are emitted — see agent_span_emit.go).
	if !strings.Contains(out, "errors: 1") {
		t.Errorf("expected 1 error (one failed run.agent_span), got:\n%s", out)
	}
}
