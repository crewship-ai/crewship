package testutil

// SQLCounter observes statements sent to the real SQLite driver, across all
// pooled connections. It does not substitute query results or cache decisions.
import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/url"
	"sync/atomic"
	"testing"
)

type SQLCounter struct{ n atomic.Int64 }

func (c *SQLCounter) Reset()       { c.n.Store(0) }
func (c *SQLCounter) Count() int64 { return c.n.Load() }
func (c *SQLCounter) add()         { c.n.Add(1) }

type countingConnector struct {
	dsn     string
	inner   driver.Driver
	counter *SQLCounter
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
	counter *SQLCounter
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

// CountingSQLDB opens a separate instrumented pool on the fixture's native file.
func CountingSQLDB(t testing.TB, path string, connections int) (*sql.DB, *SQLCounter) {
	t.Helper()
	probe, err := sql.Open("sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	inner := probe.Driver()
	_ = probe.Close()
	counter := &SQLCounter{}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	db := sql.OpenDB(&countingConnector{dsn: uri + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate", inner: inner, counter: counter})
	db.SetMaxOpenConns(connections)
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}
