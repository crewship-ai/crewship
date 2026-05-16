package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV90_MemoryProposalsSchema asserts the v90 migration created
// the memory_proposals table with the expected columns + constraints, the
// inbox_items.kind CHECK now admits 'memory_consolidation', and
// workspaces gained the memory_config column. (Originally v89 on
// feat/memory-reliability-bundle; renumbered to v90 on rebase.)
func TestMigrateV90_MemoryProposalsSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v90.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// memory_proposals table exists with the strict columns we care about.
	wantCols := map[string]string{
		"id":                 "TEXT",
		"workspace_id":       "TEXT",
		"crew_id":            "TEXT",
		"proposal_path":      "TEXT",
		"status":             "TEXT",
		"inbox_item_id":      "TEXT",
		"evidence_json":      "TEXT",
		"rules_count":        "INTEGER",
		"entries_scanned":    "INTEGER",
		"created_at":         "TEXT",
		"decided_at":         "TEXT",
		"decided_by_user_id": "TEXT",
	}
	rows, err := db.Query(`PRAGMA table_info(memory_proposals)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		got[name] = strings.ToUpper(ctype)
	}
	for col, ctype := range wantCols {
		if got[col] != ctype {
			t.Errorf("memory_proposals.%s type = %q, want %q (full schema: %+v)", col, got[col], ctype, got)
		}
	}

	// inbox_items now admits memory_consolidation kind without violating the CHECK.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_mc_1', 'ws1', 'memory_consolidation', 'prop_1', 'Memory consolidation proposal')`); err != nil {
		t.Fatalf("insert memory_consolidation inbox item: %v", err)
	}

	// Pre-existing kinds still allowed.
	if _, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_wp_1', 'ws1', 'waitpoint', 'tok_1', 'Waitpoint')`); err != nil {
		t.Fatalf("insert waitpoint inbox item: %v", err)
	}

	// Unknown kind still rejected by the rebuilt CHECK.
	if _, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_bad_1', 'ws1', 'bogus_kind', 'x', 'x')`); err == nil {
		t.Fatalf("expected CHECK violation on unknown kind, got nil")
	}

	// workspaces.memory_config column exists and is nullable.
	var memCfg *string
	if err := db.QueryRow(`SELECT memory_config FROM workspaces WHERE id = 'ws1'`).Scan(&memCfg); err != nil {
		t.Fatalf("read workspaces.memory_config: %v", err)
	}
	if memCfg != nil {
		t.Errorf("expected memory_config NULL by default, got %q", *memCfg)
	}

	// memory_proposals status CHECK: pending must have decided_at NULL.
	if _, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status)
VALUES ('p1', 'ws1', 'crew1', '/tmp/p1.md', 'pending')`); err != nil {
		t.Fatalf("insert pending proposal: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status)
VALUES ('p2', 'ws1', 'crew1', '/tmp/p2.md', 'approved')`); err == nil {
		t.Fatalf("approved without decided_at must violate CHECK, got nil")
	}
	if _, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status, decided_at)
VALUES ('p3', 'ws1', 'crew1', '/tmp/p3.md', 'approved', datetime('now'))`); err != nil {
		t.Fatalf("approved with decided_at must succeed: %v", err)
	}
}
