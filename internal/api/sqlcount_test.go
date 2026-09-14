package api

// A statement counter for handler tests whose contract is about COST rather
// than content: "this listing runs a fixed number of statements however many
// rows it returns". Nothing in database/sql exposes that number, so the
// counter sits one layer down, as a driver.Conn wrapper around the real SQLite
// connection, and increments once per statement the pool hands to it.
//
// The wrapper is opened over the same file as the test's migrated database, so
// the handler under test reads and writes the fixture's data through it while
// the fixture's own handle keeps seeding. WAL mode makes the two handles
// ordinary concurrent connections.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// sqlCounter counts statements across every connection of one *sql.DB.
type sqlCounter struct{ n atomic.Int64 }

func (c *sqlCounter) reset()     { c.n.Store(0) }
func (c *sqlCounter) count() int { return int(c.n.Load()) }
func (c *sqlCounter) add()       { c.n.Add(1) }

// countingConnector opens the underlying sqlite driver's connections and wraps
// each in a countingConn. It is a driver.Connector so the pool takes it through
// sql.OpenDB without a process-wide sql.Register.
type countingConnector struct {
	dsn     string
	inner   driver.Driver
	counter *sqlCounter
}

func (c *countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: c.counter}, nil
}

func (c *countingConnector) Driver() driver.Driver { return c.inner }

// countingConn forwards every optional interface database/sql probes for, so
// the pool takes the same fast paths it takes against the bare driver: a
// wrapper that dropped QueryerContext would make the pool prepare-then-query,
// and the count would measure the wrapper rather than the handler.
type countingConn struct {
	driver.Conn
	counter *sqlCounter
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.add()
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.add()
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.counter.add()
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	c.counter.add()
	return c.Conn.Prepare(query)
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *countingConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *countingConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *countingConn) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

// newCountingSQLDB returns a second handle over the test's migrated database
// whose statements are counted, plus the counter. The pragmas mirror
// database.Open's for the ones that change behaviour (foreign keys, WAL, the
// write lock); the cache and mmap sizing are irrelevant to a count.
func newCountingSQLDB(t *testing.T, path string) (*sql.DB, *sqlCounter) {
	t.Helper()
	probe, err := sql.Open("sqlite", "")
	if err != nil {
		t.Fatalf("resolve sqlite driver: %v", err)
	}
	inner := probe.Driver()
	_ = probe.Close()

	counter := &sqlCounter{}
	db := sql.OpenDB(&countingConnector{
		dsn: path + "?_pragma=busy_timeout(30000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=foreign_keys(ON)" +
			"&_txlock=immediate",
		inner:   inner,
		counter: counter,
	})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}

// newCountingPagesFixture is newPagesFixture with the handler's statements
// counted: the same workspace, OWNER user and crew, plus the second crew the
// two-panel page needs.
func newCountingPagesFixture(t *testing.T) (*PageHandler, *sqlCounter, string, string) {
	t.Helper()
	resetCapabilityCache()
	mdb := testutil.MigratedDB(t)
	drainBackgroundWork(t)
	userID := seedTestUser(t, mdb.DB)
	wsID := seedTestWorkspace(t, mdb.DB, userID)
	for _, crew := range [][2]string{{"crew-lookout", "lookout"}, {"crew-engine", "engine"}} {
		if _, err := mdb.DB.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
			crew[0], wsID, crew[1], crew[1]); err != nil {
			t.Fatalf("insert crew %s: %v", crew[1], err)
		}
	}
	counted, counter := newCountingSQLDB(t, mdb.Path())
	spy := &pagesJournalSpy{}
	clock := &pagesFakeClock{now: time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)}
	h := NewPageHandler(counted, nil, newTestLogger()).SetJournal(spy).SetClockForTesting(clock)
	return h, counter, wsID, userID
}
