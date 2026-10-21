package api

import (
	"context"
	"database/sql/driver"
	"sync"
	"sync/atomic"

	"modernc.org/sqlite"
)

// A benchmark-only connector observes actual SQL without changing handler queries.
// The real modernc sqlite driver still handles context cancellation and values.
type searchTraceConnector struct {
	path                   string
	queries, execs, issues atomic.Int64
	plans                  sync.Map
}

func (c *searchTraceConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *searchTraceConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, err
	}
	return &searchTraceConn{Conn: conn, trace: c}, nil
}

type searchTraceConn struct {
	driver.Conn
	trace *searchTraceConnector
}

func (c *searchTraceConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.trace.queries.Add(1)
	if len(q) >= len(restrictedIssueAudienceSQL) && q[:len(restrictedIssueAudienceSQL)] == restrictedIssueAudienceSQL {
		c.trace.issues.Add(1)
		if _, exists := c.trace.plans.Load(q); !exists {
			c.trace.plans.Store(q, append([]driver.NamedValue(nil), args...))
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *searchTraceConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.trace.execs.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
