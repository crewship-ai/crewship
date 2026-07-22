package api

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
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

// filterValues extracts the value set for a filter term from client.Filters
// (map[string]map[string]bool) as a slice, mirroring the old filters.Args.Get.
func filterValues(f client.Filters, term string) []string {
	set := f[term]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	return out
}

func (f *fakeGCClient) ContainerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	if f.containerListErr != nil {
		return client.ContainerListResult{}, f.containerListErr
	}
	// Enforce the label filter so a regression in the sweeper's filter
	// argument is caught by the test (e.g. if it stopped scoping to
	// crewship.temp=provision and started touching unrelated containers).
	wantLabels := filterValues(opts.Filters, "label")
	if len(wantLabels) == 0 {
		return client.ContainerListResult{Items: f.containers}, nil
	}
	// The name filter is emulated too, or a sweeper that drops it still
	// looks correct here while scanning the whole host in production.
	// Docker's name filter is an unanchored substring match — modelled as
	// such deliberately, because that looseness is exactly why the sweeper
	// keeps hasTempContainerName as its authoritative check.
	wantNames := filterValues(opts.Filters, "name")

	out := make([]container.Summary, 0, len(f.containers))
	for _, c := range f.containers {
		if !matchesAllLabels(c.Labels, wantLabels) {
			continue
		}
		if len(wantNames) > 0 && !matchesAnyName(c.Names, wantNames) {
			continue
		}
		out = append(out, c)
	}
	return client.ContainerListResult{Items: out}, nil
}

func matchesAnyName(names []string, wants []string) bool {
	for _, want := range wants {
		for _, n := range names {
			if strings.Contains(strings.TrimPrefix(n, "/"), want) {
				return true
			}
		}
	}
	return false
}

// tempNames builds the Names slice Docker reports for a provisioner scratch
// container: leading slash, TempContainerNamePrefix, then a unique suffix.
// Fixtures used to omit Names entirely, which modelled a container that has
// never existed — every container the daemon returns has a name.
func tempNames(suffix string) []string {
	return []string{"/" + devcontainer.TempContainerNamePrefix + suffix}
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

func (f *fakeGCClient) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.removedContainers = append(f.removedContainers, id)
	return client.ContainerRemoveResult{}, nil
}

func (f *fakeGCClient) ImageList(_ context.Context, _ client.ImageListOptions) (client.ImageListResult, error) {
	atomic.AddInt32(&f.imageListCalls, 1)
	if f.imageListErr != nil {
		return client.ImageListResult{}, f.imageListErr
	}
	return client.ImageListResult{Items: f.images}, nil
}

func (f *fakeGCClient) ImageRemove(_ context.Context, id string, _ client.ImageRemoveOptions) (client.ImageRemoveResult, error) {
	f.removedImages = append(f.removedImages, id)
	return client.ImageRemoveResult{}, nil
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
			{ID: "old1", Names: tempNames("old1"), Created: now - int64((2 * time.Hour).Seconds()), Labels: tempLabel},
			{ID: "fresh", Names: tempNames("fresh"), Created: now - int64((5 * time.Minute).Seconds()), Labels: tempLabel},
			{ID: "old2", Names: tempNames("old2"), Created: now - int64((3 * time.Hour).Seconds()), Labels: tempLabel},
		},
	}
	h := newGCTestHandler(t, fake)

	h.sweepOrphanTempContainers(context.Background())

	if got := strings.Join(fake.removedContainers, ","); got != "old1,old2" {
		t.Errorf("removed containers = %q; want old1,old2", got)
	}
}

// A live crew container must survive the sweeper even though it carries the
// temp label, because it inherits that label from its own cached image and
// cannot help doing so.
//
// The provisioner labels its scratch container crewship.temp=provision and
// then `docker commit`s it into crewship-cache:<hash>. Docker copies the
// source container's config — labels included — into the committed image,
// and every container started from that image inherits them. Crew runtime
// containers set no labels of their own (buildCrewContainerConfig passes
// none), so crewship.temp=provision is the only label they carry, and the
// sweeper's label filter matched them exactly.
//
// Observed on crewship-dev on 2026-07-20: a healthy crew container that had
// been serving for an hour was force-removed the first time a sweep ran
// after it crossed tempContainerMaxAge.
//
//	09:50:36 "orphan temp-container GC: removed stale temp containers"
//	         "removed":1,"scanned":1,"duration":829782053
//
// A label cannot be an identity marker across a commit. The container name
// can: names live on the container, never in the image.
func TestSweepOrphanTempContainers_SparesLiveCrewContainers(t *testing.T) {
	now := time.Now().Unix()
	// Exactly what a crew container looks like to the daemon: the temp
	// label inherited from its cache image, and nothing else.
	inheritedLabel := map[string]string{
		devcontainer.TempContainerLabelKey: devcontainer.TempContainerLabelValue,
	}
	fake := &fakeGCClient{
		containers: []container.Summary{
			{
				ID:      "crew-instance",
				Names:   []string{"/crewship-1-team-quality-cmrmd4i0a01b1b5a27a84"},
				Created: now - int64((3 * time.Hour).Seconds()),
				State:   "running",
				Labels:  inheritedLabel,
			},
			{
				ID:      "crew-legacy",
				Names:   []string{"/crewship-team-engineering-cmrmg4gnu000b55b3967c"},
				Created: now - int64((30 * 24 * time.Hour).Seconds()),
				State:   "running",
				Labels:  inheritedLabel,
			},
			{
				// A genuinely leaked provisioner container: same label,
				// but named by the provisioner rather than by the crew.
				ID:      "leaked-temp",
				Names:   []string{"/" + devcontainer.TempContainerNamePrefix + "a1b2c3d4"},
				Created: now - int64((2 * time.Hour).Seconds()),
				State:   "running",
				Labels:  inheritedLabel,
			},
		},
	}
	h := newGCTestHandler(t, fake)

	h.sweepOrphanTempContainers(context.Background())

	if got := strings.Join(fake.removedContainers, ","); got != "leaked-temp" {
		t.Errorf("removed containers = %q; want only leaked-temp — a crew container was killed by the orphan sweeper", got)
	}
}

// Sparing crew containers must not cost the sweeper its ability to reach
// the orphans it exists to remove.
//
// The list is capped at orphanGCSweepCap. With a label-only daemon-side
// filter, every crew container on the host is a candidate — and on a shared
// daemon they vastly outnumber leaked scratch containers, so a real orphan
// can sit beyond the cap and never be cleaned up. Docker returns
// newest-first, which puts an old leak last: precisely where the cap bites.
// Narrowing server-side by name spends the cap on real candidates.
func TestSweepOrphanTempContainers_CapNotConsumedByCrewContainers(t *testing.T) {
	now := time.Now().Unix()
	label := map[string]string{
		devcontainer.TempContainerLabelKey: devcontainer.TempContainerLabelValue,
	}

	containers := make([]container.Summary, 0, orphanGCSweepCap+51)
	for i := 0; i < orphanGCSweepCap+50; i++ {
		containers = append(containers, container.Summary{
			ID:      "crew" + strconv.Itoa(i),
			Names:   []string{"/crewship-1-team-crew" + strconv.Itoa(i) + "-cmrmd4i0a01b1b5a27a84"},
			Created: now - int64((2 * time.Hour).Seconds()),
			State:   "running",
			Labels:  label,
		})
	}
	// Oldest, so newest-first ordering leaves it last — past the cap.
	containers = append(containers, container.Summary{
		ID:      "real-orphan",
		Names:   []string{"/" + devcontainer.TempContainerNamePrefix + "deadbeef"},
		Created: now - int64((5 * time.Hour).Seconds()),
		Labels:  label,
	})

	fake := &fakeGCClient{containers: containers}
	h := newGCTestHandler(t, fake)

	h.sweepOrphanTempContainers(context.Background())

	if got := strings.Join(fake.removedContainers, ","); got != "real-orphan" {
		t.Errorf("removed = %q; want real-orphan — the cap was spent on crew containers the sweeper can never delete anyway", got)
	}
}

// The provisioner's own naming must stay distinguishable from every crew
// container name, or the sweeper's name check silently stops protecting
// them. Crew names come from crewResourceName: "<prefix>-team-<slug>-<id>".
func TestTempContainerNamePrefix_CannotCollideWithCrewNames(t *testing.T) {
	if devcontainer.TempContainerNamePrefix == "" {
		t.Fatal("TempContainerNamePrefix must not be empty — it is the sweeper's only safe identity marker")
	}
	for _, crewName := range []string{
		"crewship-team-quality-cmrmd4i0a01b1b5a27a84",
		"crewship-1-team-quality-cmrmd4i0a01b1b5a27a84",
		"crewship-12-team-data-pipeline-cmrmg4gnu000b55b3967c",
	} {
		if strings.HasPrefix(crewName, devcontainer.TempContainerNamePrefix) {
			t.Errorf("crew container name %q starts with the temp prefix %q — the sweeper would delete live crews",
				crewName, devcontainer.TempContainerNamePrefix)
		}
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
			// Old, has label AND the provisioner's name → should be removed.
			{ID: "ours-old", Names: tempNames("ours-old"), Created: now - int64((2 * time.Hour).Seconds()), Labels: tempLabel},
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

func TestSweepOrphanCacheImages_PrunesFeatureImages(t *testing.T) {
	now := time.Now().Unix()
	old := now - int64((24 * time.Hour).Seconds())
	fake := &fakeGCClient{
		images: []image.Summary{
			// Intermediate BuildKit feature image — regenerable, never
			// referenced by a crew → must be pruned.
			{RepoTags: []string{"crewship-feat:abc123"}, Created: old},
			// A feature image younger than the age floor must be kept.
			{RepoTags: []string{"crewship-feat:young1"}, Created: now - 1},
		},
	}
	h := newGCTestHandler(t, fake)
	t.Setenv(cacheGCAutoDeleteEnv, "true")

	h.sweepOrphanCacheImages(context.Background())

	if len(fake.removedImages) != 1 || fake.removedImages[0] != "crewship-feat:abc123" {
		t.Errorf("expected only the old feature image pruned, got %v", fake.removedImages)
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
