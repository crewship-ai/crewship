package resourcelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// wait is how long a test waits for something that must happen, and settle
// how long it waits to conclude that something did not.
const (
	wait   = 5 * time.Second
	settle = 50 * time.Millisecond
)

func mustUse(t *testing.T, g *ImageUse, refs ...string) func() {
	t.Helper()
	release, err := g.UseImages(context.Background(), refs...)
	if err != nil {
		t.Fatalf("UseImages(%v): %v", refs, err)
	}
	return release
}

// A build holding an image keeps the collector off it, for as long as any
// holder remains, and only for the references it named.
func TestImageUse_LeaseExcludesCollectionOfTheSameReference(t *testing.T) {
	g := &ImageUse{}
	first := mustUse(t, g, "base", "tag")
	second := mustUse(t, g, "tag")

	for _, refs := range [][]string{{"tag"}, {"base"}, {"other", "tag"}} {
		if _, ok := g.TryCollectImages(refs...); ok {
			t.Fatalf("collection of %v allowed while a lease is held", refs)
		}
	}
	releaseOther, ok := g.TryCollectImages("other")
	if !ok {
		t.Fatal("an unrelated image must stay collectable")
	}
	releaseOther()

	first()
	if _, ok := g.TryCollectImages("tag"); ok {
		t.Fatal("collection allowed while the second holder remains")
	}
	releaseBase, ok := g.TryCollectImages("base")
	if !ok {
		t.Fatal("base has no holder left and must be collectable")
	}
	releaseBase()

	second()
	releaseTag, ok := g.TryCollectImages("tag")
	if !ok {
		t.Fatal("tag has no holder left and must be collectable")
	}
	releaseTag()
}

// Releasing twice must not free a lease another holder still owns.
func TestImageUse_ReleaseIsIdempotent(t *testing.T) {
	g := &ImageUse{}
	first := mustUse(t, g, "tag")
	second := mustUse(t, g, "tag")
	first()
	first()
	if _, ok := g.TryCollectImages("tag"); ok {
		t.Fatal("a repeated release freed the second holder's lease")
	}
	second()
	release, ok := g.TryCollectImages("tag")
	if !ok {
		t.Fatal("collectable once every holder released")
	}
	release()
	release() // a collector's release is idempotent too
	if _, ok := g.TryCollectImages("tag"); !ok {
		t.Fatal("a repeated collector release left the reference marked as deleting")
	}
}

// While the collector removes an image, a build of the same reference waits
// and then proceeds; it never observes the image half-removed.
func TestImageUse_BuildWaitsForARemovalInProgress(t *testing.T) {
	g := &ImageUse{}
	finishRemoval, ok := g.TryCollectImages("id", "tag")
	if !ok {
		t.Fatal("nothing holds the image")
	}
	if _, again := g.TryCollectImages("tag"); again {
		t.Fatal("two removals of one reference at once")
	}

	acquired := make(chan func(), 1)
	go func() {
		release, err := g.UseImages(context.Background(), "base", "tag")
		if err != nil {
			t.Errorf("UseImages: %v", err)
			return
		}
		acquired <- release
	}()
	select {
	case <-acquired:
		t.Fatal("a build acquired an image that is being removed")
	case <-time.After(settle):
	}
	// The waiting build must not have taken a lease on its other reference.
	releaseBase, ok := g.TryCollectImages("base")
	if !ok {
		t.Fatal("a waiting build holds a partial lease")
	}
	releaseBase()

	finishRemoval()
	select {
	case release := <-acquired:
		if _, ok := g.TryCollectImages("tag"); ok {
			t.Fatal("collection allowed while the build that waited holds the image")
		}
		release()
	case <-time.After(wait):
		t.Fatal("the build never proceeded after the removal finished")
	}
}

// A build that gives up while waiting returns its context's error and leaves
// no lease behind.
func TestImageUse_WaitingBuildIsCancellable(t *testing.T) {
	g := &ImageUse{}
	finishRemoval, ok := g.TryCollectImages("tag")
	if !ok {
		t.Fatal("nothing holds the image")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, err := g.UseImages(ctx, "tag")
		if release != nil {
			release()
		}
		done <- err
	}()
	time.Sleep(settle)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UseImages error = %v, want context.Canceled", err)
		}
	case <-time.After(wait):
		t.Fatal("a cancelled build kept waiting")
	}
	finishRemoval()
	release, ok := g.TryCollectImages("tag")
	if !ok {
		t.Fatal("a cancelled build left a lease behind")
	}
	release()

	// An already-cancelled context never acquires.
	if release, err := g.UseImages(ctx, "tag"); err == nil || release != nil {
		t.Fatalf("UseImages on a cancelled context = %v, release set: %t", err, release != nil)
	}
}

// The empty reference names no image: it neither holds nor blocks anything.
func TestImageUse_EmptyReferenceIsIgnored(t *testing.T) {
	g := &ImageUse{}
	release := mustUse(t, g, "", "tag")
	other := mustUse(t, g, "")
	defer release()
	defer other()
	releaseEmpty, ok := g.TryCollectImages("")
	if !ok {
		t.Fatal("the empty reference blocked a collection")
	}
	releaseEmpty()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, err := g.UseImages(context.Background(), "")
		if err == nil {
			r()
		}
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("the empty reference made a build wait")
	}
}

// Crew edits and restores share the gate; the collector takes it only when
// nobody is inside, and never waits for them.
func TestImageUse_ExclusiveGateIsTryOnly(t *testing.T) {
	g := &ImageUse{}
	leaveA := g.Use()
	leaveB := g.Use() // shared
	if _, ok := g.TryCollect(); ok {
		t.Fatal("exclusive gate taken while an edit is inside")
	}
	leaveA()
	if _, ok := g.TryCollect(); ok {
		t.Fatal("exclusive gate taken while the second edit is inside")
	}
	leaveB()
	release, ok := g.TryCollect()
	if !ok {
		t.Fatal("exclusive gate refused with nobody inside")
	}
	if _, again := g.TryCollect(); again {
		t.Fatal("two exclusive holders")
	}
	entered := make(chan func(), 1)
	go func() { entered <- g.Use() }()
	select {
	case <-entered:
		t.Fatal("an edit entered while the collector holds the gate")
	case <-time.After(settle):
	}
	release()
	select {
	case leave := <-entered:
		leave()
	case <-time.After(wait):
		t.Fatal("the edit never entered after the collector left")
	}
	// Per-image leases are independent of the exclusive gate: a build is not
	// held back by an edit, and an edit is not held back by a build.
	lease := mustUse(t, g, "tag")
	leave := g.Use()
	leave()
	lease()
}

// One gate per opened database: every user of a database handle meets the
// same gate, and two databases never share one.
func TestImages_OneGatePerDatabaseHandle(t *testing.T) {
	a, b := new(sql.DB), new(sql.DB)
	first, again := Images(a), Images(a)
	if first != again {
		t.Fatal("one database handle produced two gates")
	}
	if first == Images(b) {
		t.Fatal("two database handles share a gate")
	}
	release := mustUse(t, Images(a), "tag")
	defer release()
	other, ok := Images(b).TryCollectImages("tag")
	if !ok {
		t.Fatal("a lease in one database blocked another database's collector")
	}
	other()
}
