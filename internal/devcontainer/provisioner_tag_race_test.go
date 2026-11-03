package devcontainer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// raceCommitClient is a mockCommitClient that is safe under concurrent
// Provision calls and behaves like the containerd-snapshotter image store on
// the stage host (Docker 29.3): committing to a tag that another commit has
// just created fails with AlreadyExists instead of retagging.
//
// Its commit also waits until a second temp container exists (or
// barrierWait passes), so without serialisation both callers are inside the
// build when the first commit lands — the stage interleaving of #4816,
// produced deterministically instead of by luck.
type raceCommitClient struct {
	mu          sync.Mutex
	inner       *mockCommitClient
	creates     int
	createdCh   chan struct{} // closed when the second create arrives
	barrierWait time.Duration
	// allowRetag lets a commit move an existing tag (a sequential forced
	// rebuild); overlap is then measured through maxActive instead.
	allowRetag bool
	active     int // temp containers created and not yet removed
	maxActive  int
}

func newRaceCommitClient(existing ...string) *raceCommitClient {
	return &raceCommitClient{
		inner:       &mockCommitClient{existingImages: existing},
		createdCh:   make(chan struct{}),
		barrierWait: time.Second,
	}
}

func (r *raceCommitClient) ContainerCreate(ctx context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	if r.creates == 2 {
		close(r.createdCh)
	}
	r.active++
	r.maxActive = max(r.maxActive, r.active)
	return r.inner.ContainerCreate(ctx, o)
}

func (r *raceCommitClient) ContainerStart(ctx context.Context, id string, o client.ContainerStartOptions) (client.ContainerStartResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.ContainerStart(ctx, id, o)
}

func (r *raceCommitClient) ContainerStop(ctx context.Context, id string, o client.ContainerStopOptions) (client.ContainerStopResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.ContainerStop(ctx, id, o)
}

func (r *raceCommitClient) ContainerRemove(ctx context.Context, id string, o client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
	return r.inner.ContainerRemove(ctx, id, o)
}

func (r *raceCommitClient) ContainerCommit(ctx context.Context, id string, o client.ContainerCommitOptions) (client.ContainerCommitResult, error) {
	select {
	case <-r.createdCh:
	case <-time.After(r.barrierWait):
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tag := range r.inner.existingImages {
		if tag == o.Reference && !r.allowRetag {
			r.inner.commitRefs = append(r.inner.commitRefs, o.Reference)
			return client.ContainerCommitResult{}, fmt.Errorf(
				"Error response from daemon: failed to create an image docker.io/library/%s after deleting the existing one: %w",
				o.Reference, cerrdefs.ErrAlreadyExists)
		}
	}
	res, err := r.inner.ContainerCommit(ctx, id, o)
	if err == nil {
		r.inner.existingImages = append(r.inner.existingImages, o.Reference)
	}
	return res, err
}

func (r *raceCommitClient) ImageList(ctx context.Context, o client.ImageListOptions) (client.ImageListResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.ImageList(ctx, o)
}

func (r *raceCommitClient) ImagePull(ctx context.Context, ref string, o client.ImagePullOptions) (client.ImagePullResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.ImagePull(ctx, ref, o)
}

func (r *raceCommitClient) ImageInspect(ctx context.Context, ref string, o ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.ImageInspect(ctx, ref, o...)
}

func (r *raceCommitClient) commitsFor(tag string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ref := range r.inner.commitRefs {
		if ref == tag {
			n++
		}
	}
	return n
}

// lockedExecClient serialises the installer's mockDockerClient, which records
// calls without a lock of its own.
type lockedExecClient struct {
	mu    sync.Mutex
	inner *mockDockerClient
}

func (l *lockedExecClient) ExecCreate(ctx context.Context, id string, o client.ExecCreateOptions) (client.ExecCreateResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.ExecCreate(ctx, id, o)
}

func (l *lockedExecClient) ExecAttach(ctx context.Context, id string, o client.ExecAttachOptions) (client.ExecAttachResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.ExecAttach(ctx, id, o)
}

func (l *lockedExecClient) ExecInspect(ctx context.Context, id string, o client.ExecInspectOptions) (client.ExecInspectResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.ExecInspect(ctx, id, o)
}

func (l *lockedExecClient) CopyToContainer(ctx context.Context, id string, o client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.CopyToContainer(ctx, id, o)
}

// runConcurrentProvisions starts two Provision calls for the same config at
// once and returns each caller's error and emitted steps.
func runConcurrentProvisions(t *testing.T, p *Provisioner, cfg *Config, opts ...ProvisionOption) ([2]error, [2][]string) {
	t.Helper()
	var (
		errs  [2]error
		steps [2][]string
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sink := func(ev ProvisionEvent) { steps[i] = append(steps[i], ev.Step) }
			<-start
			_, errs[i] = p.Provision(context.Background(), offlineBaseImage, cfg, "",
				append([]ProvisionOption{WithProvisionSink(sink)}, opts...)...)
		}()
	}
	close(start)
	wg.Wait()
	return errs, steps
}

func containsStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}

// TestProvision_ConcurrentSameConfigCommitsTagOnce is the #4816 regression:
// two crews with identical requirements provisioned at once both missed the
// cache, both built, and both committed to crewship-cache:<hash>; the
// containerd image store rejected the second commit with AlreadyExists and
// the stage reseed failed. Provisions of one tag are now serialised, and the
// second re-checks the cache once it holds the tag, so it is a cache hit.
func TestProvision_ConcurrentSameConfigCommitsTagOnce(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	tag := cacheImageTag(configHash(offlineBaseImage, cfg, "", dockerfileGenFingerprint(offlineBaseImage, cfg)))

	docker := newRaceCommitClient(offlineBaseImage)
	inst := NewInstaller(&lockedExecClient{inner: &mockDockerClient{exitCode: 0}}, testLogger())
	p := NewProvisioner(docker, inst, nil, testLogger())

	errs, steps := runConcurrentProvisions(t, p, cfg)
	for i, err := range errs {
		if err != nil {
			t.Errorf("provision %d: %v", i, err)
		}
	}
	if n := docker.commitsFor(tag); n != 1 {
		t.Errorf("commits to %s = %d, want exactly 1", tag, n)
	}
	hits := 0
	for _, s := range steps {
		if containsStep(s, ProvStepCacheHit) {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("cache hits = %d, want 1 (the second provision must reuse the first one's image); steps=%v", hits, steps)
	}
}

// TestProvision_ConcurrentForceRebuildStillRebuildsSerially: a forced rebuild
// keeps its meaning — both callers rebuild — but the two builds of one tag no
// longer overlap.
func TestProvision_ConcurrentForceRebuildStillRebuildsSerially(t *testing.T) {
	cfg := &Config{Image: offlineBaseImage, ContainerEnv: map[string]string{"FOO": "bar"}}
	tag := cacheImageTag(configHash(offlineBaseImage, cfg, "", dockerfileGenFingerprint(offlineBaseImage, cfg)))

	docker := newRaceCommitClient(offlineBaseImage)
	docker.allowRetag = true
	docker.barrierWait = 200 * time.Millisecond
	inst := NewInstaller(&lockedExecClient{inner: &mockDockerClient{exitCode: 0}}, testLogger())
	p := NewProvisioner(docker, inst, nil, testLogger())

	errs, steps := runConcurrentProvisions(t, p, cfg, WithForceRebuild(true))
	for i, err := range errs {
		if err != nil {
			t.Errorf("provision %d: %v", i, err)
		}
	}
	if n := docker.commitsFor(tag); n != 2 {
		t.Errorf("commits to %s = %d, want 2 (force rebuild must still rebuild)", tag, n)
	}
	for i, s := range steps {
		if containsStep(s, ProvStepCacheHit) {
			t.Errorf("provision %d reported a cache hit under force rebuild", i)
		}
	}
	docker.mu.Lock()
	overlap := docker.maxActive
	docker.mu.Unlock()
	if overlap != 1 {
		t.Errorf("concurrent builds of %s = %d, want 1 (serialised)", tag, overlap)
	}
}
