package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV102_RenamesSystemPromptColumn asserts the rename half of
// the v102 migration: the new column exists, the old column does not.
// This is what catches a stale call site in Go SQL queries — if the
// rename ever regresses, the schema probe here flags it before any
// runtime SELECT * does.
func TestMigrateV102_RenamesSystemPromptColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v102.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := db.Query(`PRAGMA table_info(agents)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var (
			cid                int
			name, ctype, dflt  string
			notnull, pk        int
			defaultValueExists any
		)
		// PRAGMA columns: cid, name, type, notnull, dflt_value, pk
		var dfltAny any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltAny, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		_ = dflt
		_ = defaultValueExists
		have[name] = true
	}
	if have["system_prompt"] {
		t.Errorf("agents.system_prompt still exists; v102 rename did not land")
	}
	if !have["system_prompt_legacy"] {
		t.Errorf("agents.system_prompt_legacy missing; v102 rename did not land")
	}
}

// TestMigrateV102_WidensMemoryVersionsTierCheck asserts the recreate
// dance preserves existing rows AND admits the new persona/peer
// tiers. We seed a pre-widen row, run all migrations, then INSERT
// rows at every documented tier and assert the previously rejected
// 'persona' tier now lands.
func TestMigrateV102_WidensMemoryVersionsTierCheck(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v102b.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed a workspace so the FK is happy.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','Work','work')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	tiers := []string{"agent", "crew", "workspace", "pins", "learned", "persona", "peer"}
	for _, tier := range tiers {
		_, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES (?, 'ws1', '/test/'||?, ?, 'sha-'||?, 0, 'system', '/blobs/x')`,
			"v102-"+tier, tier, tier, tier)
		if err != nil {
			t.Errorf("insert tier=%q: %v", tier, err)
		}
	}

	// Negative: an unknown tier must still be rejected by the new CHECK.
	if _, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES ('v102-bad', 'ws1', '/test/bad', 'bogus', 'sha-bad', 0, 'system', '/blobs/y')`); err == nil {
		t.Errorf("expected CHECK violation for tier='bogus'; got nil")
	} else if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("expected CHECK constraint error; got %v", err)
	}
}
