package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

// crew_provisioning_gc_cov_test.go covers the remaining sweeper
// branches: the startup+ctx-cancel path of runStartupAndPeriodicGC,
// the sweep cap, container-remove failures, the nil-client and DB/image
// error guards of the cache sweeper, and the autodelete remove-failure
// path. It uses its own fake (covGCFake) because the established
// fakeGCClient has no remove-error knobs and existing test files must
// not be modified.

type covGCFake struct {
	mu                 sync.Mutex
	containers         []container.Summary
	images             []image.Summary
	containerListErr   error
	imageListErr       error
	containerRemoveErr error
	imageRemoveErr     error

	removedContainers []string
	removedImages     []string
}

func (f *covGCFake) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	if f.containerListErr != nil {
		return nil, f.containerListErr
	}
	return f.containers, nil
}

func (f *covGCFake) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.containerRemoveErr != nil {
		return f.containerRemoveErr
	}
	f.removedContainers = append(f.removedContainers, id)
	return nil
}

func (f *covGCFake) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	if f.imageListErr != nil {
		return nil, f.imageListErr
	}
	return f.images, nil
}

func (f *covGCFake) ImageRemove(_ context.Context, id string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.imageRemoveErr != nil {
		return nil, f.imageRemoveErr
	}
	f.removedImages = append(f.removedImages, id)
	return nil, nil
}

func covGCStaleContainer(id string) container.Summary {
	return container.Summary{
		ID:      id,
		Created: time.Now().Add(-2 * tempContainerMaxAge).Unix(),
		Labels: map[string]string{
			devcontainer.TempContainerLabelKey: devcontainer.TempContainerLabelValue,
		},
	}
}

func covGCHandler(t *testing.T, fake *covGCFake) *ProvisioningHandler {
	t.Helper()
	return &ProvisioningHandler{
		db:       setupTestDB(t),
		logger:   newTestLogger(),
		gcClient: fake,
	}
}

// TestCovGC_RunStartupAndPeriodicGC_StartupSweepThenCtxCancel runs the
// GC loop with an already-cancelled context: the startup sweep must
// still execute (one stale container removed) and the loop must return
// immediately on ctx.Done instead of waiting for the 30-minute tick.
func TestCovGC_RunStartupAndPeriodicGC_StartupSweepThenCtxCancel(t *testing.T) {
	fake := &covGCFake{containers: []container.Summary{covGCStaleContainer("covgc-stale-1")}}
	h := covGCHandler(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: loop must exit right after the startup sweep

	done := make(chan struct{})
	go func() {
		h.runStartupAndPeriodicGC(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runStartupAndPeriodicGC did not return on cancelled context")
	}
	if len(fake.removedContainers) != 1 || fake.removedContainers[0] != "covgc-stale-1" {
		t.Errorf("removedContainers = %v, want [covgc-stale-1] from startup sweep", fake.removedContainers)
	}
}

// TestCovGC_SweepTempContainers_CapStopsScan seeds more stale temp
// containers than the sweep cap; only the first orphanGCSweepCap may
// be removed.
func TestCovGC_SweepTempContainers_CapStopsScan(t *testing.T) {
	fake := &covGCFake{}
	for i := 0; i < orphanGCSweepCap+5; i++ {
		fake.containers = append(fake.containers, covGCStaleContainer(fmt.Sprintf("covgc-c%d", i)))
	}
	h := covGCHandler(t, fake)
	h.sweepOrphanTempContainers(context.Background())
	if len(fake.removedContainers) != orphanGCSweepCap {
		t.Errorf("removed = %d, want exactly cap %d", len(fake.removedContainers), orphanGCSweepCap)
	}
}

// TestCovGC_SweepTempContainers_RemoveErrorContinues — a failing
// ContainerRemove is logged and skipped, not fatal.
func TestCovGC_SweepTempContainers_RemoveErrorContinues(t *testing.T) {
	fake := &covGCFake{
		containers:         []container.Summary{covGCStaleContainer("covgc-err")},
		containerRemoveErr: errors.New("daemon says no"),
	}
	h := covGCHandler(t, fake)
	h.sweepOrphanTempContainers(context.Background())
	if len(fake.removedContainers) != 0 {
		t.Errorf("removedContainers = %v, want none (remove always errors)", fake.removedContainers)
	}
}

func TestCovGC_SweepCacheImages_NilClientNoOp(t *testing.T) {
	h := &ProvisioningHandler{db: setupTestDB(t), logger: newTestLogger(), gcClient: nil}
	// Must return without panicking and without touching the DB.
	h.sweepOrphanCacheImages(context.Background())
}

func TestCovGC_SweepCacheImages_DBQueryErrorAborts(t *testing.T) {
	fake := &covGCFake{images: []image.Summary{{
		RepoTags: []string{cacheImagePrefix + "orphan"},
		Created:  time.Now().Add(-time.Hour).Unix(),
	}}}
	h := covGCHandler(t, fake)
	h.db.Close()
	h.sweepOrphanCacheImages(context.Background())
	if len(fake.removedImages) != 0 {
		t.Errorf("removedImages = %v, want none (DB query failed, sweep must abort)", fake.removedImages)
	}
}

func TestCovGC_SweepCacheImages_ImageListErrorAborts(t *testing.T) {
	fake := &covGCFake{imageListErr: errors.New("daemon gone")}
	h := covGCHandler(t, fake)
	h.sweepOrphanCacheImages(context.Background())
	if len(fake.removedImages) != 0 {
		t.Errorf("removedImages = %v, want none", fake.removedImages)
	}
}

// TestCovGC_SweepCacheImages_AutoDeleteRemoveError — autodelete on, but
// ImageRemove fails: the sweeper logs and continues without crediting
// a removal.
func TestCovGC_SweepCacheImages_AutoDeleteRemoveError(t *testing.T) {
	t.Setenv(cacheGCAutoDeleteEnv, "true")
	fake := &covGCFake{
		images: []image.Summary{{
			RepoTags: []string{cacheImagePrefix + "covgc-orphan"},
			Created:  time.Now().Add(-time.Hour).Unix(),
		}},
		imageRemoveErr: errors.New("image in use"),
	}
	h := covGCHandler(t, fake)
	h.sweepOrphanCacheImages(context.Background())
	if len(fake.removedImages) != 0 {
		t.Errorf("removedImages = %v, want none (remove errored)", fake.removedImages)
	}
}
