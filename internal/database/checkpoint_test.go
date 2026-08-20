package database

import (
	"context"
	"fmt"

	"path/filepath"
	"testing"
	"time"
)

// checkpointTestDB opens a scratch database with the standard pragmas plus
// whatever options the case asks for, and creates a table fat enough that a
// few thousand rows push the WAL past a megabyte.
func checkpointTestDB(t *testing.T, opts ...Option) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ckpt.db")
	db, err := Open("file:"+path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, blob TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func fillWAL(t *testing.T, db *DB, rows int) {
	t.Helper()
	pad := make([]byte, 512)
	for i := range pad {
		pad[i] = 'x'
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec(`INSERT INTO t (id, blob) VALUES (?, ?)`,
			fmt.Sprintf("row-%06d", i), string(pad)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
}

// TestCheckpointModeShrinksWALFile pins the single fact the whole design
// rests on: PASSIVE folds frames back but leaves the -wal FILE at full
// size, and only TRUNCATE returns the space. Getting this backwards is
// what makes a "just run PASSIVE often" checkpointer grow the WAL to
// ~98 MB under load instead of bounding it.
func TestCheckpointModeShrinksWALFile(t *testing.T) {
	tests := []struct {
		name          string
		mode          CheckpointMode
		wantFileEmpty bool
	}{
		{"passive folds frames but keeps the file", CheckpointPassive, false},
		{"truncate returns the file to zero", CheckpointTruncate, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := checkpointTestDB(t)
			fillWAL(t, db, 3000)

			before := db.walBytes()
			if before == 0 {
				t.Fatal("expected a non-empty WAL after 3000 inserts")
			}

			res, err := Checkpoint(context.Background(), db, tc.mode)
			if err != nil {
				t.Fatalf("Checkpoint(%s): %v", tc.mode, err)
			}
			if res.Busy {
				t.Fatalf("Checkpoint(%s) reported busy on an idle database", tc.mode)
			}
			// Both modes must actually fold the frames back.
			if !res.FullyDrained() {
				t.Errorf("frames not fully drained: log=%d checkpointed=%d", res.LogFrames, res.Checkpointed)
			}

			after := db.walBytes()
			if tc.wantFileEmpty && after != 0 {
				t.Errorf("%s left %d bytes of WAL, want 0", tc.mode, after)
			}
			if !tc.wantFileEmpty && after != before {
				t.Errorf("%s changed the WAL file size %d -> %d; this test encodes that it must NOT",
					tc.mode, before, after)
			}
			if res.WALBytesAfter != after {
				t.Errorf("result reported WALBytesAfter=%d, statted %d", res.WALBytesAfter, after)
			}
		})
	}
}

// TestWithManagedWALControlsAutocheckpoint proves the option reaches SQLite
// on every pooled connection, not just the first one handed out.
func TestWithManagedWALControlsAutocheckpoint(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
		want int
	}{
		{"default keeps SQLite's inline autocheckpoint", nil, 1000},
		{"WithManagedWAL disables it", []Option{WithManagedWAL()}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := checkpointTestDB(t, tc.opts...)
			if got := db.ManagedWAL(); got != (tc.want == 0) {
				t.Errorf("ManagedWAL() = %v, want %v", got, tc.want == 0)
			}
			// The pool holds up to 5 connections; a DSN pragma applies to
			// all of them, a session pragma would only apply to one. Check
			// enough times to pull more than one connection out of the pool.
			for i := 0; i < 8; i++ {
				var got int
				if err := db.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&got); err != nil {
					t.Fatalf("read pragma: %v", err)
				}
				if got != tc.want {
					t.Fatalf("wal_autocheckpoint = %d, want %d (attempt %d)", got, tc.want, i)
				}
			}
		})
	}
}

// TestCheckpointerBoundsWAL is the behavioural claim: with the inline
// autocheckpoint off, the checkpoint policy is the only thing reclaiming the
// WAL — so running it must bound the file, and NOT running it must not. The
// second half is what makes WithManagedWAL dangerous on its own, and it is
// worth a test precisely because it is the failure mode a future refactor
// could reintroduce by dropping the goroutine.
//
// # Why the test drives the tick instead of running the loop
//
// This test used to start the real checkpointer at a 10 ms interval, race it
// against a 6000-row write burst, and compare the two peaks with a 2x margin.
// That made the result a property of the host: the unmanaged peak is fixed by
// SQLite's page geometry (58891312 bytes, bit-identical on every run here),
// but the managed peak is writeRate x (tick interval + how long one TRUNCATE
// takes), and only the second of those moves. Measured on linux-amd64 the
// managed peak came out at 4.7 / 8.0 / 11.0 / 13.4 MB on different runs of
// the same code — against a 29.4 MB failure line. That is a 2-3x margin, not
// the "enormous" one the header used to claim, and a slower or busier runner
// walks straight into it. Raising the margin would only move the number the
// runner has to beat.
//
// So the test applies the policy itself, at a fixed point in the workload
// (every `window` rows, with the writer paused), via the same checkpointTick
// the loop calls on every tick. Nothing about the outcome then depends on
// scheduling: on this machine every quantity below is reproducible to the
// byte across runs. That the loop actually calls checkpointTick on a
// schedule is pinned separately by TestCheckpointerLoopTruncatesOnATick, and
// which mode it picks by TestCheckpointTickEscalatesOnThreshold.
//
// # Why the bound is not a constant
//
// The peak is sampled at the end of each window, immediately before the tick,
// which is exactly where the WAL is largest. At that moment it holds at most
// what survived the previous tick (never more than truncAt, or the tick would
// have truncated it) plus one window of growth. So:
//
//	peak <= truncAt + (WAL cost of one window of this workload)
//
// The second term is not guessed, it is measured — from the no-checkpointer
// arm, which writes the identical rows and whose per-window deltas are that
// cost. The doubling is headroom for page-split placement landing differently
// between the two arms; the arms in fact agree to within the 32-byte WAL
// header, so the real margin is ~2.1x, and a WAL nobody reclaims overshoots
// the bound by ~14x.
func TestCheckpointerBoundsWAL(t *testing.T) {
	const (
		rows    = 6000
		window  = 200        // rows between WAL samples, and between ticks
		truncAt = 256 * 1024 // small so the test stays quick
	)

	// writeBurst runs the workload and returns the WAL size sampled at the
	// end of every window. With tick set it applies the checkpointer's policy
	// at each of those points, so `samples` is the size just *before* each
	// tick — the peak of that window.
	writeBurst := func(t *testing.T, tick bool) (samples []int64) {
		t.Helper()
		db := checkpointTestDB(t, WithManagedWAL())
		cfg := CheckpointerConfig{TruncateBytes: truncAt}.withDefaults()

		// The premise of both arms is that the tick is the ONLY thing
		// reclaiming the WAL. If SQLite's inline autocheckpoint were on, both
		// arms would be measuring that instead and the comparison below would
		// mean nothing while still looking plausible.
		var autoCheckpoint int
		if err := db.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&autoCheckpoint); err != nil {
			t.Fatalf("read wal_autocheckpoint: %v", err)
		}
		if autoCheckpoint != 0 {
			t.Fatalf("wal_autocheckpoint = %d, want 0: SQLite is reclaiming the WAL inline, "+
				"so neither arm measures the checkpointer", autoCheckpoint)
		}

		pad := string(make([]byte, 512))
		for i := 0; i < rows; i++ {
			if _, err := db.Exec(`INSERT INTO t (id, blob) VALUES (?, ?)`,
				fmt.Sprintf("r-%06d", i), pad); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if (i+1)%window != 0 {
				continue
			}
			samples = append(samples, db.walBytes())
			if tick {
				res, err := checkpointTick(context.Background(), db, cfg)
				if err != nil {
					t.Fatalf("checkpointTick after %d rows: %v", i+1, err)
				}
				if res.Busy {
					// Nothing else holds this database: a busy result here
					// would mean the pool leaked a transaction, not load.
					t.Fatalf("checkpointTick(%s) reported busy after %d rows on an otherwise idle database",
						res.Mode, i+1)
				}
			}
		}
		return samples
	}

	tests := []struct {
		name string
		tick bool
		// wantGrowsForever: nothing reclaims the WAL, so every sample must be
		// at least as large as the one before it.
		wantGrowsForever bool
	}{
		{"without the checkpointer the WAL only ever grows", false, true},
		{"the checkpointer holds it to one window plus the threshold", true, false},
	}

	// peak[i] is the largest WAL the case observed. windowCost[i] is the
	// largest growth between two consecutive samples, and is only meaningful
	// for the arm that never checkpoints — there the samples chain, because
	// nothing resets the file between them.
	peak := make([]int64, len(tests))
	windowCost := make([]int64, len(tests))
	samplesByCase := make([][]int64, len(tests))

	// Both arms are measured here, in the parent, and NOT inside the subtests
	// below. The comparison at the end needs both, so a `-run` filter that
	// selected one subtest would otherwise leave the other at zero and the
	// bound would collapse to truncAt with no measured window cost to add —
	// a verdict invented from an arm that never ran.
	for i, tc := range tests {
		samples := writeBurst(t, tc.tick)
		if len(samples) != rows/window {
			t.Fatalf("%s: got %d samples, want %d", tc.name, len(samples), rows/window)
		}
		var prev int64
		for _, s := range samples {
			if s > peak[i] {
				peak[i] = s
			}
			if tc.wantGrowsForever {
				if g := s - prev; g > windowCost[i] {
					windowCost[i] = g
				}
			}
			prev = s
		}
		samplesByCase[i] = samples
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if peak[i] == 0 {
				t.Fatal("expected the workload to produce a WAL, got 0 bytes throughout")
			}
			if !tc.wantGrowsForever {
				t.Logf("peak WAL = %d bytes", peak[i])
				return
			}
			var prev int64
			for n, s := range samplesByCase[i] {
				if s < prev {
					t.Errorf("WAL shrank %d -> %d at sample %d; with autocheckpoint off "+
						"and no checkpointer, nothing may reclaim it", prev, s, n)
				}
				prev = s
			}
			t.Logf("peak WAL = %d bytes, largest %d-row window = %d bytes",
				peak[i], window, windowCost[i])
		})
	}

	const unmanagedArm, managedArm = 0, 1
	if t.Failed() {
		return // the numbers below are only meaningful if both arms held up
	}

	// One un-checkpointed window of this exact workload, measured rather than
	// assumed. Doubled: see the header.
	bound := int64(truncAt) + 2*windowCost[unmanagedArm]
	t.Logf("peak WAL: no checkpointer = %d bytes, with checkpointer = %d bytes; bound = %d bytes "+
		"(truncAt %d + 2 x measured window cost %d)",
		peak[unmanagedArm], peak[managedArm], bound, truncAt, windowCost[unmanagedArm])

	if peak[managedArm] > bound {
		t.Errorf("checkpointer did not bound the WAL: peak %d bytes, want at most %d "+
			"(one %d-row window costs %d bytes, so anything larger means a tick failed to reclaim)",
			peak[managedArm], bound, window, windowCost[unmanagedArm])
	}
	// The other half of the claim: leaving it alone must NOT bound it. Stated
	// against the same bound so the two halves cannot drift apart.
	if peak[unmanagedArm] <= bound {
		t.Errorf("no-checkpointer peak %d bytes is already within the bound %d; the workload no "+
			"longer distinguishes the two policies and this test proves nothing",
			peak[unmanagedArm], bound)
	}
}

// TestCheckpointTickEscalatesOnThreshold pins the decision inside one tick:
// PASSIVE while the -wal file is small, TRUNCATE once it is not. This is the
// half of the policy TestCheckpointerBoundsWAL cannot see — with a single
// idle writer SQLite restarts the WAL by itself after a PASSIVE checkpoint,
// so a PASSIVE-only policy bounds the file just as well in a quiet test and
// only falls apart under the concurrent readers of a live daemon (98 MB; see
// the package comment).
func TestCheckpointTickEscalatesOnThreshold(t *testing.T) {
	tests := []struct {
		name          string
		truncateBytes int64
		wantMode      CheckpointMode
		wantFileEmpty bool
	}{
		{"a WAL under the threshold stays on PASSIVE", 64 * 1024 * 1024, CheckpointPassive, false},
		{"a WAL over the threshold escalates to TRUNCATE", 1, CheckpointTruncate, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := checkpointTestDB(t, WithManagedWAL())
			fillWAL(t, db, 3000)

			before := db.walBytes()
			if before == 0 {
				t.Fatal("expected a non-empty WAL after 3000 inserts")
			}
			if (before > tc.truncateBytes) != (tc.wantMode == CheckpointTruncate) {
				t.Fatalf("test setup is wrong: WAL is %d bytes against a %d-byte threshold, "+
					"which does not select %s", before, tc.truncateBytes, tc.wantMode)
			}

			cfg := CheckpointerConfig{TruncateBytes: tc.truncateBytes}.withDefaults()
			res, err := checkpointTick(context.Background(), db, cfg)
			if err != nil {
				t.Fatalf("checkpointTick: %v", err)
			}
			if res.Mode != tc.wantMode {
				t.Errorf("tick chose %s, want %s", res.Mode, tc.wantMode)
			}
			if res.Busy {
				t.Fatalf("tick reported busy on an idle database")
			}

			after := db.walBytes()
			if tc.wantFileEmpty && after != 0 {
				t.Errorf("%s left %d bytes of WAL, want 0", res.Mode, after)
			}
			if !tc.wantFileEmpty && after != before {
				t.Errorf("%s changed the WAL file size %d -> %d; only TRUNCATE may do that",
					res.Mode, before, after)
			}
		})
	}
}

// TestCheckpointerLoopTruncatesOnATick covers the wiring the other two tests
// deliberately step around: that the running goroutine applies the policy on
// its ticker, not only on the shutdown path that
// TestStartCheckpointerAsyncStopTruncates exercises. A daemon whose periodic
// tick silently stopped firing would look fine to both of those.
//
// No duration appears in the pass condition. The WAL is filled *before* the
// loop starts, so "it was non-empty" is not a race, and no writes happen
// while the loop runs, so the file cannot grow back: the first tick to fire
// takes it to zero and it stays there. checkpointerAbandonAfter is only ever
// reached on the path where the test is already failing.
func TestCheckpointerLoopTruncatesOnATick(t *testing.T) {
	const checkpointerAbandonAfter = 30 * time.Second

	db := checkpointTestDB(t, WithManagedWAL())
	fillWAL(t, db, 2000)

	// Read before the loop exists, so "there was a WAL to reclaim" is a fact
	// rather than a race against the first tick.
	before := db.walBytes()
	if before <= 1 {
		t.Fatalf("WAL is %d bytes after 2000 inserts; it must exceed the 1-byte "+
			"TruncateBytes below for the tick to have anything to do", before)
	}

	// TruncateBytes: 1 means every tick escalates, so the first one to fire
	// is enough. Only TRUNCATE ever zeroes the file — PASSIVE leaving it
	// alone is pinned by TestCheckpointTickEscalatesOnThreshold — so
	// observing 0 here is proof a periodic tick ran a TRUNCATE.
	stop := StartCheckpointerAsync(db, quietLogger(), CheckpointerConfig{
		Interval:      5 * time.Millisecond,
		TruncateBytes: 1,
	})
	defer stop()

	abandon := time.After(checkpointerAbandonAfter)
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		if got := db.walBytes(); got == 0 {
			return
		}
		select {
		case <-abandon:
			t.Fatalf("the checkpointer loop never truncated a %d-byte WAL; it is still %d bytes "+
				"after %s with no writer in the way, so the periodic tick is not reaching TRUNCATE",
				before, db.walBytes(), checkpointerAbandonAfter)
		case <-poll.C:
		}
	}
}

// TestStartCheckpointerAsyncStopTruncates covers the shutdown contract: the
// returned stop function must not come back until the final TRUNCATE has
// landed, otherwise a caller's deferred db.Close() races it and the WAL
// survives into the next boot.
func TestStartCheckpointerAsyncStopTruncates(t *testing.T) {
	db := checkpointTestDB(t, WithManagedWAL())

	// A long interval guarantees no periodic tick fires: whatever truncation
	// we observe can only have come from the shutdown path.
	stop := StartCheckpointerAsync(db, quietLogger(), CheckpointerConfig{
		Interval:      time.Hour,
		TruncateBytes: 1,
	})

	fillWAL(t, db, 2000)
	if db.walBytes() == 0 {
		t.Fatal("expected a non-empty WAL before stopping")
	}

	stop()

	if got := db.walBytes(); got != 0 {
		t.Errorf("WAL is %d bytes after stop(); the final truncate either did not run or was not waited for", got)
	}

	// Idempotent: a second stop must not panic on the closed channel or
	// double-cancel. Callers get this for free via defer in some paths and
	// explicitly in others.
	stop()
}

// TestCheckpointerConfigDefaults keeps the zero value meaningful, since
// cmd_start passes an empty CheckpointerConfig{}.
func TestCheckpointerConfigDefaults(t *testing.T) {
	tests := []struct {
		name          string
		in            CheckpointerConfig
		wantInterval  time.Duration
		wantTruncateB int64
	}{
		{"zero value uses measured defaults", CheckpointerConfig{},
			DefaultCheckpointInterval, DefaultCheckpointTruncateBytes},
		{"negative values fall back too", CheckpointerConfig{Interval: -1, TruncateBytes: -1},
			DefaultCheckpointInterval, DefaultCheckpointTruncateBytes},
		{"explicit values are preserved", CheckpointerConfig{Interval: time.Second, TruncateBytes: 42},
			time.Second, 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.withDefaults()
			if got.Interval != tc.wantInterval {
				t.Errorf("Interval = %s, want %s", got.Interval, tc.wantInterval)
			}
			if got.TruncateBytes != tc.wantTruncateB {
				t.Errorf("TruncateBytes = %d, want %d", got.TruncateBytes, tc.wantTruncateB)
			}
		})
	}
}

// TestCheckpointerSurvivesMissingWAL guards the paths that have no -wal
// sidecar to stat: an in-memory database must not make the loop spin on an
// error forever, and walBytes must answer 0 rather than blowing up.
func TestCheckpointerSurvivesMissingWAL(t *testing.T) {
	db, err := Open("file::memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if got := db.filePath(); got != "" {
		t.Errorf("filePath() = %q for an in-memory DB, want empty", got)
	}
	if got := db.walBytes(); got != 0 {
		t.Errorf("walBytes() = %d for an in-memory DB, want 0", got)
	}

	// The loop must return promptly instead of logging every tick forever.
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		defer close(done)
		StartCheckpointer(ctx, db, quietLogger(), CheckpointerConfig{Interval: 5 * time.Millisecond})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkpointer did not stop on an unsupported database")
	}
}

// TestCheckpointRejectsNilDB keeps the guard honest — background loops are
// wired from several places and a nil DB must not panic the daemon.
func TestCheckpointRejectsNilDB(t *testing.T) {
	if _, err := Checkpoint(context.Background(), nil, CheckpointPassive); err == nil {
		t.Error("Checkpoint(nil) returned no error")
	}
	// StartCheckpointer must return immediately rather than nil-deref.
	done := make(chan struct{})
	go func() {
		defer close(done)
		StartCheckpointer(context.Background(), nil, quietLogger(), CheckpointerConfig{})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StartCheckpointer(nil) did not return")
	}
}
