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
