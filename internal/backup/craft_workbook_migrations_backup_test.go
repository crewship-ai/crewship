package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftWorkbookMigrationBackupRoundTrip(t *testing.T) {
	ctx := t.Context()
	source := openDurableArtifactBackupDB(t)
	ws := seedWorkspace(t, source)
	scope := mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: "migrated-sheet"}
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Legacy Sheet','u_admin','{}')`, scope.ArtifactID, ws, scope.ArtifactID); err != nil {
		t.Fatal(err)
	}
	principal := mooring.EditLeasePrincipal{ActorID: "u_admin", SessionID: "test-sheet-session"}
	store := func(db *sql.DB, workbook bool) (*mooring.NativeStore, *mooring.EditLeaseStore) {
		leases := mooring.NewEditLeaseStore(db, mooring.EditLeaseConfig{Enabled: true, Clock: recoveredOwnerClock{}, TTL: time.Minute, Authority: func(context.Context, *sql.Tx, mooring.EditLeaseAccess) error { return nil }})
		cfg := mooring.NativeStoreConfig{
			Enabled: true, Application: "gridcraft", EngineRevision: "e68d192282499bed81a9800d742cb9b239d367e5",
			EngineSHA256: strings.Repeat("a", 64), EditorBundle: strings.Repeat("b", 64), Leases: leases,
			Authorize: func(context.Context, *sql.Tx, mooring.EditLeaseScope, string) (mooring.EditLeasePrincipal, error) {
				return principal, nil
			},
			Validate: func(context.Context, []byte) error { return nil },
		}
		if workbook {
			cfg.Format = mooring.NativeFormatWorkbook
			cfg.EngineRevision, cfg.EngineSHA256, cfg.EditorBundle = strings.Repeat("c", 40), strings.Repeat("d", 64), strings.Repeat("e", 64)
			cfg.Validate = mooring.ValidateWorkbook
		}
		return mooring.NewNativeStore(db, cfg), leases
	}
	legacy, leases := store(source, false)
	original := []byte("{\n  \"sheets\": [{\"name\":\"Example\"}]\n}\n")
	lease, _, err := legacy.Initialize(ctx, scope, principal, original)
	if err != nil {
		t.Fatal(err)
	}
	if err := leases.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	before, err := backup.DumpWorkspace(ctx, source, ws)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store(source, true)
	input, err := current.LegacyWorkbook(ctx, scope, true)
	if err != nil {
		t.Fatal(err)
	}
	workbook := workbookBackupFixture(t, "before")
	if version, err := current.MigrateLegacyWorkbook(ctx, scope, input, workbook); err != nil || version != 2 {
		t.Fatalf("migrate: version=%d, %v", version, err)
	}
	migrated, err := backup.DumpWorkspace(ctx, source, ws)
	if err != nil {
		t.Fatal(err)
	}
	const pass = "test-workbook-backup-passphrase"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(ctx, source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}

	for _, asWorkspace := range []string{"", "workbook-fork"} {
		label := "same IDs"
		if asWorkspace != "" {
			label = asWorkspace
		}
		t.Run(label, func(t *testing.T) {
			target := openDurableArtifactBackupDB(t)
			opts := backup.RestoreOptions{Path: bundle.Path, Passphrase: pass, Actor: actor, AsWorkspace: asWorkspace}
			if _, err := backup.RestoreBackup(ctx, target, opts); err != nil {
				t.Fatal(err)
			}
			var restored mooring.EditLeaseScope
			if err := target.QueryRow(`SELECT workspace_id,page_id FROM craft_workbook_migrations`).Scan(&restored.WorkspaceID, &restored.ArtifactID); err != nil {
				t.Fatal(err)
			}
			if asWorkspace != "" && (restored.WorkspaceID == ws || restored.ArtifactID == scope.ArtifactID) {
				t.Fatalf("fork retained source anchors: %+v", restored)
			}
			restoredStore, _ := store(target, true)
			state, err := restoredStore.Read(ctx, restored)
			if err != nil || state.LatestVersion != 2 || !bytes.Equal(state.Manifest.Files["workbook.xlsx"].Content, workbook) {
				t.Fatalf("restored workbook: head=%d, %v", state.LatestVersion, err)
			}
			kept, err := restoredStore.LegacyWorkbook(ctx, restored, false)
			if err != nil || kept.Version != 1 || kept.WorkbookVersion != 2 || sha256.Sum256(kept.Bytes) != sha256.Sum256(original) {
				t.Fatalf("restored original: versions=%d/%d, SHA-256 %x, %v", kept.Version, kept.WorkbookVersion, sha256.Sum256(kept.Bytes), err)
			}
		})
	}

	// Restore to a target with no live custody evidence so the workbook
	// history guard is the one that must refuse the overlapping conversion.
	target := openDurableArtifactBackupDB(t)
	if err := backup.RestoreDump(ctx, target, migrated); err != nil {
		t.Fatal(err)
	}
	current, _ = store(target, true)
	newer := workbookBackupFixture(t, "after")
	history := artifacts.NewVersionStore(target, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	key := artifacts.VersionKey{WorkspaceID: ws, PageID: scope.ArtifactID}
	update := artifacts.VersionRequest{
		BaseVersion: 2, EntryPoint: "workbook.xlsx",
		Files: map[string]artifacts.VersionFile{"workbook.xlsx": {Content: newer, MediaType: mooring.WorkbookMediaTypeXLSX}},
	}
	author := func(context.Context, *sql.Tx) (string, error) { return principal.ActorID, nil }
	if version, err := history.Apply(ctx, key, update, author); err != nil || version.Version != 3 {
		t.Fatalf("newer edit: %+v %v", version, err)
	}
	if err := backup.RestoreDump(ctx, target, before); err != nil {
		t.Fatalf("merge from before conversion: %v", err)
	}
	if err := backup.RestoreDump(ctx, target, migrated); err == nil || !strings.Contains(err.Error(), "workbook history merge is not qualified") {
		t.Fatalf("restore should refuse a migration over the newer workbook: %v", err)
	}

	state, err := current.Read(ctx, scope)
	if err != nil || state.LatestVersion != 3 || !bytes.Equal(state.Manifest.Files["workbook.xlsx"].Content, newer) {
		t.Fatalf("restore changed newer workbook: head=%d, %v", state.LatestVersion, err)
	}
	kept, err := current.LegacyWorkbook(ctx, scope, false)
	if err != nil || kept.WorkbookVersion != 2 || sha256.Sum256(kept.Bytes) != sha256.Sum256(original) {
		t.Fatalf("restore changed migration receipt or source: version=%d, SHA-256 %x, %v", kept.WorkbookVersion, sha256.Sum256(kept.Bytes), err)
	}
}

func workbookBackupFixture(t *testing.T, marker string) []byte {
	t.Helper()
	var raw bytes.Buffer
	archive := zip.NewWriter(&raw)
	for path, text := range map[string]string{
		"[Content_Types].xml":      `<Types><Override ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
		"xl/workbook.xml":          `<workbook/>`,
		"xl/worksheets/sheet1.xml": "<worksheet>" + marker + "</worksheet>",
	} {
		file, err := archive.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}
