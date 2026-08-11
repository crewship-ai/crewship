package backup_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestAutomations_RoundTrip proves the rules actually survive a bundle.
//
// automations was in neither BackupTableIntent nor NonBackedUpTables, so it was
// never a decision — it was an omission, of exactly the shape that lost data in
// #1437 and #1444: a plain workspace_id COLUMN with no FOREIGN KEY into the
// workspaces chain, so the reverse-FK walk never reaches it.
//
// Classifying it is necessary and not sufficient. This asserts the row comes
// back with the fields that MAKE it a rule — the event it listens for, the
// matcher, and the routine it fires. A rule restored with an empty matcher is
// worse than a missing one: it matches everything.
//
// The failure it guards is the quiet kind. Nothing errors after a lossy
// restore; the instance simply stops doing things by itself, and the user finds
// out when the thing that was supposed to happen did not.
func TestAutomations_RoundTrip(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	const (
		autID     = "aut_roundtrip_1"
		eventType = "mission.status_change"
		matcher   = `{"payload_equals":{"to":"DONE"}}`
		action    = `{"routine_slug":"post-status-triage"}`
	)
	if _, err := source.ExecContext(ctx, `
		INSERT INTO automations (id, workspace_id, name, enabled, event_type, matcher_json,
			action_kind, action_config_json, debounce_seconds, max_per_hour, created_at, updated_at)
		VALUES (?, ?, 'triage on close', 1, ?, ?, 'routine', ?, 10, 60,
			'2026-08-07T12:00:00.000000000Z', '2026-08-07T12:00:00.000000000Z')`,
		autID, workspaceID, eventType, matcher, action); err != nil {
		t.Fatalf("seed automation: %v", err)
	}

	// A soft-deleted rule rides along too. internal/chain reads deleted rules
	// to explain the runs they caused, so dropping them on restore would make
	// restored history unexplainable — a run stamped triggered_via='automation'
	// with no rule beside it reads as "nobody started this".
	const deletedID = "aut_roundtrip_deleted"
	if _, err := source.ExecContext(ctx, `
		INSERT INTO automations (id, workspace_id, name, enabled, event_type, matcher_json,
			action_kind, action_config_json, debounce_seconds, max_per_hour, created_at, updated_at, deleted_at)
		VALUES (?, ?, 'retired rule', 1, 'run.failed', '{}', 'routine', ?, 10, 60,
			'2026-08-07T11:00:00.000000000Z', '2026-08-07T11:30:00.000000000Z',
			'2026-08-07T11:30:00.000000000Z')`,
		deletedID, workspaceID, action); err != nil {
		t.Fatalf("seed deleted automation: %v", err)
	}

	const passphrase = "automations-roundtrip-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	if _, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       created.Path,
		Passphrase: passphrase,
		Actor:      actor,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	var gotEvent, gotMatcher, gotAction string
	var gotEnabled int
	if err := target.QueryRowContext(ctx,
		`SELECT event_type, matcher_json, action_config_json, enabled
		   FROM automations WHERE id = ? AND workspace_id = ?`,
		autID, workspaceID).Scan(&gotEvent, &gotMatcher, &gotAction, &gotEnabled); err != nil {
		t.Fatalf("automation missing from restored target (silent data loss): %v", err)
	}
	if gotEvent != eventType {
		t.Errorf("event_type = %q, want %q — a rule that listens for nothing never fires", gotEvent, eventType)
	}
	if gotMatcher != matcher {
		t.Errorf("matcher_json = %q, want %q — an emptied matcher does not fail, it matches EVERYTHING",
			gotMatcher, matcher)
	}
	if gotAction != action {
		t.Errorf("action_config_json = %q, want %q — without the routine slug the rule fires nothing",
			gotAction, action)
	}
	if gotEnabled != 1 {
		t.Errorf("enabled = %d, want 1 — a rule restored switched off is indistinguishable from one a user paused", gotEnabled)
	}

	var deletedAt string
	if err := target.QueryRowContext(ctx,
		`SELECT COALESCE(deleted_at, '') FROM automations WHERE id = ? AND workspace_id = ?`,
		deletedID, workspaceID).Scan(&deletedAt); err != nil {
		t.Fatalf("soft-deleted automation missing from restored target: %v", err)
	}
	if deletedAt == "" {
		t.Error("the soft-deleted rule came back LIVE — a restore that resurrects retired rules " +
			"starts firing routines nobody asked for")
	}
}
