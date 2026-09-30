package database

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// keeperGovernanceSnapshotVersion pins every existing workspace's Keeper
// watchdog settings to what it runs on today. Until it, a workspace with no
// row of its own read the instance defaults live; from it on, the defaults
// are a template copied in at creation, and a workspace with no row reads the
// built-in opt-out. Without this backfill the switch would silently change
// every such workspace that an instance admin had covered by saving defaults.
const keeperGovernanceSnapshotVersion = 20260930092745

func TestKeeperGovernanceSnapshotPinsWhatEachWorkspaceRunsOnToday(t *testing.T) {
	t.Parallel()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db := openMigratedTestDB(t)

	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_none', 'None', 'ws-none'), ('ws_own', 'Own', 'ws-own'), ('ws_gone', 'Gone', 'ws-gone')`)
	execMigrationFixture(t, db, `UPDATE workspaces SET deleted_at = '2026-09-01' WHERE id = 'ws_gone'`)
	execMigrationFixture(t, db, `INSERT INTO keeper_governance_settings (workspace_id, enabled, deny_notify_min_risk) VALUES ('ws_own', 0, 2)`)
	execMigrationFixture(t, db, `INSERT INTO app_settings (key, value) VALUES ('keeper.governance_defaults',
		'{"enabled":true,"deny_notify_min_risk":4,"watch_spec":"flag ssh","watch_presets":["exfiltration"],"require_second_approver":true,"gov_model_provider":"ollama","gov_model_id":"qwen","auto_lease_seconds":900,"behavior_sample_every":10}')`)
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, keeperGovernanceSnapshotVersion); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}

	var enabled, risk, second, lease, sample int
	var spec, presets, provider, model string
	if err := db.QueryRow(`SELECT enabled, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver, gov_model_provider, gov_model_id, auto_lease_seconds, behavior_sample_every
		FROM keeper_governance_settings WHERE workspace_id = 'ws_none'`).Scan(&enabled, &risk, &spec, &presets, &second, &provider, &model, &lease, &sample); err != nil {
		t.Fatalf("ws_none has no row: %v", err)
	}
	if enabled != 1 || risk != 4 || spec != "flag ssh" || presets != `["exfiltration"]` || second != 1 || provider != "ollama" || model != "qwen" || lease != 900 || sample != 10 {
		t.Fatalf("ws_none = %d %d %q %q %d %q %q %d %d, want the defaults it ran on", enabled, risk, spec, presets, second, provider, model, lease, sample)
	}
	var ownRisk int
	_ = db.QueryRow(`SELECT deny_notify_min_risk FROM keeper_governance_settings WHERE workspace_id = 'ws_own'`).Scan(&ownRisk)
	if ownRisk != 2 {
		t.Fatalf("ws_own risk = %d, want its own row untouched", ownRisk)
	}
	var gone int
	_ = db.QueryRow(`SELECT COUNT(*) FROM keeper_governance_settings WHERE workspace_id = 'ws_gone'`).Scan(&gone)
	if gone != 0 {
		t.Fatal("a deleted workspace got a row")
	}
}

func TestKeeperGovernanceSnapshotWithoutDefaultsPinsTheBuiltIn(t *testing.T) {
	t.Parallel()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db := openMigratedTestDB(t)
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_plain', 'Plain', 'ws-plain')`)
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, keeperGovernanceSnapshotVersion); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}
	var enabled, risk, sample int
	var presets string
	if err := db.QueryRow(`SELECT enabled, deny_notify_min_risk, watch_presets, behavior_sample_every FROM keeper_governance_settings WHERE workspace_id = 'ws_plain'`).Scan(&enabled, &risk, &presets, &sample); err != nil {
		t.Fatalf("no row: %v", err)
	}
	if enabled != 0 || risk != 7 || presets != "" || sample != 0 {
		t.Fatalf("ws_plain = %d %d %q %d, want the built-in opt-out", enabled, risk, presets, sample)
	}
}
