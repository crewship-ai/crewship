package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Contract tests for the CLI↔handler drift family (#1576): a route changes
// shape, the command that drives it does not, and the command then answers
// confidently and wrongly — a blank column, an empty id, a report of
// regressions that never happened.
//
// Every test here stubs the response the server sends TODAY (copied from the
// handler, not from the CLI's own struct) and drives the real cobra RunE.
// That is the contract the previous tests were missing: they stubbed the
// shape the CLI already believed in, so they stayed green through the bug.
//
// If one of these fails after a handler change, the handler moved and the
// command has to move with it — that is the point.

// ─── labels: `label_group`, never `group` ────────────────────────────────
//
// api.labelResponse (internal/api/issue_handler.go) marshals the grouping
// column as `label_group`, and always has. labelItem asked for `group`, so
// the pointer decoded nil and every GROUP cell printed "-" — for grouped and
// ungrouped labels alike. No error, no warning, just a column of dashes.

func TestLabelList_ReadsLabelGroupFromServerShape(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]any{
		{"id": "lbl_bug", "name": "bug", "color": "#ef4444", "label_group": "type"},
		{"id": "lbl_none", "name": "chore", "color": "#64748b", "label_group": nil},
	}))
	labelListCmd.SetContext(context.Background())

	out := covCaptureStdoutCli5(t, func() {
		if err := labelListCmd.RunE(labelListCmd, nil); err != nil {
			t.Fatalf("label list: %v", err)
		}
	})

	// The grouped label must show its group. Before the fix this cell was
	// "-", which is the same thing the genuinely ungrouped row prints — the
	// two states were indistinguishable in the output.
	if !strings.Contains(out, "type") {
		t.Errorf("GROUP column dropped the server's label_group value.\n%s", out)
	}
	// And the ungrouped label must still read as ungrouped, so the fix does
	// not merely trade one wrong answer for another.
	if !strings.Contains(out, "chore") {
		t.Errorf("ungrouped label missing from table.\n%s", out)
	}
}

func TestLabelList_JSONFormatEmitsServerKey(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]any{
		{"id": "lbl_bug", "name": "bug", "color": "#ef4444", "label_group": "type"},
	}))
	labelListCmd.SetContext(context.Background())

	flagFormat = "json"
	out := covCaptureStdoutCli5(t, func() {
		if err := labelListCmd.RunE(labelListCmd, nil); err != nil {
			t.Fatalf("label list --format json: %v", err)
		}
	})

	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--format json output does not parse: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %v", len(rows), rows)
	}
	// `crewship label list -f json | jq` and `curl … | jq` must agree on the
	// key name, or every scripted consumer has to know which one it is
	// talking to.
	if got := rows[0]["label_group"]; got != "type" {
		t.Errorf("json output label_group = %v, want \"type\" (row: %v)", got, rows[0])
	}
}

func TestLabelCreate_ReadsCreatedLabelGroup(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]any{
		"id": "lbl_new", "name": "infra", "color": "#3b82f6", "label_group": "area",
	}))
	if err := labelCreateCmd.Flags().Set("name", "infra"); err != nil {
		t.Fatalf("set --name: %v", err)
	}
	if err := labelCreateCmd.Flags().Set("color", "#3b82f6"); err != nil {
		t.Fatalf("set --color: %v", err)
	}
	if err := labelCreateCmd.Flags().Set("group", "area"); err != nil {
		t.Fatalf("set --group: %v", err)
	}
	t.Cleanup(func() {
		_ = labelCreateCmd.Flags().Set("name", "")
		_ = labelCreateCmd.Flags().Set("color", "")
		_ = labelCreateCmd.Flags().Set("group", "")
	})
	labelCreateCmd.SetContext(context.Background())

	out := covCaptureAll(t, func() {
		if err := labelCreateCmd.RunE(labelCreateCmd, nil); err != nil {
			t.Fatalf("label create: %v", err)
		}
	})
	if !strings.Contains(out, "lbl_new") {
		t.Errorf("created label id missing from output.\n%s", out)
	}
	// The request must carry the server's key too — a create that sent
	// `group` would be silently dropped by the handler's decoder.
	calls := stub.CallsFor("POST", "/api/v1/labels")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	var body map[string]any
	if err := clitest.DecodeJSONBody(calls[0].Body, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["label_group"] != "area" {
		t.Errorf("create request did not send label_group: %v", body)
	}
}

// ─── routine backtest: run-records sends `id`, not `run_id` ──────────────
//
// runRecordDTO (internal/api/pipelines_exec.go, ListRunRecords) sends the
// run identifier as `id`. backtestSourceRun decoded `run_id`, so every
// corpus entry arrived with an empty RunID; the replay POST then went to
// `…/pipelines/runs//replay`, 404'd, and was absorbed as an ERROR row —
// which summariseBacktest counts toward REGRESSION_DETECTED and
// renderBacktestReport turns into a non-zero exit.
//
// Net effect before the fix: `crewship routine backtest` failed CI with
// "regressions detected" for a candidate version it never actually replayed.

// backtestStubs wires a one-run corpus whose replay matches the original
// exactly, so a correctly-decoding CLI must report CLEAN.
func backtestStubs(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := covSetupCli5(t)
	started := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/support-triage/run-records",
		clitest.JSONResponse(200, []map[string]any{{
			// Exactly the DTO the handler writes today.
			"id":            "run_source_0001",
			"pipeline_slug": "support-triage",
			"status":        "COMPLETED",
			"mode":          "live",
			"started_at":    started,
			"output":        "triaged: 3 tickets",
			"cost_usd":      0.02,
			"duration_ms":   1200,
			"triggered_via": "schedule",
		}}))
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/runs/run_source_0001/replay",
		clitest.JSONResponse(200, map[string]any{
			"run_id":      "run_candidate_0001",
			"status":      "COMPLETED",
			"output":      "triaged: 3 tickets",
			"duration_ms": 1150,
			"cost_usd":    0.02,
		}))
	return stub
}

func setBacktestFlags(t *testing.T) {
	t.Helper()
	if err := routineBacktestCmd.Flags().Set("against", "9"); err != nil {
		t.Fatalf("set --against: %v", err)
	}
	if err := routineBacktestCmd.Flags().Set("last", "7d"); err != nil {
		t.Fatalf("set --last: %v", err)
	}
	if err := routineBacktestCmd.Flags().Set("limit", "5"); err != nil {
		t.Fatalf("set --limit: %v", err)
	}
	t.Cleanup(func() {
		_ = routineBacktestCmd.Flags().Set("against", "0")
		_ = routineBacktestCmd.Flags().Set("last", "7d")
		_ = routineBacktestCmd.Flags().Set("limit", "20")
	})
}

func TestRoutineBacktest_ReplaysTheRunTheServerNamed(t *testing.T) {
	stub := backtestStubs(t)
	setBacktestFlags(t)
	routineBacktestCmd.SetContext(context.Background())

	flagFormat = "json"
	var runErr error
	out := covCaptureStdoutCli5(t, func() {
		runErr = runRoutineBacktest(routineBacktestCmd, []string{"support-triage"})
	})
	if runErr != nil {
		// Before the fix this is where the command lands: a non-zero exit
		// reporting regressions, caused entirely by the CLI's own decode.
		t.Fatalf("backtest reported a failure against a clean corpus: %v\n%s", runErr, out)
	}

	var summary struct {
		Runs      int    `json:"runs"`
		Matched   int    `json:"matched"`
		Errored   int    `json:"errored"`
		Verdict   string `json:"verdict"`
		RowsField []struct {
			SourceRunID    string `json:"source_run_id"`
			CandidateRunID string `json:"candidate_run_id"`
			Verdict        string `json:"verdict"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("backtest --format json does not parse: %v\n%s", err, out)
	}
	if summary.Verdict != "CLEAN" || summary.Matched != 1 || summary.Errored != 0 {
		t.Errorf("verdict=%s matched=%d errored=%d, want CLEAN/1/0 — an identical replay is not a regression",
			summary.Verdict, summary.Matched, summary.Errored)
	}
	if len(summary.RowsField) != 1 || summary.RowsField[0].SourceRunID != "run_source_0001" {
		t.Fatalf("source run id lost in decode: %+v", summary.RowsField)
	}

	// The replay must have been addressed to a real id — the empty-segment
	// URL is the tell that the corpus decoded to zero values.
	if got := len(stub.CallsFor("POST", "/api/v1/workspaces/"+covWSCli5+"/pipelines/runs/run_source_0001/replay")); got != 1 {
		t.Errorf("expected 1 replay POST against the named run, got %d; calls=%v", got, stub.Calls())
	}
}

func TestRoutineBacktest_NamesTheDriftWhenRunIDIsAbsent(t *testing.T) {
	// A server that answers with neither `id` nor `run_id` — i.e. a shape
	// this CLI cannot read. The row must say so rather than present itself
	// as an ordinary replay failure, which is indistinguishable from a real
	// regression in the report. The stub fails every request, so a green
	// test also proves the guard fired before anything went on the wire.
	stub := covSetupCli5(t)
	stub.SetFallback(clitest.ErrorResponse(500, "no request should have been made"))

	row := replayBacktestRun(newAPIClient(), covWSCli5, 9, backtestSourceRun{
		Status:    "COMPLETED",
		Output:    "whatever",
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if row.Verdict != "ERROR" {
		t.Fatalf("verdict = %q, want ERROR", row.Verdict)
	}
	if !strings.Contains(row.Error, "no run id") {
		t.Errorf("error must name the shape problem, got %q", row.Error)
	}
}

// ─── mission clone: the handler sends {id, status}, never a title ────────
//
// MissionHandler.Clone (internal/api/task_state.go) answers
// {"id": …, "status": "PLANNING"}. The command decoded a `title` and printed
// it, so every clone reported `Mission cloned: <id> ()` — an empty
// parenthesis that reads as "a mission with no title".

func TestMissionClone_ReportsWhatTheServerActuallySent(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{
		{"id": "msn_original_0001", "crew_id": "crew_0001"},
	}))
	stub.OnPost("/api/v1/crews/crew_0001/missions/msn_original_0001/clone",
		clitest.JSONResponse(201, map[string]any{"id": "msn_clone_0002", "status": "PLANNING"}))
	missionCloneCmd.SetContext(context.Background())

	out := covCaptureAll(t, func() {
		if err := missionCloneCmd.RunE(missionCloneCmd, []string{"msn_original_0001"}); err != nil {
			t.Fatalf("mission clone: %v", err)
		}
	})

	if !strings.Contains(out, "msn_clone_0002") {
		t.Errorf("clone id missing from output.\n%s", out)
	}
	if !strings.Contains(out, "PLANNING") {
		t.Errorf("status the server did send is missing from output.\n%s", out)
	}
	// The empty parenthesis is the signature of printing a field the server
	// never sent. It must not come back.
	if strings.Contains(out, "()") {
		t.Errorf("output still renders an absent field as an empty value.\n%s", out)
	}
}

func TestMissionClone_EchoesTheRequestedTitleOnly(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]any{
		{"id": "msn_original_0001", "crew_id": "crew_0001"},
	}))
	stub.OnPost("/api/v1/crews/crew_0001/missions/msn_original_0001/clone",
		clitest.JSONResponse(201, map[string]any{"id": "msn_clone_0003", "status": "PLANNING"}))
	if err := missionCloneCmd.Flags().Set("title", "Retry of the triage run"); err != nil {
		t.Fatalf("set --title: %v", err)
	}
	t.Cleanup(func() { _ = missionCloneCmd.Flags().Set("title", "") })
	missionCloneCmd.SetContext(context.Background())

	out := covCaptureAll(t, func() {
		if err := missionCloneCmd.RunE(missionCloneCmd, []string{"msn_original_0001"}); err != nil {
			t.Fatalf("mission clone --title: %v", err)
		}
	})
	// We know this string because we sent it, not because it came back —
	// and the message says so.
	if !strings.Contains(out, "Retry of the triage run") {
		t.Errorf("requested title not echoed.\n%s", out)
	}
}
