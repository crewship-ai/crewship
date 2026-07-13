package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These tests lock the --format contract for batch 4 of #964: the journal,
// activity, inbox, and checkpoint families historically honored only
// `--format json` (± yaml) and silently degraded `--format ndjson` to
// ANSI-colored human text. Each command now routes through
// Formatter.AutoHuman, so a machine format produces machine-parseable stdout
// while the default (no --format) keeps its hand-crafted human view.
//
// Each guard below drives the real RunE against a clitest stub and asserts
// the distinctive human substrings survive when no machine format is set.
// covSetupCli5 blanks flagFormat, so these exercise the default (human) arm.

// ─── journal ─────────────────────────────────────────────────────────────

func TestJournalListRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-07-13T08:00:00Z", "entry_type": "keeper.decision", "severity": "warn", "actor_type": "keeper", "summary": "OOMKilled on backend-team"},
		},
		"count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = journalCmd.RunE(journalCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"keeper.decision", "OOMKilled on backend-team"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestJournalListRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// journalCmd routes body.Entries (a slice of map[string]any) through
// f.AutoHuman now, so --format yaml/ndjson must carry the entries instead
// of the per-line human formatter.
func TestJournalListRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"ts": "2026-07-13T08:00:00Z", "entry_type": "keeper.decision", "severity": "warn", "actor_type": "keeper", "summary": "OOMKilled on backend-team"},
			{"ts": "2026-07-13T08:05:00Z", "entry_type": "peer.escalation", "severity": "error", "actor_type": "agent", "summary": "escalated to human"},
		},
		"count": 2,
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = journalCmd.RunE(journalCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 entries, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["entry_type"] != "keeper.decision" || yamlRows[1]["entry_type"] != "peer.escalation" {
		t.Errorf("yaml entries mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = journalCmd.RunE(journalCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per journal entry (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["entry_type"] != "keeper.decision" || second["entry_type"] != "peer.escalation" {
		t.Errorf("ndjson entries mismatch: %+v / %+v", first, second)
	}
}

func TestJournalLookupRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/journal/lookup", clitest.JSONResponse(200, map[string]any{
		"crews":    []map[string]any{{"id": "crew_1", "name": "Backend Team", "slug": "backend-team"}},
		"agents":   []map[string]any{},
		"missions": []map[string]any{},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = journalLookupCmd.RunE(journalLookupCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Crews (1)", "Backend Team"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestJournalGetRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/journal/j_abc", clitest.JSONResponse(200, map[string]any{
		"ts": "2026-07-13T08:00:00Z", "entry_type": "peer.escalation", "severity": "error",
		"actor_type": "agent", "summary": "escalated to human",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = journalGetCmd.RunE(journalGetCmd, []string{"j_abc"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"peer.escalation", "escalated to human"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestJournalCountRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/journal/count", clitest.JSONResponse(200, map[string]any{"total": 137}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = journalCountCmd.RunE(journalCountCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human view is the bare integer on its own line (not `"total": 137`).
	if strings.Contains(out, "total") {
		t.Errorf("default view leaked JSON key; got:\n%s", out)
	}
	if !strings.Contains(out, "137") {
		t.Errorf("human output missing count 137; got:\n%s", out)
	}
}

// ─── activity ────────────────────────────────────────────────────────────

func TestActivityRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/activity", clitest.JSONResponse(200, []map[string]any{
		{"type": "ESCALATION", "crew_slug": "backend-team", "summary": "spend cap hit", "created_at": "2026-07-13T08:00:00Z"},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = activityCmd.RunE(activityCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"ESCALATION", "backend-team", "spend cap hit"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestActivityRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// activityCmd routes the activities slice through f.AutoHuman now, so
// --format yaml/ndjson must carry the rows instead of the colored feed.
func TestActivityRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/activity", clitest.JSONResponse(200, []map[string]any{
		{"type": "ESCALATION", "crew_slug": "backend-team", "summary": "spend cap hit", "created_at": "2026-07-13T08:00:00Z"},
		{"type": "ASSIGNMENT", "crew_slug": "frontend-team", "summary": "assigned task B", "created_at": "2026-07-13T08:05:00Z"},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = activityCmd.RunE(activityCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 activity rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["type"] != "ESCALATION" || yamlRows[1]["type"] != "ASSIGNMENT" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = activityCmd.RunE(activityCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per activity row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["type"] != "ESCALATION" || second["type"] != "ASSIGNMENT" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
	}
}

// ─── inbox ───────────────────────────────────────────────────────────────

func TestInboxListRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "inb_1", "kind": "waitpoint", "state": "unread", "priority": "high", "sender_name": "eva", "title": "Approve deploy"},
		},
		"count": 1, "unread_count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Approve deploy", "unread", "1 items · 1 unread"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestInboxListRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// inboxListCmd routes body.Rows (a slice) through f.AutoHuman now, so
// --format yaml/ndjson must carry the rows instead of the colored feed
// (and the trailing "N items · N unread" summary line).
func TestInboxListRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "inb_1", "kind": "waitpoint", "state": "unread", "priority": "high", "sender_name": "eva", "title": "Approve deploy"},
			{"id": "inb_2", "kind": "message", "state": "read", "priority": "low", "sender_name": "viktor", "title": "FYI"},
		},
		"count": 2, "unread_count": 1,
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 inbox rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["kind"] != "waitpoint" || yamlRows[1]["kind"] != "message" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = inboxListCmd.RunE(inboxListCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per inbox row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["kind"] != "waitpoint" || second["kind"] != "message" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
	}
}

func TestInboxGetRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox/inb_1", clitest.JSONResponse(200, map[string]any{
		"id": "inb_1", "kind": "escalation", "state": "read", "priority": "high",
		"sender_name": "viktor", "title": "Budget review", "body_md": "please confirm",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxGetCmd.RunE(inboxGetCmd, []string{"inb_1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Budget review", "escalation", "from viktor"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestInboxCountRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/inbox/count", clitest.JSONResponse(200, map[string]any{"unread_count": 9}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = inboxCountCmd.RunE(inboxCountCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Human view is the bare integer, not `"unread_count": 9`.
	if strings.Contains(out, "unread_count") {
		t.Errorf("default view leaked JSON key; got:\n%s", out)
	}
	if !strings.Contains(out, "9") {
		t.Errorf("human output missing count 9; got:\n%s", out)
	}
}

// ─── checkpoint ──────────────────────────────────────────────────────────

func TestCheckpointCreateRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, checkpointCreateCmd, "mission", "MIS-1")
	stub.OnPost("/api/v1/missions/MIS-1/checkpoints", clitest.JSONResponse(200, map[string]any{
		"id": "chk_1", "label": "green build", "journal_cursor": "cur_42",
	}))

	var err error
	// PrintSuccess writes to stderr, so capture both streams.
	out := covCaptureAll(t, func() { err = checkpointCreateCmd.RunE(checkpointCreateCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Checkpoint created", "chk_1", "green build"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

func TestCheckpointRestoreRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/checkpoints/chk_1/restore", clitest.JSONResponse(200, map[string]any{
		"checkpoint":      map[string]any{"id": "chk_1"},
		"journal_cursor":  "cur_42",
		"warn_divergence": []string{},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Checkpoint:", "chk_1", "No divergence since checkpoint."} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestCheckpointRestoreRunE_YAMLAndNDJSON is the flip side of
// DefaultStaysHuman: checkpointRestoreCmd routes the single restore-result
// struct through f.AutoHuman now, so --format yaml/ndjson must carry the
// {checkpoint, journal_cursor, warn_divergence} payload instead of the
// prose report. Unlike the roster/hooks/journal list commands, this
// payload is a single object — ndjson must emit exactly one line.
func TestCheckpointRestoreRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/checkpoints/chk_1/restore", clitest.JSONResponse(200, map[string]any{
		"checkpoint":      map[string]any{"id": "chk_1"},
		"journal_cursor":  "cur_42",
		"warn_divergence": []string{},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_1"}) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	checkpoint, ok := yamlPayload["checkpoint"].(map[string]any)
	if !ok || checkpoint["id"] != "chk_1" {
		t.Errorf("yaml payload missing checkpoint.id=chk_1: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_1"}) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The restore result is a single struct (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the restore object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	ndCheckpoint, ok := ndjsonPayload["checkpoint"].(map[string]any)
	if !ok || ndCheckpoint["id"] != "chk_1" {
		t.Errorf("ndjson payload missing checkpoint.id=chk_1: %+v", ndjsonPayload)
	}
}

func TestCheckpointForkRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/checkpoints/chk_1/fork", clitest.JSONResponse(200, map[string]any{
		"new_mission_id": "MIS-9", "new_checkpoint_id": "chk_2",
	}))

	var err error
	// PrintSuccess writes to stderr, so capture both streams.
	out := covCaptureAll(t, func() { err = checkpointForkCmd.RunE(checkpointForkCmd, []string{"chk_1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Forked mission MIS-9", "chk_2"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}
