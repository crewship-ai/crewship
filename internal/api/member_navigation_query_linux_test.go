//go:build linux

package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

// Count actual statements through the production database/sql calls, without
// replacing the authorizer or its results.
type navigationCountingConnector struct {
	path  string
	count atomic.Int64
}

func (c *navigationCountingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.path)
	if err != nil {
		return nil, err
	}
	return &navigationCountingConn{Conn: conn, count: &c.count}, nil
}
func (c *navigationCountingConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type navigationCountingConn struct {
	driver.Conn
	count *atomic.Int64
}

func (c *navigationCountingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.count.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *navigationCountingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.count.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func countedNavigationDB(t *testing.T, db *sql.DB) (*sql.DB, *atomic.Int64) {
	t.Helper()
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	c := &navigationCountingConnector{path: path}
	out := sql.OpenDB(c)
	out.SetMaxOpenConns(1)
	t.Cleanup(func() { out.Close() })
	return out, &c.count
}

func checkNavigationScale(t *testing.T, db *sql.DB, user string, positive bool) {
	t.Helper()
	counted, count := countedNavigationDB(t, db)
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: user}))
	rr := httptest.NewRecorder()
	NewWorkspaceHandler(counted, newTestLogger()).List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("scale directory=%d %s", rr.Code, rr.Body.String())
	}
	t.Logf("50 routines/10 Pages navigation has_run_grants=%t SQL statements=%d", positive, count.Load())
	if count.Load() > 80 {
		t.Fatalf("navigation enumerated full execution catalogs: %d statements; budget80", count.Load())
	}
}

func checkNavigationProfiles(t *testing.T, db *sql.DB, router *Router, agent string, surfaces func() []any) {
	t.Helper()
	runner := &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: access.Store{DB: db}}, MaxOutputTokens: 128, StartSession: func(context.Context, string) (restricteddispatch.TextSession, error) {
		return nil, errors.New("must not execute navigation")
	}}
	service, err := restrictedworkflow.New(db, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	router.SetRestrictedWorkflow(service)
	for _, profile := range []string{"native_api_key", "disabled"} {
		execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile=? WHERE id=?`, profile, agent)
		if got := surfaces(); !reflect.DeepEqual(got, []any{"account_security"}) {
			t.Fatalf("installed text-only adapter advertises unsupported %s: %v", profile, got)
		}
	}
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id=?`, agent)
	if got := surfaces(); !reflect.DeepEqual(got, []any{"routines", "pages", "account_security"}) {
		t.Fatalf("installed text declaration hidden: %v", got)
	}
	router.SetRestrictedWorkflow(nil)
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='native_api_key' WHERE id=?`, agent)
	if got := surfaces(); !reflect.DeepEqual(got, []any{"routines", "pages", "account_security"}) {
		t.Fatalf("absent adapter hid known authorized native declaration: %v", got)
	}
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id=?`, agent)
}
