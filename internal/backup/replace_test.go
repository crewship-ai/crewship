package backup

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newReplaceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT UNIQUE);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE
		);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE
		);
		CREATE TABLE chats (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestReplaceWorkspaceContents_RequiresBundleID(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	_, err = ReplaceWorkspaceContents(ctx, tx, "", "any-slug")
	if err == nil || !strings.Contains(err.Error(), "bundleWorkspaceID") {
		t.Errorf("expected bundleWorkspaceID required error, got %v", err)
	}
}

func TestReplaceWorkspaceContents_NoMatchingTargetIsNoOp(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// Empty target — no workspace under that id OR slug.
	deleted, err := ReplaceWorkspaceContents(ctx, tx, "ws_bundle_nowhere", "nowhere-slug")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected empty deleted map on no-match target, got %v", deleted)
	}
}

func TestReplaceWorkspaceContents_WipesByIDMatch(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, slug) VALUES ('ws_keep', 'keep'), ('ws_wipe', 'wipe');
		INSERT INTO crews (id, workspace_id) VALUES ('c_keep', 'ws_keep'), ('c_wipe', 'ws_wipe');
		INSERT INTO chats (id, workspace_id) VALUES ('ch_keep', 'ws_keep'), ('ch_wipe', 'ws_wipe');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		t.Fatalf("defer fk: %v", err)
	}

	_, err = ReplaceWorkspaceContents(ctx, tx, "ws_wipe", "wipe")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Target workspace + children must be gone.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id = 'ws_wipe'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ws_wipe should be deleted, %d remain", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = 'ws_wipe'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ws_wipe crews should be deleted, %d remain", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM chats WHERE workspace_id = 'ws_wipe'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ws_wipe chats should be deleted, %d remain", n)
	}

	// Keep workspace + children must survive.
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id = 'ws_keep'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("ws_keep should survive, got %d", n)
	}
}

func TestReplaceWorkspaceContents_WipesBySlugMatch(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	// Bundle's id is "ws_bundle_old"; target has different id but same slug.
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, slug) VALUES ('ws_target_new', 'shared-slug');
		INSERT INTO crews (id, workspace_id) VALUES ('c_t', 'ws_target_new');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		t.Fatalf("defer fk: %v", err)
	}

	_, err = ReplaceWorkspaceContents(ctx, tx, "ws_bundle_old", "shared-slug")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE slug = 'shared-slug'`).Scan(&n); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if n != 0 {
		t.Errorf("slug-matched workspace should be deleted, %d remain", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = 'ws_target_new'`).Scan(&n); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if n != 0 {
		t.Errorf("dependent crews should cascade, %d remain", n)
	}
}

func TestResolveTargetWorkspaceIDs_DedupBetweenIDAndSlug(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	// Same row matches BOTH id and slug. Should appear once.
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug) VALUES ('ws_dual', 'dual-slug')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	ids, err := resolveTargetWorkspaceIDs(ctx, tx, "ws_dual", "dual-slug")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ids) != 1 || ids[0] != "ws_dual" {
		t.Errorf("expected single ws_dual entry, got %v", ids)
	}
}

func TestResolveTargetWorkspaceIDs_EmptySlugSkipsSlugBranch(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug) VALUES ('ws_x', 'slug-x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	// Empty slug — only id branch runs.
	ids, err := resolveTargetWorkspaceIDs(ctx, tx, "ws_x", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(ids) != 1 || ids[0] != "ws_x" {
		t.Errorf("expected ws_x via id branch, got %v", ids)
	}
}

func TestResolveDeletionOrder_TopologicalOnFKEdges(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	in := []ScopedTable{
		{Name: "agents"},
		{Name: "crews"},
		{Name: "chats"},
	}
	out, err := resolveDeletionOrder(ctx, tx, in)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	pos := map[string]int{}
	for i, st := range out {
		pos[st.Name] = i
	}
	agentsPos, okAgents := pos["agents"]
	crewsPos, okCrews := pos["crews"]
	if !okAgents || !okCrews {
		t.Fatalf("expected both agents and crews in deletion order, got %v", names(out))
	}
	// agents FKs to crews → agents must come before crews.
	if agentsPos > crewsPos {
		t.Errorf("agents should be deleted before crews; got order %v", names(out))
	}
}

func TestResolveDeletionOrder_HandlesEmptyInput(t *testing.T) {
	ctx := context.Background()
	db := newReplaceTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	out, err := resolveDeletionOrder(ctx, tx, nil)
	if err != nil {
		t.Errorf("nil input should be no-op, got %v", err)
	}
	if out != nil && len(out) != 0 {
		t.Errorf("expected empty output, got %v", out)
	}
}
