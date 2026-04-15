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
	ctx := context.Background()

	// seedSource builds a fresh source DB and populates it with the
	// canonical mixed-status fixture used across sub-cases. Each
	// sub-case gets its own DB so they can run in parallel and not
	// step on the shared-cache alias.
	seedSource := func(t *testing.T) *sql.DB {
		t.Helper()
		db := newCredentialsDB(t)
		if _, err := db.ExecContext(ctx, `
INSERT INTO credentials
  (id, workspace_id, name, type, status, encrypted_value, created_by, created_at)
VALUES ('c1','ws1','k1','SECRET','ACTIVE','v1:ct1','u1','2026-01-01'),
       ('c2','ws1','k2','SECRET','ACTIVE','v1:ct2','u1','2026-01-02'),
       ('c3','ws1','k3','SECRET','REVOKED','v1:ct3','u1','2026-01-03'),
       ('c4','ws1','k4','SECRET','ACTIVE','v1:ct4','u1','2026-01-04')`); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE credentials SET deleted_at = '2026-02-01' WHERE id = 'c4'`); err != nil {
			t.Fatalf("soft-delete: %v", err)
		}
		return db
	}

	t.Run("export filters deleted and non-active", func(t *testing.T) {
		db := seedSource(t)
		exported, err := ExportEncryptedCredentials(ctx, db)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		if len(exported) != 2 {
			t.Fatalf("export count: got %d want 2 (ACTIVE+non-deleted only)", len(exported))
		}
	})

	t.Run("export preserves v1: cipher prefix verbatim", func(t *testing.T) {
		db := seedSource(t)
		exported, _ := ExportEncryptedCredentials(ctx, db)
		for _, c := range exported {
			if !strings.HasPrefix(c.EncryptedValue, "v1:") {
				t.Errorf("encrypted_value prefix lost, got %q", c.EncryptedValue)
			}
		}
	})

	t.Run("import into fresh target inserts every row", func(t *testing.T) {
		db := seedSource(t)
		exported, _ := ExportEncryptedCredentials(ctx, db)
		target := newCredentialsDB(t)
		n, err := ImportEncryptedCredentials(ctx, target, exported)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if n != 2 {
			t.Errorf("insert count: got %d want 2", n)
		}
	})

	t.Run("re-import is idempotent (ON CONFLICT DO NOTHING)", func(t *testing.T) {
		db := seedSource(t)
		exported, _ := ExportEncryptedCredentials(ctx, db)
		target := newCredentialsDB(t)
		if _, err := ImportEncryptedCredentials(ctx, target, exported); err != nil {
			t.Fatalf("first import: %v", err)
		}
		n2, err := ImportEncryptedCredentials(ctx, target, exported)
		if err != nil {
			t.Fatalf("re-import: %v", err)
		}
		if n2 != 0 {
			t.Errorf("re-import: got %d inserts, want 0", n2)
		}
	})

	t.Run("nil db fails fast on export", func(t *testing.T) {
		if _, err := ExportEncryptedCredentials(ctx, nil); err == nil {
			t.Error("export with nil db must error")
		}
	})

	t.Run("nil db fails fast on import", func(t *testing.T) {
		if _, err := ImportEncryptedCredentials(ctx, nil, []EncryptedCredential{{ID: "x"}}); err == nil {
			t.Error("import with nil db must error")
		}
	})

	t.Run("empty import slice is no-op", func(t *testing.T) {
		target := newCredentialsDB(t)
		n, err := ImportEncryptedCredentials(ctx, target, nil)
		if err != nil {
			t.Fatalf("empty import: %v", err)
		}
		if n != 0 {
			t.Errorf("empty import insert count: got %d want 0", n)
		}
	})
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
  encrypted_refresh_token TEXT,
  token_expires_at TEXT,
  account_label TEXT,
  account_email TEXT,
  last_checked_at TEXT,
  last_error TEXT,
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
