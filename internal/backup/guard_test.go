package backup

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorkspaceGuard_MissionAndBackupMutualExclusion verifies the
// in-process guard cleanly rejects overlap in both directions.
func TestWorkspaceGuard_MissionAndBackupMutualExclusion(t *testing.T) {
	g := NewWorkspaceGuard()

	// Mission claims the guard first. A concurrent backup MUST fail
	// with ErrGuardMissionsInFlight.
	mRel, err := g.BeginMission("ws")
	if err != nil {
		t.Fatalf("BeginMission: %v", err)
	}
	if _, err := g.BeginBackup("ws"); !errors.Is(err, ErrGuardMissionsInFlight) {
		t.Fatalf("expected ErrGuardMissionsInFlight, got %v", err)
	}
	mRel()

	// Backup claims the guard. A concurrent mission MUST fail with
	// ErrGuardBackupInProgress.
	bRel, err := g.BeginBackup("ws")
	if err != nil {
		t.Fatalf("BeginBackup: %v", err)
	}
	if _, err := g.BeginMission("ws"); !errors.Is(err, ErrGuardBackupInProgress) {
		t.Fatalf("expected ErrGuardBackupInProgress, got %v", err)
	}
	// A second backup must also fail fast.
	if _, err := g.BeginBackup("ws"); !errors.Is(err, ErrGuardBackupInProgress) {
		t.Fatalf("expected ErrGuardBackupInProgress, got %v", err)
	}
	bRel()

	// After release, a fresh mission and a fresh backup can both
	// claim the guard (sequentially).
	if rel, err := g.BeginMission("ws"); err != nil {
		t.Fatalf("post-release mission: %v", err)
	} else {
		rel()
	}
	if rel, err := g.BeginBackup("ws"); err != nil {
		t.Fatalf("post-release backup: %v", err)
	} else {
		rel()
	}
}

// TestWorkspaceGuard_ManyMissionsOneBackup asserts the guard is a
// many-readers/one-writer primitive — every concurrent mission that
// beats a backup gets in, and the backup only wins when the counter
// drains to zero.
func TestWorkspaceGuard_ManyMissionsOneBackup(t *testing.T) {
	g := NewWorkspaceGuard()

	const N = 16
	releases := make([]func(), 0, N)
	for i := 0; i < N; i++ {
		rel, err := g.BeginMission("ws")
		if err != nil {
			t.Fatalf("BeginMission %d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	// With N missions in flight, backup must fail.
	if _, err := g.BeginBackup("ws"); !errors.Is(err, ErrGuardMissionsInFlight) {
		t.Fatalf("expected ErrGuardMissionsInFlight, got %v", err)
	}
	// Drain all but one, backup still fails.
	for i := 0; i < N-1; i++ {
		releases[i]()
	}
	if _, err := g.BeginBackup("ws"); !errors.Is(err, ErrGuardMissionsInFlight) {
		t.Fatalf("expected ErrGuardMissionsInFlight, got %v", err)
	}
	// Drain the last, backup succeeds.
	releases[N-1]()
	rel, err := g.BeginBackup("ws")
	if err != nil {
		t.Fatalf("BeginBackup: %v", err)
	}
	rel()
}

// TestWorkspaceGuard_TOCTOURace fires many concurrent mission-start
// attempts while a backup is racing to claim the guard. The invariant
// under test: at no point may a mission AND a backup both hold the
// guard simultaneously. A canary atomic counter is incremented on
// mission entry and decremented on release; the backup checks the
// counter is ZERO while it holds the guard.
func TestWorkspaceGuard_TOCTOURace(t *testing.T) {
	t.Parallel()
	g := NewWorkspaceGuard()

	var liveMissions int64
	var overlap int64

	const iterations = 200
	const missionFanout = 32

	var wg sync.WaitGroup
	for i := 0; i < iterations; i++ {
		// Fan out many mission-start attempts.
		for j := 0; j < missionFanout; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rel, err := g.BeginMission("ws")
				if err != nil {
					// Rejected because a backup held the guard — correct
					// behaviour; no overlap possible.
					return
				}
				atomic.AddInt64(&liveMissions, 1)
				// Hold briefly to simulate mission-start side effects.
				time.Sleep(50 * time.Microsecond)
				atomic.AddInt64(&liveMissions, -1)
				rel()
			}()
		}
		// One backup attempt per iteration, racing the missions.
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := g.BeginBackup("ws")
			if err != nil {
				// Rejected because a mission held the guard — correct
				// behaviour.
				return
			}
			// CRITICAL invariant: no missions may be live while a backup
			// holds the guard. If any are, the TOCTOU race is open.
			if n := atomic.LoadInt64(&liveMissions); n != 0 {
				atomic.AddInt64(&overlap, 1)
			}
			time.Sleep(50 * time.Microsecond)
			if n := atomic.LoadInt64(&liveMissions); n != 0 {
				atomic.AddInt64(&overlap, 1)
			}
			rel()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&overlap); got != 0 {
		t.Fatalf("TOCTOU race detected: %d overlaps of mission + backup", got)
	}
}

// TestWorkspaceGuard_EmptyWorkspaceNoOp ensures an empty workspace ID
// returns a usable no-op pair — the API guard depends on this short
// circuit so unit tests that fake handlers do not need a guard at all.
func TestWorkspaceGuard_EmptyWorkspaceNoOp(t *testing.T) {
	g := NewWorkspaceGuard()
	mRel, err := g.BeginMission("")
	if err != nil || mRel == nil {
		t.Fatalf("BeginMission(empty): rel=%p err=%v", mRel, err)
	}
	mRel()
	bRel, err := g.BeginBackup("")
	if err != nil || bRel == nil {
		t.Fatalf("BeginBackup(empty): rel=%p err=%v", bRel, err)
	}
	bRel()
}
