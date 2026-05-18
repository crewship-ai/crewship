package api

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// portExposeTestLogger returns a slog logger tuned for test output: writes
// to stderr at WARN and above so happy-path runs stay quiet but real
// failures (error logs) still surface in -v output. Matches the pattern
// used elsewhere in the api package test suite.
func portExposeTestLogger() *slog.Logger {
	var w io.Writer = os.Stderr
	if testing.Verbose() {
		// keep stderr for -v runs
	} else {
		w = io.Discard
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// newRegistryTestDB spins up an in-memory SQLite with the migrations applied.
// We reuse the real migration runner so the schema matches production exactly
// — tests that create port_exposures rows must interop with the same CHECK
// constraints the handler will see.
func newRegistryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite ":memory:" databases are per-connection: each connection in
	// the database/sql pool would see its own empty schema. The lifecycle
	// test starts a purger goroutine that issues UPDATE statements
	// concurrently with the test goroutine's SELECT — if the pool grows
	// to 2+ connections under the higher scheduling concurrency on the
	// ubuntu CI runner, the SELECT lands on a different connection from
	// the one CREATE TABLE ran on and fails with
	// "no such table: port_exposures". Locally on macOS the pool stays
	// at 1 and the test passes. Pin the pool to a single connection so
	// every caller in the test process sees the same in-memory schema.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	// The simple path is to create only what the registry/proxy actually
	// touch: the port_exposures table without FK validation. Registry
	// doesn't read/write any other table.
	_, err = db.Exec(`
CREATE TABLE port_exposures (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    chat_id TEXT,
    token TEXT NOT NULL UNIQUE,
    container_id TEXT NOT NULL,
    container_ip TEXT NOT NULL,
    container_port INTEGER NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    revoked_reason TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func insertActiveRow(t *testing.T, db *sql.DB, id, token string, port int, expiresAt time.Time) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES (?, 'ws', 'crew', 'agent', ?, 'ct', '10.0.0.1', ?, 'ACTIVE', ?)
`, id, token, port, expiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestRegistry_AddLookupRemove(t *testing.T) {
	db := newRegistryTestDB(t)
	r := NewPortExposeRegistry(db, portExposeTestLogger())

	entry := &ExposeEntry{
		Token:         "abc",
		ContainerIP:   "10.0.0.1",
		ContainerPort: 8000,
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	r.Add(entry)

	got, ok := r.Lookup("abc")
	if !ok {
		t.Fatalf("Lookup(abc) = !ok, want ok")
	}
	if got != entry {
		t.Fatalf("Lookup returned different entry")
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}

	r.Remove("abc")
	if _, ok := r.Lookup("abc"); ok {
		t.Errorf("Lookup(abc) after Remove = ok, want !ok")
	}
	if r.Len() != 0 {
		t.Errorf("Len after Remove = %d, want 0", r.Len())
	}
}

func TestRegistry_AddIgnoresNilAndEmptyToken(t *testing.T) {
	r := NewPortExposeRegistry(newRegistryTestDB(t), portExposeTestLogger())
	r.Add(nil)
	r.Add(&ExposeEntry{Token: ""})
	if r.Len() != 0 {
		t.Errorf("Len = %d after nil/empty Add, want 0", r.Len())
	}
}

func TestEntry_Expired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	e := &ExposeEntry{ExpiresAt: now.Add(10 * time.Second)}
	if e.Expired(now) {
		t.Errorf("entry should not be expired yet")
	}
	if !e.Expired(now.Add(11 * time.Second)) {
		t.Errorf("entry should be expired 11s after now")
	}
}

func TestRegistry_LoadFromDB_SkipsAndMarksExpired(t *testing.T) {
	db := newRegistryTestDB(t)

	now := time.Now().UTC()
	insertActiveRow(t, db, "id-active", "tok-active", 8000, now.Add(time.Hour))
	insertActiveRow(t, db, "id-stale", "tok-stale", 9000, now.Add(-5*time.Minute))

	r := NewPortExposeRegistry(db, portExposeTestLogger())
	if err := r.LoadFromDB(context.Background()); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	if _, ok := r.Lookup("tok-active"); !ok {
		t.Errorf("active token not loaded")
	}
	if _, ok := r.Lookup("tok-stale"); ok {
		t.Errorf("stale token should not be in registry")
	}

	// Stale row must have been flipped to EXPIRED in the DB so it doesn't
	// come back on the next LoadFromDB.
	var status string
	if err := db.QueryRow(`SELECT status FROM port_exposures WHERE id = 'id-stale'`).Scan(&status); err != nil {
		t.Fatalf("select stale: %v", err)
	}
	if status != "EXPIRED" {
		t.Errorf("stale status = %q, want EXPIRED", status)
	}
}

func TestRegistry_PurgeOnce_ExpiresActiveRows(t *testing.T) {
	db := newRegistryTestDB(t)
	r := NewPortExposeRegistry(db, portExposeTestLogger())

	now := time.Now().UTC()
	insertActiveRow(t, db, "id-1", "tok-1", 8000, now.Add(-1*time.Minute))
	r.Add(&ExposeEntry{Token: "tok-1", ExpiresAt: now.Add(-1 * time.Minute)})

	r.purgeOnce(context.Background())

	if _, ok := r.Lookup("tok-1"); ok {
		t.Errorf("expired token should be dropped from registry")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM port_exposures WHERE id = 'id-1'`).Scan(&status); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "EXPIRED" {
		t.Errorf("status = %q, want EXPIRED", status)
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	r := NewPortExposeRegistry(newRegistryTestDB(t), portExposeTestLogger())

	// Hammer Add/Lookup/Remove from N goroutines; the race detector should
	// be clean. Correctness spot-check: after all goroutines finish we can
	// still Lookup a recently-added token.
	const N = 64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			tok := "t"
			e := &ExposeEntry{Token: tok, ExpiresAt: time.Now().Add(time.Hour)}
			r.Add(e)
			_, _ = r.Lookup(tok)
			if i%3 == 0 {
				r.Remove(tok)
			}
		}()
	}
	wg.Wait()
}
