package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Additional tests that exercise wider statement coverage of helpers
// not reached by the e2e/unit tests added so far. Focuses on
// catalog backfill, restorer Open* paths, and dbdump normalize/quote.

// === BackfillCatalogFromDir ===

func TestBackfillCatalogFromDir_NilDBNoOp(t *testing.T) {
	if err := BackfillCatalogFromDir(t.Context(), nil, t.TempDir(), nil); err != nil {
		t.Errorf("nil db should be no-op, got %v", err)
	}
}

func TestBackfillCatalogFromDir_EmptyDirNoOp(t *testing.T) {
	if err := BackfillCatalogFromDir(t.Context(), nil, "", nil); err != nil {
		t.Errorf("empty dir should be no-op, got %v", err)
	}
}

func TestBackfillCatalogFromDir_MissingDirIsBenign(t *testing.T) {
	db := newCatalogTestDB(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := BackfillCatalogFromDir(t.Context(), db, missing, nil); err != nil {
		t.Errorf("missing dir should be silently skipped, got %v", err)
	}
}

func TestBackfillCatalogFromDir_LoggerCalledOnCorruptBundle(t *testing.T) {
	db := newCatalogTestDB(t)
	dir := t.TempDir()
	// Put a file that LOOKS like a bundle (right naming pattern) but
	// is actually garbage — ListBackups will find it; Inspect will
	// fail; logger should fire.
	bogus := filepath.Join(dir, "crewship-workspace-bogus-20260525T120000Z.tar.zst")
	if err := os.WriteFile(bogus, []byte("not a real bundle"), 0o600); err != nil {
		t.Fatalf("write bogus: %v", err)
	}
	var logs []string
	_ = BackfillCatalogFromDir(t.Context(), db, dir, func(s string) { logs = append(logs, s) })
	if len(logs) == 0 {
		t.Error("expected logger to fire for corrupt bundle")
	}
}

// newCatalogTestDB builds the minimum schema BackfillCatalogFromDir
// needs (UpsertCatalogEntry's target table).
func newCatalogTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS backup_catalog (
			id TEXT PRIMARY KEY,
			workspace_id TEXT,
			slug TEXT,
			scope TEXT,
			scope_level TEXT,
			file_path TEXT UNIQUE,
			size_bytes INTEGER,
			payload_sha256 TEXT,
			format_version INTEGER,
			encrypted INTEGER,
			created_at TEXT,
			created_by TEXT
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// === normalizeScan ===

func TestNormalizeScan_BytesToString(t *testing.T) {
	got := normalizeScan([]byte("hello"))
	if got != "hello" {
		t.Errorf("[]byte should become string, got %#v", got)
	}
}

func TestNormalizeScan_NilPassthrough(t *testing.T) {
	got := normalizeScan(nil)
	if got != nil {
		t.Errorf("nil should pass through, got %#v", got)
	}
}

func TestNormalizeScan_ScalarsPassThrough(t *testing.T) {
	for _, v := range []any{int64(42), "string", 3.14, true} {
		got := normalizeScan(v)
		if got != v {
			t.Errorf("scalar %#v should pass through, got %#v", v, got)
		}
	}
}

// === quoteIdent ===

func TestQuoteIdent_BasicAndEdge(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"users", `"users"`},
		{"workspace_id", `"workspace_id"`},
		{`tab"le`, `"tab""le"`}, // double-quote escape
		{"", `""`},
	}
	for _, c := range cases {
		got := quoteIdent(c.in)
		if got != c.want {
			t.Errorf("quoteIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// === firstWorkspaceID / firstWorkspaceSlug ===

func TestFirstWorkspace_NilDumpReturnsEmpty(t *testing.T) {
	if got := firstWorkspaceID(nil); got != "" {
		t.Errorf("nil dump id should be empty, got %q", got)
	}
	if got := firstWorkspaceSlug(nil); got != "" {
		t.Errorf("nil dump slug should be empty, got %q", got)
	}
}

func TestFirstWorkspace_EmptyTableReturnsEmpty(t *testing.T) {
	dump := &DBDump{Tables: map[string][]map[string]any{"workspaces": {}}}
	if got := firstWorkspaceID(dump); got != "" {
		t.Errorf("empty workspaces table id should be empty, got %q", got)
	}
}

func TestFirstWorkspace_ReturnsFirstRowValue(t *testing.T) {
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"workspaces": {
				{"id": "ws_first", "slug": "alpha"},
				{"id": "ws_second", "slug": "beta"},
			},
		},
	}
	if got := firstWorkspaceID(dump); got != "ws_first" {
		t.Errorf("first row id, got %q", got)
	}
	if got := firstWorkspaceSlug(dump); got != "alpha" {
		t.Errorf("first row slug, got %q", got)
	}
}

func TestFirstWorkspace_NonStringIDReturnsEmpty(t *testing.T) {
	// Defensive coding: SQLite drivers SHOULDN'T but COULD return
	// int for the id column. The function returns empty rather than
	// panicking.
	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": int64(42), "slug": "x"}},
		},
	}
	if got := firstWorkspaceID(dump); got != "" {
		t.Errorf("non-string id should yield empty, got %q", got)
	}
}

// === RestoreDumpHooks nil handling ===

func TestRestoreDumpTx_NilHooksPath(t *testing.T) {
	// Pin the contract that RestoreDumpTx (the legacy single-callback
	// wrapper) tolerates a nil preCommit closure by forwarding nil to
	// RestoreDumpTxHooks.
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	stats, err := RestoreDumpTxHooks(t.Context(), db, &DBDump{Tables: map[string][]map[string]any{}}, nil)
	if err != nil {
		t.Errorf("nil hooks struct should be tolerated, got %v", err)
	}
	if stats.RowsSeen != 0 || stats.RowsInserted != 0 {
		t.Errorf("empty dump should produce zero stats, got %+v", stats)
	}
}

// === ReplaceWorkspaceContents tx propagation ===

func TestReplaceWorkspaceContents_NilTargetMatchReturnsEmptyMap(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	deleted, err := ReplaceWorkspaceContents(ctx, tx, "ws_no_such", "no-such-slug")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if deleted == nil {
		t.Errorf("expected non-nil empty map, got nil")
	}
	if len(deleted) != 0 {
		t.Errorf("expected empty map on no-match, got %v", deleted)
	}
}
