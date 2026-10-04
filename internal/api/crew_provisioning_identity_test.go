package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

func TestCacheIdentityProtectsAliasesFromDeletionAndGC(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	for _, tag := range []string{"crewship-cache:first", "crewship-cache:second"} {
		t.Run(tag, func(t *testing.T) {
			fake := &fakeGCClient{images: []image.Summary{{ID: id, RepoTags: []string{"crewship-cache:first", "crewship-cache:second"}, Created: time.Now().Add(-time.Hour).Unix()}}}
			h := newCacheTestHandler(t, fake)
			user := seedTestUser(t, h.db)
			workspace := seedTestWorkspace(t, h.db, user)
			crew := seedCrewRow(t, h.db, "pinned-crew", workspace, "Pinned", "pinned")
			if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, id, crew); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("DELETE", "/x", nil)
			req.SetPathValue("tag", tag)
			req = withWorkspaceUser(req, user, workspace, "OWNER")
			rr := httptest.NewRecorder()
			h.CacheDelete(rr, req)
			if rr.Code != http.StatusConflict || len(fake.removedImages) != 0 {
				t.Fatalf("selected artifact deleted: status=%d removed=%v", rr.Code, fake.removedImages)
			}
			t.Setenv(cacheGCAutoDeleteEnv, "true")
			h.imgListCache = cachedImageList{images: []image.Summary{{ID: "stale-id", RepoTags: []string{tag}, Created: time.Now().Add(-time.Hour).Unix()}}, fetchedAt: time.Now()}
			h.sweepOrphanCacheImages(context.Background())
			if len(fake.removedImages) != 0 {
				t.Fatalf("GC deleted a selected artifact: %v", fake.removedImages)
			}
			rr = httptest.NewRecorder()
			h.invalidateImageListCache()
			h.CacheList(rr, req)
			if rr.Code != 200 || strings.Count(rr.Body.String(), `"pinned"`) != 2 {
				t.Fatalf("inventory hid immutable references: %s", rr.Body.String())
			}
		})
	}
}

type retaggingCacheClient struct{ *fakeGCClient }

func (f *retaggingCacheClient) ImageList(ctx context.Context, opts client.ImageListOptions) (client.ImageListResult, error) {
	listed, err := f.fakeGCClient.ImageList(ctx, opts)
	// Simulate another build moving the alias after inspection.
	f.images = []image.Summary{{ID: "sha256:" + strings.Repeat("c", 64), RepoTags: []string{"crewship-cache:candidate"}}}
	return listed, err
}

func TestCacheDeleteDoesNotFollowRetaggedAlias(t *testing.T) {
	old := "sha256:" + strings.Repeat("b", 64)
	fake := &retaggingCacheClient{&fakeGCClient{images: []image.Summary{{ID: old, RepoTags: []string{"crewship-cache:candidate"}}}}}
	h := newCacheTestHandler(t, fake)
	user := seedTestUser(t, h.db)
	workspace := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "pinned", workspace, "Pinned", "pinned")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, "sha256:"+strings.Repeat("a", 64), crew); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("tag", "crewship-cache:candidate")
	req = withWorkspaceUser(req, user, workspace, "OWNER")
	rr := httptest.NewRecorder()
	h.CacheDelete(rr, req)
	if rr.Code != 200 || len(fake.removedImages) != 1 || fake.removedImages[0] != old {
		t.Fatalf("delete followed a mutable alias: status=%d removed=%v", rr.Code, fake.removedImages)
	}
}

func TestFailedRebuildKeepsPreviousArtifact(t *testing.T) {
	config := `{invalid`
	h, workspace, crew := covProvRig(t, &covCommitClient{}, config)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image='crewship-cache:working',config_hash='working-hash' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := h.enqueueForCrew(context.Background(), crew, workspace, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.RLock()
		job := h.jobs[crew]
		failed := job != nil && job.Status == "failed"
		h.mu.RUnlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild did not reach failed state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var ref, hash string
	if err := h.db.QueryRow(`SELECT cached_image,config_hash FROM crews WHERE id=?`, crew).Scan(&ref, &hash); err != nil {
		t.Fatal(err)
	}
	if ref != "crewship-cache:working" || hash != "working-hash" {
		t.Fatalf("failed replacement erased artifact: %s %s", ref, hash)
	}
}
