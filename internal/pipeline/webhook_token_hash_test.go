package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// hashedWebhookSchemaSQL mirrors the post-migration shape of
// pipeline_webhooks: the pre-existing plaintext `token` column plus the
// `token_hash` column added by 20260810171000_hash_capability_tokens.
const hashedWebhookSchemaSQL = `
CREATE TABLE IF NOT EXISTS pipeline_webhooks (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL,
    name                     TEXT NOT NULL,
    target_pipeline_id       TEXT NOT NULL,
    target_pipeline_version  INTEGER,
    token                    TEXT NOT NULL UNIQUE,
    token_hash               TEXT,
    signing_secret           TEXT,
    inputs_template          TEXT NOT NULL DEFAULT '{}',
    enabled                  INTEGER NOT NULL DEFAULT 1,
    rate_limit_per_min       INTEGER NOT NULL DEFAULT 0,
    last_fired_at            TEXT,
    last_status              TEXT,
    last_run_id              TEXT,
    fire_count               INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_webhooks_token_hash
    ON pipeline_webhooks (token_hash) WHERE token_hash IS NOT NULL;`

func openHashedWebhookTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.ExecContext(context.Background(), hashedWebhookSchemaSQL); err != nil {
		t.Fatalf("webhook schema: %v", err)
	}
	return db
}

// tableHoldsValue reports whether any cell of any TEXT column in the table
// still contains the needle. Deliberately blunt: the point of hashing at rest
// is that a reader of the database file finds nothing usable, so the assertion
// is "the plaintext is nowhere in this table", not "one named column changed".
func tableHoldsValue(t *testing.T, db *sql.DB, table, needle string) bool {
	t.Helper()
	rows, err := db.Query("SELECT * FROM " + table) //nolint:gosec // table name is a test constant
	if err != nil {
		t.Fatalf("scan %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %s: %v", table, err)
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

// TestWebhookToken_LegacyRowStillFiresAfterHashing is the migration's whole
// point. A webhook created BEFORE the token was hashed has a URL that is
// already configured in somebody's GitHub/Stripe/whatever. Hashing in place
// must keep that URL working, while removing the plaintext from the table.
func TestWebhookToken_LegacyRowStillFiresAfterHashing(t *testing.T) {
	ctx := context.Background()
	db := openHashedWebhookTestDB(t)

	// A row exactly as a pre-migration build wrote it: plaintext token,
	// no hash.
	// Built rather than pasted — see the note in
	// internal/api/port_expose_token_hash_test.go. The wh_ prefix is kept
	// because the code checks it.
	legacyToken := "wh_fixture-legacy-webhook-token-" + t.Name()
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_webhooks (id, workspace_id, name, target_pipeline_id, token, inputs_template, enabled, created_at, updated_at)
VALUES ('pwh_legacy', 'ws_1', 'legacy', 'pl_1', ?, '{}', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		legacyToken); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	store := NewWebhookStore(db)

	// (b) the pre-migration token still authenticates.
	got, err := store.GetByToken(ctx, legacyToken)
	if err != nil {
		t.Fatalf("GetByToken(legacy plaintext) after hashing: %v — every already-published webhook URL just broke", err)
	}
	if got.ID != "pwh_legacy" {
		t.Fatalf("GetByToken returned id %q, want pwh_legacy", got.ID)
	}

	// (a) the plaintext is not recoverable from the table.
	if tableHoldsValue(t, db, "pipeline_webhooks", legacyToken) {
		t.Errorf("plaintext token still present in pipeline_webhooks after hashing — a reader of the database file walks away with a live webhook URL")
	}

	// The hash column is populated and is not the plaintext.
	var hash sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM pipeline_webhooks WHERE id = 'pwh_legacy'`).Scan(&hash); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if !hash.Valid || hash.String == "" {
		t.Fatalf("token_hash not backfilled for the legacy row")
	}
	if hash.String == legacyToken {
		t.Fatalf("token_hash holds the plaintext token")
	}

	// (c) a wrong token is refused.
	if _, err := store.GetByToken(ctx, legacyToken+"x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByToken(wrong) = %v, want ErrNotFound", err)
	}
	if _, err := store.GetByToken(ctx, hash.String); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByToken(stored digest) = %v, want ErrNotFound — presenting the at-rest digest must not authenticate", err)
	}
}

// TestWebhookToken_KeyedByEncryptionKey pins the key choice. With an
// ENCRYPTION_KEY present the digest is the keyed HMAC scheme, and it changes
// when the key changes — which is what makes a stolen database file useless on
// its own. Without a key the unkeyed scheme is used instead, so a process that
// has not got one (a test binary, a tools-only build) still resolves rows
// rather than failing every capability check.
func TestWebhookToken_KeyedByEncryptionKey(t *testing.T) {
	token := "wh_fixture-new-webhook-token-" + t.Name()

	unkeyed := HashCapabilityToken(token)
	if !strings.HasPrefix(unkeyed, capabilityDigestSHAScheme) {
		t.Fatalf("with no ENCRYPTION_KEY the digest should use the unkeyed scheme, got %q", unkeyed)
	}

	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1", 32))
	keyed := HashCapabilityToken(token)
	if !strings.HasPrefix(keyed, capabilityDigestHMACScheme) {
		t.Fatalf("with an ENCRYPTION_KEY the digest should use the keyed scheme, got %q", keyed)
	}
	if keyed == unkeyed {
		t.Fatalf("keyed and unkeyed digests must differ")
	}

	t.Setenv("ENCRYPTION_KEY", strings.Repeat("b2", 32))
	if rotated := HashCapabilityToken(token); rotated == keyed {
		t.Errorf("digest did not change with the key — the derived subkey is not actually keying anything")
	}

	// Lookups accept both schemes, so a row written before the key existed
	// keeps resolving after it appears.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1", 32))
	digests := CapabilityTokenDigests(token)
	if len(digests) != 2 || digests[0] != keyed || digests[1] != unkeyed {
		t.Errorf("CapabilityTokenDigests = %v, want [keyed unkeyed]", digests)
	}
	if CapabilityTokenDigests(keyed) != nil {
		t.Errorf("a stored digest must not resolve to any lookup digest")
	}
}

// TestWebhookToken_NewRowNeverStoresPlaintext covers the mint path: Save
// returns the cleartext once (the create response shows it) and persists only
// the digest.
func TestWebhookToken_NewRowNeverStoresPlaintext(t *testing.T) {
	ctx := context.Background()
	db := openHashedWebhookTestDB(t)
	store := NewWebhookStore(db)

	wh, err := store.Save(ctx, SaveWebhookInput{
		WorkspaceID:      "ws_1",
		Name:             "fresh",
		TargetPipelineID: "pl_1",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if wh.Token == "" {
		t.Fatalf("Save must return the cleartext token once so the create response can show it")
	}
	if tableHoldsValue(t, db, "pipeline_webhooks", wh.Token) {
		t.Errorf("newly minted webhook token stored in plaintext")
	}
	if _, err := store.GetByToken(ctx, wh.Token); err != nil {
		t.Errorf("GetByToken(fresh token) = %v, want the row", err)
	}
	if _, err := store.GetByToken(ctx, "wh_not_a_real_token"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByToken(unknown) = %v, want ErrNotFound", err)
	}
}
