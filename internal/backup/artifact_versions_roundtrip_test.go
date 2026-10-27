package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactVersionBackupPreservesHistoryPinAndBinaryAssets(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	page := "artifact-version-backup"
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, page, ws, page, "Artifact project", "u_admin"); err != nil {
		t.Fatal(err)
	}
	store := artifacts.NewVersionStore(source, artifacts.VersionOptions{Enabled: true})
	key := artifacts.VersionKey{WorkspaceID: ws, PageID: page}
	auth := func(context.Context, *sql.Tx) (string, error) { return "u_admin", nil }
	asset := []byte{0, 255, 128, 192, 0, 237, 160, 128, 17}
	publish := func(base int64, text string) {
		t.Helper()
		_, err := store.Apply(t.Context(), key, artifacts.VersionRequest{BaseVersion: base, EntryPoint: "index.html", Files: map[string]artifacts.VersionFile{"index.html": {Content: []byte(text), MediaType: "text/html"}, "asset.bin": {Content: asset, MediaType: "application/octet-stream"}}}, auth)
		if err != nil {
			t.Fatal(err)
		}
	}
	publish(0, "original")
	publish(1, "changed")
	if _, err := store.Restore(t.Context(), key, 2, 1, auth); err != nil {
		t.Fatal(err)
	}
	if err := store.Share(t.Context(), key, 3, 1, auth); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "artifact-version-roundtrip-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			opts := backup.RestoreOptions{Path: bundle.Path, Passphrase: "artifact-version-roundtrip-passphrase", Actor: actor}
			if fork {
				opts.AsWorkspace = "artifact-version-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
				t.Fatal(err)
			}
			var restored artifacts.VersionKey
			var family string
			if err := target.QueryRow(`SELECT workspace_id,page_id,content_type FROM artifact_version_heads`).Scan(&restored.WorkspaceID, &restored.PageID, &family); err != nil {
				t.Fatal(err)
			}
			if family != "artifact_project" {
				t.Fatalf("family changed: %q", family)
			}
			if fork && (restored.WorkspaceID == ws || restored.PageID == page) {
				t.Fatal("fork did not remap stable scope")
			}
			if !fork && (restored.WorkspaceID != ws || restored.PageID != page) {
				t.Fatal("ordinary restore changed scope")
			}
			recovered := artifacts.NewVersionStore(target, artifacts.VersionOptions{Enabled: true})
			state, err := recovered.Read(t.Context(), restored, auth)
			if err != nil || state.LatestVersion != 3 || state.SharedVersion != 1 || !bytes.Equal(state.Manifest.Files["asset.bin"].Content, asset) || string(state.Manifest.Files["index.html"].Content) != "original" {
				t.Fatalf("restored state %+v %v", state, err)
			}
			old, err := recovered.ReadVersion(t.Context(), restored, 2, auth)
			if err != nil || string(old.Files["index.html"].Content) != "changed" || !bytes.Equal(old.Files["asset.bin"].Content, asset) {
				t.Fatalf("old immutable version lost %+v %v", old, err)
			}
			versions, err := recovered.Versions(t.Context(), restored, auth)
			if err != nil || len(versions) != 3 {
				t.Fatalf("history lost %+v %v", versions, err)
			}
			var blobType string
			if err := target.QueryRow(`SELECT typeof(content) FROM artifact_version_blobs WHERE length(content)=?`, len(asset)).Scan(&blobType); err != nil || blobType != "blob" {
				t.Fatalf("binary storage changed %q %v", blobType, err)
			}
		})
	}
}
