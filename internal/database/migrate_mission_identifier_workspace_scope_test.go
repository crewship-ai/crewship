package database

import (
	"strings"
	"testing"
)

// missions.identifier was UNIQUE across the whole instance (v38's
// idx_mission_identifier). Identifiers are generated per crew from the crew's
// prefix — "ENG-1" is what the first issue of any crew slugged eng- gets — so
// the FIRST workspace on an instance to create one consumed that name for
// everybody. The second workspace's create failed with
//
//	UNIQUE constraint failed: missions.identifier
//
// which is both a broken feature and a cross-tenant disclosure: the only thing
// that error can mean is "a row you are not allowed to see already owns this",
// and it is returned to a user who cannot list, read or reach that row.
//
// Uniqueness belongs where every other tenant-owned table puts it — on the
// workspace. crews is UNIQUE(workspace_id, slug); missions is now
// UNIQUE(workspace_id, identifier).

// seedIdentifierWorkspace builds the FK chain one mission needs — workspace,
// crew, lead agent — under ids derived from suffix, so two calls produce two
// independent tenants.
func seedIdentifierWorkspace(t *testing.T, db *DB, suffix string) (wsID, crewID, agentID string) {
	t.Helper()
	wsID = "ws_ident_" + suffix
	crewID = "crew_ident_" + suffix
	agentID = "agent_ident_" + suffix
	execMigrationFixture(t, db,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		wsID, "WS "+suffix, "ws-ident-"+suffix)
	// Deliberately the SAME crew slug in both workspaces: that is the shape the
	// bug was found in, and it is what makes the generated identifiers collide.
	execMigrationFixture(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Engineering', 'eng', 'ENG')`,
		crewID, wsID)
	execMigrationFixture(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		 VALUES (?, ?, ?, 'Lead', ?, 'LEAD')`,
		agentID, crewID, wsID, "agent-ident-"+suffix)
	return wsID, crewID, agentID
}

// insertIssue writes one mission carrying an identifier and returns the error
// rather than failing, because both outcomes are asserted below.
func insertIssue(t *testing.T, db *DB, id, wsID, crewID, agentID, identifier string) error {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		                      status, mission_type, identifier, number, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Ship it', 'BACKLOG', 'issue', ?, 1, datetime('now'), datetime('now'))`,
		id, wsID, crewID, agentID, "trace-"+id, identifier)
	return err
}

// TestMissionIdentifierIsWorkspaceScoped is the bug: two tenants, same
// identifier, and the second one must land.
func TestMissionIdentifierIsWorkspaceScoped(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	wsA, crewA, agentA := seedIdentifierWorkspace(t, db, "a")
	wsB, crewB, agentB := seedIdentifierWorkspace(t, db, "b")

	if err := insertIssue(t, db, "msn_ident_a", wsA, crewA, agentA, "ENG-1"); err != nil {
		t.Fatalf("first workspace could not create ENG-1: %v", err)
	}
	if err := insertIssue(t, db, "msn_ident_b", wsB, crewB, agentB, "ENG-1"); err != nil {
		t.Fatalf("second workspace could not create ENG-1: %v\n"+
			"identifiers are a per-workspace namespace; a global one lets the first tenant "+
			"consume ENG-1 for every other tenant, and the constraint error discloses that "+
			"an invisible row owns it", err)
	}
}

// TestMissionIdentifierStillUniqueWithinWorkspace is the other half: widening
// the index must not turn it off. Duplicate identifiers inside ONE workspace
// are what the constraint is for — the CLI and the UI both address an issue by
// identifier within the caller's workspace, so a duplicate there makes that
// lookup ambiguous.
func TestMissionIdentifierStillUniqueWithinWorkspace(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	ws, crew, agent := seedIdentifierWorkspace(t, db, "dup")

	if err := insertIssue(t, db, "msn_dup_1", ws, crew, agent, "ENG-1"); err != nil {
		t.Fatalf("first ENG-1: %v", err)
	}
	err := insertIssue(t, db, "msn_dup_2", ws, crew, agent, "ENG-1")
	if err == nil {
		t.Fatal("a second ENG-1 in the SAME workspace was accepted — identifier lookups " +
			"scoped to a workspace are now ambiguous")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("second ENG-1 failed with %v, want a UNIQUE constraint violation", err)
	}
}

// TestMissionIdentifierNullsAreUnconstrained pins the partial predicate.
// Orchestration missions carry no identifier; without `WHERE identifier IS NOT
// NULL` SQLite would still allow them (NULLs are distinct in a UNIQUE index),
// but the predicate is what keeps them out of the index entirely, and dropping
// it silently doubles the index's size on the majority row shape.
func TestMissionIdentifierNullsAreUnconstrained(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	ws, crew, agent := seedIdentifierWorkspace(t, db, "null")
	for _, id := range []string{"msn_null_1", "msn_null_2"} {
		_, err := db.Exec(`
			INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
			                      status, mission_type, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'No identifier', 'PLANNING', 'orchestration', datetime('now'), datetime('now'))`,
			id, ws, crew, agent, "trace-"+id)
		if err != nil {
			t.Fatalf("insert %s without an identifier: %v", id, err)
		}
	}
}

// TestMissionIdentifierIndexShape asserts the schema directly, because the
// behavioural tests above pass for the wrong reason if the unique index is
// simply gone: two workspaces can hold ENG-1 with no index at all.
func TestMissionIdentifierIndexShape(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var oldIdx int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_mission_identifier'`,
	).Scan(&oldIdx); err != nil {
		t.Fatalf("read old index: %v", err)
	}
	if oldIdx != 0 {
		t.Error("idx_mission_identifier still exists — the global namespace is still enforced")
	}

	var sqlText string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_mission_workspace_identifier'`,
	).Scan(&sqlText); err != nil {
		t.Fatalf("idx_mission_workspace_identifier is missing: %v", err)
	}
	norm := strings.Join(strings.Fields(sqlText), " ")
	if !strings.Contains(strings.ToUpper(norm), "UNIQUE") {
		t.Errorf("idx_mission_workspace_identifier is not UNIQUE: %s", norm)
	}
	if !strings.Contains(strings.ToUpper(norm), "WHERE IDENTIFIER IS NOT NULL") {
		t.Errorf("idx_mission_workspace_identifier lost its partial predicate: %s", norm)
	}

	// The key columns are read back from the index itself, not matched against
	// the CREATE text: the index NAME contains the substring "workspace_id", so
	// a `strings.Contains` over the SQL passes for an index keyed on identifier
	// alone. That mutation survived the first version of this assertion.
	var keys []string
	rows, err := db.Query(`PRAGMA index_info('idx_mission_workspace_identifier')`)
	if err != nil {
		t.Fatalf("index_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info: %v", err)
		}
		keys = append(keys, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index_info rows: %v", err)
	}
	want := []string{"workspace_id", "identifier"}
	if len(keys) != len(want) {
		t.Fatalf("index keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("index key %d = %q, want %q (keys: %v)", i, keys[i], want[i], keys)
		}
	}
}
