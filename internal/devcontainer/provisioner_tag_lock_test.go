package devcontainer

import (
	"context"
	"testing"
	"time"
)

// TestLockCacheTag_ScopedPerTagAndCancellable: a different tag is not held up
// by a busy one, and a waiter gives up when its context ends.
func TestLockCacheTag_ScopedPerTagAndCancellable(t *testing.T) {
	p := NewProvisioner(nil, nil, nil, testLogger())

	unlockA, err := p.lockCacheTag(context.Background(), "crewship-cache:a")
	if err != nil {
		t.Fatal(err)
	}
	unlockB, err := p.lockCacheTag(context.Background(), "crewship-cache:b")
	if err != nil {
		t.Fatalf("other tag blocked by a held one: %v", err)
	}
	unlockB()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.lockCacheTag(ctx, "crewship-cache:a"); err == nil {
		t.Fatal("second lock on a held tag returned without waiting")
	}

	unlockA()
	unlockA2, err := p.lockCacheTag(context.Background(), "crewship-cache:a")
	if err != nil {
		t.Fatalf("tag not released: %v", err)
	}
	unlockA2()

	p.cacheTagMu.Lock()
	left := len(p.cacheTagLocks)
	p.cacheTagMu.Unlock()
	if left != 0 {
		t.Errorf("%d tag locks left after release, want 0", left)
	}
}
