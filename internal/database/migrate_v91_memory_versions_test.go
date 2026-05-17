package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV90_MemoryVersionsSchema asserts the v90 schema lands:
// the table exists with the expected columns, the tier CHECK admits
// every documented value, an unknown tier is rejected, and the
// (workspace_id, path, written_at DESC) index covers the log query.
func TestMigrateV90_MemoryVersionsSchema(t *testing.T) {
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

	wantCols := map[string]string{
		"id":           "TEXT",
		"workspace_id": "TEXT",
		"path":         "TEXT",
		"tier":         "TEXT",
		"sha256":       "TEXT",
		"bytes":        "INTEGER",
		"written_at":   "TEXT",
		"written_by":   "TEXT",
		"parent_sha":   "TEXT",
		"payload_ref":  "TEXT",
	}
	rows, err := db.Query(`PRAGMA table_info(memory_versions)`)
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
			t.Fatalf("scan: %v", err)
		}
		got[name] = strings.ToUpper(ctype)
	}
	// rows.Err() surfaces iterator-level errors that rows.Next() can
	// silently mask (DB closed mid-iteration, malformed PRAGMA output,
	// etc.). Without this check a schema-assertion FAIL could be
	// reported for "wrong reason" — the actual fail being the query
	// dying halfway through.
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	for col, ctype := range wantCols {
		if got[col] != ctype {
			t.Errorf("memory_versions.%s = %q, want %q", col, got[col], ctype)
		}
	}

	// Seed workspace + insert one row per documented tier.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	tiers := []string{"agent", "crew", "workspace", "pins", "learned"}
	for i, tier := range tiers {
		if _, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES (?, 'ws1', ?, ?, ?, ?, 'a1', ?)`,
			"mv_"+tier, "AGENT.md", tier, "sha"+tier, 100+i, "/blobs/"+tier); err != nil {
			t.Errorf("insert %s tier: %v", tier, err)
		}
	}

	// Unknown tier rejected.
	if _, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES ('mv_bad', 'ws1', 'AGENT.md', 'bogus', 'shaX', 100, 'a1', '/blobs/x')`); err == nil {
		t.Errorf("unknown tier should violate CHECK")
	}

	// payload_ref NOT NULL — INSERT without it must fail.
	if _, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by)
VALUES ('mv_no_ref', 'ws1', 'AGENT.md', 'agent', 'shaY', 100, 'a1')`); err == nil {
		t.Errorf("missing payload_ref should fail NOT NULL")
	}

	// Hot-path index query plans through the composite index.
	var plan string
	row := db.QueryRow(`
EXPLAIN QUERY PLAN
SELECT id, sha256, written_at FROM memory_versions
WHERE workspace_id = ? AND path = ?
ORDER BY written_at DESC LIMIT 10`, "ws1", "AGENT.md")
	var sid, parent, notUsed int
	if err := row.Scan(&sid, &parent, &notUsed, &plan); err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(plan, "idx_memory_versions_ws_path_ts") {
		t.Errorf("log query did not use the composite index: %q", plan)
	}
}
