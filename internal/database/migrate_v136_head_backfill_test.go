package database

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateV136_HeadVersionBackfill asserts the reconcile statement's
// semantics: a pipeline whose head_version drifted off the row matching
// its live definition_hash (the pre-#996 A→B→A dedup bug) is repointed;
// consistent rows and rows whose live hash has no version row are left
// alone. The statement is idempotent, so re-executing it against a
// fully-migrated DB exercises exactly what v136 ran at upgrade time.
func TestMigrateV136_HeadVersionBackfill(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v136.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, migLogger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)

	seedPipeline := func(id, liveHash string, head int) {
		mustExec(t, db.DB, `
			INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, dsl_version, head_version, created_at, updated_at)
			VALUES (?, 'ws1', ?, ?, '{"steps":[]}', ?, '1.0', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, id, id, liveHash, head)
	}
	seedVersion := func(pipelineID string, version int, hash string) {
		mustExec(t, db.DB, `
			INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id, created_at)
			VALUES (?, ?, ?, '{"steps":[]}', ?, 'user', 'u1', '2026-01-01T00:00:00Z')`,
			pipelineID+"-v"+string(rune('0'+version)), pipelineID, version, hash)
	}

	// Drifted: live content is v1's (hash-a) but head points at v2 —
	// the exact state the pre-#996 dedup bug left behind.
	seedPipeline("pln-drift", "hash-a", 2)
	seedVersion("pln-drift", 1, "hash-a")
	seedVersion("pln-drift", 2, "hash-b")

	// Consistent: head already on the matching row.
	seedPipeline("pln-ok", "hash-c", 1)
	seedVersion("pln-ok", 1, "hash-c")

	// No matching version row (pre-v79 pipeline never re-saved): must
	// keep whatever head it had.
	seedPipeline("pln-orphan", "hash-x", 3)

	if _, err := db.DB.Exec(migrationHeadVersionBackfill); err != nil {
		t.Fatalf("backfill exec: %v", err)
	}

	head := func(id string) int {
		var h int
		if err := db.DB.QueryRow(`SELECT head_version FROM pipelines WHERE id = ?`, id).Scan(&h); err != nil {
			t.Fatalf("read head %s: %v", id, err)
		}
		return h
	}
	if got := head("pln-drift"); got != 1 {
		t.Errorf("drifted pipeline: head = %d, want 1 (repointed at the live row)", got)
	}
	if got := head("pln-ok"); got != 1 {
		t.Errorf("consistent pipeline: head = %d, want 1 (untouched)", got)
	}
	if got := head("pln-orphan"); got != 3 {
		t.Errorf("orphan-hash pipeline: head = %d, want 3 (untouched)", got)
	}
}
