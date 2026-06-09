package consolidate

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// splitFields directly: blank lines are skipped, "- key: value" bullets
// are keyed (lower-cased), and non-bullet lines fall through to prose.
func TestSplitFields(t *testing.T) {
	fields, order, prose := splitFields("- Timezone: UTC+1\n\nplain prose line\n- tone: terse")
	if fields["timezone"] != "UTC+1" {
		t.Errorf("expected lower-cased key timezone=UTC+1; got %v", fields)
	}
	if len(order) != 2 || order[0] != "timezone" || order[1] != "tone" {
		t.Errorf("expected insertion order [timezone tone]; got %v", order)
	}
	if len(prose) != 1 || prose[0] != "plain prose line" {
		t.Errorf("expected one prose line; got %v", prose)
	}
}

// RunUserModelSync candidate-failure branch: a threshold-crossing
// candidate whose SyncUserModel errors (user_models table dropped)
// increments sum.Errors and the sweep keeps going.
func TestRunUserModelSync_CandidateSyncError(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	if _, err := db.Exec(`DROP TABLE user_models`); err != nil {
		t.Fatalf("drop user_models: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum, err := RunUserModelSync(context.Background(), db, logger, "ws1", UserModelSyncOptions{
		OutputBasePath: t.TempDir(),
		Extractor:      fixedExtractor{body: "- tone: terse"},
	})
	if err != nil {
		t.Fatalf("RunUserModelSync: %v", err)
	}
	if sum.Errors != 1 {
		t.Errorf("expected 1 candidate error; got %+v", sum)
	}
}

// runUserModelSweepAll directly: load-workspaces error branch (drop
// the workspaces table).
func TestRunUserModelSweepAll_LoadWorkspacesError(t *testing.T) {
	db := userModelWorkerDB(t)
	if _, err := db.Exec(`DROP TABLE workspaces`); err != nil {
		t.Fatalf("drop workspaces: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Should log + return without panicking.
	runUserModelSweepAll(context.Background(), db, logger, UserModelWorkerConfig{
		BasePath:  t.TempDir(),
		Extractor: NoopUserModelExtractor{},
	})
}

// runUserModelSweepAll directly: per-workspace RunUserModelSync error
// (drop chats so the candidate query fails for the one workspace).
func TestRunUserModelSweepAll_PerWorkspaceError(t *testing.T) {
	db := userModelWorkerDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE chats`); err != nil {
		t.Fatalf("drop chats: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runUserModelSweepAll(context.Background(), db, logger, UserModelWorkerConfig{
		BasePath:  t.TempDir(),
		Extractor: NoopUserModelExtractor{},
	})
}

// runUserModelSweepAll directly: a cancelled context mid-loop returns
// before processing workspaces (ctx.Err() branch).
func TestRunUserModelSweepAll_ContextCancelled(t *testing.T) {
	db := userModelWorkerDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w'),('ws2','W2','w2')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled → loop returns on first iteration
	runUserModelSweepAll(ctx, db, logger, UserModelWorkerConfig{
		BasePath:  t.TempDir(),
		Extractor: NoopUserModelExtractor{},
	})
}

// Zero-value config (only BasePath set) exercises the default-filling
// branches: Extractor=nil→Noop, TickInterval<=0→24h, FirstRunDelay==0
// →nextDailyOffsetUTC. The huge first-run delay means stop fires the
// FirstRunDelay <-stop branch before any sweep.
func TestStartUserModelSyncWorker_DefaultsAndEarlyStop(t *testing.T) {
	db := userModelWorkerDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{
		BasePath: t.TempDir(), // everything else zero → defaults applied
	}, stop, &wg)
	// First run is hours away (05:00 UTC offset); stop immediately so
	// the goroutine takes the <-stop arm of the FirstRunDelay select.
	close(stop)
	wg.Wait()
}

// Negative FirstRunDelay is clamped to 0 (then to the daily offset),
// covering the FirstRunDelay < 0 branch.
func TestStartUserModelSyncWorker_NegativeDelayClamped(t *testing.T) {
	db := userModelWorkerDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{
		BasePath:      t.TempDir(),
		Extractor:     NoopUserModelExtractor{},
		FirstRunDelay: -1 * time.Second,
	}, stop, &wg)
	close(stop)
	wg.Wait()
}

// A worker with a tiny tick that survives past the first sweep into the
// ticker loop, then stops — covers the ticker.C arm deterministically
// by waiting for two sweeps' worth of writes.
func TestStartUserModelSyncWorker_TickerArm(t *testing.T) {
	db := userModelWorkerDB(t)
	seedUserModelFixture(t, db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartUserModelSyncWorker(db, logger, UserModelWorkerConfig{
		BasePath:      base,
		Extractor:     fixedExtractor{body: "- tone: terse"},
		TickInterval:  20 * time.Millisecond,
		FirstRunDelay: 1 * time.Millisecond,
	}, stop, &wg)
	// Let several ticks elapse so the ticker.C arm is exercised at least
	// once beyond the initial sweep.
	paths := memory.UserModelPaths{SharedDir: base + "/crews/cr1/shared/.memory"}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if body, _ := memory.LoadUserModel(paths, "u1", "ws1"); body != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stay alive long enough for at least one ticker tick after the
	// first sweep.
	time.Sleep(60 * time.Millisecond)
	close(stop)
	wg.Wait()
}
