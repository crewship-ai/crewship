package backup

// Coverage tests for replace.go — ReplaceWorkspaceContents on a real
// migrated schema (id + slug matching, fresh-target no-op), the
// resolveTargetWorkspaceIDs dedupe, FK introspection guards, and the
// deletion-order cycle fallback.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func beginCovTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestReplaceWorkspaceContents_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("requires bundle workspace id", func(t *testing.T) {
		db := openMigratedDBCov(t)
		tx := beginCovTx(t, db)
		_, err := ReplaceWorkspaceContents(ctx, tx, "", "slug")
		if err == nil || !strings.Contains(err.Error(), "requires bundleWorkspaceID") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("fresh target is a no-op", func(t *testing.T) {
		db := openMigratedDBCov(t)
		tx := beginCovTx(t, db)
		deleted, err := ReplaceWorkspaceContents(ctx, tx, "ws_nowhere", "slug-nowhere")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(deleted) != 0 {
			t.Errorf("deleted = %v, want empty", deleted)
		}
	})

	t.Run("id match wipes the workspace subtree", func(t *testing.T) {
		db := openMigratedDBCov(t)
		wsID, _ := seedCovWorkspace(t, db, "replid")
		tx := beginCovTx(t, db)
		deleted, err := ReplaceWorkspaceContents(ctx, tx, wsID, "cov-replid")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if deleted["workspaces"] != 1 || deleted["crews"] != 1 || deleted["agents"] != 1 {
			t.Errorf("deleted = %v, want workspaces/crews/agents each 1", deleted)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("crews survived: %d", n)
		}
	})

	t.Run("slug match wipes a same-slug different-id workspace", func(t *testing.T) {
		// The post-nuke DR scenario: target has the SLUG under a fresh id.
		db := openMigratedDBCov(t)
		seedCovWorkspace(t, db, "replslug")
		tx := beginCovTx(t, db)
		// Bundle carries a DIFFERENT id but the same slug.
		deleted, err := ReplaceWorkspaceContents(ctx, tx, "ws_from_bundle", "cov-replslug")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if deleted["workspaces"] != 1 {
			t.Errorf("deleted = %v, want the slug-matched workspace wiped", deleted)
		}
	})
}

func TestResolveTargetWorkspaceIDs_Dedupe(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT);
		INSERT INTO workspaces VALUES ('ws1','shared-slug');
		INSERT INTO workspaces VALUES ('ws2','shared-slug');
	`)
	tx := beginCovTx(t, db)

	t.Run("id and slug both match same row → one entry", func(t *testing.T) {
		ids, err := resolveTargetWorkspaceIDs(ctx, tx, "ws1", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != "ws1" {
			t.Errorf("ids = %v", ids)
		}
	})
	t.Run("slug fans out to every same-slug workspace, deduped", func(t *testing.T) {
		ids, err := resolveTargetWorkspaceIDs(ctx, tx, "ws1", "shared-slug")
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 {
			t.Fatalf("ids = %v, want ws1+ws2", ids)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Errorf("duplicate id %s in %v", id, ids)
			}
			seen[id] = true
		}
	})
	t.Run("no match at all", func(t *testing.T) {
		ids, err := resolveTargetWorkspaceIDs(ctx, tx, "ws_void", "no-such-slug")
		if err != nil || len(ids) != 0 {
			t.Errorf("ids = %v, err = %v", ids, err)
		}
	})
}

func TestListAllTablesTx_FiltersInternals(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE notes (id TEXT PRIMARY KEY);
		CREATE TABLE notes_fts (id TEXT);
		CREATE TABLE notes_fts_idx (id TEXT);
	`)
	tx := beginCovTx(t, db)
	tables, err := listAllTablesTx(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(tables, ",")
	if got != "notes,workspaces" {
		t.Errorf("tables = %v, want FTS internals filtered", tables)
	}
}

func TestTableFKEdgesTx_Guards(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, `
		CREATE TABLE parents (id TEXT PRIMARY KEY);
		CREATE TABLE kids (id TEXT PRIMARY KEY, parent_id TEXT REFERENCES parents(id));
	`)
	tx := beginCovTx(t, db)

	t.Run("invalid identifier rejected", func(t *testing.T) {
		_, err := tableFKEdgesTx(ctx, tx, "kids; DROP TABLE kids")
		if err == nil || !strings.Contains(err.Error(), "invalid table identifier") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("edges resolved with default to-column", func(t *testing.T) {
		edges, err := tableFKEdgesTx(ctx, tx, "kids")
		if err != nil {
			t.Fatal(err)
		}
		if len(edges) != 1 {
			t.Fatalf("edges = %v", edges)
		}
		e := edges[0]
		if e.FromTable != "kids" || e.FromColumn != "parent_id" || e.ToTable != "parents" || e.ToColumn != "id" {
			t.Errorf("edge = %+v", e)
		}
	})
	t.Run("no edges on a leaf table", func(t *testing.T) {
		edges, err := tableFKEdgesTx(ctx, tx, "parents")
		if err != nil || len(edges) != 0 {
			t.Errorf("edges = %v, err = %v", edges, err)
		}
	})
}

func TestResolveDeletionOrder_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("empty input", func(t *testing.T) {
		db := newRunnerCovDB(t, `CREATE TABLE x (id TEXT PRIMARY KEY);`)
		tx := beginCovTx(t, db)
		out, err := resolveDeletionOrder(ctx, tx, nil)
		if err != nil || len(out) != 0 {
			t.Errorf("out = %v, err = %v", out, err)
		}
	})

	t.Run("children before parents", func(t *testing.T) {
		db := newRunnerCovDB(t, `
			CREATE TABLE grandparents (id TEXT PRIMARY KEY);
			CREATE TABLE parents (id TEXT PRIMARY KEY, gp_id TEXT REFERENCES grandparents(id));
			CREATE TABLE kids (id TEXT PRIMARY KEY, parent_id TEXT REFERENCES parents(id));
		`)
		tx := beginCovTx(t, db)
		in := []ScopedTable{{Name: "grandparents"}, {Name: "parents"}, {Name: "kids"}}
		out, err := resolveDeletionOrder(ctx, tx, in)
		if err != nil {
			t.Fatal(err)
		}
		pos := map[string]int{}
		for i, st := range out {
			pos[st.Name] = i
		}
		if !(pos["kids"] < pos["parents"] && pos["parents"] < pos["grandparents"]) {
			t.Errorf("order = %v, want kids < parents < grandparents", out)
		}
	})

	t.Run("FK cycle falls back to deterministic append", func(t *testing.T) {
		// SQLite tolerates forward/circular REFERENCES at DDL time.
		db := newRunnerCovDB(t, `
			CREATE TABLE chicken (id TEXT PRIMARY KEY, egg_id TEXT REFERENCES egg(id));
			CREATE TABLE egg (id TEXT PRIMARY KEY, chicken_id TEXT REFERENCES chicken(id));
		`)
		tx := beginCovTx(t, db)
		in := []ScopedTable{{Name: "chicken"}, {Name: "egg"}}
		out, err := resolveDeletionOrder(ctx, tx, in)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 {
			t.Fatalf("cycle members lost: %v", out)
		}
		// Deterministic: leftovers appended in name order.
		if out[0].Name != "chicken" || out[1].Name != "egg" {
			t.Errorf("order = %v, want name-sorted fallback", out)
		}
	})

	t.Run("self-FK ignored for ordering", func(t *testing.T) {
		db := newRunnerCovDB(t, `
			CREATE TABLE folders (id TEXT PRIMARY KEY, parent_id TEXT REFERENCES folders(id));
		`)
		tx := beginCovTx(t, db)
		out, err := resolveDeletionOrder(ctx, tx, []ScopedTable{{Name: "folders"}})
		if err != nil || len(out) != 1 {
			t.Errorf("out = %v, err = %v", out, err)
		}
	})
}
