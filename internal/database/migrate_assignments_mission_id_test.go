package database

// assignments.mission_id (#2256, PRD-ISSUES-AND-ROUTINES-2026 work package
// A2) — "every run is attributable to its issue".
//
// Three things are schema decisions rather than implementation details, and
// each is a production failure if it drifts:
//
//  1. the column + its read index exist, and the column is nullable;
//  2. deleting the MISSION (an issue is hard-deleted on several real paths —
//     see the migration's own comment) sets the run's mission_id to NULL
//     rather than deleting the run or refusing the delete;
//  3. the backfill migration recovers mission_id from
//     mission_comment_mentions for a row that predates this column, and
//     never overwrites a row that already has one.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
)

func TestMigrate_AssignmentsMissionID_ColumnAndIndexExist(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('assignments') WHERE name = 'mission_id'`).Scan(&n); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 1 {
		t.Fatal("assignments.mission_id column does not exist")
	}

	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_assignment_mission_created'`,
	).Scan(&n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if n != 1 {
		t.Fatal("idx_assignment_mission_created does not exist")
	}
}

// Deleting the mission must NOT be refused, and must NOT take the run down
// with it. PRAGMA foreign_keys is ON, so the default NO ACTION would make
// cleaning up a stale issue fail the moment it had ever dispatched a run —
// see the four `DELETE FROM missions` call sites the migration's comment
// names, every one of which hard-deletes a mission directly. CASCADE would
// instead destroy the run history this column exists to keep — an operator
// deleting a stale BACKLOG issue must not lose the record that an agent once
// worked on it.
func TestMigrate_AssignmentsMissionID_MissionDeleteSetsNull(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)

	if _, err := db.Exec(
		`UPDATE assignments SET mission_id = 'msn_mcm' WHERE id = 'asg_mcm'`); err != nil {
		t.Fatalf("set mission_id: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'msn_mcm'`); err != nil {
		t.Fatalf("deleting the mission was refused: %v", err)
	}

	var missionID sql.NullString
	if err := db.QueryRow(
		`SELECT mission_id FROM assignments WHERE id = 'asg_mcm'`).Scan(&missionID); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("the assignment was deleted along with its mission; run history must survive")
		}
		t.Fatalf("read back: %v", err)
	}
	if missionID.Valid {
		t.Errorf("mission_id = %q, want NULL after the mission was deleted", missionID.String)
	}
}

// TestMigrateBackfillsAssignmentsMissionID drives the real backfill
// migration (20260901180224) against the real schema, the way
// migrate_v148_backfill_network_mode_test.go / TestMigrateBackfillsOnboardingSkippedAt
// do: apply every migration once (0 rows to backfill, same as a real clone
// today — mission_comment_mentions holds 0 rows there), seed the case the
// backfill exists for, clear just the backfill migration's _migrations
// marker, and re-run Migrate.
func TestMigrateBackfillsAssignmentsMissionID(t *testing.T) {
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)

	// A second assignment, already carrying an explicit mission_id from a
	// write path added by this same package — the backfill must not touch
	// it (WHERE mission_id IS NULL is the guard).
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, mission_id)
		VALUES ('asg_mcm_2', 'ws_mcm', 'chat_mcm', 'agent_mcm_lead', 'agent_mcm_target', 'already linked', 'msn_mcm')`); err != nil {
		t.Fatalf("seed already-linked assignment: %v", err)
	}

	// The case the migration exists for: a mention dispatched BEFORE
	// mission_id existed on assignments left this table as the only
	// evidence linking the run to its issue.
	if err := insertMention(t, db, "mcm_backfill", "asg_mcm"); err != nil {
		t.Fatalf("insert mention: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260901180224`); err != nil {
		t.Fatalf("clear backfill marker: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (backfill): %v", err)
	}

	var missionID sql.NullString
	if err := db.QueryRow(`SELECT mission_id FROM assignments WHERE id = 'asg_mcm'`).Scan(&missionID); err != nil {
		t.Fatalf("read asg_mcm: %v", err)
	}
	if !missionID.Valid || missionID.String != "msn_mcm" {
		t.Errorf("asg_mcm mission_id = %v, want 'msn_mcm' — mission_comment_mentions named it and the "+
			"backfill did not recover it", missionID)
	}

	var untouched string
	if err := db.QueryRow(`SELECT mission_id FROM assignments WHERE id = 'asg_mcm_2'`).Scan(&untouched); err != nil {
		t.Fatalf("read asg_mcm_2: %v", err)
	}
	if untouched != "msn_mcm" {
		t.Errorf("asg_mcm_2 mission_id = %q, want unchanged 'msn_mcm'", untouched)
	}
}
