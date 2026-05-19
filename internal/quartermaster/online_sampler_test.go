package quartermaster

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	_ "modernc.org/sqlite"
)

func openSamplerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Pin to one connection so all reads + writes hit the same
	// in-memory database. Without this, database/sql's pool can hand
	// out a fresh connection for the second Exec which sees an empty
	// `:memory:` and fails with "no such table" — a long-standing
	// gotcha that's bitten this repo before.
	db.SetMaxOpenConns(1)
	// Run each CREATE TABLE separately so we get a clear error if any
	// one of them fails, rather than the silent-mid-statement-abort
	// behaviour some SQLite driver builds exhibit on multi-statement
	// Exec.
	stmts := []string{
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT, slug TEXT)`,
		`CREATE TABLE pipeline_runs (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			pipeline_id TEXT NOT NULL,
			pipeline_slug TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at TEXT,
			completed_at TEXT
		)`,
		`CREATE TABLE eval_runs (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('replay','regression','online')),
			status TEXT NOT NULL,
			routine_slug TEXT,
			pipeline_run_id TEXT,
			trace_id TEXT,
			sample_rate REAL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
	}
	for i, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("schema stmt %d: %v", i, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'W', 'w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// fakeDSLResolver maps pipeline IDs to DSLs the sampler should read.
type fakeDSLResolver struct {
	byID map[string]*pipeline.DSL
}

func (f *fakeDSLResolver) GetDSLByPipelineID(_ context.Context, id string) (*pipeline.DSL, error) {
	return f.byID[id], nil
}

type samplerTestEmitter struct {
	entries []journal.Entry
}

func (r *samplerTestEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.entries = append(r.entries, e)
	return "j", nil
}
func (r *samplerTestEmitter) Flush(context.Context) error { return nil }

func seedRun(t *testing.T, db *sql.DB, id, pipelineID, slug string, completedAt time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, completed_at)
        VALUES (?, 'ws1', ?, ?, 'completed', ?)`,
		id, pipelineID, slug, completedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
}

// TestOnlineSampler_FullRateEnqueuesEveryRun pins the sample_rate=1.0
// path: every completed run with online eval enabled becomes a queued
// eval_runs row. This is the most explicit assertion of the
// pipeline_runs → eval_runs link.
func TestOnlineSampler_FullRateEnqueuesEveryRun(t *testing.T) {
	db := openSamplerTestDB(t)
	em := &samplerTestEmitter{}

	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {
			Name: "nightly",
			Eval: &pipeline.EvalConfig{
				Online: &pipeline.OnlineEvalConfig{
					SampleRate:      1.0,
					GraderAgentSlug: "qa-grader",
				},
			},
		},
	}}

	s, err := NewOnlineSampler(SamplerConfig{
		DB:          db,
		Emitter:     em,
		DSLResolver: resolver,
		Interval:    time.Hour,
	})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	// Start watermark in the past so seeded rows fall inside the window.
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-1", "pl-1", "nightly", time.Now().UTC())
	seedRun(t, db, "prn-2", "pl-1", "nightly", time.Now().UTC())

	s.runOnce(context.Background())

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs WHERE kind = 'online'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("eval_runs at rate=1.0 = %d, want 2", count)
	}
	if len(em.entries) != 2 {
		t.Errorf("journal entries = %d, want 2", len(em.entries))
	}
	if em.entries[0].Type != journal.EntryEvalRunStarted {
		t.Errorf("entry type = %s, want eval.run_started", em.entries[0].Type)
	}
}

// TestOnlineSampler_ZeroRateEnqueuesNothing pins the off switch:
// SampleRate=0 means a routine has eval.online declared but disabled,
// and not a single run should be enqueued. This is the difference
// between "no Eval config" and "Eval config with rate=0" and matters
// because operators toggle rate to 0 to pause grading during incidents
// without editing the whole routine.
func TestOnlineSampler_ZeroRateEnqueuesNothing(t *testing.T) {
	db := openSamplerTestDB(t)
	em := &samplerTestEmitter{}

	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {
			Eval: &pipeline.EvalConfig{
				Online: &pipeline.OnlineEvalConfig{SampleRate: 0, GraderAgentSlug: "qa"},
			},
		},
	}}
	s, errCtor := NewOnlineSampler(SamplerConfig{DB: db, Emitter: em, DSLResolver: resolver, Interval: time.Hour})
	if errCtor != nil {
		t.Fatalf("ctor: %v", errCtor)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-1", "pl-1", "nightly", time.Now().UTC())

	s.runOnce(context.Background())

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM eval_runs WHERE kind = 'online'`).Scan(&count)
	if count != 0 {
		t.Errorf("rate=0 enqueued %d, want 0", count)
	}
}

// TestOnlineSampler_NoEvalConfigSkips pins that a routine without any
// Eval.Online section is silently skipped — the sampler doesn't error,
// doesn't enqueue, doesn't emit. This is the most common case (most
// routines aren't graded), so a regression that made it dispatch
// would be a huge cost spike on every interval.
func TestOnlineSampler_NoEvalConfigSkips(t *testing.T) {
	db := openSamplerTestDB(t)
	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {Name: "nightly"}, // no Eval at all
	}}
	s, errCtor := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
	if errCtor != nil {
		t.Fatalf("ctor: %v", errCtor)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-1", "pl-1", "nightly", time.Now().UTC())

	s.runOnce(context.Background())

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&count)
	if count != 0 {
		t.Errorf("no-config routine enqueued %d, want 0", count)
	}
}

// TestOnlineSampler_WatermarkAdvances pins the catch-up behaviour:
// after a tick covering (watermark, scanEnd), the next tick must scan
// past the last handled row. Re-scanning the same window would
// multi-enqueue every sampled run. Plus the UNIQUE index on
// (kind='online', pipeline_run_id) defends against multi-enqueue at
// the schema layer; this test verifies the watermark itself moves so
// we don't lean on the UNIQUE constraint to mask a watermark bug.
func TestOnlineSampler_WatermarkAdvances(t *testing.T) {
	db := openSamplerTestDB(t)
	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
			SampleRate:      1.0,
			GraderAgentSlug: "qa-grader",
		}}},
	}}
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()

	// First tick — one row, expect 1 enqueue.
	seedRun(t, db, "prn-1", "pl-1", "nightly", time.Now().UTC())
	s.runOnce(context.Background())

	// Second tick — same row, watermark should have advanced past it.
	// No new rows mean no new enqueues.
	s.runOnce(context.Background())

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("watermark didn't advance — got %d enqueues, want 1", count)
	}
}

// TestOnlineSampler_NoGraderSkips pins the new contract: a routine
// with Eval.Online.SampleRate > 0 but no GraderAgentSlug is a
// misconfiguration the sampler treats as a deterministic skip (not
// a retryable error) so it doesn't loop on the same routine every
// tick forever. Without this check the row would land in eval_runs
// with an empty grader_agent_slug and the grader worker would no-op
// + log on every poll.
func TestOnlineSampler_NoGraderSkips(t *testing.T) {
	db := openSamplerTestDB(t)
	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
			SampleRate: 1.0, // no GraderAgentSlug
		}}},
	}}
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-1", "pl-1", "nightly", time.Now().UTC())
	s.runOnce(context.Background())

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("missing grader still enqueued %d, want 0", count)
	}
}

// TestOnlineSampler_DuplicateRunNoDoubleEnqueue pins the UNIQUE index
// idempotency guard. The sampler is supposed to be safe to start
// twice (HA + accidental restart, watermark rewind on crash). The
// schema-level UNIQUE INDEX makes two enqueues against the same
// pipeline_run_id collapse to one row.
func TestOnlineSampler_DuplicateRunNoDoubleEnqueue(t *testing.T) {
	db := openSamplerTestDB(t)
	// Apply the same partial unique index the v96 migration creates.
	if _, err := db.Exec(`CREATE UNIQUE INDEX uq_eval_runs_online_pipeline_run
        ON eval_runs(pipeline_run_id) WHERE kind = 'online' AND pipeline_run_id IS NOT NULL`); err != nil {
		t.Fatalf("apply unique index: %v", err)
	}
	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
			SampleRate:      1.0,
			GraderAgentSlug: "qa-grader",
		}}},
	}}
	s, err := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
	if err != nil {
		t.Fatalf("ctor: %v", err)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	seedRun(t, db, "prn-dup", "pl-1", "nightly", time.Now().UTC())

	s.runOnce(context.Background())
	// Roll the watermark back and re-run — simulates a crash-recovery
	// or a duplicate sampler instance pointing at the same DB.
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()
	s.runOnce(context.Background())

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs WHERE pipeline_run_id = 'prn-dup'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("UNIQUE index didn't prevent double-enqueue: count=%d, want 1", count)
	}
}

// TestOnlineSampler_SameTimestampSiblings is the regression test for a
// pagination bug: when two pipeline_runs share an EXACT completed_at
// (parallel fan-out steps finishing in the same scheduler tick is the
// realistic case), a timestamp-only cursor advanced past one of them
// orphaned the others forever. The (completed_at, id) tuple cursor +
// `WHERE (ts > ? OR (ts = ? AND id > ?))` predicate + matching ORDER
// BY clauses fix it. This test wires three siblings at the same
// nanosecond and asserts all three are enqueued.
func TestOnlineSampler_SameTimestampSiblings(t *testing.T) {
	db := openSamplerTestDB(t)
	if _, err := db.Exec(`CREATE UNIQUE INDEX uq_eval_runs_online_pipeline_run
        ON eval_runs(pipeline_run_id) WHERE kind = 'online' AND pipeline_run_id IS NOT NULL`); err != nil {
		t.Fatalf("apply unique index: %v", err)
	}
	resolver := &fakeDSLResolver{byID: map[string]*pipeline.DSL{
		"pl-1": {Eval: &pipeline.EvalConfig{Online: &pipeline.OnlineEvalConfig{
			SampleRate:      1.0,
			GraderAgentSlug: "qa-grader",
		}}},
	}}
	s, errCtor := NewOnlineSampler(SamplerConfig{DB: db, DSLResolver: resolver, Interval: time.Hour})
	if errCtor != nil {
		t.Fatalf("ctor: %v", errCtor)
	}
	s.watermark = time.Now().Add(-1 * time.Hour).UTC()

	// Three pipeline_runs at the EXACT same completed_at nanosecond.
	// Ids sort alphabetically so the cursor advances "prn-a" → "prn-b"
	// → "prn-c" on successive iterations of the SAME page; without
	// the tuple predicate the second page query would set
	// completed_at > sharedTS and skip prn-b + prn-c forever.
	sharedTS := time.Now().UTC()
	seedRun(t, db, "prn-a", "pl-1", "nightly", sharedTS)
	seedRun(t, db, "prn-b", "pl-1", "nightly", sharedTS)
	seedRun(t, db, "prn-c", "pl-1", "nightly", sharedTS)

	s.runOnce(context.Background())

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM eval_runs WHERE kind = 'online'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 3 {
		t.Errorf("same-timestamp siblings enqueued %d, want 3 (pagination skipped some?)", count)
	}
}

// TestCryptoSample_DistributionRoughly bounds the crypto/rand sampler:
// 10000 draws should land between 30% and 70% under-0.5. Tighter bounds
// would create flaky tests; this is wide enough to never false-positive
// while still catching a bug like "always returns 0" or "always
// returns 1".
func TestCryptoSample_DistributionRoughly(t *testing.T) {
	below := 0
	const n = 10000
	for i := 0; i < n; i++ {
		v, ok := cryptoSample()
		if !ok {
			t.Fatalf("cryptoSample reported entropy outage on draw %d — host /dev/urandom unreachable?", i)
		}
		if v < 0.5 {
			below++
		}
	}
	if below < 3000 || below > 7000 {
		t.Errorf("cryptoSample distribution skewed: %d/%d below 0.5 (want 3000-7000)", below, n)
	}
}
