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

// TestCapabilityDigest_SurvivesMasterKeyRotation is the regression test for the
// keyed-digest design this replaced. The digest used to be an HMAC under a
// subkey derived from ENCRYPTION_KEY, which meant the documented master-key
// rotation (CREWSHIP_ENCRYPTION_KEY_VERSION + ENCRYPTION_KEY_V2 +
// POST /admin/reencrypt, whose final step RETIRES ENCRYPTION_KEY) silently
// changed the scheme every presented token hashes under, while every stored
// digest kept the old one — and the cleartext had already been overwritten, so
// nothing was recoverable. Every webhook sender 404s, permanently.
//
// The digest must therefore depend on the token and nothing else.
func TestCapabilityDigest_SurvivesMasterKeyRotation(t *testing.T) {
	ctx := context.Background()

	// A webhook minted while a master key is configured...
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1", 32))
	db := openHashedWebhookTestDB(t)
	store := NewWebhookStore(db)
	wh, err := store.Save(ctx, SaveWebhookInput{
		WorkspaceID: "ws_1", Name: "pre-rotation", TargetPipelineID: "pl_1", Enabled: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// ...survives the rotation: a second key is introduced, /admin/reencrypt
	// runs, and the old ENCRYPTION_KEY is retired.
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "2")
	t.Setenv("ENCRYPTION_KEY_V2", strings.Repeat("b2", 32))
	t.Setenv("ENCRYPTION_KEY", "")

	got, err := store.GetByToken(ctx, wh.Token)
	if err != nil {
		t.Fatalf("GetByToken after master-key rotation: %v — every configured sender just started 404ing, and the cleartext is gone so there is no way back", err)
	}
	if got.ID != wh.ID {
		t.Errorf("GetByToken returned %q, want %q", got.ID, wh.ID)
	}

	// The same property, stated directly on the digest: it depends on the
	// token and on nothing else.
	token := "wh_fixture-rotation-webhook-token-" + t.Name()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1", 32))
	before := HashCapabilityToken(token)
	if !strings.HasPrefix(before, capabilityDigestScheme) {
		t.Errorf("digest scheme = %q, want the %q prefix", before, capabilityDigestScheme)
	}
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("b2", 32))
	if rekeyed := HashCapabilityToken(token); rekeyed != before {
		t.Errorf("digest changed with the master key: %q -> %q", before, rekeyed)
	}
	t.Setenv("ENCRYPTION_KEY", "")
	if retired := HashCapabilityToken(token); retired != before {
		t.Errorf("digest changed when the master key was retired: %q -> %q", before, retired)
	}
}

// TestCapabilityLookupDigest_RejectsWhatIsNotAToken pins the two values that
// must never resolve: an at-rest digest read straight out of the database, and
// the `redacted:<row id>` marker left in the cleartext column (a row id is not
// a secret).
func TestCapabilityLookupDigest_RejectsWhatIsNotAToken(t *testing.T) {
	token := "wh_fixture-guard-token-" + t.Name()
	digest := HashCapabilityToken(token)

	if got := CapabilityLookupDigest(token); got != digest {
		t.Errorf("CapabilityLookupDigest(token) = %q, want the stored digest %q", got, digest)
	}
	for _, bad := range []string{"", digest, redactedWebhookToken("pwh_1")} {
		if got := CapabilityLookupDigest(bad); got != "" {
			t.Errorf("CapabilityLookupDigest(%q) = %q, want \"\" — that value is not a capability", bad, got)
		}
	}
}

// TestBackfill_OneFailedRowDoesNotStrandTheRest reproduces the partial-backfill
// bug: the loop used to `return` on the first failed UPDATE, so a moment of
// write-lock contention part-way through left every LATER row un-hashed — and
// the digest-only lookup then 404'd webhooks whose tokens were still valid,
// for the life of the process, while the store constructed fine and the daemon
// looked healthy.
//
// The failure is injected with a BEFORE UPDATE trigger that aborts for exactly
// one row, which is what a lost race with SQLite's single writer looks like
// from this code's point of view.
func TestBackfill_OneFailedRowDoesNotStrandTheRest(t *testing.T) {
	ctx := context.Background()
	db := openHashedWebhookTestDB(t)

	// Seeded in a fixed order so pwh_b — the row whose UPDATE aborts — is
	// reached before pwh_c. Under the old "return on first error" loop that
	// is what left pwh_c holding cleartext and 404ing.
	ids := []string{"pwh_a", "pwh_b", "pwh_c"}
	tokens := map[string]string{
		"pwh_a": "wh_fixture-backfill-a-" + t.Name(),
		"pwh_b": "wh_fixture-backfill-b-" + t.Name(), // this one's UPDATE aborts
		"pwh_c": "wh_fixture-backfill-c-" + t.Name(),
	}
	for _, id := range ids {
		tok := tokens[id]
		if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_webhooks (id, workspace_id, name, target_pipeline_id, token, inputs_template, enabled, created_at, updated_at)
VALUES (?, 'ws_1', ?, 'pl_1', ?, '{}', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, id, tok); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
CREATE TRIGGER wedge_pwh_b BEFORE UPDATE OF token_hash ON pipeline_webhooks
WHEN NEW.id = 'pwh_b'
BEGIN SELECT RAISE(ABORT, 'database is locked'); END;`); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	store := NewWebhookStore(db)

	// Every row that COULD be hashed must have been, whichever order the
	// scan produced them in.
	for _, id := range []string{"pwh_a", "pwh_c"} {
		var hash sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT token_hash FROM pipeline_webhooks WHERE id = ?`, id).Scan(&hash); err != nil {
			t.Fatalf("read token_hash %s: %v", id, err)
		}
		if !hash.Valid || hash.String == "" {
			t.Errorf("%s was left un-hashed because another row's UPDATE failed", id)
		}
		got, err := store.GetByToken(ctx, tokens[id])
		if err != nil {
			t.Errorf("GetByToken(%s) = %v — a valid webhook token 404s because a DIFFERENT row failed to hash", id, err)
		} else if got.ID != id {
			t.Errorf("GetByToken(%s) returned %q", id, got.ID)
		}
	}

	// The row that could not be hashed must still be resolvable: its token
	// is unchanged and still valid, and its cleartext is still in the
	// column, so refusing it would be a self-inflicted outage.
	got, err := store.GetByToken(ctx, tokens["pwh_b"])
	if err != nil {
		t.Fatalf("GetByToken(pwh_b) = %v — the row that could not be hashed is now unreachable by its own valid token", err)
	}
	if got.ID != "pwh_b" {
		t.Errorf("GetByToken(pwh_b) returned %q", got.ID)
	}
	// ...but the fallback is bounded to un-hashed rows: it must not let a
	// hashed row be resolved by anything other than its digest, and must
	// not resolve an unknown token at all.
	if _, err := store.GetByToken(ctx, tokens["pwh_b"]+"x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByToken(wrong token) = %v, want ErrNotFound", err)
	}
	if _, err := store.GetByToken(ctx, redactedWebhookToken("pwh_a")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByToken(redaction marker) = %v, want ErrNotFound", err)
	}

	// Once the wedge clears, the next resolution self-heals the row.
	if _, err := db.ExecContext(ctx, `DROP TRIGGER wedge_pwh_b`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := store.GetByToken(ctx, tokens["pwh_b"]); err != nil {
		t.Fatalf("GetByToken(pwh_b) after the wedge cleared: %v", err)
	}
	if tableHoldsValue(t, db, "pipeline_webhooks", tokens["pwh_b"]) {
		t.Errorf("the recovered row still holds its cleartext token — the self-heal did not run")
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
