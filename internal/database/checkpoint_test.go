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
// autocheckpoint off, the loop is the only thing reclaiming the WAL — so
// running it must bound the file, and NOT running it must not. The second
// half is what makes WithManagedWAL dangerous on its own, and it is worth a
// test precisely because it is the failure mode a future refactor could
// reintroduce by dropping the goroutine.
// The assertion is deliberately RELATIVE — "the checkpointer keeps the WAL
// materially smaller than leaving it alone" — rather than an absolute byte
// ceiling. Measured peaks over the same 6000-row workload:
//
//	                       no checkpointer   with checkpointer
//	writes back to back        58.9 MB            24.3 MB
//	writes with small gaps     58.9 MB             4.7 MB
//
// An absolute ceiling would encode the machine and the write pattern, not
// the mechanism: TRUNCATE has to find a moment when no reader holds the
// database, so a tight loop with no gaps gives it fewer openings and the
// file settles higher. Both columns are a real improvement; only the size
// of it moves. The 2x margin below sits well inside the smaller one.
func TestCheckpointerBoundsWAL(t *testing.T) {
	const (
		rows      = 6000
		truncAt   = 256 * 1024 // small so the test stays quick
		tickEvery = 10 * time.Millisecond
	)

	// writeBurst runs the same workload with and without the loop and
	// returns the peak WAL size it observed.
	writeBurst := func(t *testing.T, runCheckpointer bool) int64 {
		t.Helper()
		db := checkpointTestDB(t, WithManagedWAL())

		var stop func()
		if runCheckpointer {
			stop = StartCheckpointerAsync(db, quietLogger(), CheckpointerConfig{
				Interval:      tickEvery,
				TruncateBytes: truncAt,
			})
		}

		pad := string(make([]byte, 512))
		var peak int64
		for i := 0; i < rows; i++ {
			if _, err := db.Exec(`INSERT INTO t (id, blob) VALUES (?, ?)`,
				fmt.Sprintf("r-%06d", i), pad); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if i%20 == 0 {
				// A brief gap, standing in for an agent's think time. This
				// is what real callers look like; see the table above for
				// what happens without it.
				time.Sleep(time.Millisecond)
			}
			if i%200 == 0 {
				if w := db.walBytes(); w > peak {
					peak = w
				}
			}
		}
		if w := db.walBytes(); w > peak {
			peak = w
		}
		if stop != nil {
			stop()
			// The shutdown truncate must leave nothing behind.
			if got := db.walBytes(); got != 0 {
				t.Errorf("WAL is %d bytes after stop(), want 0", got)
			}
		}
		return peak
	}

	unmanaged := writeBurst(t, false)
	managed := writeBurst(t, true)

	t.Logf("peak WAL: no checkpointer = %d bytes, with checkpointer = %d bytes", unmanaged, managed)

	if unmanaged == 0 || managed == 0 {
		t.Fatalf("expected both runs to produce a WAL, got unmanaged=%d managed=%d", unmanaged, managed)
	}
	if managed*2 > unmanaged {
		t.Errorf("checkpointer barely helped: peak WAL %d bytes vs %d without it, want at least 2x smaller",
			managed, unmanaged)
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
