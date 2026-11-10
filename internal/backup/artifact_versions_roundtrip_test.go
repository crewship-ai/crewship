package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

func TestArtifactVersionBackupPreservesHistoryPinAndBinaryAssets(t *testing.T) {
	source := openDurableArtifactBackupDB(t)
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
	// Round-trip a real publication and pin with this existing encrypted
	// fixture, including a source Page ID that must change in a fork.
	libraryPage := "artifact-library-backup"
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, libraryPage, ws, libraryPage, "Brand library", "u_admin"); err != nil {
		t.Fatal(err)
	}
	library := artifacts.VersionKey{WorkspaceID: ws, PageID: libraryPage}
	tokens := []byte(`{"colors":{"brand":"#123456"}}` + "\n")
	// A small synthetic WOFF2 header exercises exact byte retention, not browser decoding.
	font := []byte{'w', 'O', 'F', '2', 0, 255, 128, 0, 42, 27, 199}
	license := []byte("SIL OPEN FONT LICENSE Version 1.1\n")

	if _, err := store.Apply(t.Context(), library, artifacts.VersionRequest{EntryPoint: "index.html", Files: map[string]artifacts.VersionFile{
		"index.html":               {Content: []byte("Brand library"), MediaType: "text/html"},
		artifacts.LibraryFile:      {Content: tokens, MediaType: "application/json"},
		"fonts/Brand.woff2":        {Content: []byte("data:font/woff2;base64," + base64.StdEncoding.EncodeToString(font)), MediaType: "font/woff2"},
		"fonts/OFL.txt":            {Content: license, MediaType: "text/plain"},
		artifacts.LibraryFontsFile: {Content: []byte(`{"schema_version":1,"fonts":[{"file":"fonts/Brand.woff2","family":"Brand"}]}`), MediaType: "application/json"},
	}}, auth); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishLibrary(t.Context(), library, 1, auth); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := store.PinLibrary(t.Context(), key, libraryPage, artifacts.LibraryPinExpect{Version: 1}, auth,
		func(ctx context.Context, tx *sql.Tx, _ int64) (string, error) { return auth(ctx, tx) }); err != nil || !changed {
		t.Fatalf("pin setup changed=%v: %v", changed, err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "artifact-version-roundtrip-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openDurableArtifactBackupDB(t)
			opts := backup.RestoreOptions{Path: bundle.Path, Passphrase: "artifact-version-roundtrip-passphrase", Actor: actor}
			if fork {
				opts.AsWorkspace = "artifact-version-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
				t.Fatal(err)
			}
			var restored artifacts.VersionKey
			var family string
			if err := target.QueryRow(`SELECT h.workspace_id,h.page_id,h.content_type FROM artifact_version_heads h JOIN pages p ON p.id=h.page_id WHERE p.slug=?`, page).Scan(&restored.WorkspaceID, &restored.PageID, &family); err != nil {
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
			var restoredLibrary string
			if err := target.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug=?`, restored.WorkspaceID, libraryPage).Scan(&restoredLibrary); err != nil {
				t.Fatal(err)
			}
			if (restoredLibrary != libraryPage) != fork {
				t.Fatalf("unexpected library remap: fork=%v id=%q", fork, restoredLibrary)
			}
			pub, err := recovered.ReadLibraryPublication(t.Context(), artifacts.VersionKey{WorkspaceID: restored.WorkspaceID, PageID: restoredLibrary}, auth)
			if err != nil || pub.Version != 1 {
				t.Fatalf("publication lost: %+v %v", pub, err)
			}
			pin, files, err := recovered.ReadLibraryPinSnapshot(t.Context(), restored, auth)
			if err != nil || pin.LibraryPageID != restoredLibrary || pin.LibraryVersion != 1 || !bytes.Equal(pin.Content, tokens) {
				t.Fatalf("pin lost its source or exact bytes: %+v %v", pin, err)
			}
			byPath := map[string][]byte{}
			for _, f := range files {
				byPath[f.Path] = f.Content
			}
			if len(files) != 2 || !bytes.Equal(byPath["fonts/Brand.woff2"], font) || !bytes.Equal(byPath["fonts/OFL.txt"], license) {
				t.Fatalf("pinned dependency bytes lost during restore: %+v", files)
			}
			var pinType string
			if err := target.QueryRow(`SELECT typeof(content) FROM artifact_library_pins WHERE workspace_id=? AND page_id=?`, restored.WorkspaceID, restored.PageID).Scan(&pinType); err != nil || pinType != "blob" {
				t.Fatalf("pin content changed storage type: %q %v", pinType, err)
			}
			var blobType string
			if err := target.QueryRow(`SELECT typeof(content) FROM artifact_version_blobs WHERE length(content)=?`, len(asset)).Scan(&blobType); err != nil || blobType != "blob" {
				t.Fatalf("binary storage changed %q %v", blobType, err)
			}
		})
	}
}

// The template uses NORMAL for fast setup. Reopen through the production
// constructor so every pooled connection is FULL; collectors need nested reads.
func openDurableArtifactBackupDB(t *testing.T) *sql.DB {
	t.Helper()
	fixture := openMigratedDB(t)
	var seq int
	var name, filename string
	if err := fixture.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &filename); err != nil {
		t.Fatal(err)
	}
	if filename == "" {
		t.Fatal("durable fixture requires an on-disk database")
	}
	if err := fixture.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.DB
}
