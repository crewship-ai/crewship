package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const collectionOwner = "aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"

func collectionImage(char string, owner, kind string) image.Summary {
	prefix := "crewship-cache:"
	if kind == devcontainer.ImageKindFeature {
		prefix = devcontainer.FeatureImageTagPrefix
	}
	return image.Summary{ID: "sha256:" + strings.Repeat(char, 64), Created: time.Now().Add(-48 * time.Hour).Unix(), RepoTags: []string{prefix + owner + "-" + strings.Repeat(char, 12)}, Labels: map[string]string{devcontainer.ImageInstanceLabel: owner, devcontainer.ImageKindLabel: kind}}
}

func TestOwnedImageCollectionProtectsSelectionsRollbackAndContainers(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	config := `{"image":"ubuntu:22.04"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	h.instanceID = collectionOwner
	images := []image.Summary{}
	for _, c := range []string{"a", "b", "c"} {
		img := collectionImage(c, collectionOwner, devcontainer.ImageKindCrew)
		result := revisionFixture()
		result.CachedImage = img.ID
		result.Requirements.Toolchain.ImageID = img.ID
		if _, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, result); err != nil {
			t.Fatal(err)
		}
		images = append(images, img)
	}
	// A tagless successful image is still attributable. Keep b (rollback), c
	// (current) and d (stopped foreign container); collect only a and feature e.
	images[0].RepoTags = nil
	images = append(images, collectionImage("d", collectionOwner, devcontainer.ImageKindCrew), collectionImage("e", collectionOwner, devcontainer.ImageKindFeature))
	foreign := collectionImage("f", strings.Repeat("b", 64), devcontainer.ImageKindCrew)
	legacy := image.Summary{ID: "sha256:" + strings.Repeat("1", 64), RepoTags: []string{"crewship-cache:legacy"}, Created: images[0].Created}
	images = append(images, foreign, legacy)
	fake := &fakeGCClient{images: images, containers: []container.Summary{{ImageID: images[3].ID, State: "exited"}}}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if got := strings.Join(fake.removedImages, ","); got != images[0].ID+","+images[4].ID {
		t.Fatalf("removed %s", got)
	}
}

func TestOwnedImageCollectionKeepsOnIncompleteEvidence(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	for _, failure := range []string{"containers", "images", "database", "incomplete-container", "disabled", "invalid-setting", "no-identity"} {
		t.Run(failure, func(t *testing.T) {
			fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}
			h := newGCTestHandler(t, fake)
			h.instanceID = collectionOwner
			switch failure {
			case "containers":
				fake.containerListErr = errors.New("unavailable")
			case "images":
				fake.imageListErr = errors.New("unavailable")
			case "database":
				h.db.Close()
			case "incomplete-container":
				fake.containers = []container.Summary{{ID: "unknown"}}
			case "disabled":
				t.Setenv(imageGCEnv, "false")
			case "invalid-setting":
				t.Setenv(imageGCEnv, "not-a-boolean")
			case "no-identity":
				h.instanceID = ""
			}
			h.sweepOrphanCacheImages(t.Context())
			if len(fake.removedImages) > 0 {
				t.Fatalf("deleted on %s: %v", failure, fake.removedImages)
			}
		})
	}
}

func TestOwnedImageCollectionExcludesAcquisitionAndCollectsAbandonedBuild(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	release := resourcelifecycle.Images(h.db).Use()
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("collected image during acquisition/publication")
	}
	release()
	// Models process restart after a build never published a DB reference:
	// the gate has no durable holds; an old labelled artifact is collectible.
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 {
		t.Fatalf("abandoned build retained: %v", fake.removedImages)
	}
}

func TestOwnedImageCollectionDoesNotTrustForeignAliases(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}
	fake.images[0].RepoTags = append(fake.images[0].RepoTags, "custom:keep")
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	t.Setenv(cacheGCAutoDeleteEnv, "true")
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatalf("foreign alias deleted: %v", fake.removedImages)
	}
}

func TestOwnedImageCollectionProtectsActualPreviousSelectionOnCacheReuse(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	config := `{"image":"ubuntu:22.04"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	h.instanceID = collectionOwner
	images := []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew), collectionImage("b", collectionOwner, devcontainer.ImageKindCrew), collectionImage("c", collectionOwner, devcontainer.ImageKindCrew)}
	// Build A,B,C; re-select A,B from cache. Immutable history order is still
	// A,B,C, but actual rollback must now protect A, not C.
	for _, i := range []int{0, 1, 2, 0, 1, 1} {
		r := revisionFixture()
		r.CachedImage = images[i].ID
		r.Requirements.Toolchain.ImageID = r.CachedImage
		if _, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, r); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeGCClient{images: images}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 || fake.removedImages[0] != images[2].ID {
		t.Fatalf("wrong rollback protection: %v", fake.removedImages)
	}
}

type cachedSelectionBuilder struct{ installationWiringBuilder }

func (b *cachedSelectionBuilder) ImageExists(_ context.Context, tag string) (bool, error) {
	b.tag = tag
	return true, nil
}

type blockedQualification struct {
	provider.SandboxRuntime
	reached chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (q *blockedQualification) SandboxCapabilities() provider.SandboxCapabilities {
	q.once.Do(func() { close(q.reached); <-q.proceed })
	return provider.SandboxCapabilities{}
}

// The pause is AFTER Provision returns, at offline qualification. This is the
// original deletion window: a build-tag lock alone has already been released.
func TestOwnedImageCollectionExcludesCacheHitThroughPublication(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	config := `{"image":"synthetic/base:1"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	if _, err := h.db.Exec(`INSERT INTO agents(id,crew_id,workspace_id,name,slug,cli_adapter) VALUES('collection-agent',?,?, 'Agent','agent','CODEX_CLI')`, crew, ws); err != nil {
		t.Fatal(err)
	}
	builder := &cachedSelectionBuilder{}
	h.provisioner = devcontainer.NewBuildOnlyProvisioner(builder, nil, newTestLogger())
	h.SetInstallationID(collectionOwner)
	qualification := &blockedQualification{reached: make(chan struct{}), proceed: make(chan struct{})}
	h.sandboxRuntime = qualification
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(qualification.proceed) }) })
	job := covJob(crew)
	h.jobs[crew] = job
	done := make(chan struct{})
	go func() { defer close(done); h.runProvisioningAttempt(t.Context(), crew, ws, config, "", "", job) }()
	select {
	case <-qualification.reached:
	case <-time.After(30 * time.Second):
		t.Fatal("did not reach post-build qualification")
	}
	artifact := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	artifact.RepoTags = []string{builder.tag}
	fake := &fakeGCClient{images: []image.Summary{artifact}}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("collector entered between cache hit and publication")
	}
	unblock.Do(func() { close(qualification.proceed) })
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("publication did not finish")
	}
	if job.Status != "completed" {
		t.Fatalf("job failed: %s %s", job.Status, job.Error)
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("published selection was removed")
	}
}

func TestIdentityChangePreservesExactSelectedArtifact(t *testing.T) {
	config := `{"image":"synthetic/base:1"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	foreign := collectionImage("a", strings.Repeat("b", 64), devcontainer.ImageKindCrew)
	docker, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": foreign.ID, "Config": map[string]any{"Labels": foreign.Labels}})
	})
	h.docker = docker
	h.SetInstallationID(collectionOwner)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, foreign.ID, crew); err != nil {
		t.Fatal(err)
	}
	needs, ready, _, err := crewImageReady(t.Context(), h.db, crew, ws, func(ref string) bool { return h.imagePresentLocally(t.Context(), ref) })
	if err != nil || needs || !ready {
		t.Fatalf("identity change forced reprovision: needs=%t ready=%t err=%v", needs, ready, err)
	}
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=NULL,runtime_image=? WHERE id=?`, foreign.ID, crew); err != nil {
		t.Fatal(err)
	}
	if cfg, err := buildCrewRuntimeConfig(t.Context(), h.db, crew, ws); err != nil || cfg.Image != foreign.ID {
		t.Fatalf("identity change lost exact runtime: cfg=%+v err=%v", cfg, err)
	}
}

func TestOwnedImageCollectionPreservesLastSelectionDuringConfigInvalidation(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	user := seedTestUser(t, h.db)
	ws := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "invalidated", ws, "Crew", "invalidated")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, fake.images[0].ID, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=NULL,config_hash=NULL WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) > 0 {
		t.Fatal("lost last usable image while replacement pending")
	}
}

func TestColdRegistryBaseKeepsFirstStartPullBehavior(t *testing.T) {
	h, ws, crew := covProvRig(t, &covCommitClient{}, `{"image":"synthetic/base:1"}`)
	docker, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such image"}`))
	})
	h.docker = docker
	h.SetInstallationID(collectionOwner)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=NULL,runtime_image='alpine:3' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	cfg, err := buildCrewRuntimeConfig(t.Context(), h.db, crew, ws)
	if err != nil || cfg.Image != "alpine:3" {
		t.Fatalf("cold registry base blocked: image=%s err=%v", cfg.Image, err)
	}
	if h.imagePresentLocally(t.Context(), "sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("missing cached artifact reported ready")
	}
}

func TestOwnedImageCollectionPreservesPreviousAcrossEquivalentRebuild(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	config := `{"image":"ubuntu:22.04"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	h.instanceID = collectionOwner
	images := []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew), collectionImage("b", collectionOwner, devcontainer.ImageKindCrew), collectionImage("c", collectionOwner, devcontainer.ImageKindCrew)}
	publish := func(i int) {
		t.Helper()
		r := revisionFixture()
		r.CachedImage = images[i].ID
		r.Requirements.Toolchain.ImageID = r.CachedImage
		if _, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, r); err != nil {
			t.Fatal(err)
		}
	}
	publish(0)
	publish(1)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=NULL,config_hash=NULL WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGCClient{images: images}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 || fake.removedImages[0] != images[2].ID {
		t.Fatalf("invalidation lost retained selections: %v", fake.removedImages)
	}
	fake.removedImages = nil
	publish(1)
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 || fake.removedImages[0] != images[2].ID {
		t.Fatalf("equivalent rebuild lost previous distinct image: %v", fake.removedImages)
	}
}

func TestOwnedImageCollectionKeepsConfiguredBaseWithoutContainer(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	user := seedTestUser(t, h.db)
	ws := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "direct-base", ws, "Crew", "direct-base")
	if _, err := h.db.Exec(`UPDATE crews SET runtime_image=?,devcontainer_config='{"image":"alpine:3"}' WHERE id=?`, fake.images[0].ID, crew); err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) > 0 {
		t.Fatal("configured direct runtime removed before container creation")
	}
}

func TestOwnedImageCollectionSeedsPreviousAfterLegacyBackupSelection(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	config := `{"image":"ubuntu:22.04"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	h.instanceID = collectionOwner
	images := []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew), collectionImage("b", collectionOwner, devcontainer.ImageKindCrew)}
	// Older bundles contain cached_image but not the new retention fields.
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, images[0].ID, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE crews SET last_selected_image=NULL,previous_image=NULL WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	r := revisionFixture()
	r.CachedImage = images[1].ID
	r.Requirements.Toolchain.ImageID = r.CachedImage
	if _, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, r); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGCClient{images: images}
	h.gcClient = fake
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) > 0 {
		t.Fatal("legacy restored selection lost on first publication")
	}
}

func TestCollectionSkipsMissingReferencesAndInvalidCrewConfig(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	candidate := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	fake := &fakeGCClient{images: []image.Summary{candidate}, containers: []container.Summary{{ID: "foreign-builder", ImageID: "sha256:" + strings.Repeat("b", 64), Names: tempNames("foreign-builder")}}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	user := seedTestUser(t, h.db)
	ws := seedTestWorkspace(t, h.db, user)
	crew := seedCrewRow(t, h.db, "stale", ws, "Stale", "stale")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image='crewship-cache:gone',devcontainer_config='{broken' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 || fake.removedImages[0] != candidate.ID {
		t.Fatalf("one stale crew/build blocked unrelated collection: %v", fake.removedImages)
	}
}

func TestCollectionDoesNotWaitForUnrelatedBuild(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	busy := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	unused := collectionImage("b", collectionOwner, devcontainer.ImageKindCrew)
	fake := &fakeGCClient{images: []image.Summary{busy, unused}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	release, err := resourcelifecycle.Images(h.db).UseImages(t.Context(), busy.RepoTags[0])
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 || fake.removedImages[0] != unused.ID {
		t.Fatalf("unrelated build blocked collection: %v", fake.removedImages)
	}
}

func TestOwnedCollectionIsOffByDefaultAndLegacyOptInHasExplicitPrecedence(t *testing.T) {
	t.Setenv(imageGCEnv, "")
	_ = os.Unsetenv(imageGCEnv)
	fake := &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}
	h := newGCTestHandler(t, fake)
	h.instanceID = collectionOwner
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("owned deletion enabled by default")
	}
	h.SetLegacyImageEviction(true)
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 1 {
		t.Fatal("deprecated explicit opt-in silently ignored")
	}
	fake.removedImages = nil
	t.Setenv(imageGCEnv, "false")
	h.sweepOrphanCacheImages(t.Context())
	if len(fake.removedImages) != 0 {
		t.Fatal("explicit disabled setting lost precedence")
	}
}

type slowCollectionInventory struct {
	*fakeGCClient
	entered, proceed chan struct{}
	once             sync.Once
}

func (f *slowCollectionInventory) ImageList(ctx context.Context, options client.ImageListOptions) (client.ImageListResult, error) {
	f.once.Do(func() {
		close(f.entered)
		select {
		case <-f.proceed:
		case <-ctx.Done():
		}
	})
	return f.fakeGCClient.ImageList(ctx, options)
}
func TestSlowInventoryDoesNotBlockCrewEdits(t *testing.T) {
	t.Setenv(imageGCEnv, "true")
	fake := &slowCollectionInventory{fakeGCClient: &fakeGCClient{images: []image.Summary{collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)}}, entered: make(chan struct{}), proceed: make(chan struct{})}
	h := newGCTestHandler(t, fake.fakeGCClient)
	h.instanceID = collectionOwner
	h.gcClient = fake
	done := make(chan struct{})
	go func() { defer close(done); h.sweepOrphanCacheImages(t.Context()) }()
	var unblock sync.Once
	releaseInventory := func() { unblock.Do(func() { close(fake.proceed) }) }
	t.Cleanup(releaseInventory)
	select {
	case <-fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("inventory not reached")
	}
	edited := make(chan struct{})
	go func() { release := resourcelifecycle.Images(h.db).Use(); defer release(); close(edited) }()
	select {
	case <-edited:
	case <-time.After(3 * time.Second):
		t.Fatal("slow inventory blocks crew edit")
	}
	releaseInventory()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not finish")
	}
}

func TestProviderOutageDoesNotInvalidateSelectedEnvironment(t *testing.T) {
	h, ws, crew := covProvRig(t, &covCommitClient{}, `{"image":"synthetic/base:1"}`)
	id := "sha256:" + strings.Repeat("a", 64)
	inspections := 0
	docker, _ := covCPRFakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		inspections++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"temporary daemon outage"}`))
	})
	h.docker = docker
	h.SetInstallationID(collectionOwner)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, id, crew); err != nil {
		t.Fatal(err)
	}
	if cfg, err := buildCrewRuntimeConfig(t.Context(), h.db, crew, ws); err != nil || cfg.CachedImage != id {
		t.Fatalf("start config failed on transient outage: cfg=%+v err=%v", cfg, err)
	}
	if inspections != 0 {
		t.Fatal("configuration assembly unexpectedly probed Docker")
	}
	if !h.imagePresentLocally(t.Context(), id) {
		t.Fatal("transient outage requests a rebuild")
	}
	if inspections != 1 {
		t.Fatalf("expected one presence probe, got %d", inspections)
	}
}

func TestManualCacheDeletionExcludesImmutableBuildBase(t *testing.T) {
	artifact := collectionImage("a", collectionOwner, devcontainer.ImageKindCrew)
	fake := &fakeGCClient{images: []image.Summary{artifact}}
	h := newCacheTestHandler(t, fake)
	release, err := resourcelifecycle.Images(h.db).UseImages(t.Context(), artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("tag", artifact.RepoTags[0])
	req = withWorkspaceUser(req, "user", "workspace", "OWNER")
	out := httptest.NewRecorder()
	h.CacheDelete(out, req)
	if out.Code != http.StatusConflict || len(fake.removedImages) != 0 {
		t.Fatalf("deleted active immutable base: status=%d removed=%v", out.Code, fake.removedImages)
	}
}
