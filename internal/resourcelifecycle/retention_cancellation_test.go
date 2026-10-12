package resourcelifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type canceledRetentionProbe struct {
	*fakeRetentionRuntime
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (p *canceledRetentionProbe) ListContainers(context.Context) ([]RetentionContainer, error) {
	p.calls.Add(1)
	if p.cancel != nil {
		// Virtual time makes the ticker ready before cancellation without
		// depending on host scheduling or real millisecond timers.
		time.Sleep(2 * time.Second)
		p.cancel()
	}
	return nil, errors.New("daemon unavailable")
}

func TestRetentionAlreadyCanceledDoesNotStartProviderWork(t *testing.T) {
	for _, run := range []bool{false, true} {
		name := "Tick"
		if run {
			name = "Run"
		}
		t.Run(name, func(t *testing.T) {
			retention, runtime, _ := retentionFixture(t)
			probe := &canceledRetentionProbe{fakeRetentionRuntime: runtime}
			retention.Runtime = probe
			retention.EvictCache = false
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if run {
				retention.Run(ctx)
			} else {
				retention.Tick(ctx)
			}
			if got := probe.calls.Load(); got != 0 {
				t.Fatalf("canceled %s started %d provider calls", name, got)
			}
		})
	}
}

func TestRetentionReadyTickCannotRestartCanceledPass(t *testing.T) {
	retention, runtime, _ := retentionFixture(t)
	synctest.Test(t, func(t *testing.T) {
		// Both select cases are ready. Repeat so an unfixed select cannot
		// routinely escape the regression by choosing cancellation first.
		for attempt := 0; attempt < 64; attempt++ {
			ctx, cancel := context.WithCancel(t.Context())
			probe := &canceledRetentionProbe{fakeRetentionRuntime: runtime, cancel: cancel}
			retention.Runtime = probe
			retention.EvictCache = false
			retention.Interval = time.Second
			retention.Run(ctx)
			cancel()
			if got := probe.calls.Load(); got != 1 {
				t.Fatalf("attempt %d: ready tick admitted another pass after cancellation: %d calls", attempt, got)
			}
		}
	})
}

type cancelDuringRetentionProbe struct {
	*fakeRetentionRuntime
	cancel     context.CancelFunc
	inspected  int
	imageLists int
	removals   int
}

func (p *cancelDuringRetentionProbe) InspectContainer(ctx context.Context, id string) (RetentionContainer, error) {
	p.inspected++
	p.cancel()
	return p.fakeRetentionRuntime.InspectContainer(ctx, id)
}

func (p *cancelDuringRetentionProbe) ListCacheImages(ctx context.Context) ([]CacheImage, error) {
	p.imageLists++
	return p.fakeRetentionRuntime.ListCacheImages(ctx)
}

func (p *cancelDuringRetentionProbe) RemoveImage(context.Context, string) error {
	p.removals++
	p.cancel()
	return nil
}

func TestRetentionCancellationStopsRemainingItems(t *testing.T) {
	t.Run("runtimes and next phase", func(t *testing.T) {
		r, runtime, _ := retentionFixture(t)
		runtime.containers["a"] = runtimeContainer("a", "live", r.InstanceID, "exited", week)
		runtime.containers["b"] = runtimeContainer("b", "live", r.InstanceID, "exited", week)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		probe := &cancelDuringRetentionProbe{fakeRetentionRuntime: runtime, cancel: cancel}
		r.Runtime = probe
		r.Tick(ctx)
		if probe.inspected != 1 || probe.imageLists != 0 {
			t.Fatalf("continued after cancellation: inspected=%d imageLists=%d", probe.inspected, probe.imageLists)
		}
	})
	t.Run("cache images", func(t *testing.T) {
		r, runtime, now := retentionFixture(t)
		r.RuntimeAfter = 0
		runtime.images = []CacheImage{{ID: "a", Created: now.Add(-week)}, {ID: "b", Created: now.Add(-week)}}
		if _, err := r.DB.Exec(`INSERT INTO resource_retention_images(instance_id,image_id,first_unused_at) VALUES(?,?,?),(?,?,?)`, r.InstanceID, "a", now.Add(-week).Format(time.RFC3339), r.InstanceID, "b", now.Add(-week).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		probe := &cancelDuringRetentionProbe{fakeRetentionRuntime: runtime, cancel: cancel}
		r.Runtime = probe
		r.Tick(ctx)
		if probe.removals != 1 {
			t.Fatalf("continued removals after cancellation: %d", probe.removals)
		}
	})
}
