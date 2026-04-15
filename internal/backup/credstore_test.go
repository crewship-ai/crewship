package backup

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestExportAndImportEncryptedCredentials(t *testing.T) {
	db := newCredentialsDB(t)
	ctx := context.Background()

	// Seed: one ACTIVE + one DELETED + one REVOKED. Only ACTIVE must
	// survive the round-trip.
	if _, err := db.ExecContext(ctx, `
INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_at)
VALUES ('c1','ws1','k1','SECRET','ACTIVE','v1:ct1','2026-01-01'),
       ('c2','ws1','k2','SECRET','ACTIVE','v1:ct2','2026-01-02'),
       ('c3','ws1','k3','SECRET','REVOKED','v1:ct3','2026-01-03'),
       ('c4','ws1','k4','SECRET','ACTIVE','v1:ct4','2026-01-04')`); err != nil {
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
		if c.EncryptedValue == "" || c.EncryptedValue[:3] != "v1:" {
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

func newCredentialsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE credentials (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  status TEXT,
  provider TEXT,
  security_level INTEGER DEFAULT 0,
  keeper_crew_id TEXT,
  encrypted_value TEXT NOT NULL,
  created_at TEXT,
  updated_at TEXT,
  deleted_at TEXT
)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}
