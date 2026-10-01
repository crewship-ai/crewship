package resourcelifecycle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

func TestIdleRetentionDefersWhileBackupWindowIsHeld(t *testing.T) {
	r, runtime, now := retentionFixture(t)
	runtime.containers["idle"] = runtimeContainer("idle", "live", r.InstanceID, "exited", 2*week)
	runtime.images = []CacheImage{{ID: "unused", Refs: []string{"crewship-cache:unused"}, Created: now.Add(-2 * 24 * time.Hour)}}
	w, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Release() })
	r.Tick(context.Background())
	if len(runtime.removed) != 0 || len(runtime.untagged) != 0 {
		t.Fatal("idle retention mutated Docker while the backup window was held")
	}
	var observations int
	if err := r.DB.QueryRow(`SELECT COUNT(*) FROM resource_retention_images`).Scan(&observations); err != nil || observations != 0 {
		t.Fatalf("retention wrote observations while held: count=%d error=%v", observations, err)
	}
	w.Release()
	r.Tick(context.Background())
	if len(runtime.removed) != 1 {
		t.Fatal("idle retention did not resume after the backup window was released")
	}
}

func TestCleanupDefersWhileBackupWindowIsHeld(t *testing.T) {
	c, rt := fixture(t)
	rt.items["runtime"] = item("runtime", "deleted", c.InstanceID)
	w, err := quiesce.Default().Begin(context.Background(), quiesce.Options{HoldCap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Release()
	c.Tick(context.Background())
	if len(rt.calls) != 0 {
		t.Fatalf("cleanup mutated Docker inside the consistent copy window: %v", rt.calls)
	}
	var scans int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM resource_cleanup_scans").Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 0 {
		t.Fatal("cleanup wrote diagnostics inside the consistent copy window")
	}
	w.Release()
	c.Tick(context.Background())
	if _, remains := rt.items["runtime"]; remains {
		t.Fatal("cleanup did not resume after the backup window released")
	}
}

type cleanupStopBarrier struct {
	Runtime
	entered chan struct{}
	release chan struct{}
}

func (rt *cleanupStopBarrier) Stop(ctx context.Context, id string) error {
	close(rt.entered)
	<-rt.release
	return rt.Runtime.Stop(ctx, id)
}

func TestBackupWindowDrainsCleanupAlreadyStoppingContainer(t *testing.T) {
	c, rt := fixture(t)
	rt.items["runtime"] = item("runtime", "deleted", c.InstanceID)
	blocked := &cleanupStopBarrier{Runtime: rt, entered: make(chan struct{}), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(blocked.release) })
	defer unblock()
	c.Connect = func(context.Context) (Runtime, error) { return blocked, nil }
	tickDone := make(chan struct{})
	go func() { defer close(tickDone); c.Tick(context.Background()) }()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not reach stop")
	}
	type result struct {
		window *quiesce.Window
		err    error
	}
	begun := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		w, err := quiesce.Default().Begin(ctx, quiesce.Options{HoldCap: time.Minute, DrainTimeout: 2 * time.Second})
		begun <- result{w, err}
	}()
	deadline := time.Now().Add(time.Second)
	for !quiesce.Default().Holding() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !quiesce.Default().Holding() {
		t.Error("backup did not close writer admission")
	}
	if quiesce.Default().Held() {
		t.Error("backup began its copy while cleanup was still stopping a container")
	}
	unblock()
	<-tickDone
	r := <-begun
	if r.window != nil {
		defer r.window.Release()
	}
	if r.err != nil {
		t.Fatalf("backup failed after cleanup drained: %v", r.err)
	}
	if !quiesce.Default().Held() {
		t.Fatal("backup did not hold after cleanup drained")
	}
}
