package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/image"
)

func TestQualifiedCacheImageOwnership(t *testing.T) {
	for _, kind := range []string{devcontainer.ImageKindCrew, devcontainer.ImageKindFeature} {
		img := collectionImage("1", collectionOwner, kind)
		short := img.RepoTags[0]
		for _, tc := range []struct {
			name string
			tags []string
			want bool
		}{
			{"Docker Hub library", []string{"docker.io/library/" + short}, true},
			{"both spellings", []string{short, "docker.io/library/" + short}, true},
			{"local registry", []string{"localhost/" + short}, false},
			{"other registry", []string{"other.example/library/" + short}, false},
			{"other namespace", []string{"docker.io/customer/" + short}, false},
			{"similar repository", []string{"docker.io/library/" + strings.Replace(short, ":", "-extra:", 1)}, false},
			{"mixed aliases", []string{"docker.io/library/" + short, "example.com/" + short}, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				img.RepoTags = tc.tags
				if got := ownedImage(img, collectionOwner); got != tc.want {
					t.Fatalf("ownership=%t want %t", got, tc.want)
				}
			})
		}
	}
}

func TestQualifiedCacheImageSelectionAndLease(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	removeAtFirstSight(t)
	for _, reverse := range []bool{false, true} {
		img := collectionImage("1", collectionOwner, devcontainer.ImageKindCrew)
		free := collectionImage("2", collectionOwner, devcontainer.ImageKindCrew)
		selection := img.RepoTags[0]
		if reverse {
			selection = "docker.io/library/" + selection
		} else {
			img.RepoTags[0] = "docker.io/library/" + selection
		}
		free.RepoTags[0] = "docker.io/library/" + free.RepoTags[0]
		h, _, crew := covProvRig(t, &covCommitClient{}, "{}")
		h.instanceID = collectionOwner
		fake := &fakeGCClient{images: []image.Summary{img, free}}
		h.gcClient = fake
		mustExec(t, h.db, "UPDATE crews SET runtime_image=? WHERE id=?", selection, crew)
		h.sweepOrphanCacheImages(t.Context())
		if len(fake.removedImages) != 1 || fake.removedImages[0] != free.ID {
			t.Fatalf("selected %s: removed %v", selection, fake.removedImages)
		}
		mustExec(t, h.db, "UPDATE crews SET runtime_image=NULL WHERE id=?", crew)
		fake.images = []image.Summary{img}
		fake.removedImages = nil
		release, err := resourcelifecycle.Images(h.db).UseImages(t.Context(), selection)
		if err != nil {
			t.Fatal(err)
		}
		h.sweepOrphanCacheImages(t.Context())
		release()
		if len(fake.removedImages) != 0 {
			t.Fatalf("removed leased image %s", img.ID)
		}
	}
}

func TestQualifiedCacheDigestSelection(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	removeAtFirstSight(t)
	img := collectionImage("1", collectionOwner, devcontainer.ImageKindCrew)
	img.RepoDigests = []string{"docker.io/library/crewship-cache@sha256:" + strings.Repeat("2", 64)}
	h, _, crew := covProvRig(t, &covCommitClient{}, "")
	h.instanceID = collectionOwner
	fake := &fakeGCClient{images: []image.Summary{img}}
	h.gcClient = fake
	mustExec(t, h.db, "UPDATE crews SET runtime_image=? WHERE id=?", strings.TrimPrefix(img.RepoDigests[0], "docker.io/library/"), crew)
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatalf("collected selection by manifest digest: %v", fake.removedImages)
	}
}
