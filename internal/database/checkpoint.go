package database

// WAL checkpointing.
//
// In WAL mode every committed transaction appends frames to the -wal
// sidecar. Those frames are only folded back into the main database file
// by a *checkpoint*. Left to itself SQLite runs that checkpoint inline,
// inside whichever write transaction happens to push the WAL past
// `wal_autocheckpoint` frames (1000 by default, ~4 MiB at our page size).
// The cost lands on a random caller — in this system, on whichever agent
// happened to be writing at that moment.
//
// Measured on a 12-core box against a copy of the real schema, 100
// simulated agents on a realistic duty cycle (think 300-900 ms, then one
// 4-row transaction), three runs per policy, reporting medians and the
// spread of p99 across runs:
//
//	policy                                   p99 (spread)        WAL at end
//	-------------------------------------------------------------------------
//	autocheckpoint=1000, no checkpointer     26.1ms (20.2-26.3)    13.6 MB
//	autocheckpoint=0  + PASSIVE every 500ms  32.2ms (31.5-95.4)    98.0 MB
//	autocheckpoint=0  + this policy          8.9ms  (8.4-11.0)          0 KB
//
// Two results drove the design, and both contradict the obvious guess:
//
//  1. PASSIVE alone is WORSE than doing nothing. A PASSIVE checkpoint
//     copies frames back but never resets the -wal FILE, so the file
//     grows without bound; the 98 MB above is that failure. PASSIVE
//     bounds the *reusable space* inside the WAL, not the WAL itself.
//     Only TRUNCATE returns the file to zero.
//
//  2. The win does not come from checkpointing more often. It comes from
//     checkpointing on a thread that is not serving a request. With
//     autocheckpoint disabled no writer ever pays the fold-back inline;
//     the cost moves to this goroutine, whose slowest observed checkpoint
//     was 37.6 ms — paid by nobody who was waiting on an answer.
//
// Hence the policy: PASSIVE on every tick to keep frames moving cheaply
// (3 µs when there is nothing to do), escalating to TRUNCATE only once
// the file has actually grown past a threshold. That is the row that
// measured 8.9 ms p99 with the WAL back at zero.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// CheckpointMode is a SQLite `PRAGMA wal_checkpoint` argument.
//
// RESTART and FULL are deliberately not exposed: FULL blocks new writers
// until every frame is folded back, and RESTART additionally waits for
// readers to drain. Both hand the stall we are trying to remove straight
// back to live traffic. This package only ever issues the two modes that
// are safe to run beside agent writes.
type CheckpointMode string

const (
	// CheckpointPassive folds back whatever frames it can and gives up
	// immediately if a reader or writer is in the way. It never blocks
	// anyone, and it never shrinks the -wal file.
	CheckpointPassive CheckpointMode = "PASSIVE"
	// CheckpointTruncate folds back frames and then resets the -wal file
	// to zero length. This is the only mode that returns disk space. It
	// can block briefly waiting for readers, which is why it is used on a
	// threshold rather than on every tick.
	CheckpointTruncate CheckpointMode = "TRUNCATE"
)

// Checkpoint policy defaults. Declared as vars, not consts, so tests can
// shrink them to milliseconds; production code never mutates them.
var (
	// DefaultCheckpointInterval is how often the checkpointer wakes. An
	// idle no-op PASSIVE checkpoint measured p50 3 µs / max 43 µs over 100
	// samples, so a 2 s tick costs nothing on a quiet instance. 2 s (not
	// the 500 ms first tried) because the shorter tick showed no p99
	// benefit and multiplied the number of times TRUNCATE could collide
	// with a reader.
	DefaultCheckpointInterval = 2 * time.Second

	// DefaultCheckpointTruncateBytes is the -wal size above which the tick
	// escalates from PASSIVE to TRUNCATE. 16 MiB is four times the default
	// autocheckpoint threshold: high enough that a burst of agent writes
	// does not trigger a truncate on every tick, low enough that peak
	// observed WAL stayed near 20 MB instead of the 98 MB a PASSIVE-only
	// policy reached.
	DefaultCheckpointTruncateBytes int64 = 16 * 1024 * 1024
)

// ErrNotWAL is returned by Checkpoint when the handle is not in WAL mode
// and therefore has nothing to checkpoint — an in-memory database, or one
// opened by a tool that chose another journal mode. StartCheckpointer
// treats it as "there is no work here, ever" and returns rather than
// retrying on every tick.
var ErrNotWAL = errors.New("checkpoint: database is not in WAL mode")

// CheckpointResult reports what one checkpoint actually did. SQLite
// answers `PRAGMA wal_checkpoint` with a row rather than an error when it
// cannot finish, so Busy is a normal outcome and not a failure — see
// Checkpoint.
type CheckpointResult struct {
	Mode CheckpointMode
	// Busy is true when SQLite could not complete the checkpoint because
	// another connection held it off. Expected under load; the next tick
	// retries.
	Busy bool
	// LogFrames is the number of frames in the WAL at the time of the call,
	// Checkpointed how many of them were folded back into the database.
	LogFrames    int
	Checkpointed int
	// WALBytesBefore/After bracket the -wal file size. Only TRUNCATE moves
	// After to 0; see the package comment.
	WALBytesBefore int64
	WALBytesAfter  int64
	Duration       time.Duration
}

// FullyDrained reports whether every frame made it back into the database.
func (r CheckpointResult) FullyDrained() bool {
	return !r.Busy && r.LogFrames == r.Checkpointed
}

// filePath returns the on-disk database path with any DSN query string
// stripped, or "" for in-memory databases which have no -wal sidecar to
// measure. Mirrors the same trimming Open does before chmod'ing the file.
func (d *DB) filePath() string {
	p := d.path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if p == "" || strings.Contains(p, ":memory:") {
		return ""
	}
	return p
}

// walBytes returns the size of the -wal sidecar, or 0 when it is absent
// (freshly checkpointed, or an in-memory database).
func (d *DB) walBytes() int64 {
	p := d.filePath()
	if p == "" {
		return 0
	}
	fi, err := os.Stat(p + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Checkpoint runs one `PRAGMA wal_checkpoint` in the given mode.
//
// A non-nil error means the pragma itself could not be executed. A
// checkpoint that SQLite declined to complete because someone else held
// the database is reported as Busy on the result with a nil error — that
// is SQLite's own contract (it answers with busy=1 in the first column,
// not with an error), and treating it as a failure would make the normal
// under-load path look broken.
func Checkpoint(ctx context.Context, db *DB, mode CheckpointMode) (CheckpointResult, error) {
	if db == nil {
		return CheckpointResult{}, fmt.Errorf("checkpoint: nil database")
	}
	res := CheckpointResult{Mode: mode, WALBytesBefore: db.walBytes()}

	var busy, logFrames, checkpointed int
	start := time.Now()
	// The pragma takes no bind parameters, and mode is a package-local
	// constant of an unexported-value type — never caller input — so the
	// concatenation cannot carry anything but PASSIVE or TRUNCATE.
	err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(`+string(mode)+`)`).
		Scan(&busy, &logFrames, &checkpointed)
	res.Duration = time.Since(start)
	if err != nil {
		if err == sql.ErrNoRows {
			return res, ErrNotWAL
		}
		return res, fmt.Errorf("checkpoint(%s): %w", mode, err)
	}
	// A database that is not in WAL mode — `journal_mode=memory` for the
	// in-memory handles some tests and tools open — answers the pragma
	// successfully with (busy=0, log=-1, checkpointed=-1) rather than
	// failing. Verified against modernc.org/sqlite; treating that as a
	// normal result would leave the background loop ticking forever on a
	// database it can never checkpoint.
	if logFrames < 0 && checkpointed < 0 {
		return res, ErrNotWAL
	}
	res.Busy = busy != 0
	res.LogFrames = logFrames
	res.Checkpointed = checkpointed
	res.WALBytesAfter = db.walBytes()
	return res, nil
}

// checkpointTick performs exactly one iteration of the checkpoint policy:
// PASSIVE to keep frames moving cheaply, escalating to TRUNCATE only once
// the -wal file has actually grown past cfg.TruncateBytes. See the package
// comment for why that split, rather than "checkpoint more often", is what
// bounds the file.
//
// This is the whole mechanism the loop exists to run on a schedule, factored
// out so a test can apply it at a deterministic point in a workload instead
// of racing a wall-clock ticker against a write loop. cfg must already have
// been through withDefaults.
func checkpointTick(ctx context.Context, db *DB, cfg CheckpointerConfig) (CheckpointResult, error) {
	mode := CheckpointPassive
	if db.walBytes() > cfg.TruncateBytes {
		mode = CheckpointTruncate
	}
	return Checkpoint(ctx, db, mode)
}

// CheckpointerConfig tunes StartCheckpointer. The zero value is valid and
// means "use the measured defaults".
type CheckpointerConfig struct {
	// Interval between ticks. Zero uses DefaultCheckpointInterval.
	Interval time.Duration
	// TruncateBytes is the -wal size above which a tick escalates from
	// PASSIVE to TRUNCATE. Zero uses DefaultCheckpointTruncateBytes.
	TruncateBytes int64
}

func (c CheckpointerConfig) withDefaults() CheckpointerConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultCheckpointInterval
	}
	if c.TruncateBytes <= 0 {
		c.TruncateBytes = DefaultCheckpointTruncateBytes
	}
	return c
}

// StartCheckpointerAsync starts the checkpoint loop on its own goroutine
// and returns a stop function that cancels it AND waits for the final
// TRUNCATE to finish.
//
// Prefer this over `go StartCheckpointer(...)` in any process that also
// closes the database. The shutdown truncate is a write: if the caller
// merely cancels a context and races on to db.Close(), that write lands on
// a closed handle and the WAL survives the restart. Bundling the wait into
// the returned function removes the ordering from the caller's hands.
//
// Typical use, where the deferred stop runs before the deferred Close
// because defers unwind last-in-first-out:
//
//	db, err := database.Open(url, database.WithManagedWAL())
//	...
//	defer db.Close()
//	defer database.StartCheckpointerAsync(db, logger, database.CheckpointerConfig{})()
func StartCheckpointerAsync(db *DB, logger *slog.Logger, cfg CheckpointerConfig) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		StartCheckpointer(ctx, db, logger, cfg)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

// StartCheckpointer runs the WAL checkpoint loop until ctx is cancelled,
// then performs one final TRUNCATE so a restart does not inherit a large
// WAL. It blocks, so callers run it as `go StartCheckpointer(...)`.
//
// This MUST be running whenever the database was opened with
// WithManagedWAL: that option disables SQLite's own autocheckpoint, so
// this loop becomes the only thing folding frames back. Opening with the
// option and not starting the loop grows the WAL until the disk fills.
// The pairing is asserted by TestDaemonPairsManagedWALWithCheckpointer.
//
// Running it against a database opened WITHOUT the option is harmless and
// mildly useful — SQLite's inline autocheckpoint stays as a backstop and
// this loop simply gets to most of the work first — which is why short
// lived CLI commands are free to ignore it entirely.
func StartCheckpointer(ctx context.Context, db *DB, logger *slog.Logger, cfg CheckpointerConfig) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg = cfg.withDefaults()

	t := time.NewTicker(cfg.Interval)
	defer t.Stop()

	// Consecutive transient failures, used to rate-limit the warning.
	var failures int

	for {
		select {
		case <-ctx.Done():
			// Final truncate on the way out. Best effort with a short
			// deadline of its own: ctx is already cancelled, and a
			// shutdown must not hang waiting for a reader to drain.
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			res, err := Checkpoint(shutCtx, db, CheckpointTruncate)
			cancel()
			switch {
			case err != nil:
				logger.Debug("wal checkpointer: final truncate failed", "error", err)
			case res.Busy:
				logger.Info("wal checkpointer: final truncate left frames behind",
					"wal_bytes", res.WALBytesAfter, "log_frames", res.LogFrames)
			default:
				logger.Debug("wal checkpointer: stopped", "wal_bytes", res.WALBytesAfter)
			}
			return

		case <-t.C:
			res, err := checkpointTick(ctx, db, cfg)
			switch {
			case errors.Is(err, ErrNotWAL):
				// Permanent for this handle — nothing to retry. Say so once
				// and stop, rather than ticking forever against a database
				// that has no WAL to fold. The return is what makes "once"
				// true; no guard flag is needed.
				logger.Info("wal checkpointer: nothing to do, database is not in WAL mode")
				return
			case err != nil:
				if ctx.Err() != nil {
					continue // shutting down; the ctx.Done branch handles it
				}
				// Transient (a locked database, a closing pool). Keep
				// ticking — returning here would silently disable
				// checkpointing for the rest of the process's life, which
				// with autocheckpoint off is how the disk fills up.
				failures++
				if failures == 1 || failures%100 == 0 {
					logger.Warn("wal checkpoint failed",
						"error", err, "consecutive_failures", failures)
				}
				continue
			}
			failures = 0
			// Only worth a line when the expensive mode ran, and only at
			// debug — this fires every few seconds under sustained load and
			// has no operator action attached to it.
			if res.Mode == CheckpointTruncate {
				logger.Debug("wal checkpoint truncated",
					"wal_bytes_before", res.WALBytesBefore,
					"wal_bytes_after", res.WALBytesAfter,
					"log_frames", res.LogFrames,
					"checkpointed", res.Checkpointed,
					"busy", res.Busy,
					"took", res.Duration)
			}
		}
	}
}
