package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/pagesearch"
)

// Rebuild only from the actual restored native canonical head. This proves the
// derived adapter/lifecycle, not viewer authentication or managed Docs decoding.
func seedCanonicalSearchCache(t *testing.T, db *sql.DB, workspace, page string) {
	t.Helper()
	source := pagesearch.SourceTag{WorkspaceID: workspace, PageID: page, Type: "slides"}
	var raw []byte
	if err := db.QueryRow(`SELECT schema_version,head_rev,content_hash,canonical_json FROM slides_heads WHERE workspace_id=? AND page_id=?`, workspace, page).Scan(&source.Schema, &source.Revision, &source.Hash, &raw); err != nil {
		t.Fatal(err)
	}
	projection, err := pagesearch.FromSlides(source, raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, hash, err := projection.Encode()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO page_search_projections(workspace_id,page_id,adapter_version,source_type,source_schema,source_rev,source_generation,source_hash,encoded,encoded_sha256) VALUES(?,?,1,'slides',?,?,0,?,?,?)`, workspace, page, source.Schema, source.Revision, source.Hash, encoded, hash)
	if err != nil {
		t.Fatal(err)
	}
}
func searchCacheCount(t *testing.T, db *sql.DB, workspace string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM page_search_projections WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != want {
		t.Fatalf("search caches %s=%d want%d: %v", workspace, count, want, err)
	}
}

func TestNativeSearchDerivedWorkspaceForkAndRollback(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	if _, err := seedBackupSlides(t, source, workspace, "search-source-page", "u_admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('search-foreign','Foreign','search-foreign')`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedBackupSlides(t, source, "search-foreign", "search-foreign-page", "u_admin"); err != nil {
		t.Fatal(err)
	}
	seedCanonicalSearchCache(t, source, workspace, "search-source-page")
	seedCanonicalSearchCache(t, source, "search-foreign", "search-foreign-page")
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["page_search_projections"]) != 0 || len(dump.Tables["page_search_writer_fence"]) != 0 {
		t.Fatal("derived rows exported as authority")
	}
	// Prove the scoped delete ran, then the whole restore transaction rolls back.
	reached := false
	_, err = backup.RestoreDumpTxHooks(t.Context(), source, dump, &backup.RestoreDumpHooks{PostInsert: func(ctx context.Context, tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM page_search_projections WHERE workspace_id=?`, workspace).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("search invalidation was not reached")
		}
		reached = true
		return errors.New("synthetic search restore rollback")
	}})
	if err == nil || !strings.Contains(err.Error(), "synthetic search restore rollback") || !reached {
		t.Fatalf("wrong rollback boundary: %v reached=%v", err, reached)
	}
	searchCacheCount(t, source, workspace, 1)
	searchCacheCount(t, source, "search-foreign", 1)
	if err = backup.RestoreDump(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	searchCacheCount(t, source, workspace, 0)
	searchCacheCount(t, source, "search-foreign", 1)
	seedCanonicalSearchCache(t, source, workspace, "search-source-page")
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: "synthetic-search-roundtrip-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Contents.TableRowCounts["page_search_projections"] != 0 {
		t.Fatal("encrypted workspace bundle includes derived text")
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "synthetic-search-roundtrip-passphrase", Actor: actor}
			if fork {
				options.AsWorkspace = "search-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var workspace, page string
			if err := target.QueryRow(`SELECT workspace_id,page_id FROM slides_heads`).Scan(&workspace, &page); err != nil {
				t.Fatal(err)
			}
			searchCacheCount(t, target, workspace, 0)
			seedCanonicalSearchCache(t, target, workspace, page)
			searchCacheCount(t, target, workspace, 1)
		})
	}
	// Exercise the actual Replace runner against existing target cache rows,
	// not only the generic dump transaction. Retain Page IDs and foreign state.
	var foreignHash string
	if err := source.QueryRow(`SELECT encoded_sha256 FROM page_search_projections WHERE workspace_id='search-foreign'`).Scan(&foreignHash); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.RestoreBackup(t.Context(), source, backup.RestoreOptions{Path: bundle.Path, Passphrase: "synthetic-search-roundtrip-passphrase", Actor: actor, Replace: true}); err != nil {
		t.Fatal(err)
	}
	searchCacheCount(t, source, workspace, 0)
	searchCacheCount(t, source, "search-foreign", 1)
	var pageID, currentForeignHash string
	if err := source.QueryRow(`SELECT page_id FROM slides_heads WHERE workspace_id=?`, workspace).Scan(&pageID); err != nil || pageID != "search-source-page" {
		t.Fatalf("Replace changed Page identity: %s %v", pageID, err)
	}
	if err := source.QueryRow(`SELECT encoded_sha256 FROM page_search_projections WHERE workspace_id='search-foreign'`).Scan(&currentForeignHash); err != nil || currentForeignHash != foreignHash {
		t.Fatal("Replace altered foreign cache")
	}
	seedCanonicalSearchCache(t, source, workspace, pageID)
	searchCacheCount(t, source, workspace, 1)

}

func TestNativeSearchPhysicalRecoveryClearsDerivedText(t *testing.T) {
	fixture := newInstanceFixture(t)
	if _, err := seedBackupSlides(t, fixture.db, "ws_e2e_1", "search-instance-page", "u_admin"); err != nil {
		t.Fatal(err)
	}
	seedCanonicalSearchCache(t, fixture.db, "ws_e2e_1", "search-instance-page")
	created := fixture.create(t, true)
	targetPath := filepath.Join(t.TempDir(), "recovered")
	report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{fixture.identity}, DataDir: targetPath, Actor: "u_admin"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, reset := range report.RuntimeReset {
		if strings.Contains(reset, "derived native content search projections (1)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("offline cache clear not reported: %+v", report.RuntimeReset)
	}
	target, err := database.Open("file:" + filepath.Join(targetPath, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	searchCacheCount(t, target.DB, "ws_e2e_1", 0)
	seedCanonicalSearchCache(t, target.DB, "ws_e2e_1", "search-instance-page")
	searchCacheCount(t, target.DB, "ws_e2e_1", 1)
	var singleton int
	if err = target.DB.QueryRow(`SELECT count(*) FROM page_search_writer_fence WHERE id=1`).Scan(&singleton); err != nil || singleton != 1 {
		t.Fatalf("cache fence unavailable after recovery: %d %v", singleton, err)
	}
}
