package policy

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"

	_ "modernc.org/sqlite"
)

// TestResolver_LoadsFromDB_AndCaches verifies a hot path: first
// Resolve hits the DB, subsequent calls within the TTL come from
// cache (skipping the DB entirely). Without caching every behavior
// monitor + memory write + skill create would issue a SELECT per
// call, which gets expensive fast on busy crews.
func TestResolver_LoadsFromDB_AndCaches(t *testing.T) {
	db := setupResolverDB(t)
	defer db.Close()
	seedCrew(t, db, "cr1", "trusted", "warn")

	cnt := &counting{db: db}
	r := NewResolverWithClock(cnt, func() time.Time { return time.Unix(1_700_000_000, 0) })

	for i := 0; i < 5; i++ {
		p, err := r.Resolve(context.Background(), "cr1")
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if p.AutonomyLevel != AutonomyTrusted {
			t.Errorf("iter %d: got %s, want trusted", i, p.AutonomyLevel)
		}
		if p.BehaviorMode != BehaviorWarn {
			t.Errorf("iter %d: got %s, want warn", i, p.BehaviorMode)
		}
	}

	if cnt.queries != 1 {
		t.Errorf("expected 1 DB query (4 cache hits), got %d", cnt.queries)
	}
}

// TestResolver_TTLExpires forces a clock advance past the 10-second
// TTL and asserts the next Resolve refetches.
func TestResolver_TTLExpires(t *testing.T) {
	db := setupResolverDB(t)
	defer db.Close()
	seedCrew(t, db, "cr1", "guided", "warn")

	now := time.Unix(1_700_000_000, 0)
	clock := &fakeClock{now: now}
	cnt := &counting{db: db}
	r := NewResolverWithClock(cnt, clock.Now)

	if _, err := r.Resolve(context.Background(), "cr1"); err != nil {
		t.Fatal(err)
	}
	clock.now = now.Add(15 * time.Second) // past the 10s TTL
	if _, err := r.Resolve(context.Background(), "cr1"); err != nil {
		t.Fatal(err)
	}

	if cnt.queries != 2 {
		t.Errorf("expected 2 DB queries (TTL expired between them), got %d", cnt.queries)
	}
}

// TestResolver_Invalidate drops the cache entry so the next Resolve
// refetches even within the TTL. Called by the API PATCH handler
// after updating policy so callers see fresh state immediately.
func TestResolver_Invalidate(t *testing.T) {
	db := setupResolverDB(t)
	defer db.Close()
	seedCrew(t, db, "cr1", "guided", "warn")

	cnt := &counting{db: db}
	r := NewResolver(cnt)

	if _, err := r.Resolve(context.Background(), "cr1"); err != nil {
		t.Fatal(err)
	}
	r.Invalidate("cr1")
	if _, err := r.Resolve(context.Background(), "cr1"); err != nil {
		t.Fatal(err)
	}
	if cnt.queries != 2 {
		t.Errorf("expected 2 DB queries after Invalidate, got %d", cnt.queries)
	}
}

// TestResolver_UnknownCrew_DefaultsToGuided returns the documented
// safe default (guided/warn) when the crew row is missing. Defensive
// posture: a transient race (crew deleted between SELECT and our
// call) should not crash the caller.
func TestResolver_UnknownCrew_DefaultsToGuided(t *testing.T) {
	db := setupResolverDB(t)
	defer db.Close()

	r := NewResolver(db)
	p, err := r.Resolve(context.Background(), "missing-crew")
	if err != nil {
		t.Fatal(err)
	}
	if p.AutonomyLevel != AutonomyGuided || p.BehaviorMode != BehaviorWarn {
		t.Errorf("missing crew should default to guided/warn; got %s/%s", p.AutonomyLevel, p.BehaviorMode)
	}
}

// TestResolver_RejectsInvalidStoredData uses a stub queryRower that
// returns a bogus autonomy_level, asserting the resolver's defense-
// in-depth Validate() call catches it. The v98 CHECK constraint
// already blocks this at insert time, but a future schema drift or
// manual SQL hot-patch could bypass — the resolver is the second
// gate that keeps a corrupt enum from reaching DecideAction (where
// the default-fallthrough would silently send everything through
// inbox approval, surprising the operator).
func TestResolver_RejectsInvalidStoredData(t *testing.T) {
	// Synthetic queryRower that returns bogus values without going
	// through the CHECK-enforced INSERT path.
	stub := &stubRower{
		level: "YOLO",
		mode:  "warn",
	}
	r := NewResolver(stub)
	_, err := r.Resolve(context.Background(), "cr_drift")
	if err == nil {
		t.Error("expected validation error for stored bogus autonomy_level, got nil")
	}
}

// stubRower implements queryRower by hand-crafting a *sql.Row whose
// Scan returns the configured values. Used by
// TestResolver_RejectsInvalidStoredData to exercise the validation
// path without persisting the bogus row.
type stubRower struct {
	db          *sql.DB
	level, mode string
}

func (s *stubRower) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	// Build a transient in-memory DB on first call so we can hand
	// back a real *sql.Row pre-loaded with our bogus values. This
	// is simpler than mocking sql.Row directly (which has private
	// internals).
	if s.db == nil {
		d, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic(err)
		}
		if _, err := d.Exec(`CREATE TABLE t (level TEXT, mode TEXT, set_by TEXT, set_at TEXT, reason TEXT)`); err != nil {
			panic(err)
		}
		if _, err := d.Exec(`INSERT INTO t VALUES (?, ?, NULL, NULL, NULL)`, s.level, s.mode); err != nil {
			panic(err)
		}
		s.db = d
	}
	return s.db.QueryRowContext(ctx, `SELECT level, mode, set_by, set_at, reason FROM t LIMIT 1`)
}

// --- helpers ---

type counting struct {
	db      *sql.DB
	queries int
}

func (c *counting) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.queries++
	return c.db.QueryRowContext(ctx, q, args...)
}

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }

func setupResolverDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := database.Open("file:" + filepath.Join(t.TempDir(), "policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := database.Migrate(context.Background(), d.DB, migLogger); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)
	return d.DB
}

func seedCrew(t *testing.T, db *sql.DB, id, level, mode string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, behavior_mode) VALUES (?, 'ws1', ?, ?, ?, ?)`,
		id, id+"-name", id, level, mode,
	)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
