//go:build crewship_testbarrier && unix

package appengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// activateBarrier configures a private barrier directory for one test and
// deactivates it afterwards. It returns the directory.
func activateBarrier(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TestBarrierDirEnv, dir)
	t.Setenv(TestBarrierSlotEnv, "dev2")
	if active, err := ConfigureTestBarrier(); !active || err != nil {
		t.Fatalf("barrier not active: %v", err)
	}
	t.Cleanup(func() {
		barrierState.mu.Lock()
		barrierState.dir = ""
		barrierState.mu.Unlock()
	})
	return dir
}

func barrierBase(dir string, k Key, authority, mutation string, point barrierPoint) string {
	sum := sha256.Sum256([]byte(k.PageID + "|" + authority + "|" + mutation))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+"."+string(point))
}

// waitParked waits until the operation's .parked file exists and returns the
// .release path that unparks it.
func waitParked(t *testing.T, dir string, k Key, authority, mutation string, point barrierPoint) string {
	t.Helper()
	base := barrierBase(dir, k, authority, mutation, point)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(base + ".parked"); err == nil {
			return base + ".release"
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never parked at %s", mutation, point)
	return ""
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func noFiles(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("barrier left files: %v %v", entries, err)
	}
}

var barrierKey = Key{"ws", "page"}

func TestTestBarrierCompiledInOnlyWithTag(t *testing.T) {
	if !barrierCompiledIn {
		t.Fatal("tagged build without barrier")
	}
}

func TestTestBarrierStartupRefusals(t *testing.T) {
	good := t.TempDir()
	if err := os.Chmod(good, 0o700); err != nil {
		t.Fatal(err)
	}
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir, slot string
		active, fails   bool
	}{
		{"unset stays off", "", "stage", false, false},
		{"dev slot", good, "dev2", true, false},
		{"stage refused", good, "stage", false, true},
		{"prod refused", good, "prod", false, true},
		{"missing slot refused", good, "", false, true},
		{"unknown slot refused", good, "dev5", false, true},
		{"relative dir refused", "barrier", "dev2", false, true},
		{"group-readable dir refused", open, "dev2", false, true},
		{"symlink refused", link, "dev2", false, true},
		{"missing dir refused", filepath.Join(good, "absent"), "dev2", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(TestBarrierDirEnv, tc.dir)
			t.Setenv(TestBarrierSlotEnv, tc.slot)
			t.Cleanup(func() { barrierState.mu.Lock(); barrierState.dir = ""; barrierState.mu.Unlock() })
			active, err := ConfigureTestBarrier()
			if active != tc.active || (err != nil) != tc.fails || TestBarrierActive() != tc.active {
				t.Fatalf("active=%v err=%v", active, err)
			}
		})
	}
}

func TestTestBarrierParkAndRelease(t *testing.T) {
	dir := activateBarrier(t)
	done := make(chan error, 1)
	go func() { done <- testBarrier(t.Context(), barrierPreCommit, barrierKey, "user:owner", "barrier-1") }()
	release := waitParked(t, dir, barrierKey, "user:owner", "barrier-1", barrierPreCommit)
	select {
	case err := <-done:
		t.Fatalf("returned before release: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	touch(t, release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	noFiles(t, dir)
}

func TestTestBarrierHardCap(t *testing.T) {
	if barrierMaxPark != 45*time.Second {
		t.Fatalf("cap=%v", barrierMaxPark)
	}
	dir := activateBarrier(t)
	barrierState.mu.Lock()
	barrierState.cap = 300 * time.Millisecond
	barrierState.mu.Unlock()
	started := time.Now()
	if err := testBarrier(t.Context(), barrierPreCommit, barrierKey, "user:owner", "barrier-cap"); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited < 300*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("waited %v", waited)
	}
	noFiles(t, dir)
}

func TestTestBarrierCancellationEndsPark(t *testing.T) {
	dir := activateBarrier(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- testBarrier(ctx, barrierPostCommit, barrierKey, "user:owner", "barrier-cancel") }()
	waitParked(t, dir, barrierKey, "user:owner", "barrier-cancel", barrierPostCommit)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	noFiles(t, dir)
}

func TestTestBarrierIgnoresUnprefixedMutations(t *testing.T) {
	dir := activateBarrier(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, mutation := range []string{"import-1", "Barrier-1", "x-barrier-1", "barrier"} {
		if err := testBarrier(ctx, barrierPreCommit, barrierKey, "user:owner", mutation); err != nil {
			t.Fatal(mutation, err)
		}
	}
	noFiles(t, dir)
}

func TestTestBarrierConcurrentParksAreIndependent(t *testing.T) {
	dir := activateBarrier(t)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- testBarrier(t.Context(), barrierPreCommit, barrierKey, "user:owner", "barrier-a") }()
	go func() { second <- testBarrier(t.Context(), barrierPreCommit, barrierKey, "user:owner", "barrier-b") }()
	releaseA := waitParked(t, dir, barrierKey, "user:owner", "barrier-a", barrierPreCommit)
	releaseB := waitParked(t, dir, barrierKey, "user:owner", "barrier-b", barrierPreCommit)
	touch(t, releaseA)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second:
		t.Fatalf("second released by first: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	touch(t, releaseB)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	noFiles(t, dir)
}

func TestTestBarrierIgnoresEarlierReleaseAndStaleFiles(t *testing.T) {
	dir := activateBarrier(t)
	base := barrierBase(dir, barrierKey, "user:owner", "barrier-stale", barrierPreCommit)
	touch(t, base+".parked")
	touch(t, base+".release")
	// A new service incarnation clears files left by a killed predecessor.
	if active, err := ConfigureTestBarrier(); !active || err != nil {
		t.Fatal(err)
	}
	noFiles(t, dir)
	// A release written before the park must not pre-approve it.
	touch(t, base+".release")
	done := make(chan error, 1)
	go func() { done <- testBarrier(t.Context(), barrierPreCommit, barrierKey, "user:owner", "barrier-stale") }()
	release := waitParked(t, dir, barrierKey, "user:owner", "barrier-stale", barrierPreCommit)
	select {
	case err := <-done:
		t.Fatalf("earlier release honored: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	touch(t, release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
