package consolidate

// The per-workspace memory-version sweep only ever ran from the daily
// compaction tick, and that tick waits for the NEXT 03:00 UTC with no
// catch-up. An instance restarted more often than once a day therefore never
// swept: on a dev box redeployed every half hour, the retention window an
// operator sets in the admin UI had no effect whatsoever, and memory_versions
// grew without bound.
//
// The sweep is one DELETE per workspace with a concrete cutoff — idempotent,
// cheap, and safe to run at boot. Doing so is what makes the configured
// window mean something on any instance that is not up for 24 hours straight.

import (
	"context"
	"testing"
	"time"
)

func TestStartBackground_SweepsAtBootWithoutWaitingForTheDailyTick(t *testing.T) {
	// The rig's second return is the BLOB ROOT, not a workspace — the
	// workspace has to be seeded, and it is what SweepAllWorkspaces walks.
	db, blobRoot, _ := retentionCoordRig(t)
	const wsID = "ws_catchup"
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, slug, memory_config) VALUES(?, 'Catch up', 'catchup', '{"versions_retention_days":7}')`,
		wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	seedRow(t, db, blobRoot, wsID, "AGENT.md", "aaaaaaaabbbbcccc", 90)
	seedRow(t, db, blobRoot, wsID, "AGENT.md", "ddddddddeeeeffff", 1)

	if got := countRows(t, db, wsID); got != 2 {
		t.Fatalf("seeded %d rows, want 2", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := StartBackground(ctx, db, nil, nil, RunnerOptions{
		// Immediately, so the test does not wait a minute — or a day.
		RetentionCatchUpDelay: time.Nanosecond,
	})
	defer stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countRows(t, db, wsID) == 1 {
			return // the 90-day-old row is gone, the recent one stayed
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("boot sweep did not run: still %d rows after 3s", countRows(t, db, wsID))
}

func TestStartBackground_CatchUpCanBeDisabled(t *testing.T) {
	db, blobRoot, _ := retentionCoordRig(t)
	const wsID = "ws_nocatchup"
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, slug, memory_config) VALUES(?, 'No catch up', 'nocatchup', '{"versions_retention_days":7}')`,
		wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	seedRow(t, db, blobRoot, wsID, "AGENT.md", "aaaaaaaabbbbcccc", 90)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := StartBackground(ctx, db, nil, nil, RunnerOptions{
		// Negative disables it — an operator who wants deletions to happen
		// only in the maintenance window must be able to say so.
		RetentionCatchUpDelay: -1,
	})
	defer stop()

	time.Sleep(200 * time.Millisecond)
	if got := countRows(t, db, wsID); got != 1 {
		t.Fatalf("rows = %d, want 1 — the sweep ran despite being disabled", got)
	}
}
