package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var catalogMemCounter atomic.Uint64

// newCatalogDB returns an in-memory SQLite DB with just the backup_catalog
// schema. Unique mode=memory name per helper call so different tests don't
// alias; shared cache so the driver's connection pool sees one DB.
func newCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("crewship-catalog-test-%d", catalogMemCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
CREATE TABLE backup_catalog (
    id TEXT PRIMARY KEY,
    file_path TEXT NOT NULL UNIQUE,
    scope TEXT NOT NULL,
    slug TEXT,
    workspace_id TEXT,
    created_at TEXT NOT NULL,
    created_by TEXT,
    size INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    encrypted INTEGER NOT NULL,
    format_version INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestBoolToInt(t *testing.T) {
	t.Parallel()

	if got := boolToInt(true); got != 1 {
		t.Errorf("boolToInt(true): got %d want 1", got)
	}
	if got := boolToInt(false); got != 0 {
		t.Errorf("boolToInt(false): got %d want 0", got)
	}
}

func TestNewCatalogID(t *testing.T) {
	t.Parallel()

	id, err := newCatalogID()
	if err != nil {
		t.Fatalf("newCatalogID: %v", err)
	}
	if !strings.HasPrefix(id, "bk_") {
		t.Errorf("id should start with bk_; got %q", id)
	}
	// "bk_" (3) + 16 random bytes as hex (32) = 35 chars total.
	if len(id) != 35 {
		t.Errorf("id length: got %d want 35", len(id))
	}

	// Two consecutive calls must produce different ids — catastrophic dup
	// would surface as a UNIQUE PRIMARY KEY constraint violation later.
	other, err := newCatalogID()
	if err != nil {
		t.Fatalf("newCatalogID #2: %v", err)
	}
	if other == id {
		t.Errorf("collision: both calls returned %q", id)
	}
}

func TestCatalogEntryFromResult_WorkspaceScope(t *testing.T) {
	t.Parallel()

	res := &CreateResult{
		Path:   "/b/workspace-eng.tar.zst",
		Size:   4096,
		SHA256: "sha256:aaa",
	}
	m := &Manifest{
		Scope:         ScopeWorkspace,
		FormatVersion: 2,
		CreatedAt:     time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC),
		CreatedBy:     Actor{Email: "admin@example"},
		Encryption:    Encryption{Enabled: true},
		Contents: Contents{
			Workspace: &WorkspaceSummary{ID: "ws-1", Slug: "main", Name: "Main"},
		},
	}

	entry := CatalogEntryFromResult(res, m)
	if entry.FilePath != res.Path || entry.Size != res.Size || entry.SHA256 != res.SHA256 {
		t.Errorf("res fields not copied: %+v", entry)
	}
	if entry.Scope != "workspace" {
		t.Errorf("scope: got %q", entry.Scope)
	}
	if entry.WorkspaceID != "ws-1" || entry.Slug != "main" {
		t.Errorf("workspace fields: got WorkspaceID=%q Slug=%q", entry.WorkspaceID, entry.Slug)
	}
	if entry.Encrypted != true {
		t.Errorf("Encrypted mapping failed")
	}
	if entry.CreatedBy != "admin@example" {
		t.Errorf("CreatedBy: got %q", entry.CreatedBy)
	}
}

func TestCatalogEntryFromResult_CrewScopeUsesCrewSlug(t *testing.T) {
	t.Parallel()

	res := &CreateResult{Path: "/b/crew-eng.tar.zst", Size: 10, SHA256: "s"}
	m := &Manifest{
		Scope: ScopeCrew,
		Contents: Contents{
			Crews: []CrewSummary{
				{ID: "crew-eng", Slug: "engineering", Name: "Engineering"},
				// Second crew should be ignored — the schema only has one slug column.
				{ID: "crew-qa", Slug: "quality", Name: "Quality"},
			},
		},
	}
	entry := CatalogEntryFromResult(res, m)
	if entry.Slug != "engineering" {
		t.Errorf("crew-scope slug: got %q want engineering", entry.Slug)
	}
}

func TestCatalogEntryFromResult_SlugFallbackToFilename(t *testing.T) {
	t.Parallel()

	res := &CreateResult{Path: "/b/instance-ci.tar.zst", Size: 1, SHA256: "s"}
	// Instance scope, no workspace or crews in contents, so slug starts empty.
	m := &Manifest{Scope: ScopeInstance, Contents: Contents{}}

	entry := CatalogEntryFromResult(res, m)
	if entry.Slug != "instance-ci" {
		t.Errorf("slug fallback: got %q want %q", entry.Slug, "instance-ci")
	}
}

func TestUpsertCatalogEntry_NilDBIsNoOp(t *testing.T) {
	t.Parallel()
	if err := UpsertCatalogEntry(context.Background(), nil, CatalogEntry{}); err != nil {
		t.Errorf("nil db should be a no-op; got %v", err)
	}
}

func TestUpsertCatalogEntry_InsertsWithGeneratedID(t *testing.T) {
	db := newCatalogDB(t)

	entry := CatalogEntry{
		FilePath: "/b/a.tar.zst", Scope: "workspace", Slug: "main",
		WorkspaceID: "ws-1", CreatedAt: time.Now().UTC().Truncate(time.Second),
		CreatedBy: "a@b", Size: 10, SHA256: "sha256:a",
		Encrypted: true, FormatVersion: 2,
	}
	if err := UpsertCatalogEntry(context.Background(), db, entry); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var id string
	if err := db.QueryRow(`SELECT id FROM backup_catalog WHERE file_path = ?`, entry.FilePath).Scan(&id); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !strings.HasPrefix(id, "bk_") {
		t.Errorf("id auto-generated with bk_ prefix; got %q", id)
	}
}

// UpsertCatalogEntry must tolerate duplicate (file_path) via ON CONFLICT and
// overwrite the mutable fields — size/checksum/encrypted/format_version
// drift between the first and second write must end up persisted.
func TestUpsertCatalogEntry_UpsertOverwritesMutableFields(t *testing.T) {
	db := newCatalogDB(t)
	ctx := context.Background()

	first := CatalogEntry{
		ID: "bk_first", FilePath: "/b/a.tar.zst", Scope: "workspace",
		WorkspaceID: "ws-1", CreatedAt: time.Now().UTC().Truncate(time.Second),
		CreatedBy: "a@b", Size: 10, SHA256: "sha256:first",
		Encrypted: false, FormatVersion: 1,
	}
	if err := UpsertCatalogEntry(ctx, db, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	second := first
	second.ID = "bk_second-id" // different id, same path — should not create a new row
	second.Size = 999
	second.SHA256 = "sha256:second"
	second.Encrypted = true
	second.FormatVersion = 2
	if err := UpsertCatalogEntry(ctx, db, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var size int64
	var sha string
	var enc int
	var fv int
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_catalog`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count after upsert: got %d want 1", count)
	}
	if err := db.QueryRow(`
SELECT size, sha256, encrypted, format_version FROM backup_catalog WHERE file_path = ?`,
		first.FilePath).Scan(&size, &sha, &enc, &fv); err != nil {
		t.Fatalf("select: %v", err)
	}
	if size != 999 || sha != "sha256:second" || enc != 1 || fv != 2 {
		t.Errorf("mutable fields not overwritten: size=%d sha=%q enc=%d fv=%d", size, sha, enc, fv)
	}
}

func TestDeleteCatalogEntry(t *testing.T) {
	db := newCatalogDB(t)
	ctx := context.Background()

	// Seed one row via upsert.
	if err := UpsertCatalogEntry(ctx, db, CatalogEntry{
		FilePath: "/b/a.tar.zst", Scope: "workspace", Slug: "main",
		WorkspaceID: "ws-1", CreatedAt: time.Now().UTC().Truncate(time.Second),
		CreatedBy: "a@b", Size: 10, SHA256: "s", FormatVersion: 2,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := DeleteCatalogEntry(ctx, db, "/b/a.tar.zst"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_catalog`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("row still present after delete; count=%d", count)
	}

	// Deleting an already-absent path must not error (legacy bundles).
	if err := DeleteCatalogEntry(ctx, db, "/b/nonexistent.tar.zst"); err != nil {
		t.Errorf("delete of missing path should be a no-op; got %v", err)
	}
	// Nil DB is tolerated too.
	if err := DeleteCatalogEntry(ctx, nil, "/b/x"); err != nil {
		t.Errorf("nil db delete: got %v", err)
	}
}

func TestListCatalog_SortedByCreatedAtDesc(t *testing.T) {
	db := newCatalogDB(t)
	ctx := context.Background()

	base := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	// Insert out of order to force the ORDER BY to do real work.
	entries := []CatalogEntry{
		{FilePath: "/b/old.tar.zst", Scope: "workspace", Size: 1, SHA256: "s", CreatedAt: base, WorkspaceID: "ws-1"},
		{FilePath: "/b/new.tar.zst", Scope: "workspace", Size: 1, SHA256: "s", CreatedAt: base.Add(2 * time.Hour), WorkspaceID: "ws-1"},
		{FilePath: "/b/mid.tar.zst", Scope: "workspace", Size: 1, SHA256: "s", CreatedAt: base.Add(1 * time.Hour), WorkspaceID: "ws-1"},
	}
	for _, e := range entries {
		if err := UpsertCatalogEntry(ctx, db, e); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	got, err := ListCatalog(ctx, db, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("row count: got %d want 3", len(got))
	}
	want := []string{"/b/new.tar.zst", "/b/mid.tar.zst", "/b/old.tar.zst"}
	for i, w := range want {
		if got[i].FilePath != w {
			t.Errorf("row %d: got %q want %q", i, got[i].FilePath, w)
		}
	}
}

func TestListCatalog_WorkspaceFilter(t *testing.T) {
	db := newCatalogDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	for _, e := range []CatalogEntry{
		{FilePath: "/b/ws1-a.tar.zst", Scope: "workspace", WorkspaceID: "ws-1", CreatedAt: now, Size: 1, SHA256: "s"},
		{FilePath: "/b/ws2-a.tar.zst", Scope: "workspace", WorkspaceID: "ws-2", CreatedAt: now, Size: 1, SHA256: "s"},
	} {
		if err := UpsertCatalogEntry(ctx, db, e); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	scoped, err := ListCatalog(ctx, db, "ws-1")
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(scoped) != 1 || scoped[0].FilePath != "/b/ws1-a.tar.zst" {
		t.Errorf("workspace filter failed: %+v", scoped)
	}

	all, err := ListCatalog(ctx, db, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("empty workspace filter should return all; got %d", len(all))
	}
}

func TestListCatalog_NilDBReturnsNil(t *testing.T) {
	t.Parallel()
	got, err := ListCatalog(context.Background(), nil, "")
	if err != nil || got != nil {
		t.Errorf("nil db: got=%v err=%v", got, err)
	}
}

func TestListCatalog_DecodesEncryptedIntToBool(t *testing.T) {
	db := newCatalogDB(t)
	ctx := context.Background()
	for _, enc := range []bool{true, false} {
		path := fmt.Sprintf("/b/enc-%v.tar.zst", enc)
		if err := UpsertCatalogEntry(ctx, db, CatalogEntry{
			FilePath: path, Scope: "workspace", WorkspaceID: "ws-1",
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			Size:      1, SHA256: "s", Encrypted: enc,
		}); err != nil {
			t.Fatalf("seed %v: %v", enc, err)
		}
	}
	rows, err := ListCatalog(ctx, db, "ws-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("row count: %d", len(rows))
	}
	// Each row's Encrypted must match its source bool.
	for _, r := range rows {
		want := strings.Contains(r.FilePath, "enc-true")
		if r.Encrypted != want {
			t.Errorf("row %q: got Encrypted=%v want %v", r.FilePath, r.Encrypted, want)
		}
	}
}
