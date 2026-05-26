package backup

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newDiscoveryTestDB builds a small but production-shaped schema:
// direct-scoped tables (chats: workspace_id), one-hop transitive
// (crews → workspaces), two-hop (agents → crews → workspaces),
// three-hop (agent_skills → agents → crews → workspaces), globally
// namespaced FK target (skills, no workspace_id), and tables that
// FK into "operational" anchors we don't want to traverse.
func newDiscoveryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT UNIQUE);
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id)
		);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			crew_id TEXT NOT NULL REFERENCES crews(id)
		);
		CREATE TABLE chats (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id),
			created_by TEXT REFERENCES users(id)
		);
		CREATE TABLE skills (id TEXT PRIMARY KEY, name TEXT UNIQUE);
		CREATE TABLE agent_skills (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL REFERENCES agents(id),
			skill_id TEXT NOT NULL REFERENCES skills(id)
		);
		-- Operational table: FKs into workspaces but should NOT be
		-- bundled (per-instance state).
		CREATE TABLE audit_logs (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id),
			action TEXT
		);
		-- FTS5 virtual + shadow tables should be filtered out of
		-- listAllTables.
		CREATE VIRTUAL TABLE chats_fts USING fts5(content);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestDiscoverScopedTables_FindsTransitiveScopes(t *testing.T) {
	db := newDiscoveryTestDB(t)
	got, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	found := map[string]ScopedTable{}
	for _, st := range got {
		found[st.Name] = st
	}
	for _, want := range []string{"crews", "agents", "chats", "agent_skills", "audit_logs"} {
		if _, ok := found[want]; !ok {
			t.Errorf("expected %q in discovered tables; got %v", want, names(got))
		}
	}
	// skills FKs INTO agent_skills, not FROM workspaces — globally
	// namespaced. Must NOT be discovered via direct walk.
	if _, ok := found["skills"]; ok {
		t.Error("skills is globally namespaced; should not be discovered as workspace-scoped")
	}
	// users is the same: workspace-relevant only via reverse-FK from
	// chats.created_by. Discovery walks forward from workspaces, so
	// users stays out (correctly — it's a globally namespaced table
	// with its own identity).
	if _, ok := found["users"]; ok {
		t.Error("users should not be discovered as workspace-scoped (global identity)")
	}
	// FTS shadow tables filtered.
	for _, st := range got {
		if strings.Contains(st.Name, "_fts") {
			t.Errorf("FTS table leaked into discovery: %s", st.Name)
		}
	}
}

func TestDiscoverScopedTables_JoinPathDepth(t *testing.T) {
	db := newDiscoveryTestDB(t)
	got, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	depths := map[string]int{}
	for _, st := range got {
		depths[st.Name] = len(st.JoinPath)
	}
	cases := map[string]int{
		"crews":        1, // crews.workspace_id → workspaces.id
		"chats":        1, // chats.workspace_id → workspaces.id
		"audit_logs":   1,
		"agents":       2, // agents.crew_id → crews.workspace_id → workspaces.id
		"agent_skills": 3, // agent_skills.agent_id → agents.crew_id → crews.workspace_id → workspaces.id
	}
	for table, want := range cases {
		if got := depths[table]; got != want {
			t.Errorf("%s: JoinPath depth got %d, want %d", table, got, want)
		}
	}
}

func TestScopedTable_WorkspaceScopeFilter_Direct(t *testing.T) {
	db := newDiscoveryTestDB(t)
	all, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var crews ScopedTable
	for _, st := range all {
		if st.Name == "crews" {
			crews = st
		}
	}
	expr, args := crews.WorkspaceScopeFilter("ws_1")
	if !strings.Contains(expr, `"workspace_id"`) {
		t.Errorf("direct-scoped filter missing workspace_id column: %s", expr)
	}
	if len(args) != 1 || args[0] != "ws_1" {
		t.Errorf("filter args got %v, want [ws_1]", args)
	}
	// Smoke: filter SQL is executable against the real table.
	query := `SELECT id FROM crews WHERE ` + expr
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("execute filter: %v\nquery: %s", err, query)
	}
	rows.Close()
}

func TestScopedTable_WorkspaceScopeFilter_Transitive(t *testing.T) {
	db := newDiscoveryTestDB(t)
	all, err := DiscoverScopedTables(context.Background(), db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var as ScopedTable
	for _, st := range all {
		if st.Name == "agent_skills" {
			as = st
		}
	}
	if as.Name == "" {
		t.Fatal("agent_skills missing from discovery")
	}

	if _, err := db.Exec(`
		INSERT INTO workspaces (id, slug) VALUES ('ws_a', 'a'), ('ws_b', 'b');
		INSERT INTO crews (id, workspace_id) VALUES ('c_a', 'ws_a'), ('c_b', 'ws_b');
		INSERT INTO agents (id, crew_id) VALUES ('ag_a', 'c_a'), ('ag_b', 'c_b');
		INSERT INTO skills (id, name) VALUES ('sk_x', 'x');
		INSERT INTO agent_skills (id, agent_id, skill_id) VALUES
			('as_a', 'ag_a', 'sk_x'),
			('as_b', 'ag_b', 'sk_x');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	expr, args := as.WorkspaceScopeFilter("ws_a")
	rows, err := db.Query(`SELECT id FROM agent_skills WHERE `+expr, args...)
	if err != nil {
		t.Fatalf("execute transitive filter: %v\nexpr: %s", err, expr)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != "as_a" {
		t.Errorf("transitive filter returned %v, want [as_a] only", ids)
	}
}

func TestCategoriseScopedTables_HappyPath(t *testing.T) {
	discovered := []ScopedTable{
		{Name: "crews"},
		{Name: "agents"},
		{Name: "audit_logs"},
		{Name: "agent_skills"},
	}
	intent := map[string]ScopedTableIntent{
		"crews":        IntentInclude,
		"agents":       IntentInclude,
		"audit_logs":   IntentExcludeOperational,
		"agent_skills": IntentInclude,
	}
	inc, exc, err := CategoriseScopedTables(discovered, intent)
	if err != nil {
		t.Fatalf("categorise: %v", err)
	}
	if got := names(inc); len(got) != 3 {
		t.Errorf("include: got %v, want 3 tables", got)
	}
	if got := names(exc); len(got) != 1 || got[0] != "audit_logs" {
		t.Errorf("exclude: got %v, want [audit_logs]", got)
	}
}

func TestCategoriseScopedTables_DriftFailsLoudly(t *testing.T) {
	discovered := []ScopedTable{
		{Name: "crews"},
		{Name: "new_table_from_migration_57"}, // not in intent
	}
	intent := map[string]ScopedTableIntent{
		"crews": IntentInclude,
	}
	_, _, err := CategoriseScopedTables(discovered, intent)
	if !errors.Is(err, ErrDiscoveryDrift) {
		t.Fatalf("expected ErrDiscoveryDrift, got %v", err)
	}
	if !strings.Contains(err.Error(), "new_table_from_migration_57") {
		t.Errorf("drift error should name the unknown table: %v", err)
	}
}

// names is a small helper for tests — returns the Name field of each
// ScopedTable in a slice (preserves order).
func names(sts []ScopedTable) []string {
	out := make([]string, 0, len(sts))
	for _, st := range sts {
		out = append(out, st.Name)
	}
	return out
}
