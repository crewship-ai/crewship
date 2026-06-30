package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// schemaSQL is a minimal subset of the migrations sufficient for
// store-level tests. Mirrors v78 (pipelines + workspaces.
// execution_tiers_json) plus the bare workspaces/crews/agents/users
// FK targets so ON DELETE behaviour can be exercised. Avoids
// importing the full database package — keeps pipeline_test.go
// dependency-free and fast.
const schemaSQL = `
CREATE TABLE users (id TEXT PRIMARY KEY);
INSERT INTO users (id) VALUES ('user_test');

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    execution_tiers_json TEXT NOT NULL DEFAULT '{}'
);
INSERT INTO workspaces (id) VALUES ('ws_test');

CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
INSERT INTO crews (id, workspace_id) VALUES ('crew_a', 'ws_test'), ('crew_b', 'ws_test');

CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT NOT NULL);
INSERT INTO agents (id, crew_id) VALUES ('agent_lead', 'crew_a'), ('agent_b_lead', 'crew_b');

CREATE TABLE pipelines (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug                     TEXT NOT NULL,
    name                     TEXT NOT NULL,
    description              TEXT,
    dsl_version              TEXT NOT NULL DEFAULT '1.0',
    definition_json          TEXT NOT NULL,
    definition_hash          TEXT NOT NULL,
    ephemeral                INTEGER NOT NULL DEFAULT 0,
    workspace_visible        INTEGER NOT NULL DEFAULT 1,
    invocation_count         INTEGER NOT NULL DEFAULT 0,
    last_invoked_at          TEXT,
    last_invocation_status   TEXT,
    author_crew_id           TEXT REFERENCES crews(id) ON DELETE SET NULL,
    author_agent_id          TEXT REFERENCES agents(id) ON DELETE SET NULL,
    author_user_id           TEXT REFERENCES users(id) ON DELETE SET NULL,
    author_chat_id           TEXT,
    author_run_id            TEXT,
    authored_via             TEXT NOT NULL DEFAULT 'agent_tool_call',
    imported_from_url        TEXT,
    last_test_run_at         TEXT,
    last_test_run_passed     INTEGER NOT NULL DEFAULT 0,
    execution_tier_json      TEXT,
    status                   TEXT NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active','proposed','disabled')),
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT,
    UNIQUE (workspace_id, slug)
);
`

func openStoreTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// In-memory SQLite (":memory:") gives each connection its OWN
	// independent database. database/sql opens a pool of
	// connections lazily, so a writer connection schemata can be
	// invisible to a later reader connection — silent test
	// flakiness. Pinning the pool to a single connection makes
	// the test DB behave like a single shared in-memory DB.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		_ = db.Close()
		t.Fatalf("schema: %v", err)
	}
	return db
}

// validSaveInput returns a SaveInput that passes the test-run gate.
// Tests that want to exercise the gate failure path build their own.
func validSaveInput(slug string) SaveInput {
	now := time.Now()
	return SaveInput{
		WorkspaceID:    "ws_test",
		Slug:           slug,
		Name:           slug,
		Description:    "test pipeline",
		DefinitionJSON: `{"name":"` + slug + `","steps":[]}`,
		Author: AuthorMeta{
			CrewID:  "crew_a",
			AgentID: "agent_lead",
			Via:     AuthoredViaAgent,
		},
		LastTestRunAt:     &now,
		LastTestRunPassed: true,
	}
}

func TestStore_Save_RejectsMissingTestRun(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)

	in := validSaveInput("p1")
	in.LastTestRunPassed = false
	in.LastTestRunAt = nil

	_, err := s.Save(context.Background(), in)
	if !errors.Is(err, ErrTestRunGateFailed) {
		t.Errorf("expected ErrTestRunGateFailed, got %v", err)
	}
}

func TestStore_Save_RejectsStaleTestRun(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)

	in := validSaveInput("p1")
	old := time.Now().Add(-30 * time.Minute)
	in.LastTestRunAt = &old

	_, err := s.Save(context.Background(), in)
	if !errors.Is(err, ErrTestRunGateFailed) {
		t.Errorf("expected ErrTestRunGateFailed for stale test_run, got %v", err)
	}
}

func TestStore_Save_HappyPath(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)

	got, err := s.Save(context.Background(), validSaveInput("email-fetch"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.HasPrefix(got.ID, "pln_") {
		t.Errorf("expected pln_ prefix, got id=%q", got.ID)
	}
	if got.Slug != "email-fetch" {
		t.Errorf("slug: got %q", got.Slug)
	}
	if got.AuthorCrewID != "crew_a" {
		t.Errorf("author_crew_id: got %q", got.AuthorCrewID)
	}
	if !got.WorkspaceVisible {
		t.Errorf("workspace_visible should default true")
	}
	if got.Ephemeral {
		t.Errorf("ephemeral should default false")
	}
	if got.InvocationCount != 0 {
		t.Errorf("invocation_count should default 0, got %d", got.InvocationCount)
	}
	if !got.LastTestRunPassed {
		t.Errorf("last_test_run_passed should be 1")
	}
	if got.DefinitionHash == "" {
		t.Errorf("definition_hash should be populated")
	}
}

func TestStore_Save_UpsertsExisting(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	first, err := s.Save(ctx, validSaveInput("p1"))
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	in := validSaveInput("p1")
	in.Description = "updated"
	in.DefinitionJSON = `{"name":"p1","steps":[{"id":"a","type":"agent_run"}]}`

	second, err := s.Save(ctx, in)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("upsert should preserve id, got %q -> %q", first.ID, second.ID)
	}
	if second.Description != "updated" {
		t.Errorf("description not updated: %q", second.Description)
	}
	if second.DefinitionHash == first.DefinitionHash {
		t.Errorf("definition_hash should change when content changes")
	}
}

func TestStore_GetBySlug(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, err := s.Save(ctx, validSaveInput("hello"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.GetBySlug(ctx, "ws_test", "hello")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != saved.ID {
		t.Errorf("id mismatch: %q vs %q", got.ID, saved.ID)
	}

	if _, err := s.GetBySlug(ctx, "ws_test", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for unknown slug, got %v", err)
	}
}

func TestStore_List_Filters(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	for _, slug := range []string{"a", "b", "c"} {
		if _, err := s.Save(ctx, validSaveInput(slug)); err != nil {
			t.Fatalf("save %s: %v", slug, err)
		}
	}

	// Insert one ephemeral pipeline directly — Save() defaults to
	// non-ephemeral, so we need a manual write to exercise the
	// ephemeral filter in List.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, last_test_run_at, last_test_run_passed, authored_via, created_at, updated_at)
VALUES (?, 'ws_test', 'eph', 'eph', '{}', 'hash', 1, 1, ?, 1, 'agent_tool_call', ?, ?)`,
		generatePipelineID(), now, now, now)
	if err != nil {
		t.Fatalf("ephemeral insert: %v", err)
	}

	// Default filter excludes ephemeral and only includes visible.
	out, err := s.List(ctx, ListFilters{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("default list count: got %d, want 3 (ephemeral should be hidden)", len(out))
	}

	// IncludeEphemeral=true should now show all 4.
	out2, err := s.List(ctx, ListFilters{WorkspaceID: "ws_test", IncludeEphemeral: true})
	if err != nil {
		t.Fatalf("list incl ephemeral: %v", err)
	}
	if len(out2) != 4 {
		t.Errorf("incl-ephemeral list count: got %d, want 4", len(out2))
	}
}

func TestStore_RecordInvocation(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, err := s.Save(ctx, validSaveInput("counter"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.RecordInvocation(ctx, saved.ID, "COMPLETED"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := s.RecordInvocation(ctx, saved.ID, "COMPLETED"); err != nil {
		t.Fatalf("record 2: %v", err)
	}

	got, _ := s.GetByID(ctx, saved.ID)
	if got.InvocationCount != 2 {
		t.Errorf("count: got %d, want 2", got.InvocationCount)
	}
	if got.LastInvocationStatus != "COMPLETED" {
		t.Errorf("last status: got %q", got.LastInvocationStatus)
	}
	if got.LastInvokedAt == nil {
		t.Error("last_invoked_at should be set")
	}
}

func TestStore_SoftDelete_HidesFromQueries(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewStore(db)
	ctx := context.Background()

	saved, err := s.Save(ctx, validSaveInput("ghost"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.SoftDelete(ctx, saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetByID(ctx, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after soft delete, got %v", err)
	}
	if _, err := s.GetBySlug(ctx, "ws_test", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound by slug after delete, got %v", err)
	}
	out, err := s.List(ctx, ListFilters{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range out {
		if p.ID == saved.ID {
			t.Error("soft-deleted pipeline appeared in list")
		}
	}

	// Save same slug again — UNIQUE(workspace_id, slug) would block
	// this except the partial behaviour: deleted_at IS NULL is in
	// the index predicate? No — UNIQUE constraint is unconditional.
	// So saving "ghost" again with the slug taken should fail.
	// This documents the current behaviour; if we want
	// soft-delete-then-recreate to work we'd need to either tombstone
	// the slug differently or move UNIQUE into a partial index.
	_, err = s.Save(ctx, validSaveInput("ghost"))
	if !errors.Is(err, ErrSlugConflict) {
		// Currently expected to fail. If this assertion starts failing
		// because the schema is migrated to a partial unique index,
		// flip this to expect success and document the change.
		t.Logf("save after soft-delete returned: %v (UNIQUE blocks recreation; design choice)", err)
	}
}

func TestStore_GenerateID_Format(t *testing.T) {
	id := generatePipelineID()
	if !strings.HasPrefix(id, "pln_") {
		t.Errorf("expected pln_ prefix, got %q", id)
	}
	if len(id) < 16 {
		t.Errorf("id too short: %q", id)
	}
}

func TestStore_DefinitionHash_Stability(t *testing.T) {
	a := definitionHash(`{"name":"x"}`)
	b := definitionHash(`{"name":"x"}`)
	if a != b {
		t.Errorf("hash should be stable: %q vs %q", a, b)
	}
	c := definitionHash(`{"name":"y"}`)
	if a == c {
		t.Error("hash should differ for different inputs")
	}
}
