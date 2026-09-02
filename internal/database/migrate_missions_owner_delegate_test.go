package database

// missions.owner_user_id / missions.delegate_agent_id (PRD-ISSUES-AND-ROUTINES-2026
// work package A10; invariant I5; F62; rev-1 dev1 observation 11, §2.9).
//
// Three things are schema decisions rather than implementation details, and
// each is a production failure if it drifts:
//
//  1. both columns + their read indexes exist, and both are nullable;
//  2. deleting the referenced USER or AGENT sets the corresponding column to
//     NULL rather than deleting the mission or refusing the delete (F55);
//  3. the backfill migration recovers owner_user_id from rows where
//     assignee_type='user' and delegate_agent_id from rows where
//     assignee_type='agent', and never overwrites a row that already has one.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
)

// seedOwnerDelegateFixture creates the FK targets (workspace, crew, lead
// agent, a second "delegate" agent, and a user) needed to exercise
// owner_user_id/delegate_agent_id, plus one mission row with neither column
// set yet.
func seedOwnerDelegateFixture(t *testing.T, db *DB) {
	t.Helper()
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_od', 'WS', 'ws-od')`)
	execMigrationFixture(t, db, `INSERT INTO users (id, email, full_name) VALUES ('user_od_owner', 'owner@example.com', 'Owner')`)
	execMigrationFixture(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_od', 'ws_od', 'Crew', 'crew-od')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_od_lead', 'crew_od', 'ws_od', 'Lead', 'agent-od-lead', 'LEAD')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_od_delegate', 'crew_od', 'ws_od', 'Delegate', 'agent-od-delegate', 'WORKER')`)
	execMigrationFixture(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('msn_od', 'ws_od', 'crew_od', 'agent_od_lead', 'trace-od', 'owned issue', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`)
}

func TestMigrate_MissionsOwnerDelegate_ColumnsAndIndexesExist(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	for _, col := range []string{"owner_user_id", "delegate_agent_id"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('missions') WHERE name = ?`, col).Scan(&n); err != nil {
			t.Fatalf("pragma_table_info(%s): %v", col, err)
		}
		if n != 1 {
			t.Fatalf("missions.%s column does not exist", col)
		}
	}

	for _, idx := range []string{"idx_mission_owner_user", "idx_mission_delegate_agent"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx,
		).Scan(&n); err != nil {
			t.Fatalf("sqlite_master(%s): %v", idx, err)
		}
		if n != 1 {
			t.Fatalf("%s does not exist", idx)
		}
	}
}

// Deleting the USER named as owner must NOT be refused, and must NOT take
// the mission down with it (F55). PRAGMA foreign_keys is ON, so the default
// NO ACTION would make deleting a user who owns any issue fail outright.
func TestMigrate_MissionsOwnerDelegate_UserDeleteSetsOwnerNull(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedOwnerDelegateFixture(t, db)

	if _, err := db.Exec(`UPDATE missions SET owner_user_id = 'user_od_owner' WHERE id = 'msn_od'`); err != nil {
		t.Fatalf("set owner_user_id: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM users WHERE id = 'user_od_owner'`); err != nil {
		t.Fatalf("deleting the owner was refused: %v", err)
	}

	var ownerID sql.NullString
	if err := db.QueryRow(`SELECT owner_user_id FROM missions WHERE id = 'msn_od'`).Scan(&ownerID); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("the mission was deleted along with its owner; the issue must survive")
		}
		t.Fatalf("read back: %v", err)
	}
	if ownerID.Valid {
		t.Errorf("owner_user_id = %q, want NULL after the owner was deleted", ownerID.String)
	}
}

// Deleting the AGENT named as delegate must NOT be refused, and must NOT
// take the mission down with it (F55) — same guarantee as the owner case,
// mirrored for the other column.
func TestMigrate_MissionsOwnerDelegate_AgentDeleteSetsDelegateNull(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedOwnerDelegateFixture(t, db)

	if _, err := db.Exec(`UPDATE missions SET delegate_agent_id = 'agent_od_delegate' WHERE id = 'msn_od'`); err != nil {
		t.Fatalf("set delegate_agent_id: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM agents WHERE id = 'agent_od_delegate'`); err != nil {
		t.Fatalf("deleting the delegate was refused: %v", err)
	}

	var delegateID sql.NullString
	if err := db.QueryRow(`SELECT delegate_agent_id FROM missions WHERE id = 'msn_od'`).Scan(&delegateID); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("the mission was deleted along with its delegate; the issue must survive")
		}
		t.Fatalf("read back: %v", err)
	}
	if delegateID.Valid {
		t.Errorf("delegate_agent_id = %q, want NULL after the delegate was deleted", delegateID.String)
	}
}

// TestMigrateBackfillsMissionsOwnerDelegate drives the real backfill
// migration (20260902080722) against a populated DB carrying both assignee
// kinds — the case this migration exists for — the way
// TestMigrateBackfillsAssignmentsMissionID does: apply every migration once,
// seed rows under the legacy assignee_type/assignee_id pair, clear just the
// backfill migration's _migrations marker, and re-run Migrate.
func TestMigrateBackfillsMissionsOwnerDelegate(t *testing.T) {
	db := migrateChainSetup(t)
	seedOwnerDelegateFixture(t, db)

	// A user-assigned issue, predating the typed columns.
	execMigrationFixture(t, db, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type,
			assignee_type, assignee_id, created_at, updated_at)
		VALUES ('msn_od_user', 'ws_od', 'crew_od', 'agent_od_lead', 'trace-od-user', 'user-owned issue', 'BACKLOG', 'issue',
			'user', 'user_od_owner', datetime('now'), datetime('now'))`)

	// An agent-assigned (delegated) issue, predating the typed columns.
	execMigrationFixture(t, db, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type,
			assignee_type, assignee_id, created_at, updated_at)
		VALUES ('msn_od_agent', 'ws_od', 'crew_od', 'agent_od_lead', 'trace-od-agent', 'agent-delegated issue', 'BACKLOG', 'issue',
			'agent', 'agent_od_delegate', datetime('now'), datetime('now'))`)

	// A row that already carries an explicit owner_user_id from a write path
	// added by this same package — the backfill must not touch it (WHERE
	// owner_user_id IS NULL is the guard).
	execMigrationFixture(t, db, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type,
			assignee_type, assignee_id, owner_user_id, created_at, updated_at)
		VALUES ('msn_od_already', 'ws_od', 'crew_od', 'agent_od_lead', 'trace-od-already', 'already linked', 'BACKLOG', 'issue',
			'user', 'user_od_owner', 'user_od_owner', datetime('now'), datetime('now'))`)

	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260902080722`); err != nil {
		t.Fatalf("clear backfill marker: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (backfill): %v", err)
	}

	var ownerID sql.NullString
	if err := db.QueryRow(`SELECT owner_user_id FROM missions WHERE id = 'msn_od_user'`).Scan(&ownerID); err != nil {
		t.Fatalf("read msn_od_user: %v", err)
	}
	if !ownerID.Valid || ownerID.String != "user_od_owner" {
		t.Errorf("msn_od_user owner_user_id = %v, want 'user_od_owner' — assignee_type='user' and the backfill did not recover it", ownerID)
	}
	var delegateOfUserRow sql.NullString
	if err := db.QueryRow(`SELECT delegate_agent_id FROM missions WHERE id = 'msn_od_user'`).Scan(&delegateOfUserRow); err != nil {
		t.Fatalf("read msn_od_user delegate: %v", err)
	}
	if delegateOfUserRow.Valid {
		t.Errorf("msn_od_user delegate_agent_id = %v, want NULL — a user-assigned row must not gain a delegate", delegateOfUserRow)
	}

	var delegateID sql.NullString
	if err := db.QueryRow(`SELECT delegate_agent_id FROM missions WHERE id = 'msn_od_agent'`).Scan(&delegateID); err != nil {
		t.Fatalf("read msn_od_agent: %v", err)
	}
	if !delegateID.Valid || delegateID.String != "agent_od_delegate" {
		t.Errorf("msn_od_agent delegate_agent_id = %v, want 'agent_od_delegate' — assignee_type='agent' and the backfill did not recover it", delegateID)
	}
	var ownerOfAgentRow sql.NullString
	if err := db.QueryRow(`SELECT owner_user_id FROM missions WHERE id = 'msn_od_agent'`).Scan(&ownerOfAgentRow); err != nil {
		t.Fatalf("read msn_od_agent owner: %v", err)
	}
	if ownerOfAgentRow.Valid {
		t.Errorf("msn_od_agent owner_user_id = %v, want NULL — an agent-delegated row must not gain an owner", ownerOfAgentRow)
	}

	var untouched string
	if err := db.QueryRow(`SELECT owner_user_id FROM missions WHERE id = 'msn_od_already'`).Scan(&untouched); err != nil {
		t.Fatalf("read msn_od_already: %v", err)
	}
	if untouched != "user_od_owner" {
		t.Errorf("msn_od_already owner_user_id = %q, want unchanged 'user_od_owner'", untouched)
	}
}
