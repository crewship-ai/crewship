package chataudience

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

// Interpose only before the real audit INSERT. A second SQLite connection
// commits an authority change after the real initial SELECT, without replacing
// either query's result or adding a production hook.
type artifactAuditConnector struct {
	inner  driver.Driver
	dsn    string
	before func()
}

func (c *artifactAuditConnector) Driver() driver.Driver { return c.inner }
func (c *artifactAuditConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &artifactAuditConn{Conn: conn, connector: c}, nil
}

type artifactAuditConn struct {
	driver.Conn
	connector *artifactAuditConnector
}

func (c *artifactAuditConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "INSERT INTO audit_logs") && c.connector.before != nil {
		before := c.connector.before
		c.connector.before = nil
		before()
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func TestPrivateArtifactAuditRechecksConcurrentAuthority(t *testing.T) {
	for _, change := range []string{"OWNER revoked", "creator changed"} {
		t.Run(change, func(t *testing.T) {
			db := audienceDB(t)
			artifactConsent(t, db, "consent", "author")
			var path string
			if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil || path == "" {
				t.Fatalf("file-backed fixture required: %s %v", path, err)
			}
			fired := false
			connector := &artifactAuditConnector{inner: db.Driver(), dsn: "file:" + path, before: func() {
				fired = true
				if change == "OWNER revoked" {
					audienceExec(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='owner' AND workspace_id='ws'`)
				} else {
					audienceExec(t, db, `UPDATE chats SET created_by='other' WHERE id='private'`)
				}
			}}
			reader := sql.OpenDB(connector)
			t.Cleanup(func() { _ = reader.Close() })
			ok, err := CanReadArtifactConversation(t.Context(), reader, "private", "owner", "ws", true)
			if !fired {
				t.Fatal("writer interleaving did not reach real audit INSERT")
			}
			if ok || err == nil {
				t.Errorf("changed authority released output after audit: allowed=%v err=%v", ok, err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='private'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale audit committed count=%d err=%v", count, err)
			}
		})
	}
}
