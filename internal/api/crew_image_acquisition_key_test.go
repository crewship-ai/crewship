package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func TestOwnedTaglessImageReservesItsCanonicalAcquisition(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	removeAtFirstSight(t)
	img := collectionImage("a", collectionOwner, devcontainer.ImageKindFeature)
	canonical := img.RepoTags[0]
	img.RepoTags = nil
	f := &fakeGCClient{images: []image.Summary{img}}
	h := newGCTestHandler(t, f)
	h.instanceID = collectionOwner
	release, err := resourcelifecycle.Images(h.db).UseImages(t.Context(), canonical)
	if err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	release()
	if len(f.removedImages) != 0 {
		t.Fatal("tagless image removed during canonical alias acquisition")
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(f.removedImages) != 1 {
		t.Fatal("unused attributable tagless image was never reclaimed")
	}
}

func TestOwnedTaglessImageWithoutValidAcquisitionKeyIsRetained(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	removeAtFirstSight(t)
	for _, key := range []string{"", "bad", strings.Repeat("z", 64)} {
		img := collectionImage("a", collectionOwner, devcontainer.ImageKindFeature)
		img.RepoTags = nil
		img.Labels[devcontainer.ImageAcquisitionKeyLabel] = key
		f := &fakeGCClient{images: []image.Summary{img}}
		h := newGCTestHandler(t, f)
		h.instanceID = collectionOwner
		h.sweepOrphanCacheImages(t.Context())
		if len(f.removedImages) != 0 {
			t.Fatalf("tagless image with unknown acquisition %q was removed", key)
		}
	}
}

type acquisitionCheckingRemoval struct {
	*fakeGCClient
	check func()
}

func (f *acquisitionCheckingRemoval) ImageRemove(ctx context.Context, id string, options client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	f.check()
	return f.fakeGCClient.ImageRemove(ctx, id, options)
}

func TestTaglessCollectionHoldsCanonicalLeaseThroughActualRemoval(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	removeAtFirstSight(t)
	img := collectionImage("a", collectionOwner, devcontainer.ImageKindFeature)
	canonical := img.RepoTags[0]
	img.RepoTags = nil
	f := &fakeGCClient{images: []image.Summary{img}}
	h := newGCTestHandler(t, f)
	h.instanceID = collectionOwner
	checked := false
	h.gcClient = &acquisitionCheckingRemoval{fakeGCClient: f, check: func() {
		checked = true
		if release, idle := resourcelifecycle.Images(h.db).TryCollectImages(canonical); idle {
			release()
			t.Fatal("canonical acquisition not reserved during image removal")
		}
	}}
	h.sweepOrphanCacheImages(t.Context())
	if !checked || len(f.removedImages) != 1 {
		t.Fatal("removal boundary not exercised")
	}
}

func TestManualDeletionReservesCanonicalAliasWithoutSelfConflict(t *testing.T) {
	for _, busy := range []bool{true, false} {
		img := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
		canonical := img.RepoTags[0]
		requested := canonical
		if busy {
			requested = "crewship-cache:another-alias"
		}
		img.RepoTags = []string{requested}
		fake := &fakeGCClient{images: []image.Summary{img}}
		h := newCacheTestHandler(t, fake)
		user := seedTestUser(t, h.db)
		ws := seedTestWorkspace(t, h.db, user)
		if busy {
			release, err := resourcelifecycle.Images(h.db).UseImages(t.Context(), canonical)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
		}
		req := httptest.NewRequest(http.MethodDelete, "/x", nil)
		req.SetPathValue("tag", requested)
		req = withWorkspaceUser(req, user, ws, "OWNER")
		response := httptest.NewRecorder()
		h.CacheDelete(response, req)
		if busy && (response.Code != http.StatusConflict || len(fake.removedImages) != 0) {
			t.Fatalf("manual delete ignored active canonical alias: status=%d removed=%v", response.Code, fake.removedImages)
		}
		if !busy && (response.Code != http.StatusOK || len(fake.removedImages) != 1) {
			t.Fatalf("canonical tag deadlocked its own deletion: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
