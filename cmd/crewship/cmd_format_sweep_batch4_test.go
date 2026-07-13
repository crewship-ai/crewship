package main

import (
	"strings"
	"testing"

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
