package main

// Tests for the second follow-up format sweep (#1195) and the yaml/json
// field-casing bug (#1211), both filed from dev3 live testing on
// 2026-07-15 (follow-up to the #964 sweep).
//
// #1195 covers commands that silently ignored --format entirely (routine
// runs, backup list/status, server list, routine webhooks create) or the
// INVERSE bug (backup inspect always printing raw JSON no matter what
// --format said). copy-prompt is confirmed a deliberate exception (raw
// text passthrough for piping) and is only checked for its documented
// doc-comment here, not "fixed" to emit JSON.
//
// #1211 covers commands whose --format yaml output used different
// (squashed-lowercase, e.g. "crewid") field names than --format json
// (snake_case, e.g. "crew_id") for the exact same data — a missing
// yaml:"..." struct tag defaulting to yaml.v3's lowercased-fieldname
// fallback instead of inheriting the json:"..." tag's snake_case.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── #1195: routine runs ──────────────────────────────────────────────

func TestPipelineRunsRunE_FormatJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFormatCov(t, "json")
	runsPath := pipelinesPathCov() + "/email-fetch/runs"
	stub.OnGet(runsPath, clitest.JSONResponse(200, []map[string]any{
		{"id": "j1", "ts": "2026-01-01T00:00:00Z", "entry_type": "pipeline.completed",
			"severity": "info", "summary": "ok", "run_id": "run_aaaaaaaaaaaaaaaaaaaaaa"},
	}))

	out, err := captureStdoutCov(t, func() error {
		return pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"email-fetch"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", jerr, out)
	}
	// The human table truncates run_id to 16 chars + "…"; the whole point
	// of #1195 item 1 is that JSON must carry the FULL id (needed by
	// diff/inspect/explain per #1193).
	if len(rows) != 1 || rows[0]["run_id"] != "run_aaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("expected full untruncated run_id in JSON output; got %v", rows)
	}
}

func TestPipelineRunsRunE_FormatYAML(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFormatCov(t, "yaml")
	runsPath := pipelinesPathCov() + "/email-fetch/runs"
	stub.OnGet(runsPath, clitest.JSONResponse(200, []map[string]any{
		{"id": "j1", "ts": "2026-01-01T00:00:00Z", "entry_type": "pipeline.completed",
			"severity": "info", "summary": "ok", "run_id": "run_yyyy"},
	}))

	out, err := captureStdoutCov(t, func() error {
		return pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"email-fetch"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "run_id: run_yyyy") {
		t.Errorf("--format yaml must emit YAML rows; got:\n%s", out)
	}
	if strings.Contains(out, "TS\tTYPE") {
		t.Errorf("--format yaml must not fall back to the human tabwriter table; got:\n%s", out)
	}
}

// ─── #1195: backup list / status / inspect ────────────────────────────

func TestBackupListRunE_FormatJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })
	stub.OnGet("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{
			"path": "/srv/backups/b1.tar.zst", "file_name": "b1.tar.zst",
			"size_bytes": 2048, "scope": "workspace", "encrypted": true,
			"created_at": "2026-06-01T00:00:00Z", "format_version": 2,
		}},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupListCmd.RunE(backupListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["file_name"] != "b1.tar.zst" {
		t.Errorf("rows = %v", rows)
	}
}

func TestBackupListRunE_FormatJSON_EmptyEmitsArray(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })
	stub.OnGet("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{"data": []any{}}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupListCmd.RunE(backupListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--format json with no backups should emit an empty JSON array, not the human stderr message; got %q", out)
	}
}

func TestBackupStatusRunE_FormatJSON_NotHeld(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })
	stub.OnGet("/api/v1/admin/backups/status", clitest.JSONResponse(200, map[string]any{"held": false}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupStatusCmd.RunE(backupStatusCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", err, out)
	}
	if payload["held"] != false {
		t.Errorf("payload = %v", payload)
	}
}

func TestBackupInspectRunE_DefaultStaysJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups/inspect", clitest.JSONResponse(200, map[string]any{
		"scope": "crew", "format_version": 2,
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupInspectCmd.RunE(backupInspectCmd, []string{"/tmp/b.tar.zst"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"scope"`) || !strings.Contains(out, "crew") {
		t.Errorf("default (no --format) must stay raw JSON, matching the pre-existing behavior scripts depend on; got:\n%s", out)
	}
}

func TestBackupInspectRunE_FormatTableRendersHuman(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	origFormat := flagFormat
	flagFormat = "table"
	t.Cleanup(func() { flagFormat = origFormat })
	stub.OnGet("/api/v1/admin/backups/inspect", clitest.JSONResponse(200, map[string]any{
		"format_version":             2,
		"crewship_version_at_backup": "1.0.0",
		"scope":                      "workspace",
		"scope_level":                "standard",
		"created_at":                 "2026-06-01T00:00:00Z",
		"created_by":                 map[string]any{"email": "ops@example.com", "role": "OWNER"},
		"source_instance":            map[string]any{"hostname": "host1", "platform": "linux"},
		"encryption":                 map[string]any{"enabled": true, "algorithm": "age-x25519"},
		"checksums":                  map[string]any{"payload_sha256": "abc123def456"},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupInspectCmd.RunE(backupInspectCmd, []string{"/tmp/b.tar.zst"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if strings.Contains(out, `"scope"`) || strings.Contains(out, `"format_version"`) {
		t.Errorf("-f table must render a human summary, not raw JSON; got:\n%s", out)
	}
	for _, want := range []string{"Format version", "v2", "workspace", "ops@example.com", "age-x25519"} {
		if !strings.Contains(out, want) {
			t.Errorf("human summary missing %q; got:\n%s", want, out)
		}
	}
}

func TestBackupInspectRunE_FormatYAML(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	origFormat := flagFormat
	flagFormat = "yaml"
	t.Cleanup(func() { flagFormat = origFormat })
	stub.OnGet("/api/v1/admin/backups/inspect", clitest.JSONResponse(200, map[string]any{
		"scope": "crew", "format_version": 2,
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupInspectCmd.RunE(backupInspectCmd, []string{"/tmp/b.tar.zst"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "scope: crew") {
		t.Errorf("--format yaml must emit YAML; got:\n%s", out)
	}
}

// ─── #1195: server list ────────────────────────────────────────────────

func TestServerListRunE_FormatJSON(t *testing.T) {
	redirectConfigHome(t)
	t.Setenv("CREWSHIP_PROFILE", "") // don't let an ambient shell profile win over cfg.Current
	oldServer := flagServer
	t.Cleanup(func() { flagServer = oldServer })
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })

	flagServer = "https://dev1.example"
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	var err error
	out := covCaptureStdoutCli8(t, func() { err = serverListCmd.RunE(serverListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var rows []serverProfileRow
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", jerr, out)
	}
	if len(rows) != 1 || rows[0].Name != "dev1" || rows[0].Server != "https://dev1.example" {
		t.Errorf("rows = %+v", rows)
	}
	if !rows[0].Active {
		t.Errorf("the only (first-added) profile should be marked active: %+v", rows[0])
	}
}

func TestServerListRunE_DefaultStaysHuman(t *testing.T) {
	redirectConfigHome(t)
	oldServer := flagServer
	t.Cleanup(func() { flagServer = oldServer })

	flagServer = "https://dev1.example"
	if err := serverAddCmd.RunE(serverAddCmd, []string{"dev1"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	var err error
	out := covCaptureStdoutCli8(t, func() { err = serverListCmd.RunE(serverListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "dev1") || !strings.Contains(out, "https://dev1.example") {
		t.Errorf("human table missing profile row; got:\n%s", out)
	}
}

// ─── #1195: routine webhooks create ────────────────────────────────────

func TestRoutineWebhooksCreateRunE_FormatJSON(t *testing.T) {
	stub := covSetupCli4(t)
	created := covWebhookRow("wh-9", "github-prs", "pr-review", "tok_new_9999", true, true)
	created["signing_secret"] = "shhh-hmac-secret"
	stub.OnPost(covWebhooksPath, clitest.JSONResponse(201, created))

	c := covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"slug":        "pr-review",
		"hmac-secret": "shhh-hmac-secret",
		"base-url":    "https://hooks.example.com",
	})
	origFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = origFormat })

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var payload struct {
		ID            string `json:"id"`
		SigningSecret string `json:"signing_secret"`
		PublicURL     string `json:"public_url"`
	}
	if jerr := json.Unmarshal([]byte(out), &payload); jerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", jerr, out)
	}
	if payload.ID != "wh-9" {
		t.Errorf("id = %q", payload.ID)
	}
	// Unlike `webhooks list` (which redacts on machine output since it can
	// be re-run any time), `create` is the ONE moment the secret exists in
	// plaintext — a script capturing --format json needs it unredacted.
	if payload.SigningSecret != "shhh-hmac-secret" {
		t.Errorf("signing_secret should be present unredacted on create; got %q", payload.SigningSecret)
	}
	if payload.PublicURL != "https://hooks.example.com/api/v1/webhooks/tok_new_9999" {
		t.Errorf("public_url = %q", payload.PublicURL)
	}
	if strings.Contains(out, "shown once") {
		t.Errorf("--format json must not include the human reveal banner text; got:\n%s", out)
	}
}

// ─── #1195: copy-prompt (documented deliberate exception) ─────────────

func TestCopyPromptCmd_DocumentsFormatException(t *testing.T) {
	if !strings.Contains(copyPromptCmd.Long, "--format is intentionally ignored") {
		t.Errorf("copy-prompt's Long help should document that --format is intentionally ignored (deliberate exception, #1195); got:\n%s", copyPromptCmd.Long)
	}
}

// ─── #1211: yaml/json field-name casing parity ─────────────────────────

func TestIssueListRunE_YAMLFieldNamesMatchJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, issueListCmd, "status", "priority", "crew", "assignee", "label", "search", "limit")
	origFormat := flagFormat
	flagFormat = "yaml"
	t.Cleanup(func() { flagFormat = origFormat })
	stub.OnGet("/api/v1/issues", clitest.JSONResponse(200, covIssueListPayload()))

	out, err := covCaptureStdoutCli6(t, func() error {
		return issueListCmd.RunE(issueListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "crew_id:") {
		t.Errorf("--format yaml must use snake_case crew_id (matching --format json), not crewid; got:\n%s", out)
	}
	if strings.Contains(out, "crewid:") {
		t.Errorf("--format yaml regressed to squashed-lowercase crewid; got:\n%s", out)
	}
}

func TestInboxListRunE_YAMLFieldNamesMatchJSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "yaml"
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, covInboxRows()))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"source_id:", "body_md:", "sender_type:"} {
		if !strings.Contains(out, want) {
			t.Errorf("--format yaml missing %q (snake_case, matching --format json); got:\n%s", want, out)
		}
	}
	for _, bad := range []string{"sourceid:", "bodymd:", "sendertype:"} {
		if strings.Contains(out, bad) {
			t.Errorf("--format yaml regressed to squashed-lowercase %q; got:\n%s", bad, out)
		}
	}
}

func TestAuditRunE_YAMLFieldNamesMatchJSON(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)
	origFormat := flagFormat
	flagFormat = "yaml"
	t.Cleanup(func() { flagFormat = origFormat })

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/audit", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{
			{
				"id": "a1", "action": "credential.rotate", "entity_type": "CREDENTIAL",
				"entity_id": "cred_1", "user_email": "ops@example.com",
				"created_at": "2026-06-01T10:20:30.123Z",
			},
		},
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	out, err := captureStdout(t, func() error {
		return auditCmd.RunE(auditCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "entity_type:") {
		t.Errorf("--format yaml must use snake_case entity_type (matching --format json), not entitytype; got:\n%s", out)
	}
	if strings.Contains(out, "entitytype:") {
		t.Errorf("--format yaml regressed to squashed-lowercase entitytype; got:\n%s", out)
	}
}
