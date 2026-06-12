package backup

// Coverage tests for catalog.go — BackfillCatalogFromDir (the startup
// reconciliation walk), the error paths of the CRUD helpers, and
// CatalogEntryFromResult's slug fallbacks.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackfillCatalogFromDir_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db and empty dir are no-ops", func(t *testing.T) {
		if err := BackfillCatalogFromDir(ctx, nil, "/anywhere", nil); err != nil {
			t.Fatalf("nil db: %v", err)
		}
		if err := BackfillCatalogFromDir(ctx, newCatalogDB(t), "", nil); err != nil {
			t.Fatalf("empty dir: %v", err)
		}
	})

	t.Run("missing dir is a no-op", func(t *testing.T) {
		db := newCatalogDB(t)
		if err := BackfillCatalogFromDir(ctx, db, filepath.Join(t.TempDir(), "ghost"), nil); err != nil {
			t.Fatalf("missing dir: %v", err)
		}
	})

	t.Run("valid bundles upserted, corrupt ones logged and skipped", func(t *testing.T) {
		db := newCatalogDB(t)
		dir := t.TempDir()
		created := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
		bundle := writeBundleFile(t, dir, covManifestForWorkspace("ws_bf", created), "payload")
		corrupt := filepath.Join(dir, "corrupt.tar.zst")
		if err := os.WriteFile(corrupt, []byte("not a bundle"), 0o600); err != nil {
			t.Fatal(err)
		}

		var logged []string
		if err := BackfillCatalogFromDir(ctx, db, dir, func(s string) { logged = append(logged, s) }); err != nil {
			t.Fatalf("BackfillCatalogFromDir: %v", err)
		}
		entries, err := ListCatalog(ctx, db, "")
		if err != nil {
			t.Fatalf("ListCatalog: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("catalog rows = %d (%+v), want 1", len(entries), entries)
		}
		e := entries[0]
		if e.FilePath != bundle || e.WorkspaceID != "ws_bf" || e.Slug != "covslug" {
			t.Errorf("entry = %+v", e)
		}
		if e.Scope != string(ScopeWorkspace) || e.ScopeLevel != string(DefaultScopeLevel) {
			t.Errorf("scope fields = %q/%q", e.Scope, e.ScopeLevel)
		}
		if !e.CreatedAt.Equal(created) {
			t.Errorf("created_at = %v, want %v", e.CreatedAt, created)
		}
		if e.SHA256 == "" || e.Size <= 0 {
			t.Errorf("size/sha not carried: %+v", e)
		}
		skipLogged := false
		for _, l := range logged {
			if strings.Contains(l, "skip") && strings.Contains(l, "corrupt.tar.zst") {
				skipLogged = true
			}
		}
		if !skipLogged {
			t.Errorf("corrupt bundle skip never logged: %v", logged)
		}

		// Idempotent: second walk leaves a single row.
		if err := BackfillCatalogFromDir(ctx, db, dir, nil); err != nil {
			t.Fatalf("second walk: %v", err)
		}
		entries, _ = ListCatalog(ctx, db, "")
		if len(entries) != 1 {
			t.Errorf("idempotence broken: %d rows", len(entries))
		}
	})

	t.Run("upsert failure logged but not fatal", func(t *testing.T) {
		// DB WITHOUT the backup_catalog table → every upsert fails.
		db := newRunnerCovDB(t, ``)
		dir := t.TempDir()
		writeBundleFile(t, dir, covManifestForWorkspace("ws_uf", time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)), "x")
		var logged []string
		if err := BackfillCatalogFromDir(ctx, db, dir, func(s string) { logged = append(logged, s) }); err != nil {
			t.Fatalf("must not abort on upsert failure: %v", err)
		}
		found := false
		for _, l := range logged {
			if strings.Contains(l, "upsert") {
				found = true
			}
		}
		if !found {
			t.Errorf("upsert failure never logged: %v", logged)
		}
	})

	t.Run("crew bundles record the crew slug", func(t *testing.T) {
		db := newCatalogDB(t)
		dir := t.TempDir()
		m := &Manifest{
			FormatVersion:     FormatVersion,
			Scope:             ScopeCrew,
			CompatibleTargets: []Target{TargetSameInstance},
			CreatedAt:         time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
			CreatedBy:         Actor{UserID: "u_cov", Email: "cov@x.test"},
			Contents:          Contents{Crews: []CrewSummary{{ID: "c1", Slug: "alpha-crew"}}},
		}
		writeBundleFile(t, dir, m, "x")
		if err := BackfillCatalogFromDir(ctx, db, dir, nil); err != nil {
			t.Fatal(err)
		}
		entries, _ := ListCatalog(ctx, db, "")
		if len(entries) != 1 || entries[0].Slug != "alpha-crew" || entries[0].CreatedBy != "cov@x.test" {
			t.Errorf("entries = %+v", entries)
		}
	})
}

func TestCatalogCRUD_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db short-circuits", func(t *testing.T) {
		if err := UpsertCatalogEntry(ctx, nil, CatalogEntry{}); err != nil {
			t.Errorf("upsert nil db: %v", err)
		}
		if err := DeleteCatalogEntry(ctx, nil, "p"); err != nil {
			t.Errorf("delete nil db: %v", err)
		}
		if got, err := ListCatalog(ctx, nil, ""); got != nil || err != nil {
			t.Errorf("list nil db = (%v, %v)", got, err)
		}
		if got, err := ReconcileCatalog(ctx, nil, ""); got != nil || err != nil {
			t.Errorf("reconcile nil db = (%v, %v)", got, err)
		}
	})

	t.Run("missing table errors are wrapped", func(t *testing.T) {
		db := newRunnerCovDB(t, ``)
		err := UpsertCatalogEntry(ctx, db, CatalogEntry{FilePath: "/p", Scope: "workspace", CreatedAt: time.Now()})
		if err == nil || !strings.Contains(err.Error(), "upsert catalog") {
			t.Errorf("upsert err = %v", err)
		}
		err = DeleteCatalogEntry(ctx, db, "/p")
		if err == nil || !strings.Contains(err.Error(), "delete catalog") {
			t.Errorf("delete err = %v", err)
		}
		_, err = ListCatalog(ctx, db, "")
		if err == nil || !strings.Contains(err.Error(), "list catalog") {
			t.Errorf("list err = %v", err)
		}
	})

	t.Run("unparseable created_at fails loudly", func(t *testing.T) {
		db := newCatalogDB(t)
		if _, err := db.Exec(`INSERT INTO backup_catalog
			(id, file_path, scope, created_at, size, sha256, encrypted, format_version)
			VALUES ('bk_x', '/p', 'workspace', 'NOT-A-TIMESTAMP', 1, 's', 0, 2)`); err != nil {
			t.Fatal(err)
		}
		_, err := ListCatalog(ctx, db, "")
		if err == nil || !strings.Contains(err.Error(), "parse created_at") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestReconcileCatalog_WorkspaceScopedPrune(t *testing.T) {
	ctx := context.Background()
	db := newCatalogDB(t)
	dir := t.TempDir()

	alive := filepath.Join(dir, "alive.tar.zst")
	if err := os.WriteFile(alive, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone.tar.zst")

	for i, p := range []string{alive, gone} {
		if err := UpsertCatalogEntry(ctx, db, CatalogEntry{
			FilePath: p, Scope: "workspace", WorkspaceID: "ws_rc",
			CreatedAt: time.Date(2026, 5, 1, 0, i, 0, 0, time.UTC),
			SHA256:    "s", Size: 1, FormatVersion: 2,
		}); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := ReconcileCatalog(ctx, db, "ws_rc")
	if err != nil {
		t.Fatalf("ReconcileCatalog: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != gone {
		t.Errorf("pruned = %v, want [%s]", pruned, gone)
	}
	rest, _ := ListCatalog(ctx, db, "ws_rc")
	if len(rest) != 1 || rest[0].FilePath != alive {
		t.Errorf("remaining = %+v", rest)
	}
	// Workspace filter: other tenants see nothing pruned.
	pruned2, err := ReconcileCatalog(ctx, db, "ws_other")
	if err != nil || len(pruned2) != 0 {
		t.Errorf("other tenant = (%v, %v)", pruned2, err)
	}
}

func TestReconcileCatalog_NonNotExistStatErrorIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	db := newCatalogDB(t)
	// NUL in the stored path makes Stat fail with ErrUnsafeBackupPath
	// (NOT os.ErrNotExist) — the row must survive the reconcile.
	if err := UpsertCatalogEntry(ctx, db, CatalogEntry{
		FilePath: "/weird\x00path.tar.zst", Scope: "workspace", WorkspaceID: "ws_st",
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		SHA256:    "s", Size: 1, FormatVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	pruned, err := ReconcileCatalog(ctx, db, "ws_st")
	if err != nil {
		t.Fatalf("ReconcileCatalog: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("pruned = %v, want untouched on non-NotExist stat error", pruned)
	}
	rest, _ := ListCatalog(ctx, db, "ws_st")
	if len(rest) != 1 {
		t.Errorf("row vacuumed despite transient stat error")
	}
}

func TestCatalogEntryFromResult_SlugFallbacks(t *testing.T) {
	res := &CreateResult{Path: "/backups/crewship-crew-x-20260101T000000Z.tar.zst", Size: 42, SHA256: "sha256:abc"}

	t.Run("workspace scope", func(t *testing.T) {
		m := covManifestForWorkspace("ws_ce", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
		e := CatalogEntryFromResult(res, m)
		if e.WorkspaceID != "ws_ce" || e.Slug != "covslug" || e.Size != 42 || e.SHA256 != "sha256:abc" {
			t.Errorf("entry = %+v", e)
		}
		if e.ScopeLevel != string(DefaultScopeLevel) {
			t.Errorf("scope level = %q", e.ScopeLevel)
		}
	})
	t.Run("crew scope uses first crew slug", func(t *testing.T) {
		m := &Manifest{Scope: ScopeCrew, ScopeLevel: ScopeLevelFull,
			Contents: Contents{Crews: []CrewSummary{{Slug: "the-crew"}}}}
		e := CatalogEntryFromResult(res, m)
		if e.Slug != "the-crew" || e.ScopeLevel != "full" {
			t.Errorf("entry = %+v", e)
		}
	})
	t.Run("empty slug falls back to filename", func(t *testing.T) {
		m := &Manifest{Scope: ScopeCrew}
		e := CatalogEntryFromResult(res, m)
		if e.Slug != "crewship-crew-x-20260101T000000Z" {
			t.Errorf("slug = %q", e.Slug)
		}
	})
}
