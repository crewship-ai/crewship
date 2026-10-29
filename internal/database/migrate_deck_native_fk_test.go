package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Keep real SQLite transactions and constraints. Only the post-schema integrity
// query injects a new deferred target violation, as a faulty migration would.
type deckFKProbeConnector struct {
	base          *journalFaultConnector
	inject        bool
	replaceLegacy bool
	schemaChanged atomic.Bool
	injected      atomic.Bool
	commits       atomic.Int32
}

func (c *deckFKProbeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &deckFKProbeConn{Conn: raw, owner: c}, nil
}
func (c *deckFKProbeConnector) Driver() driver.Driver { return c.base.Driver() }

type deckFKProbeConn struct {
	driver.Conn
	owner *deckFKProbeConnector
}

func (c *deckFKProbeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && strings.HasPrefix(query, "UPDATE sqlite_master SET sql=") {
		c.owner.schemaChanged.Store(true)
	}
	return result, err
}
func (c *deckFKProbeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == "PRAGMA foreign_key_check" && c.owner.inject && c.owner.schemaChanged.Load() && c.owner.injected.CompareAndSwap(false, true) {
		queries := []string{`PRAGMA defer_foreign_keys=ON`}
		if c.owner.replaceLegacy {
			// Equal before/after counts must not hide a different broken row.
			queries = append(queries, `DELETE FROM unrelated_orphans WHERE id='legacy-orphan'`)
		}
		queries = append(queries, `INSERT INTO craft_native_documents VALUES('new-target-orphan','wordcraft')`)
		for _, q := range queries {
			if _, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, nil); err != nil {
				return nil, err
			}
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func (c *deckFKProbeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &deckFKProbeTx{Tx: tx, owner: c.owner}, nil
}

type deckFKProbeTx struct {
	driver.Tx
	owner *deckFKProbeConnector
}

func (t *deckFKProbeTx) Commit() error { t.owner.commits.Add(1); return t.Tx.Commit() }

func deckFKProbeFixture(t *testing.T, inject, replace bool) (*sql.DB, *deckFKProbeConnector) {
	t.Helper()
	c := &deckFKProbeConnector{base: &journalFaultConnector{path: filepath.Join(t.TempDir(), "deck-fk.db")}, inject: inject, replaceLegacy: replace}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, query := range []string{
		`CREATE TABLE workspaces(id TEXT PRIMARY KEY)`,
		`CREATE TABLE pages(id TEXT PRIMARY KEY)`,
		`CREATE TABLE craft_native_documents(page_id TEXT PRIMARY KEY REFERENCES pages(id), application_id TEXT NOT NULL CHECK(application_id IN ('wordcraft','gridcraft')))`,
		`CREATE TABLE artifact_version_heads(page_id TEXT PRIMARY KEY REFERENCES pages(id), content_type TEXT NOT NULL CHECK(content_type IN ('artifact_project','wordcraft','gridcraft')), latest_version INTEGER NOT NULL)`,
		`CREATE TABLE unrelated_orphans(id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id))`,
		`INSERT INTO pages VALUES('kept')`,
		`INSERT INTO craft_native_documents VALUES('kept','wordcraft')`,
		`INSERT INTO artifact_version_heads VALUES('kept','wordcraft',7)`,
		`PRAGMA foreign_keys=OFF`,
		`INSERT INTO unrelated_orphans VALUES('legacy-orphan','removed-workspace')`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db, c
}

func TestDeckNativeMigrationKeepsUnrelatedLegacyForeignKeyOrphans(t *testing.T) {
	db, _ := deckFKProbeFixture(t, false, false)
	before := deckMigrationRows(t, db, `SELECT * FROM unrelated_orphans`)
	rootpages := deckMigrationRows(t, db, `SELECT name,rootpage FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err := migrationDeckNativeKinds(t.Context(), db, nil); err != nil {
		t.Fatalf("unrelated historical FK orphan blocked native family upgrade: %v", err)
	}
	if deckMigrationRows(t, db, `SELECT * FROM unrelated_orphans`) != before || deckMigrationRows(t, db, `SELECT name,rootpage FROM sqlite_master WHERE type='table' ORDER BY name`) != rootpages {
		t.Fatal("native upgrade removed legacy rows or rebuilt tables")
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 1 {
		t.Fatalf("historical violation was silently removed: count=%d err=%v", violations, err)
	}
}

func TestDeckNativeMigrationRejectsNewTargetForeignKeyViolationsBeforeCommit(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "additional violation"
		if replace {
			name = "different violation with equal total count"
		}
		t.Run(name, func(t *testing.T) {
			db, probe := deckFKProbeFixture(t, true, replace)
			queries := []string{
				`SELECT name,rootpage,sql FROM sqlite_master WHERE type='table' ORDER BY name`,
				`SELECT * FROM craft_native_documents`,
				`SELECT * FROM artifact_version_heads`,
				`SELECT * FROM unrelated_orphans`,
			}
			before := make([]string, len(queries))
			for i, q := range queries {
				before[i] = deckMigrationRows(t, db, q)
			}
			err := migrationDeckNativeKinds(t.Context(), db, nil)
			if !probe.injected.Load() || err == nil || probe.commits.Load() != 0 {
				t.Fatalf("new target violation reached commit: injected=%t commits=%d err=%v", probe.injected.Load(), probe.commits.Load(), err)
			}
			for i, q := range queries {
				if deckMigrationRows(t, db, q) != before[i] {
					t.Fatalf("failed integrity gate changed data or schema: %s", q)
				}
			}
			for _, check := range []struct {
				query string
				want  int
			}{{"PRAGMA foreign_keys", 1}, {"PRAGMA writable_schema", 0}} {
				var actual int
				if err := db.QueryRow(check.query).Scan(&actual); err != nil || actual != check.want {
					t.Fatalf("connection escaped cleanup: %s=%d err=%v", check.query, actual, err)
				}
			}
		})
	}
}
