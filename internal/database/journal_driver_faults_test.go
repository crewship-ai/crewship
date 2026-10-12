package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"modernc.org/sqlite"
)

// Keep SQLite's actual transaction and schema behavior. Only one selected
// driver operation fails, as a disk/connection error would; cleanup and later
// borrowers then use the real driver. Each test owns its connector and file.
type journalFaultConnector struct {
	path  string
	mu    sync.Mutex
	armed bool
	match func(string) bool
	hits  int
	opens int
}

var errJournalStorageFault = errors.New("injected journal storage failure")

func (c *journalFaultConnector) fail(operation string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.armed && c.match(operation) {
		c.armed = false
		c.hits++
		return true
	}
	return false
}
func (c *journalFaultConnector) Connect(context.Context) (driver.Conn, error) {
	raw, err := (&sqlite.Driver{}).Open(c.path)
	if err != nil {
		return nil, err
	}
	if _, err = raw.(driver.ExecerContext).ExecContext(context.Background(), `PRAGMA foreign_keys=ON`, nil); err != nil {
		raw.Close()
		return nil, err
	}
	c.mu.Lock()
	c.opens++
	c.mu.Unlock()
	return &journalFaultConn{Conn: raw, fault: c}, nil
}
func (c *journalFaultConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type journalFaultConn struct {
	driver.Conn
	fault *journalFaultConnector
}

func (c *journalFaultConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.fault.fail(q) {
		return nil, errJournalStorageFault
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *journalFaultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.fault.fail(q) {
		return nil, errJournalStorageFault
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *journalFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.fault.fail("BEGIN") {
		return nil, errJournalStorageFault
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &journalFaultTx{Tx: tx, fault: c.fault}, nil
}

type journalFaultTx struct {
	driver.Tx
	fault *journalFaultConnector
}

func (t *journalFaultTx) Commit() error {
	if t.fault.fail("COMMIT") {
		_ = t.Tx.Rollback()
		return errJournalStorageFault
	}
	return t.Tx.Commit()
}

func TestJournalRebuildDriverFailuresNeverLoseAudit(t *testing.T) {
	const ddl = `CREATE TABLE journal_entries(id TEXT PRIMARY KEY, crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE, agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL, mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL, summary TEXT)`
	for _, tc := range []struct {
		name, query, want string
		afterCommit       bool
	}{
		{"dependent metadata", "SELECT type, name, sql", "", false},
		{"columns", "SELECT name FROM pragma_table_info", "", false},
		{"integrity scan", "PRAGMA foreign_key_check", "", false},
		{"foreign key policy read", "PRAGMA foreign_keys", "read foreign_keys", false},
		{"disable foreign keys", "PRAGMA foreign_keys = OFF", "disable foreign_keys", false},
		{"legacy alter policy", "PRAGMA legacy_alter_table = ON", "enable legacy_alter_table", false},
		{"begin", "BEGIN", "begin rebuild", false},
		{"clear staging", "DROP TABLE IF EXISTS", "clear staging table", false},
		{"create staging", "CREATE TABLE journal_entries_v167", "create rebuilt table", false},
		{"copy", "INSERT INTO journal_entries_v167", "copy rows", false},
		{"count source", "SELECT COUNT(*) FROM journal_entries", "count source rows", false},
		{"drop old", "DROP TABLE journal_entries", "drop old table", false},
		{"rename", "ALTER TABLE journal_entries_v167", "rename rebuilt table", false},
		{"recreate index", "CREATE INDEX audit_summary", "recreate index", false},
		{"probe FTS", "SELECT COUNT(*) FROM sqlite_master WHERE name='journal_entries_fts'", "probe fts table", false},
		{"commit", "COMMIT", "commit rebuild", false},
		{"restore legacy policy", "PRAGMA legacy_alter_table = OFF", "restore legacy_alter_table", true},
		{"restore foreign keys", "PRAGMA foreign_keys = ON", "re-enable foreign_keys", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connector := &journalFaultConnector{path: filepath.Join(t.TempDir(), "audit.db"), match: func(q string) bool { return strings.HasPrefix(strings.TrimSpace(q), tc.query) }}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer db.Close()
			for _, q := range []string{`CREATE TABLE crews(id TEXT PRIMARY KEY);CREATE TABLE agents(id TEXT PRIMARY KEY);CREATE TABLE missions(id TEXT PRIMARY KEY)`, ddl, `INSERT INTO journal_entries(id,summary) VALUES('audit','retained');CREATE INDEX audit_summary ON journal_entries(summary)`} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			connector.mu.Lock()
			connector.armed = true
			connector.mu.Unlock()
			err = rebuildJournalEntries(context.Background(), conn, ddl, slog.New(slog.NewTextHandler(io.Discard, nil)))
			_ = conn.Close()
			connector.mu.Lock()
			hits := connector.hits
			connector.mu.Unlock()
			if hits != 1 {
				t.Fatalf("fixture did not inject at %q", tc.query)
			}
			if !errors.Is(err, errJournalStorageFault) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("failure=%v, want storage error with %q", err, tc.want)
			}
			var summary, storedDDL string
			if err := db.QueryRow(`SELECT summary FROM journal_entries WHERE id='audit'`).Scan(&summary); err != nil || summary != "retained" {
				t.Fatalf("audit loss after failure: %q %v", summary, err)
			}
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='journal_entries'`).Scan(&storedDDL); err != nil {
				t.Fatal(err)
			}
			if !tc.afterCommit && storedDDL != ddl {
				t.Fatalf("uncommitted schema escaped rollback: %s", storedDDL)
			}
			var fk, legacy int
			if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("next borrower lost FK enforcement: %d %v", fk, err)
			}
			if err := db.QueryRow(`PRAGMA legacy_alter_table`).Scan(&legacy); err != nil || legacy != 0 {
				t.Fatalf("legacy policy escaped: %d %v", legacy, err)
			}
			var indexes int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='audit_summary'`).Scan(&indexes); err != nil || indexes != 1 {
				t.Fatalf("index lost: %d %v", indexes, err)
			}
			if tc.afterCommit {
				connector.mu.Lock()
				opens := connector.opens
				connector.mu.Unlock()
				if opens < 2 {
					t.Fatal("poisoned connection was reused")
				}
			}
		})
	}
}
