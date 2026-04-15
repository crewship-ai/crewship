package api

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

// fakeGCClient is a minimal orphanGCClient implementation for unit-testing the
// orphan sweepers without a real Docker daemon.
type fakeGCClient struct {
	containers       []container.Summary
	images           []image.Summary
	containerListErr error
	imageListErr     error

	removedContainers []string
	removedImages     []string

	imageListCalls int32
}

func (f *fakeGCClient) ContainerList(_ context.Context, opts container.ListOptions) ([]container.Summary, error) {
	if f.containerListErr != nil {
		return nil, f.containerListErr
	}
	// Enforce the label filter so a regression in the sweeper's filter
	// argument is caught by the test (e.g. if it stopped scoping to
	// crewship.temp=provision and started touching unrelated containers).
	wantLabels := opts.Filters.Get("label")
	if len(wantLabels) == 0 {
		return f.containers, nil
	}
	out := make([]container.Summary, 0, len(f.containers))
	for _, c := range f.containers {
		if matchesAllLabels(c.Labels, wantLabels) {
			out = append(out, c)
		}
	}
	return out, nil
}

// matchesAllLabels returns true when every "key=value" filter expression in
// wants is present in labels. Mirrors the real Docker daemon's exact-match
// label filter semantics for our test scope.
func matchesAllLabels(labels map[string]string, wants []string) bool {
	for _, expr := range wants {
		eq := strings.Index(expr, "=")
		if eq < 0 {
			// Key-only filter ("label=foo") — require key presence.
			if _, ok := labels[expr]; !ok {
				return false
			}
			continue
		}
		key, val := expr[:eq], expr[eq+1:]
		if labels[key] != val {
			return false
		}
	}
	return true
}

func (f *fakeGCClient) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.removedContainers = append(f.removedContainers, id)
	return nil
}

func (f *fakeGCClient) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	atomic.AddInt32(&f.imageListCalls, 1)
	if f.imageListErr != nil {
		return nil, f.imageListErr
	}
	return f.images, nil
}

func (f *fakeGCClient) ImageRemove(_ context.Context, id string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.removedImages = append(f.removedImages, id)
	return nil, nil
}

func newGCTestHandler(t *testing.T, fake *fakeGCClient) *ProvisioningHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return &ProvisioningHandler{
		db:       setupTestDB(t),
		logger:   logger,
		gcClient: fake,
	}
}

// Temp container sweeper — removes stale, keeps fresh, respects cap.
func TestSweepOrphanTempContainers_RemovesStaleKeepsFresh(t *testing.T) {
	now := time.Now().Unix()
	tempLabel := map[string]string{
		devcontainer.TempContainerLabelKey: devcontainer.TempContainerLabelValue,
	}
	fake := &fakeGCClient{
		containers: []container.Summary{
			{ID: "old1", Created: now - int64((2 * time.Hour).Seconds()), Labels: tempLabel},
			{ID: "fresh", Created: now - int64((5 * time.Minute).Seconds()), Labels: tempLabel},
			{ID: "old2", Created: now - int64((3 * time.Hour).Seconds()), Labels: tempLabel},
		},
	}
	h := newGCTestHandler(t, fake)

	h.sweepOrphanTempContainers(context.Background())

	if got := strings.Join(fake.removedContainers, ","); got != "old1,old2" {
		t.Errorf("removed containers = %q; want old1,old2", got)
	}
}

// Sentinel: if the sweeper ever drops or weakens its label filter, this test
// fails — guards against accidentally broadening the destructive scope to
// non-Crewship containers. Unrelated old containers must NEVER be removed.
func TestSweepOrphanTempContainers_LabelFilterScopesDestruction(t *testing.T) {
	now := time.Now().Unix()
	tempLabel := map[string]string{
		devcontainer.TempContainerLabelKey: devcontainer.TempContainerLabelValue,
	}
	fake := &fakeGCClient{
		containers: []container.Summary{
			// Old, has label → should be removed.
			{ID: "ours-old", Created: now - int64((2 * time.Hour).Seconds()), Labels: tempLabel},
			// Old, NO crewship label → must be ignored even though it's old.
			{ID: "user-old", Created: now - int64((24 * time.Hour).Seconds()), Labels: map[string]string{"app": "their-thing"}},
			// Old, different crewship label value → must be ignored.
			{ID: "wrong-value", Created: now - int64((24 * time.Hour).Seconds()), Labels: map[string]string{devcontainer.TempContainerLabelKey: "different"}},
		},
	}
	h := newGCTestHandler(t, fake)

	h.sweepOrphanTempContainers(context.Background())

	if got := strings.Join(fake.removedContainers, ","); got != "ours-old" {
		t.Errorf("removed = %q; want ours-old (label filter must scope destruction)", got)
	}
}

func TestSweepOrphanTempContainers_ListErrorDoesNotPanic(t *testing.T) {
	fake := &fakeGCClient{containerListErr: errors.New("daemon gone")}
	h := newGCTestHandler(t, fake)
	h.sweepOrphanTempContainers(context.Background()) // must not panic
	if len(fake.removedContainers) != 0 {
		t.Errorf("no removals expected on list error")
	}
}

func TestSweepOrphanTempContainers_NilClientNoOp(t *testing.T) {
	h := &ProvisioningHandler{
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
		gcClient: nil,
	}
	h.sweepOrphanTempContainers(context.Background()) // must not panic
}

// Cache image sweeper — the critical age-filter test.
func TestSweepOrphanCacheImages_SkipsTooYoungImage(t *testing.T) {
	now := time.Now().Unix()
	fake := &fakeGCClient{
		images: []image.Summary{
			// Old, unreferenced → would normally be swept, but log-only default.
			{RepoTags: []string{"crewship-cache:old"}, Created: now - int64((24 * time.Hour).Seconds())},
			// Just committed, DB row not yet written → must be protected.
			{RepoTags: []string{"crewship-cache:just-committed"}, Created: now - 1},
			// Non-cache image → ignored.
			{RepoTags: []string{"ghcr.io/foo/bar:latest"}, Created: now - int64((24 * time.Hour).Seconds())},
		},
	}
	h := newGCTestHandler(t, fake)
	t.Setenv(cacheGCAutoDeleteEnv, "true") // force destructive path

	h.sweepOrphanCacheImages(context.Background())

	// just-committed must never be removed (age floor). old is allowed.
	for _, removed := range fake.removedImages {
		if removed == "crewship-cache:just-committed" {
			t.Fatalf("removed freshly-committed image: the age-floor failed")
		}
	}
	if len(fake.removedImages) != 1 || fake.removedImages[0] != "crewship-cache:old" {
		t.Errorf("expected to remove only 'crewship-cache:old'; got %v", fake.removedImages)
	}
}

func TestSweepOrphanCacheImages_AutoDeleteDisabledByDefault(t *testing.T) {
	now := time.Now().Unix()
	fake := &fakeGCClient{
		images: []image.Summary{
			{RepoTags: []string{"crewship-cache:old"}, Created: now - int64((24 * time.Hour).Seconds())},
		},
	}
	h := newGCTestHandler(t, fake)
	// No env var → log-only.

	h.sweepOrphanCacheImages(context.Background())

	if len(fake.removedImages) != 0 {
		t.Errorf("images removed without CREWSHIP_CACHE_GC_AUTODELETE=true: %v", fake.removedImages)
	}
}

func TestSweepOrphanCacheImages_SkipsReferenced(t *testing.T) {
	now := time.Now().Unix()
	fake := &fakeGCClient{
		images: []image.Summary{
			{RepoTags: []string{"crewship-cache:live"}, Created: now - int64((24 * time.Hour).Seconds())},
		},
	}
	h := newGCTestHandler(t, fake)
	// Seed a crew that references the image.
	_, err := h.db.Exec(`INSERT INTO workspaces (id, slug, name, created_at, updated_at)
		VALUES ('ws1','ws1','ws1',datetime('now'),datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.db.Exec(`INSERT INTO crews (id, workspace_id, slug, name, cached_image, created_at, updated_at)
		VALUES ('c1','ws1','c1','c1','crewship-cache:live',datetime('now'),datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(cacheGCAutoDeleteEnv, "true")

	h.sweepOrphanCacheImages(context.Background())

	if len(fake.removedImages) != 0 {
		t.Errorf("referenced image was removed: %v", fake.removedImages)
	}
}

// listLocalImagesCached — TTL behaviour.
func TestListLocalImagesCached_Memoizes(t *testing.T) {
	fake := &fakeGCClient{images: []image.Summary{{RepoTags: []string{"crewship-cache:a"}}}}
	h := newGCTestHandler(t, fake)

	if _, err := h.listLocalImagesCached(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.listLocalImagesCached(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&fake.imageListCalls); got != 1 {
		t.Errorf("ImageList called %d times; want 1 (cache hit)", got)
	}
}

func TestListLocalImagesCached_InvalidateForcesRefetch(t *testing.T) {
	fake := &fakeGCClient{images: []image.Summary{{RepoTags: []string{"crewship-cache:a"}}}}
	h := newGCTestHandler(t, fake)

	if _, err := h.listLocalImagesCached(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.invalidateImageListCache()
	if _, err := h.listLocalImagesCached(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&fake.imageListCalls); got != 2 {
		t.Errorf("ImageList called %d times; want 2 (post-invalidate)", got)
	}
}

// Sentinel test: temp containers reference the exported label so sweeper +
// provisioner stay in lock-step. If the label constant ever changes, at least
// one of the tests fails loudly.
func TestTempContainerLabelContract(t *testing.T) {
	if devcontainer.TempContainerLabelKey == "" || devcontainer.TempContainerLabelValue == "" {
		t.Fatal("temp container label constants must be non-empty — sweeper filter depends on them")
	}
}
