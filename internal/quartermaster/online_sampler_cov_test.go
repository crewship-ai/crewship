package quartermaster

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// erroringDSLResolver fails every lookup — models a pipeline.Store outage.
type erroringDSLResolver struct{}

func (erroringDSLResolver) GetDSLByPipelineID(context.Context, string) (*pipeline.DSL, error) {
	return nil, errors.New("resolver down")
}

func fullRateResolver(pipelineID string) *fakeDSLResolver {
	return &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		pipelineID: {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
			SampleRate:      1.0,
			GraderAgentSlug: "qa-grader",
		}}},
	}}
}

func evalRunCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&n); err != nil {
		t.Fatalf("count eval_runs: %v", err)
	}
	return n
}

// TestNewOnlineSampler_RequiresDeps pins the constructor contract: a nil
// DB or nil DSLResolver is a hard error, and missing logger/interval get
// safe defaults instead of zero values.
func TestNewOnlineSampler_RequiresDeps(t *testing.T) {
	db := openSamplerTestDB(t)
	resolver := &fakeDSLResolver{}

	if _, err := NewOnlineSampler(SamplerConfig{DSLResolver: resolver}); err == nil {
		t.Error("nil DB accepted; want error")
	}
	if _, err := NewOnlineSampler(SamplerConfig{DB: db}); err == nil {
		t.Error("nil DSLResolver accepted; want error")
	}

	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver})
	if err != nil {
		t.Fatalf("minimal config rejected: %v", err)
	}
	if s.interval != time.Minute {
		t.Errorf("default interval = %v, want 1m", s.interval)
	}
	if s.logger == nil {
		t.Error("default logger not assigned")
	}
	if s.watermark.IsZero() {
		t.Error("startup watermark not initialized")
	}
}

// TestOnlineSampler_StartTicksAndStops drives the real Start loop: the
// immediate first tick must enqueue the seeded row, the ticker must keep
// firing until ctx cancellation, and Start must return promptly when the
// context dies. A second Start call after the first completed must be a
// no-op (startOnce) and return without spawning another loop.
func TestOnlineSampler_StartTicksAndStops(t *testing.T) {
	db := openSamplerTestDB(t)
	s, err := NewOnlineSampler(SamplerConfig{
		DB:          db,
		DSLResolver: fullRateResolver("pl-1"),
		Interval:    5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-start", "pl-1", "nightly", time.Now().UTC())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	// Wait (bounded) for the first tick to land the enqueue.
	deadline := time.Now().Add(2 * time.Second)
	for evalRunCount(t, db) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Start never enqueued the seeded run")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Let at least one ticker-driven tick fire (covers the <-t.C branch).
	time.Sleep(20 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}

	// Watermark advanced past the row; no duplicate despite many ticks.
	if got := evalRunCount(t, db); got != 1 {
		t.Errorf("eval_runs after repeated ticks = %d, want 1", got)
	}

	// Second Start is a no-op thanks to sync.Once — must return fast even
	// with a live (non-cancelled) context.
	begin := time.Now()
	s.Start(context.Background())
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Errorf("second Start took %v; want immediate no-op", elapsed)
	}
}

// TestOnlineSampler_TickBackoffSkips pins the entropy-outage backoff
// consumption: with backoffSkips=2 the next two ticks must NOT run
// runOnce (eligible row stays unenqueued), and the third tick processes
// normally.
func TestOnlineSampler_TickBackoffSkips(t *testing.T) {
	db := openSamplerTestDB(t)
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: fullRateResolver("pl-1"), Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-skip", "pl-1", "nightly", time.Now().UTC())

	s.backoffSkips = 2

	s.tickWithBackoff(context.Background())
	if got := evalRunCount(t, db); got != 0 {
		t.Fatalf("tick during backoff enqueued %d rows, want 0", got)
	}
	if s.backoffSkips != 1 {
		t.Errorf("backoffSkips after first skipped tick = %d, want 1", s.backoffSkips)
	}

	s.tickWithBackoff(context.Background())
	if got := evalRunCount(t, db); got != 0 {
		t.Fatalf("second backoff tick enqueued %d rows, want 0", got)
	}
	if s.backoffSkips != 0 {
		t.Errorf("backoffSkips after second skipped tick = %d, want 0", s.backoffSkips)
	}

	// Backoff drained — the third tick runs runOnce and enqueues.
	s.tickWithBackoff(context.Background())
	if got := evalRunCount(t, db); got != 1 {
		t.Errorf("post-backoff tick enqueued %d rows, want 1", got)
	}
}

// TestOnlineSampler_CleanTickClearsResidualBackoff pins the recovery
// rule: a successful runOnce zeroes any residual backoffSkips so healthy
// steady-state operation doesn't keep paying for an old outage.
func TestOnlineSampler_CleanTickClearsResidualBackoff(t *testing.T) {
	db := openSamplerTestDB(t)
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: &fakeDSLResolver{}, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	s.backoffSkips = 7
	s.runOnce(context.Background())
	if s.backoffSkips != 0 {
		t.Errorf("backoffSkips after clean tick = %d, want 0", s.backoffSkips)
	}
}

// TestOnlineSampler_ScanFailureLeavesWatermark pins the fail-safe on a
// broken scan query (table missing, DB down): runOnce must return without
// panicking and without corrupting the watermark.
func TestOnlineSampler_ScanFailureLeavesWatermark(t *testing.T) {
	db := openSamplerTestDB(t)
	if _, err := db.Exec(`DROP TABLE pipeline_runs`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: &fakeDSLResolver{}, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	before := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	s.watermark = before
	s.watermarkID = "wm-id"

	s.runOnce(context.Background())

	if !s.watermark.Equal(before) || s.watermarkID != "wm-id" {
		t.Errorf("watermark moved on scan failure: %v / %q", s.watermark, s.watermarkID)
	}
}

// TestOnlineSampler_ResolverErrorRetriesNextTick pins the retryable-error
// contract: a DSL resolver outage holds the watermark so the SAME row is
// re-scanned and successfully enqueued once the resolver recovers.
func TestOnlineSampler_ResolverErrorRetriesNextTick(t *testing.T) {
	db := openSamplerTestDB(t)
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: erroringDSLResolver{}, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	start := time.Now().Add(-1 * time.Hour).UTC()
	s.watermark = start
	seedRun(t, db, "prn-retry", "pl-1", "nightly", time.Now().UTC())

	s.runOnce(context.Background())
	if got := evalRunCount(t, db); got != 0 {
		t.Fatalf("resolver outage still enqueued %d rows", got)
	}
	if !s.watermark.Equal(start) {
		t.Fatalf("watermark advanced past a failed row: %v", s.watermark)
	}

	// Resolver recovers — the held watermark makes the next tick retry.
	s.dslResolver = fullRateResolver("pl-1")
	s.runOnce(context.Background())
	if got := evalRunCount(t, db); got != 1 {
		t.Errorf("recovered tick enqueued %d rows, want 1", got)
	}
	if s.watermark.Equal(start) {
		t.Error("watermark did not advance after successful retry")
	}
}

// TestOnlineSampler_EnqueueErrorRetriesNextTick pins the INSERT-failure
// path: a broken eval_runs table (any non-UNIQUE error) is retryable.
// The watermark holds, and the row is enqueued once the table is back.
func TestOnlineSampler_EnqueueErrorRetriesNextTick(t *testing.T) {
	db := openSamplerTestDB(t)
	if _, err := db.Exec(`DROP TABLE eval_runs`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: fullRateResolver("pl-1"), Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	start := time.Now().Add(-1 * time.Hour).UTC()
	s.watermark = start
	seedRun(t, db, "prn-insfail", "pl-1", "nightly", time.Now().UTC())

	s.runOnce(context.Background())
	if !s.watermark.Equal(start) {
		t.Fatalf("watermark advanced past failed INSERT: %v", s.watermark)
	}

	// Restore the table; the next tick must pick the row back up.
	if _, err := db.Exec(`CREATE TABLE eval_runs (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		routine_slug TEXT,
		pipeline_run_id TEXT,
		sample_rate REAL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("recreate eval_runs: %v", err)
	}
	s.runOnce(context.Background())
	if got := evalRunCount(t, db); got != 1 {
		t.Errorf("retry tick enqueued %d rows, want 1", got)
	}
}

// TestOnlineSampler_FractionalRate covers both arms of the rate < 1.0
// sampling branch with probabilistically-certain rates: a rate within
// 1e-9 of 1.0 must enqueue (P[miss] ≈ 1e-9) and a 1e-12 rate must skip
// (P[hit] ≈ 1e-12) while STILL advancing the watermark — a skipped row
// is handled, not deferred.
func TestOnlineSampler_FractionalRate(t *testing.T) {
	t.Run("near-one rate enqueues", func(t *testing.T) {
		db := openSamplerTestDB(t)
		resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
			"pl-1": {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
				SampleRate:      1 - 1e-9,
				GraderAgentSlug: "qa",
			}}},
		}}
		s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
		if err != nil {
			t.Fatalf("ctor: %v", err)
		}
		s.watermark = time.Now().Add(-1 * time.Hour).UTC()
		seedRun(t, db, "prn-hi", "pl-1", "nightly", time.Now().UTC())
		s.runOnce(context.Background())
		if got := evalRunCount(t, db); got != 1 {
			t.Errorf("rate≈1.0 enqueued %d, want 1", got)
		}
	})

	t.Run("near-zero rate skips but advances", func(t *testing.T) {
		db := openSamplerTestDB(t)
		resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
			"pl-1": {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
				SampleRate:      1e-12,
				GraderAgentSlug: "qa",
			}}},
		}}
		s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
		if err != nil {
			t.Fatalf("ctor: %v", err)
		}
		s.watermark = time.Now().Add(-1 * time.Hour).UTC()
		seedRun(t, db, "prn-lo", "pl-1", "nightly", time.Now().UTC())
		s.runOnce(context.Background())
		if got := evalRunCount(t, db); got != 0 {
			t.Errorf("rate≈0 enqueued %d, want 0", got)
		}
		// Deterministic skip advances the watermark to the handled row.
		if s.watermarkID != "prn-lo" {
			t.Errorf("watermarkID = %q, want prn-lo (skip must advance)", s.watermarkID)
		}
	})
}

// TestOnlineSampler_GarbageTimestampNeverAdvancesWatermark pins the
// defensive parse on watermark advancement: a row whose ended_at string
// is unparseable must not poison the in-memory watermark — the sampler
// keeps the previous (valid) value rather than advancing to garbage.
func TestOnlineSampler_GarbageTimestampNeverAdvancesWatermark(t *testing.T) {
	db := openSamplerTestDB(t)
	// Routine without eval config → deterministic advance path, which is
	// exactly where the garbage timestamp would be adopted.
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: &fakeDSLResolver{}, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	s.watermark = old

	// "2020-junk" sorts between the year-2000 watermark string and the
	// current scanEnd string, so the row IS selected — but time.Parse
	// rejects it.
	if _, err := db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, ended_at)
		VALUES ('prn-garbage', 'ws1', 'pl-1', 'nightly', 'completed', '2020-junk')`); err != nil {
		t.Fatalf("seed garbage row: %v", err)
	}

	s.runOnce(context.Background())

	if !s.watermark.Equal(old) {
		t.Errorf("watermark adopted garbage timestamp: %v (want %v)", s.watermark, old)
	}
}
