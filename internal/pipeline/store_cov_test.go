package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// store.go — Save input validation, gate edge cases, the slug-conflict
// mapping, the versioned upsert path (saveVersionTx), List branches,
// and the closed-DB error paths of the smaller methods.
// ---------------------------------------------------------------------------

func TestStore_Save_InputValidation(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	base := validSaveInput("p-args")

	in := base
	in.WorkspaceID = ""
	if _, err := store.Save(ctx, in); err == nil || !strings.Contains(err.Error(), "workspace_id required") {
		t.Errorf("missing workspace: %v", err)
	}

	in = base
	in.Slug = ""
	if _, err := store.Save(ctx, in); err == nil || !strings.Contains(err.Error(), "slug required") {
		t.Errorf("missing slug: %v", err)
	}

	in = base
	in.DefinitionJSON = ""
	if _, err := store.Save(ctx, in); err == nil || !strings.Contains(err.Error(), "definition_json required") {
		t.Errorf("missing definition: %v", err)
	}

	// Nil LastTestRunAt fails the gate even with passed=true.
	in = base
	in.LastTestRunAt = nil
	if _, err := store.Save(ctx, in); !errors.Is(err, ErrTestRunGateFailed) {
		t.Errorf("nil test-run timestamp: %v", err)
	}

	// Far-future timestamp (beyond 1min skew) fails the gate.
	in = base
	future := time.Now().Add(2 * time.Hour)
	in.LastTestRunAt = &future
	if _, err := store.Save(ctx, in); !errors.Is(err, ErrTestRunGateFailed) {
		t.Errorf("future test-run timestamp: %v", err)
	}
}

func TestStore_Save_DefaultsViaAndDSLVersion(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("p-defaults")
	in.Author.Via = ""
	in.DSLVersion = ""
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if p.AuthoredVia != AuthoredViaAgent {
		t.Errorf("authored_via default: %q", p.AuthoredVia)
	}
	if p.DSLVersion != "1.0" {
		t.Errorf("dsl_version default: %q", p.DSLVersion)
	}
}

// TestStore_Save_ResurrectsSoftDeletedRow pins the upsert-by-slug
// contract: saving a slug whose row was soft-deleted RESURRECTS that
// row (clears deleted_at, appends a version) instead of returning
// ErrSlugConflict. The (workspace_id, slug) UNIQUE index still counts
// the tombstone, so findIDBySlug locates it and Save routes down the
// UPDATE path. This is what makes `seed --nuke` re-seeds work: nuke
// soft-deletes every routine, and the next seed brings each one back
// under the same slug + row id (history preserved).
func TestStore_Save_ResurrectsSoftDeletedRow(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	p, err := store.Save(ctx, validSaveInput("p-resurrect"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.SoftDelete(ctx, p.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	// While tombstoned the slug is invisible to the active view.
	if _, err := store.GetBySlug(ctx, "ws_test", "p-resurrect"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on tombstoned slug, got %v", err)
	}
	// Re-saving the same slug resurrects the original row.
	p2, err := store.Save(ctx, validSaveInput("p-resurrect"))
	if err != nil {
		t.Fatalf("resurrect save should succeed, got %v", err)
	}
	if p2.ID != p.ID {
		t.Errorf("resurrect must reuse the row: got id %q, want %q", p2.ID, p.ID)
	}
	got, err := store.GetBySlug(ctx, "ws_test", "p-resurrect")
	if err != nil {
		t.Fatalf("slug should be active again after resurrect: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("active slug id mismatch after resurrect: %q vs %q", got.ID, p.ID)
	}
}

// TestStore_Save_VersionedUpsert drives the saveVersionTx path through
// Save with the v79 schema present: insert creates v1, an edit bumps
// to v2 with parent=1, and a byte-identical re-save dedupes (no v3).
func TestStore_Save_VersionedUpsert(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("p-versioned")
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}

	v1, err := store.GetVersion(ctx, p.ID, 1)
	if err != nil {
		t.Fatalf("v1 missing after insert: %v", err)
	}
	if v1.ParentVersion != nil {
		t.Errorf("v1 parent should be nil, got %v", *v1.ParentVersion)
	}
	if v1.AuthorType != "agent" || v1.AuthorID != "agent_lead" {
		t.Errorf("v1 author: %s/%s", v1.AuthorType, v1.AuthorID)
	}

	// Edit → update path + v2.
	in2 := validSaveInput("p-versioned")
	in2.DefinitionJSON = `{"name":"p-versioned","steps":[{"id":"s1"}]}`
	p2, err := store.Save(ctx, in2)
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if p2.ID != p.ID {
		t.Errorf("upsert minted a new id: %s vs %s", p2.ID, p.ID)
	}
	v2, err := store.GetVersion(ctx, p.ID, 2)
	if err != nil {
		t.Fatalf("v2 missing after update: %v", err)
	}
	if v2.ParentVersion == nil || *v2.ParentVersion != 1 {
		t.Errorf("v2 parent: %v", v2.ParentVersion)
	}

	// head_version bumped on the pipelines row.
	var head int
	if err := db.QueryRow(`SELECT head_version FROM pipelines WHERE id = ?`, p.ID).Scan(&head); err != nil {
		t.Fatalf("head: %v", err)
	}
	if head != 2 {
		t.Errorf("head_version = %d, want 2", head)
	}

	// Identical re-save: no v3 (hash dedup inside the tx).
	if _, err := store.Save(ctx, in2); err != nil {
		t.Fatalf("idempotent re-save: %v", err)
	}
	if _, err := store.GetVersion(ctx, p.ID, 3); !errors.Is(err, ErrNotFound) {
		t.Errorf("identical re-save must not create v3, got %v", err)
	}
}

// TestStore_Save_VersionAuthorAttribution pins the author_type mapping
// for user- and import-authored saves, plus the "unknown" fallback.
func TestStore_Save_VersionAuthorAttribution(t *testing.T) {
	db := openVersioningTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	// Via=user → author_type=user, author_id=UserID.
	in := validSaveInput("p-user-auth")
	in.Author = AuthorMeta{UserID: "user_test", Via: AuthoredViaUser}
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save user: %v", err)
	}
	v, err := store.GetVersion(ctx, p.ID, 1)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if v.AuthorType != "user" || v.AuthorID != "user_test" {
		t.Errorf("user attribution: %s/%s", v.AuthorType, v.AuthorID)
	}

	// Via=imported → author_type=imported, author_id=URL.
	in = validSaveInput("p-import-auth")
	in.Author = AuthorMeta{Via: AuthoredViaImported, ImportedURL: "https://example.com/r.json"}
	p, err = store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save imported: %v", err)
	}
	v, err = store.GetVersion(ctx, p.ID, 1)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if v.AuthorType != "imported" || v.AuthorID != "https://example.com/r.json" {
		t.Errorf("import attribution: %s/%s", v.AuthorType, v.AuthorID)
	}

	// Empty author id falls back to "unknown".
	in = validSaveInput("p-anon-auth")
	in.Author = AuthorMeta{Via: AuthoredViaAgent} // AgentID empty
	p, err = store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save anon: %v", err)
	}
	v, err = store.GetVersion(ctx, p.ID, 1)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if v.AuthorID != "unknown" {
		t.Errorf("anon attribution: %q", v.AuthorID)
	}
}

func TestStore_List_OrderAndFilterBranches(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	// Missing workspace.
	if _, err := store.List(ctx, ListFilters{}); err == nil || !strings.Contains(err.Error(), "workspace_id required") {
		t.Errorf("missing workspace: %v", err)
	}

	mk := func(slug, crew string) *Pipeline {
		in := validSaveInput(slug)
		in.Author.CrewID = crew
		p, err := store.Save(ctx, in)
		if err != nil {
			t.Fatalf("save %s: %v", slug, err)
		}
		return p
	}
	mk("alpha", "crew_a")
	mk("beta", "crew_b")

	// AuthorCrewID filter.
	got, err := store.List(ctx, ListFilters{WorkspaceID: "ws_test", AuthorCrewID: "crew_b"})
	if err != nil {
		t.Fatalf("crew filter: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "beta" {
		t.Errorf("crew filter returned %d rows", len(got))
	}

	// OrderByName.
	got, err = store.List(ctx, ListFilters{WorkspaceID: "ws_test", OrderBy: OrderByName})
	if err != nil {
		t.Fatalf("order by name: %v", err)
	}
	if len(got) != 2 || got[0].Slug != "alpha" || got[1].Slug != "beta" {
		t.Errorf("name order wrong: %v", []string{got[0].Slug, got[1].Slug})
	}

	// OrderByRecent: bump beta's last_invoked_at so it sorts first.
	if err := store.RecordInvocation(ctx, got[1].ID, "COMPLETED"); err != nil {
		t.Fatalf("record invocation: %v", err)
	}
	got, err = store.List(ctx, ListFilters{WorkspaceID: "ws_test", OrderBy: OrderByRecent})
	if err != nil {
		t.Fatalf("order by recent: %v", err)
	}
	if got[0].Slug != "beta" {
		t.Errorf("recent order: first = %q, want beta", got[0].Slug)
	}

	// IncludeEphemeral + IncludeHidden flip the WHERE conds off; with an
	// out-of-range limit the cap kicks in. Just assert both rows return.
	got, err = store.List(ctx, ListFilters{
		WorkspaceID: "ws_test", IncludeEphemeral: true, IncludeHidden: true, Limit: 9999,
	})
	if err != nil {
		t.Fatalf("include flags: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("include flags returned %d rows", len(got))
	}
}

// TestStore_ClosedDB_ErrorPaths drives the wrapped-error returns of
// the query helpers via a closed DB handle, asserting each method's
// distinguishing error context survives the wrap.
func TestStore_ClosedDB_ErrorPaths(t *testing.T) {
	db := openStoreTestDB(t)
	store := NewStore(db)
	ctx := context.Background()
	seed := validSaveInput("p-closed")
	if _, err := store.Save(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	if _, err := store.Save(ctx, validSaveInput("p-closed-2")); err == nil || !strings.Contains(err.Error(), "lookup existing slug") {
		t.Errorf("Save lookup error: %v", err)
	}
	if _, err := store.GetByID(ctx, "x"); err == nil || !strings.Contains(err.Error(), "pipeline: query") {
		t.Errorf("GetByID error: %v", err)
	}
	if _, err := store.List(ctx, ListFilters{WorkspaceID: "ws_test"}); err == nil || !strings.Contains(err.Error(), "pipeline: list") {
		t.Errorf("List error: %v", err)
	}
	if err := store.SoftDelete(ctx, "x"); err == nil || !strings.Contains(err.Error(), "soft delete") {
		t.Errorf("SoftDelete error: %v", err)
	}
	if err := store.RecordInvocation(ctx, "x", "COMPLETED"); err == nil || !strings.Contains(err.Error(), "record invocation") {
		t.Errorf("RecordInvocation error: %v", err)
	}
}

// TestScanPipeline_DeletedAtBranch decodes a soft-deleted row directly
// — every public lookup filters deleted rows, leaving the DeletedAt
// branch unreachable through the API.
func TestScanPipeline_DeletedAtBranch(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	p, err := store.Save(ctx, validSaveInput("p-scan-deleted"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.SoftDelete(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, err := db.Query(`SELECT `+pipelineColumns+` FROM pipelines WHERE id = ?`, p.ID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("row missing")
	}
	got, err := scanPipeline(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got.DeletedAt == nil {
		t.Error("deleted_at lost in scan")
	}
}

func TestParseTimeHelpers(t *testing.T) {
	t.Parallel()

	if parseTimePtr("") != nil {
		t.Error("empty string should give nil")
	}
	if parseTimePtr("garbage") != nil {
		t.Error("unparseable should give nil")
	}
	if got := parseTimePtr("2026-01-02T03:04:05Z"); got == nil || got.Hour() != 3 {
		t.Errorf("RFC3339 fallback: %v", got)
	}
	if got := parseTimeOrZero("2026-01-02T03:04:05Z"); got.IsZero() {
		t.Error("RFC3339 fallback in parseTimeOrZero failed")
	}
	if got := parseTimeOrZero("nope"); !got.IsZero() {
		t.Errorf("unparseable should give zero, got %v", got)
	}
}

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()
	if isUniqueViolation(nil) {
		t.Error("nil is not a violation")
	}
	if !isUniqueViolation(errors.New("UNIQUE constraint failed: pipelines.slug")) {
		t.Error("modernc wording must match")
	}
	if !isUniqueViolation(errors.New("constraint failed: UNIQUE something")) {
		t.Error("alternate wording must match")
	}
	if isUniqueViolation(errors.New("syntax error")) {
		t.Error("unrelated error must not match")
	}
}
