//go:build crewship_testbarrier && unix

package appengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const barrierCompiledIn = true

// barrierMaxPark is the hard cap on one park. It exceeds the default 30 s
// lease so a parked operation can deliberately outlive its lease.
const barrierMaxPark = 45 * time.Second

// The operator, not the service, decides which slot may run a barrier: a
// marker file /etc/crewship/test-barrier-<slot>.enabled, owned by root and not
// writable by group or others, managed by provisioning on development slots
// only. A service running as root could write it itself and is refused.
var (
	barrierMarkerDir   = "/etc/crewship"
	barrierMarkerOwner = 0
	barrierServiceUID  = os.Geteuid
)

// SetTestBarrierMarkerForTest points the marker check at a test directory and
// owner, because tests cannot create root-owned files. It exists only in
// tagged builds and returns a function restoring the fixed values.
func SetTestBarrierMarkerForTest(dir string, owner int, serviceUID func() int) (restore func()) {
	barrierState.mu.Lock()
	defer barrierState.mu.Unlock()
	oldDir, oldOwner, oldUID := barrierMarkerDir, barrierMarkerOwner, barrierServiceUID
	barrierMarkerDir, barrierMarkerOwner, barrierServiceUID = dir, owner, serviceUID
	return func() {
		barrierState.mu.Lock()
		defer barrierState.mu.Unlock()
		barrierMarkerDir, barrierMarkerOwner, barrierServiceUID = oldDir, oldOwner, oldUID
	}
}

var barrierState struct {
	mu   sync.RWMutex
	dir  string
	cap  time.Duration
	poll time.Duration
}

// ConfigureTestBarrier activates the barrier from the environment. Unset
// directory: inactive, no error. Otherwise every condition must hold or the
// service must refuse to start: a development slot (never stage or prod), an
// absolute, real (non-symlink) directory with mode 0700 owned by this user.
func ConfigureTestBarrier() (bool, error) {
	barrierState.mu.Lock()
	defer barrierState.mu.Unlock()
	barrierState.dir = ""
	dir := os.Getenv(TestBarrierDirEnv)
	if dir == "" {
		return false, nil
	}
	slot := os.Getenv(TestBarrierSlotEnv)
	switch slot {
	case "dev1", "dev2", "dev3", "dev4":
	default:
		return false, fmt.Errorf("gridcraft test barrier refused on slot %q: only dev1..dev4", slot)
	}
	if err := checkBarrierMarker(slot); err != nil {
		return false, err
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false, fmt.Errorf("gridcraft test barrier directory must be a clean absolute path")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return false, fmt.Errorf("gridcraft test barrier directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ok || int(stat.Uid) != os.Geteuid() {
		return false, fmt.Errorf("gridcraft test barrier directory must be a 0700 directory owned by the service user")
	}
	// Files left by a previous incarnation (for example a killed process)
	// must neither block a retry nor pre-release a new park.
	for _, pattern := range []string{"*.parked", "*.release"} {
		stale, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, f := range stale {
			_ = os.Remove(f)
		}
	}
	barrierState.dir, barrierState.cap, barrierState.poll = dir, barrierMaxPark, 50*time.Millisecond
	return true, nil
}

// TestBarrierActive reports whether a configured barrier can park operations.
func TestBarrierActive() bool {
	barrierState.mu.RLock()
	defer barrierState.mu.RUnlock()
	return barrierState.dir != ""
}

// testBarrier parks a barrier- operation at each point it reaches (first
// pre-commit, then post-commit when the commit succeeded) until <name>.release appears, the
// request context ends, or the hard cap elapses. It never substitutes for the
// commit: it returns nil (continue to the full checks) or the context error.
func testBarrier(ctx context.Context, point barrierPoint, k Key, authority, mutation string) error {
	barrierState.mu.RLock()
	dir, limit, poll := barrierState.dir, barrierState.cap, barrierState.poll
	barrierState.mu.RUnlock()
	if dir == "" || !strings.HasPrefix(mutation, TestBarrierMutationPrefix) {
		return nil
	}
	sum := sha256.Sum256([]byte(k.PageID + "|" + authority + "|" + mutation))
	base := filepath.Join(dir, hex.EncodeToString(sum[:])+"."+string(point))
	parked, release := base+".parked", base+".release"
	// A release must follow the park it answers; never honor an earlier one.
	if err := os.Remove(release); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gridcraft test barrier: %w", err)
	}
	f, err := os.OpenFile(parked, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("gridcraft test barrier: %w", err)
	}
	_, werr := fmt.Fprintf(f, "point=%s\nartifact=%s\nmutation=%s\nparked_at=%s\n", point, k.PageID, mutation, time.Now().UTC().Format(time.RFC3339Nano))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	defer func() {
		_ = os.Remove(parked)
		_ = os.Remove(release)
	}()
	if werr != nil {
		return fmt.Errorf("gridcraft test barrier: %w", werr)
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-tick.C:
			if _, err := os.Lstat(release); err == nil {
				return nil
			}
		}
	}
}

// checkBarrierMarker requires the operator's marker for exactly this slot.
// Called with barrierState.mu held.
func checkBarrierMarker(slot string) error {
	marker := filepath.Join(barrierMarkerDir, "test-barrier-"+slot+".enabled")
	if barrierServiceUID() == barrierMarkerOwner {
		return fmt.Errorf("gridcraft test barrier refused: the service must not run as the owner of %s", marker)
	}
	info, err := os.Lstat(marker)
	if err != nil {
		return fmt.Errorf("gridcraft test barrier refused: operator marker %s: %w", marker, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !ok || int(stat.Uid) != barrierMarkerOwner {
		return fmt.Errorf("gridcraft test barrier refused: %s must be a regular file owned by uid %d and not writable by group or others", marker, barrierMarkerOwner)
	}
	return nil
}
