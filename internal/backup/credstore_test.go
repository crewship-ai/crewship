package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

func TestExportAndImportEncryptedCredentials(t *testing.T) {
	db := newCredentialsDB(t)
	ctx := context.Background()

	// Seed: one ACTIVE + one DELETED + one REVOKED. Only ACTIVE must
	// survive the round-trip. created_by is populated because the
	// production column is NOT NULL — exporting a row without it would
	// fail the restore INSERT.
	if _, err := db.ExecContext(ctx, `
INSERT INTO credentials
  (id, workspace_id, name, type, status, encrypted_value, created_by, created_at)
VALUES ('c1','ws1','k1','SECRET','ACTIVE','v1:ct1','u1','2026-01-01'),
       ('c2','ws1','k2','SECRET','ACTIVE','v1:ct2','u1','2026-01-02'),
       ('c3','ws1','k3','SECRET','REVOKED','v1:ct3','u1','2026-01-03'),
       ('c4','ws1','k4','SECRET','ACTIVE','v1:ct4','u1','2026-01-04')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Soft-delete c4 — must also be excluded.
	if _, err := db.ExecContext(ctx, `UPDATE credentials SET deleted_at = '2026-02-01' WHERE id = 'c4'`); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	exported, err := ExportEncryptedCredentials(ctx, db)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported) != 2 {
		t.Fatalf("export count: got %d want 2 (ACTIVE+non-deleted only)", len(exported))
	}
	for _, c := range exported {
		if !strings.HasPrefix(c.EncryptedValue, "v1:") {
			t.Errorf("encrypted_value must carry v1: prefix verbatim, got %q", c.EncryptedValue)
		}
	}

	// Fresh target DB to prove restore is additive.
	target := newCredentialsDB(t)
	n, err := ImportEncryptedCredentials(ctx, target, exported)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n != 2 {
		t.Errorf("insert count: got %d want 2", n)
	}
	// Second import must be a no-op (INSERT OR IGNORE).
	n2, err := ImportEncryptedCredentials(ctx, target, exported)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if n2 != 0 {
		t.Errorf("re-import: got %d inserts, want 0 (IGNORE on duplicate)", n2)
	}
}

// sharedMemCounter disambiguates the shared-cache DSN across test
// helper calls so two newCredentialsDB() instances get independent
// in-memory DBs (otherwise they'd alias and the "fresh target"
// half of the round-trip test would see the source's rows).
var sharedMemCounter atomic.Uint64

func newCredentialsDB(t *testing.T) *sql.DB {
	t.Helper()
	// Use a shared-cache in-memory URI so every pooled connection
	// opened by database/sql sees the same schema. Plain ":memory:"
	// creates an isolated DB per connection under modernc.org/sqlite,
	// which would cause the fan-out of a future parallel test to lose
	// state. Unique `mode=memory` name per helper call keeps different
	// DBs from aliasing.
	name := fmt.Sprintf("crewship-cred-test-%d", sharedMemCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE credentials (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  crew_id TEXT,
  name TEXT NOT NULL,
  description TEXT,
  type TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT 'WORKSPACE',
  status TEXT,
  provider TEXT,
  security_level INTEGER DEFAULT 0,
  keeper_crew_id TEXT,
  encrypted_value TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT,
  updated_at TEXT,
  deleted_at TEXT,
  UNIQUE(workspace_id, name)
)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}
