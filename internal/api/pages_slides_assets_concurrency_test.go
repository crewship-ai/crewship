package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestSlidesImageAttachmentExpiryGCConcurrency(t *testing.T) {
	for _, promoteFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "promotion-wins", false: "expiry-wins"}[promoteFirst], func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			f.r.pages.artifactStoragePath = storageDir(t)
			const page = "gc-slide-page"
			wmSeedPage(t, f.db, "ws-old", page, page, "GC Slides", "boss", "")
			uploaded := f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/assets?mutation_id=gc", string(pageAssetPNG(t)))
			wantCode(t, uploaded, 201, "upload")
			asset := assetMeta(t, uploaded)
			deck := slideTestDeck(t, page)
			wantCode(t, f.slideDo(f.boss, "POST", "/api/v1/pages/"+page+"/slides/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "initialize")
			// Only the fixture expiry is shortened. Production rows are immutable; the
			// hook, Store transaction, collector and canonical bytes are all real.
			mustExec(t, f.db, `DROP TRIGGER page_asset_immutable`)
			entered := make(chan time.Time, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			hook := func(ctx context.Context, tx *sql.Tx, key slides.Key, revision int64, candidate slides.Deck) error {
				expiry := time.Now().UTC().Add(200 * time.Millisecond)
				if _, err := tx.ExecContext(ctx, `UPDATE page_assets SET expires_at=? WHERE id=?`, tsformat.Format(expiry), asset.ID); err != nil {
					return err
				}
				if promoteFirst {
					if err := f.r.pages.pinSlideAssets(ctx, tx, key, revision, candidate); err != nil {
						return err
					}
				}
				entered <- expiry
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if !promoteFirst {
					return f.r.pages.pinSlideAssets(ctx, tx, key, revision, candidate)
				}
				return nil
			}
			authority := func(context.Context, *sql.Tx) (string, error) { return "user:boss", nil }
			store := slides.New(f.db, slides.Options{OnRevision: hook})
			key := slides.Key{WorkspaceID: "ws-old", PageID: page}
			state, err := store.Read(t.Context(), key, 0, authority)
			if err != nil {
				t.Fatal(err)
			}
			slide := state.Deck.Slides[0]
			hash, _ := slides.SlideHash(slide)
			target := 2
			element := slides.Element{ID: "gc_image", Type: "image", Width: 10, Height: 10, Image: &slides.Image{AssetID: asset.ID, ExpectedSHA256: asset.SHA256, Alt: "GC"}}
			request := slides.PatchRequest{PageID: page, SchemaVersion: 1, BaseRev: 1, MutationID: "attach", TargetSchemaVersion: &target, IfContentHash: state.ContentHash, Operations: []slides.Operation{{Op: "insert_element", SlideID: slide.ID, IfHash: hash, Element: &element}}}
			done := make(chan error, 1)
			go func() { _, err := store.Patch(t.Context(), key, request, authority); done <- err }()
			var expiry time.Time
			select {
			case expiry = <-entered:
			case err := <-done:
				t.Fatalf("attach ended before hook %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("hook admission timeout")
			}
			gcDone := make(chan struct{})
			go func() {
				runAttachmentGCPass(t.Context(), f.db, newTestLogger(), f.r.pages.artifactStoragePath)
				close(gcDone)
			}()
			timer := time.NewTimer(time.Until(expiry.Add(20 * time.Millisecond)))
			defer timer.Stop()
			<-timer.C
			unblock()
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("attachment deadlocked with collector")
			}
			if promoteFirst && err != nil {
				t.Fatal(err)
			}
			if !promoteFirst && !errors.Is(err, slides.ErrInvalid) {
				t.Fatalf("expired attach %v", err)
			}
			select {
			case <-gcDone:
			case <-time.After(3 * time.Second):
				t.Fatal("collector deadlocked with attachment")
			}
			var rows, pins int
			if err = f.db.QueryRow(`SELECT COUNT(*) FROM page_assets WHERE id=?`, asset.ID).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if err = f.db.QueryRow(`SELECT COUNT(*) FROM slides_revision_assets WHERE page_id=?`, page).Scan(&pins); err != nil {
				t.Fatal(err)
			}
			if promoteFirst {
				if rows != 1 || pins != 1 {
					t.Fatalf("retained rows/pins %d/%d", rows, pins)
				}
				data, err := readPageAssetBlob(f.r.pages.artifactStoragePath, "ws-old", asset.SHA256)
				if err != nil || attachmentDigest(data) != asset.SHA256 {
					t.Fatal("collector lost retained bytes", err)
				}
			} else {
				// The shortened expiry rolled back too. Expire the still-staged fixture
				// explicitly, then prove normal collection reclaims its unreferenced bytes.
				mustExec(t, f.db, `UPDATE page_assets SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, asset.ID)
				runAttachmentGCPass(t.Context(), f.db, newTestLogger(), f.r.pages.artifactStoragePath)
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM page_assets WHERE id=?`, asset.ID).Scan(&rows); err != nil || rows != 0 {
					t.Fatalf("expired rows remained %d %v", rows, err)
				}
				path, err := attachmentBlobPath(f.r.pages.artifactStoragePath, "ws-old", asset.SHA256)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unreferenced expired bytes not reclaimed: %v", err)
				}
				if pins != 0 {
					t.Fatal("expired revision retained a pin")
				}
				if current, err := store.Read(t.Context(), key, 0, authority); err != nil || current.Revision != 1 {
					t.Fatal("expired attach changed head", err)
				}
			}
		})
	}
}
