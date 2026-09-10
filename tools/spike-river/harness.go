// Command spike-river is the isolated harness for the River-over-SQLite
// evaluation described in docs/prd/SPIKE-RIVER-SQLITE-1-0.md.
//
// It lives in its own Go module on purpose: the spike must not add River to
// the main crewship go.mod before the ADR says adopt. Nothing here touches a
// live database — every scenario opens a fresh file under a temp directory.
//
// Usage:
//
//	go run . -scenario tx
//	go run . -scenario dup -workers 32
//	go run . -scenario contention -pool 1 -hold 3s
//	go run . -scenario durability -ops 200
//	go run . -scenario all -json results.json
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	_ "modernc.org/sqlite"
)

// crewshipDSN mirrors internal/database/database.go so the spike measures the
// pragmas the product actually runs with. syncMode is the synchronous pragma; the
// product ships NORMAL today and the PRD requires FULL for authoritative
// writes, so both are measurable here.
func crewshipDSN(path, syncMode string) string {
	return path +
		"?_pragma=busy_timeout(30000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(" + syncMode + ")" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=cache_size(-65536)" +
		"&_pragma=temp_store(MEMORY)" +
		"&_pragma=mmap_size(268435456)" +
		"&_txlock=immediate"
}

// schemaSQL is a minimal stand-in for the delivery ledger of §3 of the
// implementation contract: the identity that must be created in the same
// transaction as the queued work.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS deliveries (
  id                 TEXT PRIMARY KEY,
  workspace_id       TEXT NOT NULL,
  endpoint_id        TEXT NOT NULL,
  source_delivery_id TEXT NOT NULL,
  body_sha256        TEXT NOT NULL,
  work_id            TEXT,
  received_at        TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS deliveries_source_uniq
  ON deliveries(workspace_id, endpoint_id, source_delivery_id);
`

// MockArgs is the spike's job payload. The worker does no I/O and holds no
// credentials, per the spike protocol.
type MockArgs struct {
	WorkID   string `json:"work_id"`
	SleepMS  int    `json:"sleep_ms"`
	Delivery string `json:"delivery"`
}

func (MockArgs) Kind() string { return "spike_mock_work" }

type mockWorker struct {
	river.WorkerDefaults[MockArgs]
	mu      sync.Mutex
	worked  []string
	started chan string
	block   chan struct{}
}

func (w *mockWorker) Work(ctx context.Context, job *river.Job[MockArgs]) error {
	if w.started != nil {
		select {
		case w.started <- job.Args.WorkID:
		default:
		}
	}
	if w.block != nil {
		select {
		case <-w.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if job.Args.SleepMS > 0 {
		select {
		case <-time.After(time.Duration(job.Args.SleepMS) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w.mu.Lock()
	w.worked = append(w.worked, job.Args.WorkID)
	w.mu.Unlock()
	return nil
}

func (w *mockWorker) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.worked)
}

type env struct {
	dir    string
	dbPath string
	pool   *sql.DB
	driver *riversqlite.Driver
}

func newEnv(t testingT, poolSize int, syncMode string) *env {
	dir, err := os.MkdirTemp("", "spike-river-")
	must(t, err)
	dbPath := filepath.Join(dir, "spike.db")
	pool, err := sql.Open("sqlite", crewshipDSN(dbPath, syncMode))
	must(t, err)
	pool.SetMaxOpenConns(poolSize)
	pool.SetMaxIdleConns(poolSize)
	if _, err := pool.Exec(schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return &env{dir: dir, dbPath: dbPath, pool: pool, driver: riversqlite.New(pool)}
}

func (e *env) migrateRiver(t testingT) *rivermigrate.MigrateResult {
	m, err := rivermigrate.New(e.driver, nil)
	must(t, err)
	res, err := m.Migrate(context.Background(), rivermigrate.DirectionUp, nil)
	must(t, err)
	return res
}

func (e *env) client(t testingT, w *mockWorker, maxWorkers int) *river.Client[*sql.Tx] {
	workers := river.NewWorkers()
	river.AddWorker(workers, w)
	c, err := river.NewClient(e.driver, &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: maxWorkers}},
		Workers: workers,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		// Poll fast: the spike measures Crewship's dispatch latency budget, not
		// River's default 1s poll interval.
		FetchPollInterval: 50 * time.Millisecond,
		FetchCooldown:     10 * time.Millisecond,
	})
	must(t, err)
	return c
}

func (e *env) close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
	if e.dir != "" {
		_ = os.RemoveAll(e.dir)
	}
}

// jobCount reads the River job table directly. The spike deliberately looks at
// storage rather than at the client's own view: the question is whether a
// rolled-back transaction can leave a row behind.
func (e *env) jobCount(ctx context.Context, db queryer) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM river_job`).Scan(&n)
	return n, err
}

func (e *env) deliveryCount(ctx context.Context, db queryer) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries`).Scan(&n)
	return n, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// acceptDelivery is the acceptance path under test: one *sql.Tx that writes the
// delivery ledger row and enqueues the work. Nothing outside the transaction
// happens before the commit.
func acceptDelivery(ctx context.Context, tx *sql.Tx, c *river.Client[*sql.Tx], d delivery) (string, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO deliveries (id, workspace_id, endpoint_id, source_delivery_id, body_sha256, work_id, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.WorkspaceID, d.EndpointID, d.SourceDeliveryID, d.BodySHA256, d.WorkID, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return "", err
	}
	res, err := c.InsertTx(ctx, tx, MockArgs{WorkID: d.WorkID, SleepMS: d.SleepMS, Delivery: d.ID}, nil)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", res.Job.ID), nil
}

type delivery struct {
	ID               string
	WorkspaceID      string
	EndpointID       string
	SourceDeliveryID string
	BodySHA256       string
	WorkID           string
	SleepMS          int
}

// --- result plumbing -------------------------------------------------------

type scenarioResult struct {
	Scenario string         `json:"scenario"`
	Pass     bool           `json:"pass"`
	Detail   map[string]any `json:"detail"`
	Notes    []string       `json:"notes,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type report struct {
	RunAt     time.Time        `json:"run_at"`
	Versions  map[string]any   `json:"versions"`
	Scenarios []scenarioResult `json:"scenarios"`
}

// testingT lets the same scenario bodies run from `go test` and from main.
type testingT interface {
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
}

func must(t testingT, err error) {
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

type cliT struct {
	failed bool
	msgs   []string
}

func (c *cliT) Fatalf(f string, a ...any) { c.failed = true; panic(fatalPanic{fmt.Sprintf(f, a...)}) }
func (c *cliT) Errorf(f string, a ...any) {
	c.failed = true
	c.msgs = append(c.msgs, fmt.Sprintf(f, a...))
}
func (c *cliT) Logf(f string, a ...any) { c.msgs = append(c.msgs, fmt.Sprintf(f, a...)) }

type fatalPanic struct{ msg string }

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s)-1) * p)
	return s[idx]
}

func sqliteVersion() string {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return "unknown"
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT sqlite_version()`).Scan(&v); err != nil {
		return "unknown"
	}
	return v
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "SQLITE_BUSY") || contains(msg, "database is locked")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var errNotImplemented = errors.New("scenario not implemented")

func main() {
	var (
		scenario = flag.String("scenario", "all", "tx|dup|fencing|pool|contention|durability|all")
		poolSize = flag.Int("pool", 1, "max open connections on the shared pool")
		sync     = flag.String("sync", "FULL", "synchronous pragma: NORMAL or FULL")
		workers  = flag.Int("workers", 32, "concurrency for the dup scenario")
		ops      = flag.Int("ops", 200, "operations for durability/contention scenarios")
		hold     = flag.Duration("hold", 3*time.Second, "how long the contention scenario holds a write lock")
		out      = flag.String("json", "", "write the JSON report to this path")
	)
	flag.Parse()

	rep := report{
		RunAt: time.Now().UTC(),
		Versions: map[string]any{
			"river":          "v0.47.0",
			"riversqlite":    "v0.47.0",
			"modernc_sqlite": "v1.58.0",
			"sqlite_library": sqliteVersion(),
			"go":             goVersion(),
			"pool_size":      *poolSize,
			"synchronous":    *sync,
		},
	}

	run := func(name string, fn func(t testingT) scenarioResult) {
		t := &cliT{}
		res := func() (res scenarioResult) {
			defer func() {
				if r := recover(); r != nil {
					if fp, ok := r.(fatalPanic); ok {
						res = scenarioResult{Scenario: name, Pass: false, Error: fp.msg}
						return
					}
					panic(r)
				}
			}()
			return fn(t)
		}()
		if len(t.msgs) > 0 {
			res.Notes = append(res.Notes, t.msgs...)
		}
		if t.failed && res.Error == "" {
			res.Pass = false
		}
		rep.Scenarios = append(rep.Scenarios, res)
		status := "PASS"
		if !res.Pass {
			status = "FAIL"
		}
		fmt.Printf("%-14s %s\n", name, status)
		if res.Error != "" {
			fmt.Printf("               error: %s\n", res.Error)
		}
		for k, v := range res.Detail {
			fmt.Printf("               %-28s %v\n", k, v)
		}
		for _, n := range res.Notes {
			fmt.Printf("               note: %s\n", n)
		}
	}

	want := func(n string) bool { return *scenario == "all" || *scenario == n }

	if want("tx") {
		run("tx", func(t testingT) scenarioResult { return scenarioTx(t, *poolSize, *sync) })
	}
	if want("dup") {
		run("dup", func(t testingT) scenarioResult { return scenarioDup(t, *poolSize, *sync, *workers) })
	}
	if want("contention") {
		run("contention", func(t testingT) scenarioResult {
			return scenarioContention(t, *poolSize, *sync, *ops, *hold)
		})
	}
	if want("fencing") {
		run("fencing", func(t testingT) scenarioResult { return scenarioFencing(t, *sync) })
	}
	if want("pool") {
		run("pool", func(t testingT) scenarioResult { return scenarioPool(t, *sync, []int{1, 2, 5}) })
	}
	if want("durability") {
		run("durability", func(t testingT) scenarioResult { return scenarioDurability(t, *ops) })
	}

	if *out != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nreport written to %s\n", *out)
	}

	for _, s := range rep.Scenarios {
		if !s.Pass {
			os.Exit(1)
		}
	}
}

func goVersion() string { return runtimeVersion() }

var _ = rivertype.JobStateAvailable
