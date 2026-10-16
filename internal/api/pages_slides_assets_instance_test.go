package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesImagesPhysicalInstanceRecoveryIntegrity(t *testing.T) {
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	f.r.pages.artifactStoragePath = storageDir(t)
	const page = "instance-image-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Instance Image", "boss", "")
	uploaded := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=first", string(pageAssetPNG(t)))
	wantCode(t, uploaded, 201, "upload")
	asset := assetMeta(t, uploaded)
	deck := slideTestDeck(t, page)
	deck.SchemaVersion = 2
	deck.Slides[0].Elements = append(deck.Slides[0].Elements, slides.Element{ID: "instance_image", Type: "image", Width: 10, Height: 10, Image: &slides.Image{AssetID: asset.ID, ExpectedSHA256: asset.SHA256, Alt: "Instance"}})
	wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 2, MutationID: "init", Deck: deck})), 200, "initialize")
	sourcePath, err := attachmentBlobPath(f.r.pages.artifactStoragePath, "ws-old", asset.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	const pass = "slides-instance-images-recovery-passphrase"
	for _, kind := range []string{"valid", "missing", "corrupt", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "missing":
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(sourcePath, []byte("corrupt image"), 0600); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				mustExec(t, f.db, `DROP TRIGGER page_asset_immutable`)
				mustExec(t, f.db, `UPDATE page_assets SET width=width+1 WHERE id=?`, asset.ID)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(sourcePath, original, 0600); err != nil {
					t.Error(err)
				}
				if kind == "metadata" {
					mustExec(t, f.db, `UPDATE page_assets SET width=width-1 WHERE id=?`, asset.ID)
				}
			})
			made, err := backup.CreateInstanceBackup(t.Context(), f.db, backup.InstanceOptions{OutputDir: t.TempDir(), Passphrase: pass, Paths: backup.InstancePaths{Output: f.r.pages.artifactStoragePath}, Actor: backup.Actor{UserID: "boss", Role: "instance_admin"}, Quiesce: quiesce.New(), Poll: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Join(t.TempDir(), "recovered")
			_, err = backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: made.Path, Passphrase: pass, DataDir: dataDir, Drill: true, Actor: "boss"})
			if kind != "valid" {
				if err == nil {
					t.Fatal("invalid native image instance reported recovery success")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			restored, err := sql.Open("sqlite", filepath.Join(dataDir, "crewship.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var pins int
			if err = restored.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=?`, page).Scan(&pins); err != nil || pins != 1 {
				t.Fatalf("recovered pins %d %v", pins, err)
			}
			bytes, err := readPageAssetBlob(backup.RecoveredStoreDir(dataDir, backup.StoreOutput), "ws-old", asset.SHA256)
			if err != nil || attachmentDigest(bytes) != asset.SHA256 {
				t.Fatal("recovered canonical bytes lost", err)
			}
		})
	}
}

func TestSlidesImagesLegacyNoKitRecoveryVersionBoundary(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(map[int]string{1: "legacy-v1-partial", 2: "v2-missing-pins-refused"}[version], func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			f.r.pages.artifactStoragePath = storageDir(t)
			const page = "legacy-slides-page"
			wmSeedPage(t, f.db, "ws-old", page, page, "Legacy Slides", "boss", "")
			deck := slideTestDeck(t, page)
			deck.SchemaVersion = version
			wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: version, MutationID: "init", Deck: deck})), 200, "initialize source")
			mustExec(t, f.db, `DROP TABLE slides_revision_assets; DELETE FROM _migrations WHERE version=20261016010758`)
			t.Setenv("ENCRYPTION_KEY", "")
			const pass = "legacy-slide-instance-no-kit-passphrase"
			made, err := backup.CreateInstanceBackup(t.Context(), f.db, backup.InstanceOptions{OutputDir: t.TempDir(), Passphrase: pass, Paths: backup.InstancePaths{Output: f.r.pages.artifactStoragePath}, Actor: backup.Actor{UserID: "boss", Role: "instance_admin"}, RecoveryKit: false, Quiesce: quiesce.New(), Poll: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: made.Path, Passphrase: pass, DataDir: filepath.Join(t.TempDir(), "recovered"), Drill: true, Actor: "boss"})
			if version == 2 {
				if err == nil {
					t.Fatal("schema2 without pin schema recovered successfully")
				}
				return
			}
			if err != nil {
				t.Fatalf("legacy-v1 no-kit recovery must preserve existing partial recovery: %v", err)
			}
			if len(recovered.Warnings) == 0 {
				t.Fatal("pending no-kit migration warning absent")
			}
		})
	}
}
