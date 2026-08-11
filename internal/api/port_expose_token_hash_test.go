package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
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
	if !r.RemoveByHash(hash.String) {
		t.Errorf("RemoveByHash(stored digest) reported no entry to drop")
	}
	if _, ok := r.Lookup(token); ok {
		t.Errorf("RemoveByHash(stored digest) did not drop the entry")
	}
}

// wedgeExposureHash installs a trigger that aborts the token_hash UPDATE for
// one row — what losing the race with SQLite's single writer looks like from
// this code's point of view, and the condition every test below is about.
func wedgeExposureHash(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`
CREATE TRIGGER wedge_` + id + ` BEFORE UPDATE OF token_hash ON port_exposures
WHEN NEW.id = '` + id + `'
BEGIN SELECT RAISE(ABORT, 'database is locked'); END;`); err != nil { //nolint:gosec // id is a test constant
		t.Fatalf("install trigger: %v", err)
	}
}

// TestPortExposeLoad_UnhashedActiveRowIsRecoveredNotSkipped covers the
// un-hashed ACTIVE row on boot. LoadFromDB used to skip it and leave it ACTIVE:
// GET …/port-expose then reported a live exposure with a future expiry while
// every request to its URL 404'd, for the whole TTL. The row's cleartext is
// still in the column in that state, so the honest answer is to hash it now and
// keep serving the URL the user already holds.
func TestPortExposeLoad_UnhashedActiveRowIsRecoveredNotSkipped(t *testing.T) {
	ctx := context.Background()
	db := newHashedExposureTestDB(t)

	token := "fixture-unhashed-exposure-token-" + t.Name()
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES ('pe_wedged', 'ws', 'crew', 'agent', ?, 'ct1', '10.0.0.7', 8080, 'ACTIVE', ?)`,
		token, expires); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wedgeExposureHash(t, db, "pe_wedged")

	r := NewPortExposeRegistry(db, portExposeTestLogger())
	if err := r.LoadFromDB(ctx); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	if _, ok := r.Lookup(token); !ok {
		t.Fatalf("Lookup missed for an ACTIVE row whose hashing failed — the exposure is reported live and 404s on every request")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM port_exposures WHERE id = 'pe_wedged'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE — the exposure is serving, so the row must say so", status)
	}
}

// TestPortExposeLoad_UnresolvableActiveRowIsExpired is the other half: when the
// cleartext is gone too (the row was written by the create path, which stores
// the spent marker, and the digest write then failed) no token can ever resolve
// it. It must not be left ACTIVE, because the list endpoint would keep
// promising a URL that cannot work.
func TestPortExposeLoad_UnresolvableActiveRowIsExpired(t *testing.T) {
	ctx := context.Background()
	db := newHashedExposureTestDB(t)

	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES ('pe_dead', 'ws', 'crew', 'agent', 'redacted:pe_dead', 'ct1', '10.0.0.7', 8080, 'ACTIVE', ?)`,
		expires); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := NewPortExposeRegistry(db, portExposeTestLogger())
	if err := r.LoadFromDB(ctx); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("a row with neither a digest nor a cleartext token was loaded into the registry")
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM port_exposures WHERE id = 'pe_dead'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "EXPIRED" {
		t.Fatalf("status = %q, want EXPIRED — the user is being told an unreachable exposure is live", status)
	}
	// The marker must not be usable as a token either.
	if _, ok := r.Lookup("redacted:pe_dead"); ok {
		t.Errorf("the redaction marker resolved as a capability token")
	}
}

// TestRevoke_DropsEntryWhenTheRowHasNoDigest is the revoke-path regression.
// persistTokenHash is best-effort, so an exposure can be live in memory (keyed
// by a digest that exists nowhere else) while its row carries no token_hash.
// Revoke's old fallback read the `token` column — which post-#1888 always holds
// `redacted:<id>` — so it matched nothing, returned 200, and ServeExposed kept
// reverse-proxying into the crew container until the process restarted.
func TestRevoke_DropsEntryWhenTheRowHasNoDigest(t *testing.T) {
	db := newHashedExposureTestDB(t)
	token := "fixture-revoke-exposure-token-" + t.Name()
	expires := time.Now().Add(time.Hour)
	if _, err := db.Exec(`
INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
VALUES ('pe_live', 'ws1', 'crew1', 'agent1', 'redacted:pe_live', 'ct1', '10.0.0.9', 9000, 'ACTIVE', ?)`,
		expires.UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wedgeExposureHash(t, db, "pe_live")

	reg := NewPortExposeRegistry(db, portExposeTestLogger())
	h := NewPortExposeHandler(db, reg, nil, AllowAllPolicy{}, nil, DefaultPortExposeConfig(), portExposeTestLogger())

	reg.Add(&ExposeEntry{
		ID: "pe_live", Token: token,
		ContainerID: "ct1", ContainerIP: "10.0.0.9", ContainerPort: 9000,
		ExpiresAt: expires,
	})
	var stored sql.NullString
	if err := db.QueryRow(`SELECT token_hash FROM port_exposures WHERE id = 'pe_live'`).Scan(&stored); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if stored.Valid && stored.String != "" {
		t.Fatalf("fixture is wrong: the digest write was supposed to fail, got %q", stored.String)
	}
	if _, ok := reg.Lookup(token); !ok {
		t.Fatalf("fixture is wrong: the exposure should be live in memory")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/crews/crew1/port-expose/pe_live/revoke", nil)
	req = req.WithContext(withWorkspace(req.Context(), "ws1", "MANAGER"))
	req.SetPathValue("crewId", "crew1")
	req.SetPathValue("id", "pe_live")
	rec := httptest.NewRecorder()
	h.Revoke(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if _, ok := reg.Lookup(token); ok {
		t.Errorf("revoke returned 200 but left the exposure in the registry — it keeps proxying into the crew container")
	}
	serveRec := httptest.NewRecorder()
	serveReq := httptest.NewRequest(http.MethodGet, "/exposed/"+token+"/", nil)
	serveReq.SetPathValue("token", token)
	h.ServeExposed(serveRec, serveReq)
	if serveRec.Code != http.StatusNotFound {
		t.Errorf("ServeExposed after revoke = %d, want 404 — the revoked capability URL still works", serveRec.Code)
	}
}
