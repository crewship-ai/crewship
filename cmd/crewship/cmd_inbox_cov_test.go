package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func covInboxRows() map[string]any {
	return map[string]any{
		"rows": []map[string]any{
			{"id": "inb_1", "kind": "waitpoint", "title": "Approve deploy", "sender_name": "eva",
				"state": "unread", "priority": "high", "created_at": "2026-06-12T09:00:00Z"},
			{"id": "inb_2", "kind": "escalation", "title": "Disk full", "sender_name": "viktor",
				"state": "read", "priority": "urgent"},
			{"id": "inb_3", "kind": "failed_run", "title": "Run crashed", "sender_name": "petra",
				"state": "resolved", "priority": "low"},
			{"id": "inb_4", "kind": "message", "title": "FYI", "sender_name": "ona",
				"state": "unread", "priority": "normal"},
		},
		"count":        4,
		"unread_count": 2,
	}
}

// ─── inbox list ──────────────────────────────────────────────────────────

func TestInboxListRunE_Table(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, covInboxRows()))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"inb_1", "Approve deploy", "waitpoint", "escalation", "4 items · 2 unread"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got:\n%s", want, out)
		}
	}
	calls := stub.CallsFor("GET", "/api/v1/inbox")
	if len(calls) != 1 {
		t.Fatalf("expected 1 GET inbox, got %d", len(calls))
	}
	q := calls[0].Query
	for _, want := range []string{"state=unread", "limit=50", "workspace_id=" + covWSCli5} {
		if !strings.Contains(q, want) {
			t.Errorf("list query missing %q: %q", want, q)
		}
	}
}

func TestInboxListRunE_StateAllAndKind(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxListCmd, "state", "all")
	covSetFlagCli5(t, inboxListCmd, "kind", "waitpoint")
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, covInboxRows()))

	var err error
	covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	q := stub.CallsFor("GET", "/api/v1/inbox")[0].Query
	if !strings.Contains(q, "state=all") || !strings.Contains(q, "kind=waitpoint") {
		t.Errorf("query missing state=all/kind=waitpoint: %q", q)
	}
}

func TestInboxListRunE_JSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, covInboxRows()))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var rows []map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &rows); jsonErr != nil {
		t.Fatalf("output is not a JSON array: %v\n%s", jsonErr, out)
	}
	if len(rows) != 4 || rows[0]["id"] != "inb_1" {
		t.Errorf("rows = %v", rows)
	}
}

func TestInboxListRunE_Quiet(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "quiet"
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, covInboxRows()))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if out != "" {
		t.Errorf("quiet must print nothing; got %q", out)
	}
}

func TestInboxListRunE_NoWorkspace(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}

	err := inboxListCmd.RunE(inboxListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// ─── state transitions ───────────────────────────────────────────────────

func TestInboxReadCmdRunE(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/inbox/inb_1", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureAll(t, func() { err = inboxReadCmd.RunE(inboxReadCmd, []string{"inb_1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/inbox/inb_1")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["state"] != "read" {
		t.Errorf("state = %q, want read", body["state"])
	}
	if _, ok := body["resolved_action"]; ok {
		t.Errorf("read transition must not send resolved_action; body=%v", body)
	}
	if !strings.Contains(calls[0].Query, "workspace_id="+covWSCli5) {
		t.Errorf("PATCH query missing workspace_id: %q", calls[0].Query)
	}
}

func TestInboxUnreadCmdRunE(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/inbox/inb_2", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureAll(t, func() { err = inboxUnreadCmd.RunE(inboxUnreadCmd, []string{"inb_2"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/inbox/inb_2")[0].Body, &body)
	if body["state"] != "unread" {
		t.Errorf("state = %q, want unread", body["state"])
	}
}

func TestInboxResolveCmdRunE_WithAction(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxResolveCmd, "action", "approved")
	stub.OnPatch("/api/v1/inbox/inb_3", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureAll(t, func() { err = inboxResolveCmd.RunE(inboxResolveCmd, []string{"inb_3"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/inbox/inb_3")[0].Body, &body)
	if body["state"] != "resolved" || body["resolved_action"] != "approved" {
		t.Errorf("body = %v, want state=resolved action=approved", body)
	}
}

func TestPatchInboxState_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/inbox/ghost", clitest.ErrorResponse(404, "inbox item not found"))

	err := patchInboxState("ghost", "read", "")
	if err == nil || !strings.Contains(err.Error(), "inbox item not found") {
		t.Errorf("expected 404 error; got %v", err)
	}
}

// ─── inbox count ─────────────────────────────────────────────────────────

func TestInboxCountRunE_Plain(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox/count", clitest.JSONResponse(200, map[string]any{"unread_count": 7}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxCountCmd.RunE(inboxCountCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.TrimSpace(out) != "7" {
		t.Errorf("plain count output = %q, want 7", out)
	}
}

func TestInboxCountRunE_JSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/inbox/count", clitest.JSONResponse(200, map[string]any{"unread_count": 3}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxCountCmd.RunE(inboxCountCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"unread_count": 3`) {
		t.Errorf("JSON count output = %q", out)
	}
}

// ─── inbox bulk ──────────────────────────────────────────────────────────

func TestInboxBulkReadRunE_FlagValidation(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkReadCmd, "ids", "")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "false")

	err := inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "either --ids <csv> or --all-unread is required") {
		t.Errorf("expected required-flag error; got %v", err)
	}

	covSetFlagCli5(t, inboxBulkReadCmd, "ids", "a,b")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "true")
	err = inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error; got %v", err)
	}
}

func TestInboxBulkReadRunE_EmptyIDsList(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkReadCmd, "ids", " , ,")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "false")

	err := inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "parsed to empty list") {
		t.Errorf("expected empty-list error; got %v", err)
	}
}

func TestInboxBulkReadRunE_SubmitsToBulkEndpoint(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkReadCmd, "ids", "ok_1, ghost ,ok_2")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "false")
	// One bulk request carries all parsed ids; the server reports the split.
	stub.OnPost("/api/v1/inbox/bulk", clitest.JSONResponse(200, map[string]any{
		"updated": 2, "skipped": 0, "not_found": 1, "state": "read",
	}))

	var err error
	out := covCaptureAll(t, func() { err = inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "2 updated") || !strings.Contains(out, "1 not found") {
		t.Errorf("expected aggregated summary; got:\n%s", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/inbox/bulk")
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 bulk POST, got %d", len(calls))
	}
	var body struct {
		IDs   []string `json:"ids"`
		State string   `json:"state"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.State != "read" {
		t.Errorf("bulk state = %q, want read", body.State)
	}
	// The empty token (" ghost " trims to "ghost"; the lone spaces drop) leaves
	// exactly the 3 non-empty ids in a single batch.
	if len(body.IDs) != 3 {
		t.Errorf("bulk ids = %v, want [ok_1 ghost ok_2]", body.IDs)
	}
}

func TestInboxBulkResolveRunE_AllUnread(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkResolveCmd, "ids", "")
	covSetFlagCli5(t, inboxBulkResolveCmd, "all-unread", "true")
	covSetFlagCli5(t, inboxBulkResolveCmd, "action", "acknowledged")
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"id": "u1"}, {"id": "u2"}},
	}))
	stub.OnPost("/api/v1/inbox/bulk", clitest.JSONResponse(200, map[string]any{
		"updated": 2, "skipped": 0, "not_found": 0, "state": "resolved",
	}))

	var err error
	covCaptureAll(t, func() { err = inboxBulkResolveCmd.RunE(inboxBulkResolveCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	listQ := stub.CallsFor("GET", "/api/v1/inbox")[0].Query
	if !strings.Contains(listQ, "state=unread") || !strings.Contains(listQ, "limit=500") {
		t.Errorf("all-unread list query wrong: %q", listQ)
	}
	// The fetched unread ids are submitted to the bulk endpoint in ONE request.
	calls := stub.CallsFor("POST", "/api/v1/inbox/bulk")
	if len(calls) != 1 {
		t.Fatalf("expected 1 bulk POST, got %d", len(calls))
	}
	var body struct {
		IDs            []string `json:"ids"`
		State          string   `json:"state"`
		ResolvedAction string   `json:"resolved_action"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.State != "resolved" || body.ResolvedAction != "acknowledged" {
		t.Errorf("bulk body = %+v, want state=resolved action=acknowledged", body)
	}
	if len(body.IDs) != 2 || body.IDs[0] != "u1" || body.IDs[1] != "u2" {
		t.Errorf("bulk ids = %v, want [u1 u2]", body.IDs)
	}
}

func TestInboxBulkReadRunE_AllUnreadAlreadyClean(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkReadCmd, "ids", "")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "true")
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, map[string]any{"rows": []map[string]any{}}))

	var err error
	out := covCaptureAll(t, func() { err = inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "already clean") {
		t.Errorf("expected already-clean message; got:\n%s", out)
	}
}

func TestInboxBulkReadRunE_AllUnreadHitsCap(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkReadCmd, "ids", "")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "true")
	rows := make([]map[string]any, 500)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("cap_%d", i)}
	}
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, map[string]any{"rows": rows}))

	err := inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unread-item page cap") {
		t.Errorf("expected cap refusal; got %v", err)
	}
	// Refusal must happen BEFORE any bulk mutation fires.
	for _, c := range stub.Calls() {
		if c.Method == "POST" {
			t.Fatalf("cap refusal must not submit a bulk request; saw POST %s", c.Path)
		}
	}
}

// ─── error + format branches round 2 ─────────────────────────────────────

func TestInboxListRunE_ErrorBranches(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := inboxListCmd.RunE(inboxListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox", clitest.ErrorResponse(500, "inbox wedged"))
	if err := inboxListCmd.RunE(inboxListCmd, nil); err == nil || !strings.Contains(err.Error(), "inbox wedged") {
		t.Errorf("expected 500; got %v", err)
	}

	stub2 := covSetupCli5(t)
	stub2.OnGet("/api/v1/inbox", clitest.TextResponse(200, "not json"))
	if err := inboxListCmd.RunE(inboxListCmd, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestInboxListRunE_YAML(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "yaml"
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, covInboxRows()))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE yaml: %v", err)
	}
	if !strings.Contains(out, "id: inb_1") {
		t.Errorf("yaml output missing rows; got:\n%s", out)
	}
}

func TestInboxCountRunE_ErrorBranchesAndYAML(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := inboxCountCmd.RunE(inboxCountCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	if err := inboxCountCmd.RunE(inboxCountCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}

	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox/count", clitest.ErrorResponse(500, "count wedged"))
	if err := inboxCountCmd.RunE(inboxCountCmd, nil); err == nil || !strings.Contains(err.Error(), "count wedged") {
		t.Errorf("expected 500; got %v", err)
	}

	stub2 := covSetupCli5(t)
	stub2.OnGet("/api/v1/inbox/count", clitest.TextResponse(200, "not json"))
	if err := inboxCountCmd.RunE(inboxCountCmd, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}

	stub3 := covSetupCli5(t)
	flagFormat = "yaml"
	stub3.OnGet("/api/v1/inbox/count", clitest.JSONResponse(200, map[string]any{"unread_count": 9}))
	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxCountCmd.RunE(inboxCountCmd, nil) })
	if err != nil {
		t.Fatalf("RunE yaml: %v", err)
	}
	if !strings.Contains(out, "unreadcount: 9") {
		t.Errorf("yaml count output wrong; got:\n%s", out)
	}
}

func TestInboxBulkRunE_AuthGatesAndListErrors(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	// --all-unread list fetch fails with API error.
	stub := covSetupCli5(t)
	covSetFlagCli5(t, inboxBulkReadCmd, "ids", "")
	covSetFlagCli5(t, inboxBulkReadCmd, "all-unread", "true")
	stub.OnGet("/api/v1/inbox", clitest.ErrorResponse(500, "list wedged"))
	if err := inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil); err == nil || !strings.Contains(err.Error(), "list wedged") {
		t.Errorf("expected list error; got %v", err)
	}

	// --all-unread list response malformed.
	stub.OnGet("/api/v1/inbox", clitest.TextResponse(200, "not json"))
	if err := inboxBulkReadCmd.RunE(inboxBulkReadCmd, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

// ─── inbox get ───────────────────────────────────────────────────────────

func TestInboxGetRunE_RendersBodyAndContext(t *testing.T) {
	stub := covSetupCli5(t)
	item := map[string]any{
		"id": "inb_9", "kind": "waitpoint", "title": "Approve restart",
		"body_md": "## Plan\nrolling restart", "sender_name": "atlas",
		"sender_type": "pipeline", "state": "unread", "priority": "high",
		"payload": map[string]any{"pipeline_run_id": "run_1", "step_id": "approve"},
	}
	stub.OnGet("/api/v1/inbox/inb_9", clitest.JSONResponse(200, item))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxGetCmd.RunE(inboxGetCmd, []string{"inb_9"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Approve restart", "atlas", "## Plan", "pipeline_run_id"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output missing %q; got:\n%s", want, out)
		}
	}
	calls := stub.CallsFor("GET", "/api/v1/inbox/inb_9")
	if len(calls) != 1 {
		t.Fatalf("expected 1 GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "workspace_id="+covWSCli5) {
		t.Errorf("get query missing workspace_id: %q", calls[0].Query)
	}
}

// ─── inbox archive ───────────────────────────────────────────────────────

func TestInboxArchiveRunE_MapsToResolvedArchived(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/inbox/inb_7", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureStdoutCli5(t, func() { err = inboxArchiveCmd.RunE(inboxArchiveCmd, []string{"inb_7"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/inbox/inb_7")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["state"] != "resolved" {
		t.Errorf("state = %q, want resolved", body["state"])
	}
	if body["resolved_action"] != "archived" {
		t.Errorf("resolved_action = %q, want archived", body["resolved_action"])
	}
}
