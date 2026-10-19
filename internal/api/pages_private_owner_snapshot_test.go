package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Bound lock-contention fixtures at SQLite itself: modernc's native busy
// handler can outlive a cancelled request context. Production timeouts stay intact.
func privateMetadataBoundedPool(t *testing.T, source *sql.DB) *sql.DB {
	t.Helper()
	path := reviewDBPath(t, source)
	uri := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", uri.String()+"?_pragma=busy_timeout(100)&_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if reviewDBPath(t, db) != path {
		t.Fatal("bounded pool opened a different native database")
	}
	return db
}

func TestPrivateMetadataReadOneConnection(t *testing.T) {
	for _, route := range privateMetadataRoutes {
		t.Run(route.name, func(t *testing.T) {
			h, ws, custodian, _ := privateMetadataFixture(t)
			h.db.SetMaxOpenConns(1)
			out := privateMetadataRequest(t, h, route, ws, "ptadmin", "OWNER", "created-doc", custodian)
			if out.Code != 200 {
				t.Fatalf("single-connection private read: %d %s", out.Code, out.Body.String())
			}
		})
	}
}

func TestPrivateMetadataReadWALWriterContention(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "private"}[private], func(t *testing.T) {
			h, ws, custodian, _ := privateMetadataFixture(t)
			slug := "created-doc"
			if !private {
				// An ordinary page is created through the existing ordinary handler fixture,
				// not by changing the immutable private classification of the document.
				other, _, _, workspace, user := newPagesFixture(t)
				h, ws, custodian = other, workspace, user
				slug = "ordinary-wal-reader"
				pagesCreateWith(t, h, ws, user, pagesReachBody(slug, "lookout"))
			}
			original := h.db
			h.db = privateMetadataBoundedPool(t, original)
			defer func() { h.db = original }()
			path := reviewDBPath(t, h.db)
			other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(100)&_pragma=foreign_keys(ON)&_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			tx, err := other.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, route := range privateMetadataRoutes {
				t.Run(route.name, func(t *testing.T) {
					request := pagesRequest(t, "GET", "/api/v1/pages/"+slug+route.path, ws, custodian, "OWNER", "")
					if route.path == "subject" {
						request = pagesRequest(t, "GET", "/api/v1/pages/access?subject=user:"+custodian, ws, custodian, "OWNER", "")
					}
					ctx, cancel := context.WithTimeout(request.Context(), 100*time.Millisecond)
					defer cancel()
					request = request.WithContext(ctx)
					request.SetPathValue("slug", slug)
					out := httptest.NewRecorder()
					route.handler(h, out, request)
					if !private && out.Code != 200 {
						t.Fatalf("ordinary read acquired writer lock: %d %s", out.Code, out.Body.String())
					}
					if private && (out.Code != 500 || strings.Contains(out.Body.String(), "Created report")) {
						t.Fatalf("private writer contention must fail without bytes: %d %s", out.Code, out.Body.String())
					}
				})
			}
		})
	}
}

func TestPrivateMetadataReadCustodySnapshot(t *testing.T) {
	for _, mutation := range []string{"custody", "owner-demotion"} {
		t.Run(mutation, func(t *testing.T) {
			h, ws, custodian, page := privateMetadataFixture(t)
			original := h.db
			if mutation == "custody" {
				execOrFatal(t, original, `UPDATE pages SET owner_user_id='ptadmin' WHERE id=?`, page)
			}
			probe := privateMetadataBoundedPool(t, original)
			query := `UPDATE pages SET owner_user_id=? WHERE id=?`
			args := []any{custodian, page}
			if mutation == "owner-demotion" {
				query = `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id='ptadmin'`
				args = []any{ws}
			}
			// The very same mutation succeeds on this pool outside the writer fence.
			res, err := probe.ExecContext(t.Context(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("unlocked mutation calibration: %d %v", n, err)
			}
			if mutation == "custody" {
				execOrFatal(t, original, `UPDATE pages SET owner_user_id='ptadmin' WHERE id=?`, page)
			} else {
				execOrFatal(t, original, `UPDATE workspace_members SET role='OWNER' WHERE workspace_id=? AND user_id='ptadmin'`, ws)
			}
			var mutationErr error
			hook := &reviewQueryHook{match: "SELECT spec_json FROM pages", nth: 1}
			hook.run = func() {
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				if ctx.Err() != nil {
					t.Fatal("mutation probe began with cancelled context")
				}
				_, mutationErr = probe.ExecContext(ctx, query, args...)
			}
			hooked := reviewHookedDB(t, reviewDBPath(t, original), hook)
			h.db = hooked
			defer func() { h.db = original }()
			out := privateMetadataRequest(t, h, privateMetadataRoutes[1], ws, "ptadmin", "OWNER", "created-doc", custodian)
			if !hook.didFire() {
				t.Fatal("native metadata-read interleave did not run")
			}
			if mutationErr == nil {
				t.Fatalf("custody/role mutated while private actual metadata read was unlocked; status%d", out.Code)
			}
			if out.Code != 200 {
				t.Fatalf("healthy fenced snapshot: %d %s", out.Code, out.Body.String())
			}
			expected := 1
			owner := custodian
			if mutation == "custody" {
				expected = 0
				owner = "ptadmin"
			}
			if got := privateMetadataAuditCount(t, h, ws, page, "ptadmin", owner); got != expected {
				t.Fatalf("audit actual snapshot custodian: got%d want%d", got, expected)
			}
		})
	}
}

func TestPrivateSubjectAccessLateAuditDeletion(t *testing.T) {
	h, ws, custodian, first := privateMetadataFixture(t)
	// A fresh native private Page exercises a second emitted metadata row. Its
	// classification is supplied at INSERT, never retrofitted onto an ordinary
	// Page; this storage fixture is not a second document-admission claim.
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) SELECT 'second-private-metadata',workspace_id,'second-private-metadata','Second private metadata',owner_user_id,spec_json,1 FROM pages WHERE id=?`, first)
	execOrFatal(t, h.db, `CREATE TRIGGER erase_earlier_private_audit AFTER INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' AND (SELECT COUNT(*) FROM audit_logs WHERE action=NEW.action)>1 BEGIN DELETE FROM audit_logs WHERE action=NEW.action AND id<>NEW.id; END`)
	out := privateMetadataRequest(t, h, privateMetadataRoutes[6], ws, "ptadmin", "OWNER", "created-doc", custodian)
	if out.Code != 500 || strings.Contains(out.Body.String(), "created-doc") || strings.Contains(out.Body.String(), "Second private metadata") {
		t.Fatalf("batch accepted an erased earlier audit: %d %s", out.Code, out.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed batch committed audit subset: %d %v", count, err)
	}
}

// This driver seam interleaves a real committed mutation after classification
// and immediately BEFORE the actual native private writer begins.
type privateMetadataBeginConnector struct {
	inner  *reviewHookConnector
	before func()
	once   sync.Once
}

func (c *privateMetadataBeginConnector) Driver() driver.Driver { return c.inner.Driver() }
func (c *privateMetadataBeginConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &privateMetadataBeginConn{Conn: conn, owner: c}, nil
}

type privateMetadataBeginConn struct {
	driver.Conn
	owner *privateMetadataBeginConnector
}

func (c *privateMetadataBeginConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if !opts.ReadOnly {
		c.owner.once.Do(c.owner.before)
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func TestPrivateMetadataReadFreshWriterAuthority(t *testing.T) {
	for _, mutation := range []string{"custody", "owner-demotion"} {
		t.Run(mutation, func(t *testing.T) {
			h, ws, custodian, page := privateMetadataFixture(t)
			original := h.db
			if mutation == "custody" {
				execOrFatal(t, original, `UPDATE pages SET owner_user_id='ptadmin' WHERE id=?`, page)
			}
			c := &privateMetadataBeginConnector{inner: &reviewHookConnector{inner: original.Driver(), dsn: reviewDBPath(t, original) + "?_pragma=busy_timeout(100)&_pragma=foreign_keys(ON)&_txlock=immediate", hook: &reviewQueryHook{}}}
			fired := false
			c.before = func() {
				fired = true
				if mutation == "custody" {
					execOrFatal(t, original, `UPDATE pages SET owner_user_id=? WHERE id=?`, custodian, page)
				} else {
					execOrFatal(t, original, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id='ptadmin'`, ws)
				}
			}
			hooked := sql.OpenDB(c)
			defer hooked.Close()
			h.db = hooked
			defer func() { h.db = original }()
			out := privateMetadataRequest(t, h, privateMetadataRoutes[1], ws, "ptadmin", "OWNER", "created-doc", custodian)
			if !fired {
				t.Fatal("private writer boundary not exercised")
			}
			if mutation == "custody" {
				if out.Code != 200 || privateMetadataAuditCount(t, h, ws, page, "ptadmin", custodian) != 1 {
					t.Fatalf("fresh actual custodian not audited: %d %s", out.Code, out.Body.String())
				}
			} else if out.Code != 404 || strings.Contains(out.Body.String(), "Created report") {
				t.Fatalf("stale OWNER admitted: %d %s", out.Code, out.Body.String())
			}
		})
	}
}
func TestOrdinaryMetadataReadClassificationSnapshot(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	slug := "ordinary-before-private"
	pagesCreateWith(t, h, ws, user, pagesReachBody(slug, "lookout"))
	original := h.db
	hook := &reviewQueryHook{match: "SELECT spec_json FROM pages", nth: 1}
	hook.run = func() {
		execOrFatal(t, original, `UPDATE pages SET private_audience=1,spec_json=json_set(spec_json,'$.metadata.name','NEW PRIVATE ONLY') WHERE workspace_id=? AND slug=?`, ws, slug)
	}
	hooked := reviewHookedDB(t, reviewDBPath(t, original), hook)
	h.db = hooked
	defer func() { h.db = original }()
	out := privateMetadataRequest(t, h, privateMetadataRoutes[1], ws, user, "OWNER", slug, user)
	if !hook.didFire() {
		t.Fatal("classification interleave not exercised")
	}
	if out.Code != 200 || strings.Contains(out.Body.String(), "NEW PRIVATE ONLY") {
		t.Fatalf("ordinary snapshot mixed later private metadata: %d %s", out.Code, out.Body.String())
	}
}
