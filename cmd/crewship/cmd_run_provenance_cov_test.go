package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// provenanceRunBody is a GET /api/v1/runs/{id} payload carrying the full
// session provenance, including one MCP server the CLI skipped at startup —
// the run still exited 0.
func provenanceRunBody() map[string]any {
	return map[string]any{
		"id": "msg_prov", "agent_id": covAgentIDCli4, "agent_slug": "viktor",
		"workspace_id": covWorkspaceIDCli4, "trigger_type": "MANUAL", "status": "COMPLETED",
		"started_at": "2026-01-01T00:00:00Z", "finished_at": "2026-01-01T00:01:00Z",
		"created_at": "2026-01-01T00:00:00Z", "exit_code": 0,
		"model": "claude-sonnet-4-5", "cli_version": "2.1.204",
		"api_key_source": "ANTHROPIC_API_KEY", "permission_mode": "bypassPermissions",
		"session_id": "sess-9f1c",
		"mcp_server_errors": []map[string]any{
			{"name": "crewship-memory", "type": "config_error", "message": "no such file"},
		},
	}
}

func TestRunGetRunE_RendersSessionProvenance(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, provenanceRunBody()))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"msg_prov", "COMPLETED", "claude-sonnet-4-5",
		"2.1.204", "ANTHROPIC_API_KEY", "bypassPermissions", "sess-9f1c",
		// The skipped server is the point of the command: an operator must
		// see the lost capability without reaching for --format json.
		"crewship-memory", "config_error", "no such file",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	if got := len(stub.CallsFor("GET", "/api/v1/runs/msg_prov")); got != 1 {
		t.Errorf("GET /api/v1/runs/msg_prov calls = %d, want 1", got)
	}
}

func TestRunGetRunE_OmitsAbsentProvenance(t *testing.T) {
	stub := covSetupCli4(t)
	// An older run, or a non-Claude adapter: the server omits every
	// provenance key.
	stub.OnGet("/api/v1/runs/msg_plain", clitest.JSONResponse(200, map[string]any{
		"id": "msg_plain", "agent_id": covAgentIDCli4, "agent_slug": "viktor",
		"workspace_id": covWorkspaceIDCli4, "trigger_type": "MANUAL", "status": "COMPLETED",
		"created_at": "2026-01-01T00:00:00Z",
	}))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_plain"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "msg_plain") || !strings.Contains(out, "COMPLETED") {
		t.Fatalf("output missing the run itself:\n%s", out)
	}
	// Absent stays absent: no blank rows, no "<nil>", no misleading label.
	for _, unwanted := range []string{"<nil>", "CLI version", "Auth source", "Permission mode", "Session", "MCP skipped"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output rendered %q for a run that recorded none; got:\n%s", unwanted, out)
		}
	}
}

func TestRunGetRunE_AuthRequired(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := runGetCmd.RunE(runGetCmd, []string{"msg_prov"}); err == nil ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want not logged in, got %v", err)
	}
}

// TestRunListRunE_CarriesModelAndProvenanceIntoJSON is the regression for the
// decode bug: `run list` re-serialises what it decoded, so a field missing
// from the decode struct was silently dropped from --format json even though
// the server had been sending it all along.
func TestRunListRunE_CarriesModelAndProvenanceIntoJSON(t *testing.T) {
	stub := covSetupCli4(t)
	setFormatCov(t, "json")
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{provenanceRunBody()},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		`"model": "claude-sonnet-4-5"`,
		`"cli_version": "2.1.204"`,
		`"api_key_source": "ANTHROPIC_API_KEY"`,
		`"permission_mode": "bypassPermissions"`,
		`"session_id": "sess-9f1c"`,
		`"name": "crewship-memory"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("json output missing %s; got:\n%s", want, out)
		}
	}
}

// TestRunListRunE_FlagsSkippedMCPServers proves the loss is visible in the
// human table too — a run that quietly lost a capability looks identical to a
// clean one otherwise.
func TestRunListRunE_FlagsSkippedMCPServers(t *testing.T) {
	stub := covSetupCli4(t)
	setFormatCov(t, "table")
	clean := map[string]any{
		"id": "msg_clean", "agent_slug": "eva", "status": "COMPLETED",
		"trigger_type": "SCHEDULE", "created_at": "2026-01-01T00:00:00Z",
	}
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{provenanceRunBody(), clean},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "msg_clean") {
		t.Fatalf("table missing the clean run:\n%s", out)
	}
	for _, want := range []string{"MCP", "crewship-memory", "msg_prov"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunListRunE_NoNoticeWhenNothingSkipped(t *testing.T) {
	stub := covSetupCli4(t)
	setFormatCov(t, "table")
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{
			"id": "msg_clean", "agent_slug": "eva", "status": "COMPLETED",
			"trigger_type": "SCHEDULE", "created_at": "2026-01-01T00:00:00Z",
		}},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "MCP") {
		t.Errorf("clean listing raised an MCP notice; got:\n%s", out)
	}
}

// `--format quiet` exists so a script can pipe ids into the next command.
// `run list` truncated the id to 16 characters while BUILDING the rows the
// quiet renderer prints, so the ids it emitted were not ids: feeding one back
// into `run get` answered 404. The truncation belongs to the table, which has
// a column width to respect; quiet has no columns.
//
// Caught by the runtime harness (scripts/test-harness/test-session-provenance.sh),
// not by a unit test — nothing until then had piped one command into another.
func TestRunListRunE_QuietEmitsTheWholeRunID(t *testing.T) {
	const fullID = "msg_1786552750441963636_4bd885e80dc9485e"

	stub := covSetupCli4(t)
	setFormatCov(t, "quiet")
	body := provenanceRunBody()
	body["id"] = fullID
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{body},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, fullID) {
		t.Errorf("quiet output does not carry the full run id — a script cannot use it.\nwant: %s\ngot:\n%s", fullID, out)
	}
}

// The table still truncates: it is a fixed-width column and a 40-character id
// pushes every other column off a narrow terminal.
func TestRunListRunE_TableStillTruncatesTheID(t *testing.T) {
	const fullID = "msg_1786552750441963636_4bd885e80dc9485e"

	stub := covSetupCli4(t)
	setFormatCov(t, "table")
	body := provenanceRunBody()
	body["id"] = fullID
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{body},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Only the table block: the skipped-MCP notice below it deliberately
	// prints whole ids, because that line exists to be copied into `run get`.
	table, _, _ := strings.Cut(out, "\u26a0")
	if strings.Contains(table, fullID) {
		t.Errorf("table column shows the untruncated id; got:\n%s", table)
	}
	if !strings.Contains(table, fullID[:16]) {
		t.Errorf("table lost the id prefix entirely; got:\n%s", table)
	}
}

// A run blocked by permissions reads, in every other surface, as a run that
// CHOSE not to act — which sends an operator after a prompt problem instead of
// a permission rule. `run get` is where that distinction has to be visible.
//
// Tool names only: the producer drops the denied input before the run record
// is written, because that input is arbitrary agent text and the record is
// hash-chained.
func TestRunGetRunE_RendersPermissionDenials(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["permission_denials"] = []string{"Bash", "Write"}
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Bash", "Write"} {
		if !strings.Contains(out, want) {
			t.Errorf("run get output does not name the denied tool %q; got:\n%s", want, out)
		}
	}
}

// Nothing denied is the normal case, and an empty row would claim the CLI was
// asked and answered nothing.
func TestRunGetRunE_OmitsPermissionRowWhenNothingDenied(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, provenanceRunBody()))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(strings.ToLower(out), "denied") {
		t.Errorf("run get shows a permission row for a run that had none; got:\n%s", out)
	}
}

// When the CLI reports skipped MCP servers in a shape the producer cannot read,
// it stores a sentinel — a category and no name — so the alarm survives the
// unreadable details. Every renderer here formats these entries BY NAME, so the
// alarm arrived naming nothing: `run list` printed "1 run started with MCP
// servers skipped" followed by an empty list, and `run get` a bare
// "MCP skipped:" row. The alarm was preserved and the ability to act on it was
// not.
//
// Falling back to the category also covers a real entry the CLI sends without a
// name.
func TestRunGetRunE_NamesTheUnreadableSkipSentinel(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["mcp_server_errors"] = []map[string]any{{"type": "unrecognized_shape"}}
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "MCP skipped") {
		t.Fatalf("run get dropped the skip row entirely; got:\n%s", out)
	}
	// The row must READ as the thing that was recorded, not as a parenthesised
	// afterthought hanging off a name that is not there.
	if strings.Contains(out, "(unrecognized_shape)") {
		t.Errorf("skip row renders as a qualifier with nothing to qualify — "+
			"the name slot is empty; got:\n%s", out)
	}
	if !strings.Contains(out, "unrecognized_shape") {
		t.Errorf("run get shows a skipped server it does not identify at all; got:\n%s", out)
	}
}

func TestRunListRunE_NoticeNamesTheUnreadableSkipSentinel(t *testing.T) {
	stub := covSetupCli4(t)
	setFormatCov(t, "table")
	body := provenanceRunBody()
	body["mcp_server_errors"] = []map[string]any{{"type": "unrecognized_shape"}}
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{body},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "started with MCP servers skipped") {
		t.Fatalf("listing lost the notice entirely; got:\n%s", out)
	}
	// The notice line is the one an operator copies into `run get`; a banner
	// counting runs it cannot name tells them a capability was lost and nothing
	// they can act on.
	if !strings.Contains(out, "skipped: unrecognized_shape") {
		t.Errorf("notice names no server for the run it flagged; got:\n%s", out)
	}
}

// The producer collapses an agent's repeated refusals of one tool into a single
// entry carrying the tally, deliberately: one denial is an agent that tried
// something once, forty is an agent hammering a wall it cannot see, and those
// send an operator to different fixes. `run get` is where that is read, and the
// row showed bare names — so the two runs looked identical.
func TestRunGetRunE_ShowsHowOftenEachToolWasDenied(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["permission_denials"] = []map[string]any{
		{"tool_name": "Bash", "count": 39},
		{"tool_name": "Write", "count": 1},
	}
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Bash ×39") {
		t.Errorf("run get does not show how hard the agent hammered the blocked tool; got:\n%s", out)
	}
	if !strings.Contains(out, "Write") {
		t.Errorf("run get lost the second denied tool; got:\n%s", out)
	}
	// A single refusal reads as the tool, not as "Write ×1" — a count nobody
	// needs is noise in the row that has to stay scannable.
	if strings.Contains(out, "Write ×1") {
		t.Errorf("run get decorates a single refusal with a count; got:\n%s", out)
	}
}

// A server sent a bare list of names — an older server this CLI is talking to.
// The count is a newer field, and a CLI that could no longer read the old shape
// would report NO denials at all for a blocked run, which is the misdiagnosis
// the row exists to prevent.
func TestRunGetRunE_ReadsTheOlderDenialShape(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["permission_denials"] = []string{"Bash", "Write"}
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Tools denied", "Bash", "Write"} {
		if !strings.Contains(out, want) {
			t.Errorf("run get lost %q from a server sending the older shape; got:\n%s", want, out)
		}
	}
}

// Both lists on a run record are lossy: the skip list drops entries this build
// could not identify, and both are capped. The producer records the true count
// and a truncation marker for exactly that reason, and `run get` printed the
// stored list alone — so a partial list read as the complete one.
func TestRunGetRunE_SaysWhenTheProvenanceListsArePartial(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["mcp_server_error_count"] = 3
	body["permission_denials"] = []map[string]any{{"tool_name": "Bash", "count": 2}}
	body["permission_denials_truncated"] = true
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// One server is named, three were skipped: the row must not read as one.
	if !strings.Contains(out, "2 more") {
		t.Errorf("run get names 1 skipped server for a run that skipped 3, and says nothing about the "+
			"other 2; got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "capped") {
		t.Errorf("run get shows a capped denial list as the whole story — the truncation marker the "+
			"producer wrote reaches nobody; got:\n%s", out)
	}
}

// A complete record must stay quiet: a caveat printed unconditionally is one
// operators learn to skip, and then the real one is invisible too.
func TestRunGetRunE_NoPartialNoteWhenTheRecordIsComplete(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["mcp_server_error_count"] = 1 // exactly what the list names
	body["permission_denials"] = []map[string]any{{"tool_name": "Bash", "count": 2}}
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, unwanted := range []string{"more", "capped"} {
		if strings.Contains(strings.ToLower(out), unwanted) {
			t.Errorf("run get hedges a complete record with %q; got:\n%s", unwanted, out)
		}
	}
}

// The listing's notice is the surface an operator scans before opening any run,
// and it names the skipped servers from the stored list — which is not all of
// them when entries dropped or the list was capped.
func TestRunListRunE_NoticeSaysWhenItNamesFewerThanWereSkipped(t *testing.T) {
	stub := covSetupCli4(t)
	setFormatCov(t, "table")
	body := provenanceRunBody()
	body["mcp_server_error_count"] = 4
	stub.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{body},
	}))

	out, err := covCaptureStdoutCli4(t, func() error {
		return runListCmd.RunE(runListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "crewship-memory") {
		t.Fatalf("notice lost the server it could name; got:\n%s", out)
	}
	if !strings.Contains(out, "3 more") {
		t.Errorf("notice names 1 of 4 skipped servers and reads as complete; got:\n%s", out)
	}
}

// The producer records the same sentinel for a permission denial it could not
// read, and it must reach the operator for the same reason: a blocked run that
// names nothing still has to be visible as blocked.
//
// This pins the LAST hop only — the row this command renders once
// journal.decodeDeniedToolNames has carried the sentinel through (the hop that
// was broken, covered in internal/journal). It was already green: kept because
// the sentinel's whole point is arriving here, and the previous round proved
// that a sentinel nobody rendered is a sentinel that did not exist.
func TestRunGetRunE_RendersTheDenialSentinel(t *testing.T) {
	stub := covSetupCli4(t)
	body := provenanceRunBody()
	body["permission_denials"] = []string{"unrecognized_shape"}
	stub.OnGet("/api/v1/runs/msg_prov", clitest.JSONResponse(200, body))
	runGetCmd.SetContext(context.Background())

	out, err := covCaptureStdoutCli4(t, func() error {
		return runGetCmd.RunE(runGetCmd, []string{"msg_prov"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Tools denied") || !strings.Contains(out, "unrecognized_shape") {
		t.Errorf("run get hides a run the CLI blocked; got:\n%s", out)
	}
}
