package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// hashedPortExposureSchemaSQL mirrors the post-migration shape of
// port_exposures: the original `token` column (NOT NULL UNIQUE, so it cannot
// be blanked to an empty string on every row and cannot be dropped while
// the create path still names it) plus the `token_hash` column added by
// 20260810171000_hash_capability_tokens.
const hashedPortExposureSchemaSQL = `
CREATE TABLE port_exposures (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    chat_id TEXT,
    token TEXT NOT NULL UNIQUE,
    token_hash TEXT,
    container_id TEXT NOT NULL,
    container_ip TEXT NOT NULL,
    container_port INTEGER NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    revoked_reason TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_port_exposures_token_hash
    ON port_exposures (token_hash) WHERE token_hash IS NOT NULL;`

func newHashedExposureTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(hashedPortExposureSchemaSQL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// portExposeTableHoldsValue reports whether the needle survives anywhere in
// port_exposures. Blunt on purpose: hashing at rest is only worth anything if
// a reader of the database file finds nothing usable.
func portExposeTableHoldsValue(t *testing.T, db *sql.DB, needle string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM port_exposures`)
	if err != nil {
		t.Fatalf("scan port_exposures: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		for _, c := range cells {
			if v := c.(*sql.NullString); v.Valid && strings.Contains(v.String, needle) {
				return true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return false
}

// TestPortExposeToken_LegacyRowStillProxiesAfterHashing is the migration's
// point for /exposed/{token}/…: a capability URL an agent already handed to a
// user must keep working across the upgrade, and the plaintext must leave the
// table.
func TestPortExposeToken_LegacyRowStillProxiesAfterHashing(t *testing.T) {
	ctx := context.Background()
	db := newHashedExposureTestDB(t)

	// Built rather than pasted: a 32-hex literal reads as a real credential to
	// the secret scanner, and a repo that teaches people to silence that
	// scanner for fixtures will one day silence it for a live key.
	legacyToken := "fixture-legacy-exposure-token-" + t.Name()
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES ('pe_legacy', 'ws', 'crew', 'agent', ?, 'ct1', '10.0.0.7', 8080, 'ACTIVE', ?)`,
		legacyToken, expires); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	r := NewPortExposeRegistry(db, portExposeTestLogger())
	if err := r.LoadFromDB(ctx); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	// (b) the pre-migration token still resolves — this is the lookup
	// ServeExposed performs on every proxied request.
	entry, ok := r.Lookup(legacyToken)
	if !ok {
		t.Fatalf("Lookup(legacy plaintext) missed — every published /exposed/ URL just 404'd")
	}
	if entry.ID != "pe_legacy" {
		t.Fatalf("Lookup returned id %q, want pe_legacy", entry.ID)
	}

	// (a) the plaintext is not recoverable from the table.
	if portExposeTableHoldsValue(t, db, legacyToken) {
		t.Errorf("plaintext token still present in port_exposures — a reader of the database file walks away with a live capability URL")
	}
	var hash sql.NullString
	if err := db.QueryRow(`SELECT token_hash FROM port_exposures WHERE id = 'pe_legacy'`).Scan(&hash); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if !hash.Valid || hash.String == "" {
		t.Fatalf("token_hash not backfilled")
	}

	// (c) a wrong token is refused, and so is the at-rest digest.
	if _, ok := r.Lookup(legacyToken + "0"); ok {
		t.Errorf("Lookup(wrong token) hit")
	}
	if _, ok := r.Lookup(hash.String); ok {
		t.Errorf("Lookup(at-rest digest) hit — the digest must not be usable as the capability")
	}
}

// TestPortExposeToken_AddRedactsTheRowItJustIndexed covers the mint path: the
// create handler INSERTs the row and immediately hands the entry to Add, which
// is the choke point that converts the row to its digest at rest.
func TestPortExposeToken_AddRedactsTheRowItJustIndexed(t *testing.T) {
	db := newHashedExposureTestDB(t)
	token := "fixture-new-exposure-token-" + t.Name()
	expires := time.Now().Add(time.Hour)
	if _, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES ('pe_new', 'ws', 'crew', 'agent', ?, 'ct1', '10.0.0.8', 9000, 'ACTIVE', ?)`,
		token, expires.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := NewPortExposeRegistry(db, portExposeTestLogger())
	r.Add(&ExposeEntry{
		ID:            "pe_new",
		Token:         token,
		ContainerID:   "ct1",
		ContainerIP:   "10.0.0.8",
		ContainerPort: 9000,
		ExpiresAt:     expires,
	})

	if _, ok := r.Lookup(token); !ok {
		t.Fatalf("Lookup after Add missed")
	}
	if portExposeTableHoldsValue(t, db, token) {
		t.Errorf("Add left the plaintext token in port_exposures")
	}
	var hash sql.NullString
	if err := db.QueryRow(`SELECT token_hash FROM port_exposures WHERE id = 'pe_new'`).Scan(&hash); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if !hash.Valid || hash.String == "" {
		t.Errorf("Add did not persist token_hash")
	}

	// Revoke drops the in-memory entry using the stored digest, since the
	// plaintext is no longer readable from the row.
	r.RemoveByHash(hash.String)
	if _, ok := r.Lookup(token); ok {
		t.Errorf("RemoveByHash(stored digest) did not drop the entry")
	}
}
