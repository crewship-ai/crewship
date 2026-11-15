package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func TestOwnedImageObservedUseResetFailureRevokesTrust(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	img := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	fake := &fakeGCClient{images: []image.Summary{img}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	user := seedTestUser(t, h.db)
	ws := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "state-failure", ws, "State", "state-failure")
	h.sweepOrphanCacheImages(t.Context())
	setUnusedSince(t, h, img.ID, time.Now().Add(-2*ownedImageUnusedWindow))
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, img.ID, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`CREATE TRIGGER refuse_image_reset BEFORE DELETE ON resource_retention_images WHEN OLD.image_id <> 'last-completed-pass' BEGIN SELECT RAISE(FAIL,'synthetic state write failure'); END`); err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	if _, known := unusedSinceRows(t, h)[lastPassMarker]; known {
		t.Fatal("failed reset still published a trusted pass")
	}
	if _, err := h.db.Exec(`DROP TRIGGER refuse_image_reset`); err != nil {
		t.Fatal(err)
	}
	// Release every selection, including pointers maintained by the trigger.
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=NULL,last_selected_image=NULL,previous_image=NULL WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("old unused window survived the failed observed-use reset")
	}
	if first := unusedSinceRows(t, h)[img.ID]; time.Since(first) > time.Minute {
		t.Fatalf("window was not restarted: %v", first)
	}
}

func TestOwnedImageFinalActiveLeaseRestartsWindow(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	img := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	fake := &fakeGCClient{images: []image.Summary{img}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	h.sweepOrphanCacheImages(t.Context())
	setUnusedSince(t, h, img.ID, time.Now().Add(-2*ownedImageUnusedWindow))
	release, err := resourcelifecycle.Images(h.db).UseImages(t.Context(), img.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	release()
	if _, known := unusedSinceRows(t, h)[img.ID]; known {
		t.Fatal("positively observed lease retained mature unused window")
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("image removed immediately after observed lease use")
	}
}

type selectingFinalInventory struct {
	*fakeGCClient
	selectImage func()
	calls       int
}

func (f *selectingFinalInventory) ImageList(ctx context.Context, o client.ImageListOptions) (client.ImageListResult, error) {
	f.calls++
	if f.calls == 2 {
		f.selectImage()
	}
	return f.fakeGCClient.ImageList(ctx, o)
}
func TestOwnedImageFinalSelectionRestartsWindow(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	img := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	fake := &fakeGCClient{images: []image.Summary{img}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	user := seedTestUser(t, h.db)
	ws := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "late-selection", ws, "Late", "late-selection")
	h.sweepOrphanCacheImages(t.Context())
	setUnusedSince(t, h, img.ID, time.Now().Add(-2*ownedImageUnusedWindow))
	h.gcClient = &selectingFinalInventory{fakeGCClient: fake, selectImage: func() {
		if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, img.ID, crew); err != nil {
			t.Fatal(err)
		}
	}}
	h.sweepOrphanCacheImages(t.Context())
	if _, known := unusedSinceRows(t, h)[img.ID]; known {
		t.Fatal("selection at final check did not restart window")
	}
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=NULL,last_selected_image=NULL,previous_image=NULL WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("image removed after a selection observed only by final check")
	}
}

func TestOwnedImageMalformedOldLookingTombstoneProtectsAllSelections(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew), collectionImage("b", collectionOwner, devcontainer.ImageKindCrew)}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	user := seedTestUser(t, h.db)
	ws := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "bad-time", ws, "Bad", "bad-time")
	if _, err := h.db.Exec(`UPDATE crews SET deleted_at='2020-01-01Tgarbage',cached_image=?,previous_image=? WHERE id=?`, fake.images[0].ID, fake.images[1].ID, crew); err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	for _, img := range fake.images {
		setUnusedSince(t, h, img.ID, time.Now().Add(-2*ownedImageUnusedWindow))
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatalf("malformed deletion time released selections: %s", strings.Join(fake.removedImages, ","))
	}
}
