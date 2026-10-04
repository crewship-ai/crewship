package api

import (
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/image"
)

func TestCacheGCCollectsSupersededUntaggedRevisionOnly(t *testing.T) {
	t.Setenv(cacheGCAutoDeleteEnv, "true")
	config := `{"image":"ubuntu:22.04"}`
	h, workspace, crew := covProvRig(t, &covCommitClient{}, config)
	definition := provisionDefinition{Config: config}
	first := revisionFixture()
	oldID := first.Requirements.Toolchain.ImageID
	if _, err := h.saveProvisionResult(t.Context(), crew, workspace, definition, first); err != nil {
		t.Fatal(err)
	}
	second := revisionFixture()
	newID := "sha256:" + strings.Repeat("b", 64)
	second.Requirements.Toolchain.ImageID = newID
	if _, err := h.saveProvisionResult(t.Context(), crew, workspace, definition, second); err != nil {
		t.Fatal(err)
	}
	// Real builds publish their immutable id. The fixture uses a legacy tag;
	// select the new immutable artifact exactly as the production build does.
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, newID, crew); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-2 * cacheImageMinAge).Unix()
	fake := &covGCFake{images: []image.Summary{
		{ID: oldID, Created: created},
		{ID: newID, Created: created},
		{ID: "sha256:" + strings.Repeat("c", 64), Created: created},
	}}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 || fake.removedImages[0] != oldID {
		t.Fatalf("want only superseded owned artifact removed; got %v", fake.removedImages)
	}
}
