package database

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOpenKeepsPoolWarm pins the connection-pool sizing decided in #1817.
//
// database/sql defaults MaxIdleConns to 2. With MaxOpenConns(5) and no
// explicit idle setting, any burst above two concurrent statements ends with
// three connections being torn down on release and reopened on the next
// request — each reopen re-paying the full DSN pragma set (WAL handshake,
// 64 MiB page-cache allocation, 256 MiB mmap window).
//
// The assertion is deliberately timing-free: reserve every connection the
// pool is allowed to hand out, return them all, then look at the pool
// accounting. No SQL concurrency, no busy-timeout exposure — this must never
// join TestPeerMemoryEngineFor_Concurrent on the flaky list.
func TestOpenKeepsPoolWarm(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	maxOpen := db.Stats().MaxOpenConnections
	if maxOpen <= 0 {
		t.Fatalf("MaxOpenConnections = %d, want a positive cap", maxOpen)
	}

	ctx := context.Background()
	conns := make([]*sql.Conn, 0, maxOpen)
	for i := 0; i < maxOpen; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("reserve conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatalf("release conn %d: %v", i, err)
		}
	}

	// sql.Conn.Close returns the connection to the pool synchronously, so the
	// stats below are settled by the time the loop above finishes.
	stats := db.Stats()
	if stats.Idle != maxOpen {
		t.Errorf("Idle = %d after releasing %d connections, want %d — "+
			"MaxIdleConns is below MaxOpenConns, so the pool churns connections "+
			"and re-applies the DSN pragmas on every reopen (#1817)",
			stats.Idle, maxOpen, maxOpen)
	}
	if stats.MaxIdleClosed != 0 {
		t.Errorf("MaxIdleClosed = %d, want 0 — connections were discarded on "+
			"release because the idle pool was full (#1817)", stats.MaxIdleClosed)
	}
}

// TestOpenLeavesConnLifetimeUnset guards the other half of the #1817 decision:
// SetConnMaxLifetime and SetConnMaxIdleTime stay unset on purpose. The
// reasoning lives in database.go next to the pool setup; this test exists so
// that adding either one is a deliberate act with a test to update, not a
// drive-by "tidy up the pool config" commit.
//
// The knobs are write-only in database/sql's public API — sql.DBStats reports
// only how many connections an expiry has *already* closed, and that counter
// is bumped by the pool's cleaner goroutine, which never runs inside a short
// test. A behavioural assertion here would pass against any expiry long enough
// to matter, so it would have no teeth. Read the fields instead: reflect can
// read an unexported field's value through a typed accessor (only Interface()
// and Set are blocked).
//
// If a future stdlib renames the fields this fails rather than skipping. A
// skip would leave the guard silently doing nothing, which is the failure mode
// scripts/skip-budget.sh exists to price; and the trigger is not flaky — it
// can only fire on a deliberate Go toolchain bump, which is exactly when
// someone should re-derive this against the new database/sql.
func TestOpenLeavesConnLifetimeUnset(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "lifetime.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for _, tc := range []struct{ field, setter, why string }{
		{"maxLifetime", "SetConnMaxLifetime",
			"nothing on the far end of a local file handle expires it — no load balancer, no server-side idle kill, no replica to rebalance onto"},
		{"maxIdleTime", "SetConnMaxIdleTime",
			"releasing idle connections returned 21 MiB of 725 MiB in the #1817 measurement, because modernc.org/memory keeps freed slabs"},
	} {
		v := reflect.ValueOf(db.DB).Elem().FieldByName(tc.field)
		if !v.IsValid() || v.Kind() != reflect.Int64 {
			t.Fatalf("database/sql no longer has an int64 %q field, so this guard "+
				"is no longer guarding anything — re-derive it against the current "+
				"stdlib rather than deleting it (#1817)", tc.field)
		}
		if got := time.Duration(v.Int()); got != 0 {
			t.Errorf("%s = %v, want unset: %s (#1817)", tc.setter, got, tc.why)
		}
	}
}

// ---------------------------------------------------------------------------
// Guards on the benchmark harness itself. The harness is left in the tree so
// the #1817 measurement can be repeated, which makes its failure modes real:
// a config that hangs forever or panics on a plausible env-var value wastes
// the time of whoever comes back to re-measure, and a delta that silently
// wraps would corrupt the evidence rather than fail loudly. These are cheap
// pure-function tests — no database, no concurrency.
// ---------------------------------------------------------------------------

// TestRSSDeltaMiB covers the arithmetic that reports memory movement.
//
// The first version subtracted two uint64 samples inside a float64 conversion,
// so the subtraction happened in unsigned arithmetic: any sample that shrank
// (memory released, which is exactly what the release measurement looks for)
// or that failed to read wrapped to ~1.7e13 MiB instead of going negative.
// CodeRabbit caught it on PR #1821.
func TestRSSDeltaMiB(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		name    string
		before  rssSample
		after   rssSample
		wantMiB float64
		wantOK  bool
	}{
		{
			name:    "growth",
			before:  rssSample{bytes: 10 * mib, ok: true},
			after:   rssSample{bytes: 30 * mib, ok: true},
			wantMiB: 20,
			wantOK:  true,
		},
		{
			// The case the unsigned version got wrong. Releasing memory must
			// read as a negative delta, not as a wrapped astronomical one.
			name:    "shrink reports negative",
			before:  rssSample{bytes: 30 * mib, ok: true},
			after:   rssSample{bytes: 10 * mib, ok: true},
			wantMiB: -20,
			wantOK:  true,
		},
		{
			name:   "unreadable after sample is not a delta",
			before: rssSample{bytes: 30 * mib, ok: true},
			after:  rssSample{ok: false},
			wantOK: false,
		},
		{
			name:   "unreadable before sample is not a delta",
			before: rssSample{ok: false},
			after:  rssSample{bytes: 30 * mib, ok: true},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rssDeltaMiB(tc.before, tc.after)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.wantMiB {
				t.Errorf("delta = %v MiB, want %v MiB", got, tc.wantMiB)
			}
		})
	}
}

// TestWarmConnCount guards against the benchmark deadlocking itself. Open()
// caps the pool at 5; warming one connection per worker with
// CREWSHIP_BENCH_WORKERS above that reserves every connection and then blocks
// forever on the next db.Conn. Warm at most what the pool can hand out.
func TestWarmConnCount(t *testing.T) {
	for _, tc := range []struct {
		name             string
		workers, maxOpen int
		want             int
	}{
		{"under the cap", 3, 5, 3},
		{"at the cap", 5, 5, 5},
		{"over the cap is clamped, not deadlocked", 8, 5, 5},
		{"unlimited pool reports 0 and clamps nothing", 8, 0, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := warmConnCount(tc.workers, tc.maxOpen); got != tc.want {
				t.Errorf("warmConnCount(%d, %d) = %d, want %d",
					tc.workers, tc.maxOpen, got, tc.want)
			}
		})
	}
}

// TestParseBenchEnv covers the two env knobs. A row count at or below the
// query window makes rand.Intn(rows-benchWindowRows) panic before the
// benchmark starts, which reads as a broken harness rather than as bad input.
func TestParseBenchEnv(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			raw     string
			want    int
			wantErr bool
		}{
			{"empty uses default", "", 60000, false},
			{"valid", "90000", 90000, false},
			{"at the window is rejected", strconv.Itoa(benchWindowRows), 0, true},
			{"below the window is rejected", "10", 0, true},
			{"zero is rejected", "0", 0, true},
			{"negative is rejected", "-5", 0, true},
			{"non-numeric is rejected", "lots", 0, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := parseBenchRows(tc.raw)
				if tc.wantErr {
					if err == nil {
						t.Fatalf("parseBenchRows(%q) = %d, want error", tc.raw, got)
					}
					return
				}
				if err != nil {
					t.Fatalf("parseBenchRows(%q): %v", tc.raw, err)
				}
				if got != tc.want {
					t.Errorf("parseBenchRows(%q) = %d, want %d", tc.raw, got, tc.want)
				}
			})
		}
	})

	t.Run("workers", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			raw     string
			want    int
			wantErr bool
		}{
			{"empty uses default", "", 5, false},
			{"valid", "8", 8, false},
			{"zero is rejected", "0", 0, true},
			{"negative is rejected", "-1", 0, true},
			{"non-numeric is rejected", "many", 0, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := parseBenchWorkers(tc.raw)
				if tc.wantErr {
					if err == nil {
						t.Fatalf("parseBenchWorkers(%q) = %d, want error", tc.raw, got)
					}
					return
				}
				if err != nil {
					t.Fatalf("parseBenchWorkers(%q): %v", tc.raw, err)
				}
				if got != tc.want {
					t.Errorf("parseBenchWorkers(%q) = %d, want %d", tc.raw, got, tc.want)
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// Benchmarks for #1817. These are Benchmarks, not Tests: they build a
// multi-hundred-megabyte database and drive concurrent readers, which is far
// too heavy for `go test ./...` and would be a flake magnet if it asserted
// anything about wall time. `go test` never runs them without -bench.
//
// Authoritative numbers come from one process per configuration, because
// resident memory is measured process-wide and the OS never returns the
// high-water mark once a config has run:
//
//	go test ./internal/database/ -run '^$' -bench 'BenchmarkPoolIdleConns/idle-2' -benchtime 20000x
//	go test ./internal/database/ -run '^$' -bench 'BenchmarkPoolIdleConns/idle-3' -benchtime 20000x
//	go test ./internal/database/ -run '^$' -bench 'BenchmarkPoolIdleConns/idle-5' -benchtime 20000x
// ---------------------------------------------------------------------------

const (
	benchPayloadBytes = 2048
	benchWindowRows   = 200
)

const (
	defaultBenchRows    = 60000
	defaultBenchWorkers = 5
)

// parseBenchRows validates CREWSHIP_BENCH_ROWS. The default sizes the corpus so
// that a 2 KiB payload plus row overhead — which does not fit two to a 4 KiB
// page — lands at ~235 MiB on disk: past the 64 MiB per-connection page cache,
// so each connection's cache can actually fill, but still inside the 256 MiB
// mmap window. Raise it past ~65k rows to measure the other regime, where
// reads fall out of the mapping and back onto the pager cache.
//
// Anything at or below the query window is rejected rather than clamped: the
// workloads draw from rand.Intn(rows-benchWindowRows), so a smaller corpus
// panics before the first query and looks like a broken harness instead of bad
// input.
func parseBenchRows(raw string) (int, error) {
	if raw == "" {
		return defaultBenchRows, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("CREWSHIP_BENCH_ROWS=%q is not a number: %w", raw, err)
	}
	if n <= benchWindowRows {
		return 0, fmt.Errorf("CREWSHIP_BENCH_ROWS=%d must exceed the %d-row query "+
			"window, or the workload's rand.Intn gets a non-positive argument",
			n, benchWindowRows)
	}
	return n, nil
}

// parseBenchWorkers validates CREWSHIP_BENCH_WORKERS. Values above the pool cap
// are allowed — queueing on a full pool is a legitimate thing to measure — but
// the warm-up must not try to hold one connection per worker; see
// warmConnCount.
func parseBenchWorkers(raw string) (int, error) {
	if raw == "" {
		return defaultBenchWorkers, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("CREWSHIP_BENCH_WORKERS=%q is not a number: %w", raw, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("CREWSHIP_BENCH_WORKERS=%d must be positive", n)
	}
	return n, nil
}

func benchRowCount(tb testing.TB) int {
	tb.Helper()
	n, err := parseBenchRows(os.Getenv("CREWSHIP_BENCH_ROWS"))
	if err != nil {
		tb.Fatal(err)
	}
	return n
}

func benchWorkers(tb testing.TB) int {
	tb.Helper()
	n, err := parseBenchWorkers(os.Getenv("CREWSHIP_BENCH_WORKERS"))
	if err != nil {
		tb.Fatal(err)
	}
	return n
}

// warmConnCount caps the warm-up at what the pool can actually hand out.
// Reserving one connection per worker deadlocks the moment workers exceed
// MaxOpenConns: the warm loop holds every connection and then waits forever
// for one more. sql.DB reports 0 for an unlimited pool, in which case there is
// nothing to clamp against.
func warmConnCount(workers, maxOpen int) int {
	if maxOpen > 0 && workers > maxOpen {
		return maxOpen
	}
	return workers
}

// buildBenchDB creates the corpus once and returns its path. Built through the
// real Open() so the file carries the production page size, WAL mode and
// journal settings.
//
// Set CREWSHIP_BENCH_DB to reuse a corpus across processes — measuring one
// configuration per process (see above) otherwise rebuilds it every time.
func buildBenchDB(b *testing.B, rows int) string {
	b.Helper()
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "bench.db")
	if cached := os.Getenv("CREWSHIP_BENCH_DB"); cached != "" {
		if _, err := os.Stat(cached); err == nil {
			return cached
		}
		path = cached
	}
	db, err := Open("file:" + path)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE bench_rows (
		id INTEGER PRIMARY KEY,
		ws TEXT NOT NULL,
		payload BLOB NOT NULL
	)`); err != nil {
		b.Fatalf("create table: %v", err)
	}

	payload := make([]byte, benchPayloadBytes)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	const batch = 500
	for start := 0; start < rows; start += batch {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			b.Fatalf("begin: %v", err)
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO bench_rows (id, ws, payload) VALUES (?, ?, ?)`)
		if err != nil {
			tx.Rollback()
			b.Fatalf("prepare: %v", err)
		}
		end := start + batch
		if end > rows {
			end = rows
		}
		for i := start; i < end; i++ {
			if _, err := stmt.ExecContext(ctx, i+1, "ws-"+strconv.Itoa(i%8), payload); err != nil {
				stmt.Close()
				tx.Rollback()
				b.Fatalf("insert: %v", err)
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			b.Fatalf("commit: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		b.Fatalf("checkpoint: %v", err)
	}
	return path
}

// rssSample is one resident-set-size reading. ok distinguishes "the process
// holds nothing" from "we could not read it" — conflating those as a bare 0 is
// what let the delta arithmetic below go wrong in the first place.
type rssSample struct {
	bytes int64
	ok    bool
}

func (s rssSample) mib() float64 { return float64(s.bytes) / (1 << 20) }

// rssBytes reports the process's current resident set size. modernc.org/sqlite
// allocates its page cache through modernc.org/memory, which mmaps outside the
// Go heap — runtime.MemStats cannot see it, so RSS is the only in-process
// measure of what a warm connection costs the host.
//
// RSS is not the last word, and the #1817 measurement does not treat it as
// one: it counts a file mapped N times N times over, so the memory figures in
// the PR and in database.go's comment come from an external physical-footprint
// tool (vmmap on macOS) rather than from here. These metrics exist to show
// movement while a benchmark runs.
func rssBytes(tb testing.TB) rssSample {
	tb.Helper()
	if runtime.GOOS == "windows" {
		return rssSample{}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return rssSample{}
	}
	kib, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return rssSample{}
	}
	return rssSample{bytes: kib * 1024, ok: true}
}

// rssDeltaMiB subtracts two samples in signed arithmetic, so a shrinking
// process reports a negative delta instead of wrapping. Reports ok=false when
// either sample is missing, so callers omit the metric rather than publishing
// a number derived from a failed read.
func rssDeltaMiB(before, after rssSample) (float64, bool) {
	if !before.ok || !after.ok {
		return 0, false
	}
	return float64(after.bytes-before.bytes) / (1 << 20), true
}

// reportRSS emits an absolute and a delta metric, skipping whichever is not
// backed by a real reading. divisor > 1 additionally reports a per-connection
// figure.
func reportRSS(b *testing.B, before, after rssSample, perConn int) {
	b.Helper()
	if after.ok {
		b.ReportMetric(after.mib(), "rss_MiB")
	}
	delta, ok := rssDeltaMiB(before, after)
	if !ok {
		return
	}
	b.ReportMetric(delta, "rss_delta_MiB")
	if perConn > 0 {
		b.ReportMetric(delta/float64(perConn), "MiB/conn")
	}
}

// BenchmarkPoolIdleConns drives concurrent range reads through the real pool
// (database.Open, production DSN) at several MaxIdleConns settings and reports
// throughput alongside resident memory. idle-2 is the current production
// behaviour: MaxIdleConns is never called, so database/sql's default applies.
func BenchmarkPoolIdleConns(b *testing.B) {
	rows := benchRowCount(b)
	path := buildBenchDB(b, rows)
	if fi, err := os.Stat(path); err == nil {
		b.Logf("corpus: %d rows, %.1f MiB on disk", rows, float64(fi.Size())/(1<<20))
	}

	for _, idle := range []int{2, 3, 5} {
		b.Run(fmt.Sprintf("idle-%d", idle), func(b *testing.B) {
			runPoolBench(b, path, rows, idle)
		})
	}
}

func runPoolBench(b *testing.B, path string, rows, idle int) {
	db, err := Open("file:" + path)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer db.Close()
	db.SetMaxIdleConns(idle)

	workers := benchWorkers(b)
	rssBefore := rssBytes(b)

	// Warm the pool the way a running server would: every connection the pool
	// is allowed to open has served at least one query before timing starts.
	// Cap the warm-up at the pool size — with more workers than connections,
	// reserving one per worker would hold them all and block forever.
	ctx := context.Background()
	toWarm := warmConnCount(workers, db.Stats().MaxOpenConnections)
	warm := make([]*sql.Conn, 0, toWarm)
	for i := 0; i < toWarm; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			b.Fatalf("warm conn: %v", err)
		}
		var n int64
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM bench_rows WHERE id < 1000`).Scan(&n); err != nil {
			b.Fatalf("warm query: %v", err)
		}
		warm = append(warm, c)
	}
	for _, c := range warm {
		c.Close()
	}

	perWorker := b.N / workers
	if perWorker < 1 {
		perWorker = 1
	}

	b.ResetTimer()
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < perWorker; i++ {
				lo := rng.Intn(rows-benchWindowRows) + 1
				var cnt int64
				var total sql.NullInt64
				err := db.QueryRowContext(ctx,
					`SELECT count(*), sum(length(payload)) FROM bench_rows WHERE id BETWEEN ? AND ?`,
					lo, lo+benchWindowRows,
				).Scan(&cnt, &total)
				if err != nil {
					errs <- err
					return
				}
			}
		}(int64(w) + 1)
	}
	wg.Wait()
	elapsed := time.Since(start)
	b.StopTimer()

	select {
	case err := <-errs:
		b.Fatalf("worker query: %v", err)
	default:
	}

	ops := perWorker * workers
	rssAfter := rssBytes(b)
	stats := db.Stats()

	b.ReportMetric(float64(ops)/elapsed.Seconds(), "queries/s")
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(ops), "ns/query")
	reportRSS(b, rssBefore, rssAfter, 0)
	b.ReportMetric(float64(stats.MaxIdleClosed), "conns_churned")
	b.ReportMetric(float64(stats.OpenConnections), "conns_open")
}

// BenchmarkPoolBurst models the traffic pattern the pool sizing comment in
// database.go actually cites: several dashboard tabs polling on an interval.
// Each iteration is one burst of `workers` simultaneous reads followed by an
// idle gap, which is where the idle cap bites — under *sustained* load
// database/sql hands a released connection straight to a waiting goroutine and
// never consults the idle pool, so continuous benchmarks under-report the
// churn. It only shows up when a burst drains and the released connections
// find no waiter.
func BenchmarkPoolBurst(b *testing.B) {
	rows := benchRowCount(b)
	path := buildBenchDB(b, rows)
	if fi, err := os.Stat(path); err == nil {
		b.Logf("corpus: %d rows, %.1f MiB on disk", rows, float64(fi.Size())/(1<<20))
	}

	for _, idle := range []int{2, 3, 5} {
		b.Run(fmt.Sprintf("idle-%d", idle), func(b *testing.B) {
			db, err := Open("file:" + path)
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			defer db.Close()
			db.SetMaxIdleConns(idle)

			ctx := context.Background()
			workers := benchWorkers(b)
			rssBefore := rssBytes(b)
			rng := rand.New(rand.NewSource(42))
			los := make([]int, b.N*workers)
			for i := range los {
				los[i] = rng.Intn(rows-benchWindowRows) + 1
			}

			b.ResetTimer()
			start := time.Now()
			errs := make(chan error, workers)
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func(lo int) {
						defer wg.Done()
						var cnt int64
						var total sql.NullInt64
						if err := db.QueryRowContext(ctx,
							`SELECT count(*), sum(length(payload)) FROM bench_rows WHERE id BETWEEN ? AND ?`,
							lo, lo+benchWindowRows,
						).Scan(&cnt, &total); err != nil {
							select {
							case errs <- err:
							default:
							}
						}
					}(los[i*workers+w])
				}
				wg.Wait()
			}
			elapsed := time.Since(start)
			b.StopTimer()

			select {
			case err := <-errs:
				b.Fatalf("burst query: %v", err)
			default:
			}

			rssAfter := rssBytes(b)
			stats := db.Stats()
			b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), "ns/burst")
			b.ReportMetric(float64(b.N*workers)/elapsed.Seconds(), "queries/s")
			reportRSS(b, rssBefore, rssAfter, 0)
			b.ReportMetric(float64(stats.MaxIdleClosed)/float64(b.N), "churn/burst")
		})
	}
}

// BenchmarkWarmConnMemory answers the question #1817 raises about cost:
// cache_size(-65536) is 64 MiB *per connection*, so does keeping five warm
// cost the host 320 MiB?
//
// It holds N warm connections, drives enough reads through each to fill its
// pager cache, and reports resident memory while they are still held. The
// mmap-off arm re-runs the identical workload with `PRAGMA mmap_size=0` set on
// each pooled connection — still through the production Open() path, the
// pragma is settable at runtime — which is what separates the two things the
// RSS number mixes together:
//
//   - mmap-on RSS includes each connection's mapping of the database file.
//     Those pages are clean, file-backed and shared between the mappings, so
//     they are counted once by the kernel but N times in RSS, and are
//     reclaimed under pressure rather than driving the box into swap.
//   - mmap-off RSS is the anonymous allocation — the pager cache — which is
//     the memory that is genuinely charged per connection.
//
// Run one configuration per process:
//
//	CREWSHIP_BENCH_DB=/tmp/bench.db go test ./internal/database/ -run '^$' \
//	  -bench 'BenchmarkWarmConnMemory/mmap-off/conns-5' -benchtime 1x
func BenchmarkWarmConnMemory(b *testing.B) {
	rows := benchRowCount(b)
	path := buildBenchDB(b, rows)

	for _, mmapOn := range []bool{true, false} {
		label := "mmap-on"
		if !mmapOn {
			label = "mmap-off"
		}
		for _, conns := range []int{1, 2, 3, 5} {
			b.Run(fmt.Sprintf("%s/conns-%d", label, conns), func(b *testing.B) {
				rssBase := rssBytes(b)
				db, err := Open("file:" + path)
				if err != nil {
					b.Fatalf("Open: %v", err)
				}
				defer db.Close()
				db.SetMaxIdleConns(conns)

				ctx := context.Background()
				held := make([]*sql.Conn, 0, conns)
				for i := 0; i < conns; i++ {
					c, err := db.Conn(ctx)
					if err != nil {
						b.Fatalf("reserve: %v", err)
					}
					if !mmapOn {
						if _, err := c.ExecContext(ctx, "PRAGMA mmap_size=0"); err != nil {
							b.Fatalf("disable mmap: %v", err)
						}
					}
					held = append(held, c)
				}

				// Fill each connection's cache from a different slice of the
				// corpus, the way separate requests would.
				const passes = 4000
				var wg sync.WaitGroup
				for i, c := range held {
					wg.Add(1)
					go func(idx int, c *sql.Conn) {
						defer wg.Done()
						rng := rand.New(rand.NewSource(int64(idx) + 7))
						for p := 0; p < passes; p++ {
							lo := rng.Intn(rows-benchWindowRows) + 1
							var cnt int64
							var total sql.NullInt64
							if err := c.QueryRowContext(ctx,
								`SELECT count(*), sum(length(payload)) FROM bench_rows WHERE id BETWEEN ? AND ?`,
								lo, lo+benchWindowRows).Scan(&cnt, &total); err != nil {
								b.Errorf("fill: %v", err)
								return
							}
						}
					}(i, c)
				}
				wg.Wait()

				rssWarm := rssBytes(b)
				// RSS counts each mapping of the database file separately even
				// though the kernel keeps one physical copy. Set
				// CREWSHIP_BENCH_HOLD to keep the connections warm long enough
				// to point a footprint tool (vmmap --summary on macOS,
				// /proc/<pid>/smaps_rollup on Linux) at the live process and
				// read the physical figure instead.
				if d := os.Getenv("CREWSHIP_BENCH_HOLD"); d != "" {
					if dur, err := time.ParseDuration(d); err == nil {
						b.Logf("holding %d warm connections for %s, pid %d", conns, dur, os.Getpid())
						time.Sleep(dur)
					}
				}
				for _, c := range held {
					c.Close()
				}

				reportRSS(b, rssBase, rssWarm, conns)
			})
		}
	}
}

// BenchmarkWarmThenRelease decides the memory half of #1817. Peak footprint is
// a property of MaxOpenConns, not of the idle cap: a burst of five concurrent
// requests opens five connections and fills five page caches whatever
// MaxIdleConns says. The idle cap only decides whether that memory is handed
// back when the burst drains.
//
// So: fill all five, release them all, and measure what the process is still
// holding. If a low idle cap does not actually return memory, it is buying
// nothing for the throughput it costs.
//
//	CREWSHIP_BENCH_DB=/tmp/big.db CREWSHIP_BENCH_HOLD=25s go test ./internal/database/ \
//	  -run '^$' -bench 'BenchmarkWarmThenRelease/idle-2' -benchtime 1x -v
func BenchmarkWarmThenRelease(b *testing.B) {
	rows := benchRowCount(b)
	path := buildBenchDB(b, rows)

	for _, idle := range []int{2, 5} {
		b.Run(fmt.Sprintf("idle-%d", idle), func(b *testing.B) {
			rssBase := rssBytes(b)
			db, err := Open("file:" + path)
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			defer db.Close()
			db.SetMaxIdleConns(idle)

			ctx := context.Background()
			// A full burst is whatever the pool will hand out at once; asking
			// for more than that would block here forever.
			burst := warmConnCount(defaultBenchWorkers, db.Stats().MaxOpenConnections)
			held := make([]*sql.Conn, 0, burst)
			for i := 0; i < burst; i++ {
				c, err := db.Conn(ctx)
				if err != nil {
					b.Fatalf("reserve: %v", err)
				}
				held = append(held, c)
			}
			var wg sync.WaitGroup
			for i, c := range held {
				wg.Add(1)
				go func(idx int, c *sql.Conn) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(int64(idx) + 11))
					for p := 0; p < 4000; p++ {
						lo := rng.Intn(rows-benchWindowRows) + 1
						var cnt int64
						var total sql.NullInt64
						if err := c.QueryRowContext(ctx,
							`SELECT count(*), sum(length(payload)) FROM bench_rows WHERE id BETWEEN ? AND ?`,
							lo, lo+benchWindowRows).Scan(&cnt, &total); err != nil {
							b.Errorf("fill: %v", err)
							return
						}
					}
				}(i, c)
			}
			wg.Wait()
			rssPeak := rssBytes(b)

			for _, c := range held {
				c.Close()
			}
			// The surplus connections are closed synchronously on release, but
			// give the allocator a moment to unmap what it freed.
			time.Sleep(500 * time.Millisecond)
			runtime.GC()
			rssAfter := rssBytes(b)

			if d := os.Getenv("CREWSHIP_BENCH_HOLD"); d != "" {
				if dur, err := time.ParseDuration(d); err == nil {
					b.Logf("released down to %d idle, holding for %s, pid %d", idle, dur, os.Getpid())
					time.Sleep(dur)
				}
			}

			if peak, ok := rssDeltaMiB(rssBase, rssPeak); ok {
				b.ReportMetric(peak, "peak_MiB")
			}
			// Signed: the whole point of this benchmark is memory going back
			// down, which unsigned arithmetic would have wrapped.
			if after, ok := rssDeltaMiB(rssBase, rssAfter); ok {
				b.ReportMetric(after, "after_release_MiB")
			}
			b.ReportMetric(float64(db.Stats().MaxIdleClosed), "conns_churned")
		})
	}
}

// BenchmarkConnReopenCost isolates the price of the churn itself: one worker,
// no contention, MaxIdleConns 0 vs 1. At 0 the pool discards the connection
// after every statement, so each iteration pays a fresh open plus the whole
// DSN pragma set (WAL handshake, cache_size, mmap_size). At 1 the same
// statement reuses a warm connection. The delta is what each churned
// connection costs a request.
func BenchmarkConnReopenCost(b *testing.B) {
	rows := 5000
	path := buildBenchDB(b, rows)

	for _, idle := range []int{0, 1} {
		b.Run(fmt.Sprintf("idle-%d", idle), func(b *testing.B) {
			db, err := Open("file:" + path)
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			defer db.Close()
			db.SetMaxIdleConns(idle)

			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var n int64
				if err := db.QueryRowContext(ctx,
					`SELECT count(*) FROM bench_rows WHERE id BETWEEN ? AND ?`,
					1, 100).Scan(&n); err != nil {
					b.Fatalf("query: %v", err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(db.Stats().MaxIdleClosed), "conns_churned")
		})
	}
}
