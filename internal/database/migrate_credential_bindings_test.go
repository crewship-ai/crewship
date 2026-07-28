package database

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// credential_bindings — the schema half of PRD-CREDENTIALS-V2 §2.5b.
//
// The migration exists because credentials.name was both the human identity of
// the credential and the env var the agent reads, under UNIQUE(workspace_id,
// name). A workspace could therefore hold exactly one GitHub account. The name
// keeps its UNIQUE; the env var becomes the SLOT of a binding.
//
// These tests pin the parts of the DDL whose failure is silent: a UNIQUE index
// that does not fire for the workspace scope, a CHECK that lets scope and owner
// disagree, and a cascade that does not follow a deleted crew. None of those
// surface as an error — they surface as a container holding the wrong identity.

// seedBindingFixture creates the FK targets a binding needs.
func seedBindingFixture(t *testing.T, db *DB) (wsID, crewID, agentID, credA, credB string) {
	t.Helper()
	wsID, crewID, agentID = "ws_cb", "crew_cb", "agent_cb"
	credA, credB = "cred_cb_a", "cred_cb_b"
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws-cb')`, wsID)
	execMigrationFixture(t, db, `INSERT INTO users (id, email, full_name) VALUES ('user_cb', 'cb@example.com', 'CB')`)
	execMigrationFixture(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', 'crew-cb')`, crewID, wsID)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'agent-cb')`,
		agentID, crewID, wsID)
	for _, c := range []struct{ id, name string }{{credA, "github-acme"}, {credB, "github-globex"}} {
		execMigrationFixture(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
			VALUES (?, ?, ?, 'enc', 'user_cb')`, c.id, wsID, c.name)
	}
	return
}

func execMigrationFixture(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
}

// TestMigrate_CredentialBindings_TableAndIndexExist is the smoke test: the
// table and the invariant's index landed, and the index is UNIQUE. A
// non-unique index would still make every query fast and every duplicate legal.
func TestMigrate_CredentialBindings_TableAndIndexExist(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='credential_bindings'`).Scan(&n); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if n != 1 {
		t.Fatal("credential_bindings table missing after migration")
	}

	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_credential_bindings_slot'`).Scan(&ddl); err != nil {
		t.Fatalf("query slot index: %v", err)
	}
	if !strings.Contains(ddl, "UNIQUE") {
		t.Fatalf("idx_credential_bindings_slot = %q, want a UNIQUE index — the invariant is the uniqueness, "+
			"not the lookup", ddl)
	}
}

// TestMigrate_CredentialBindings_UniquePerScopeIncludingWorkspace is the one
// the COALESCE in the index exists for. WORKSPACE scope has a NULL crew_id AND
// a NULL agent_id, and SQLite treats NULLs as distinct in a UNIQUE index — so a
// plain multi-column UNIQUE enforces the invariant for crews and agents and
// silently permits unlimited duplicates at the one scope that applies to every
// agent in the tenant.
func TestMigrate_CredentialBindings_UniquePerScopeIncludingWorkspace(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, crewID, agentID, credA, credB := seedBindingFixture(t, db)

	cases := []struct {
		name           string
		scope          string
		crew, agent    any
		firstID, dupID string
	}{
		{"workspace", "WORKSPACE", nil, nil, "b_ws_1", "b_ws_2"},
		{"crew", "CREW", crewID, nil, "b_crew_1", "b_crew_2"},
		{"agent", "AGENT", nil, agentID, "b_agent_1", "b_agent_2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`INSERT INTO credential_bindings
				(id, workspace_id, credential_id, scope, crew_id, agent_id, slot)
				VALUES (?, ?, ?, ?, ?, ?, 'GH_TOKEN')`,
				tc.firstID, wsID, credA, tc.scope, tc.crew, tc.agent); err != nil {
				t.Fatalf("first binding: %v", err)
			}
			_, err := db.Exec(`INSERT INTO credential_bindings
				(id, workspace_id, credential_id, scope, crew_id, agent_id, slot)
				VALUES (?, ?, ?, ?, ?, ?, 'GH_TOKEN')`,
				tc.dupID, wsID, credB, tc.scope, tc.crew, tc.agent)
			if err == nil {
				t.Fatalf("%s scope accepted a second binding for GH_TOKEN — the slot now has two answers", tc.name)
			}
		})
	}

	// Different slots in the same scope are NOT a conflict: §2.5b's honest
	// boundary is that a second account in one scope needs an explicit slot
	// (GH_TOKEN_READONLY), and the schema has to allow exactly that.
	if _, err := db.Exec(`INSERT INTO credential_bindings
		(id, workspace_id, credential_id, scope, crew_id, slot)
		VALUES ('b_ro', ?, ?, 'CREW', ?, 'GH_TOKEN_READONLY')`, wsID, credB, crewID); err != nil {
		t.Fatalf("second account under an explicit slot rejected: %v", err)
	}
}

// TestMigrate_CredentialBindings_ScopeAndOwnerMustAgree pins the CHECK. A row
// claiming scope='CREW' with a NULL crew_id matches no crew in the delivery
// join while every listing renders it as a live crew binding — a credential the
// UI says is assigned and the container never receives.
func TestMigrate_CredentialBindings_ScopeAndOwnerMustAgree(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, crewID, agentID, credA, _ := seedBindingFixture(t, db)

	bad := []struct {
		name        string
		scope       string
		crew, agent any
		slot        string
	}{
		{"crew scope with no crew", "CREW", nil, nil, "GH_TOKEN"},
		{"agent scope with no agent", "AGENT", nil, nil, "GH_TOKEN"},
		{"workspace scope with a crew", "WORKSPACE", crewID, nil, "GH_TOKEN"},
		{"crew scope with an agent too", "CREW", crewID, agentID, "GH_TOKEN"},
		{"unknown scope", "GALAXY", nil, nil, "GH_TOKEN"},
		{"blank slot", "WORKSPACE", nil, nil, "   "},
	}
	for i, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(`INSERT INTO credential_bindings
				(id, workspace_id, credential_id, scope, crew_id, agent_id, slot)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				"b_bad_"+string(rune('a'+i)), wsID, credA, tc.scope, tc.crew, tc.agent, tc.slot)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}

// TestMigrate_CredentialBindings_CascadeFollowsTheOwner is why crew_id/agent_id
// are typed FK columns instead of one polymorphic scope_id. A binding is a
// claim on a slot; if it outlived its crew it would keep claiming that slot
// (and, with an id reused, keep delivering) with nothing left to explain it.
func TestMigrate_CredentialBindings_CascadeFollowsTheOwner(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, crewID, agentID, credA, _ := seedBindingFixture(t, db)

	execMigrationFixture(t, db, `INSERT INTO credential_bindings
		(id, workspace_id, credential_id, scope, crew_id, slot)
		VALUES ('b_crew', ?, ?, 'CREW', ?, 'GH_TOKEN')`, wsID, credA, crewID)
	execMigrationFixture(t, db, `INSERT INTO credential_bindings
		(id, workspace_id, credential_id, scope, agent_id, slot)
		VALUES ('b_agent', ?, ?, 'AGENT', ?, 'NPM_TOKEN')`, wsID, credA, agentID)
	execMigrationFixture(t, db, `INSERT INTO credential_bindings
		(id, workspace_id, credential_id, scope, slot)
		VALUES ('b_ws', ?, ?, 'WORKSPACE', 'PYPI_TOKEN')`, wsID, credA)

	// Deleting the agent takes its binding; the crew's and the workspace's stay.
	execMigrationFixture(t, db, `DELETE FROM agents WHERE id = ?`, agentID)
	assertBindingCount(t, db, `SELECT COUNT(*) FROM credential_bindings WHERE id = 'b_agent'`, 0,
		"an AGENT binding outlived its agent")
	assertBindingCount(t, db, `SELECT COUNT(*) FROM credential_bindings WHERE id IN ('b_crew','b_ws')`, 2,
		"deleting an agent took bindings that were not its own")

	execMigrationFixture(t, db, `DELETE FROM crews WHERE id = ?`, crewID)
	assertBindingCount(t, db, `SELECT COUNT(*) FROM credential_bindings WHERE id = 'b_crew'`, 0,
		"a CREW binding outlived its crew")

	// Hard-deleting the credential removes every claim on it. (Soft delete is
	// the normal path and is handled by the delivery query's deleted_at
	// filter; this covers the row actually going away.)
	execMigrationFixture(t, db, `DELETE FROM credentials WHERE id = ?`, credA)
	assertBindingCount(t, db, `SELECT COUNT(*) FROM credential_bindings`, 0,
		"a binding outlived the credential it points at")
}

// TestMigrate_CredentialBindings_CrossWorkspaceRejected pins the trigger. A
// WORKSPACE-scope binding has no crew or agent to narrow it, so workspace_id is
// the only thing keeping one tenant's credential out of another tenant's
// containers — and unlike a crew link, nothing else in the row would look wrong.
func TestMigrate_CredentialBindings_CrossWorkspaceRejected(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, crewID, agentID, credA, _ := seedBindingFixture(t, db)

	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_cb_other', 'Other', 'other-cb')`)
	execMigrationFixture(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_cb_foreign', 'ws_cb_other', 'github-foreign', 'enc', 'user_cb')`)

	if _, err := db.Exec(`INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, slot)
		VALUES ('b_x1', ?, 'cred_cb_foreign', 'WORKSPACE', 'GH_TOKEN')`, wsID); err == nil {
		t.Error("a binding pointed at another tenant's credential was accepted")
	}
	if _, err := db.Exec(`INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, crew_id, slot)
		VALUES ('b_x2', 'ws_cb_other', ?, 'CREW', ?, 'GH_TOKEN')`, credA, crewID); err == nil {
		t.Error("a binding whose credential and crew live in different workspaces was accepted")
	}
	if _, err := db.Exec(`INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, agent_id, slot)
		VALUES ('b_x3', 'ws_cb_other', ?, 'AGENT', ?, 'GH_TOKEN')`, credA, agentID); err == nil {
		t.Error("a binding whose credential and agent live in different workspaces was accepted")
	}
}

// TestMigrate_CredentialBindings_NoBackfillAndNoRename is the compatibility
// guarantee stated as an assertion. The migration adds a table and nothing
// else: an existing crew link is NOT copied into a binding (a materialised copy
// would outlive the unlink and keep delivering), and no credential is renamed.
// Every existing workspace therefore upgrades to zero binding rows, which the
// delivery query reads as "unchanged".
func TestMigrate_CredentialBindings_NoBackfillAndNoRename(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	_, crewID, _, credA, _ := seedBindingFixture(t, db)
	execMigrationFixture(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, ?)`, credA, crewID)

	// Re-running the chain is the closest a test gets to "an existing database
	// upgrades": the table already exists and no data migration may fire.
	if err := Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("re-run migrations: %v", err)
	}

	assertBindingCount(t, db, `SELECT COUNT(*) FROM credential_bindings`, 0,
		"the migration invented bindings from existing crew links")
	var name string
	if err := db.QueryRow(`SELECT name FROM credentials WHERE id = ?`, credA).Scan(&name); err != nil {
		t.Fatalf("read credential name: %v", err)
	}
	if name != "github-acme" {
		t.Fatalf("credential name = %q, want it untouched — the migration must not rename rows", name)
	}
}

func assertBindingCount(t *testing.T, db *DB, query string, want int, why string) {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if n != want {
		t.Fatalf("%s → %d, want %d: %s", query, n, want, why)
	}
}
