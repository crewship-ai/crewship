package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// setV140TestKey installs a random AES-256 key for the test (generated at
// runtime so no secret-shaped literal lives in the source).
func setV140TestKey(t *testing.T) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(b))
}

func v140FreshDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v140.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// TestMigrateV140_EncryptsWebhookSecrets backfills plaintext webhook secrets
// (#1072/#1029). Migrate itself runs v140 on empty tables (no-op); we seed
// plaintext rows and re-run the idempotent hook to exercise the backfill.
func TestMigrateV140_EncryptsWebhookSecrets(t *testing.T) {
	setV140TestKey(t)
	db := v140FreshDB(t)

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C','c')`)
	mustExec(t, db.DB, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, webhook_secret)
		VALUES ('ag1','cr1','ws1','A','a','plain-agent-secret')`)
	mustExec(t, db.DB, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('p','ws1','p','P','{}','h')`)
	mustExec(t, db.DB, `INSERT INTO pipeline_webhooks
		(id, workspace_id, name, target_pipeline_id, token, signing_secret, inputs_template, enabled, rate_limit_per_min, created_at, updated_at)
		VALUES ('wh1','ws1','n','p','tok','plain-signing-secret','{}',1,60,'t','t')`)
	// #1029 F3: a USER signing_secret literally shaped like an envelope
	// ("v2:...") must still be encrypted — the vN: prefix is not proof of
	// encryption for a user-controlled value, so v140 decrypt-verifies.
	mustExec(t, db.DB, `INSERT INTO pipeline_webhooks
		(id, workspace_id, name, target_pipeline_id, token, signing_secret, inputs_template, enabled, rate_limit_per_min, created_at, updated_at)
		VALUES ('wh2','ws1','n2','p','tok2','v2:looks-like-an-envelope-but-is-plaintext','{}',1,60,'t','t')`)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runV140 := func() {
		tx, err := db.DB.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := migrationEncryptWebhookSecrets(context.Background(), tx, logger); err != nil {
			tx.Rollback()
			t.Fatalf("migrationEncryptWebhookSecrets: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	runV140()

	assertEncrypted := func(query, wantPlain string) {
		var stored string
		if err := db.DB.QueryRow(query).Scan(&stored); err != nil {
			t.Fatalf("read %q: %v", query, err)
		}
		if !encryption.IsEncrypted(stored) {
			t.Errorf("%s: stored still plaintext: %q", query, stored)
		}
		dec, err := encryption.Decrypt(stored)
		if err != nil {
			t.Fatalf("%s: decrypt: %v", query, err)
		}
		if dec != wantPlain {
			t.Errorf("%s: decrypts to %q, want %q", query, dec, wantPlain)
		}
	}
	assertEncrypted(`SELECT webhook_secret FROM agents WHERE id='ag1'`, "plain-agent-secret")
	assertEncrypted(`SELECT signing_secret FROM pipeline_webhooks WHERE id='wh1'`, "plain-signing-secret")
	assertEncrypted(`SELECT signing_secret FROM pipeline_webhooks WHERE id='wh2'`, "v2:looks-like-an-envelope-but-is-plaintext")

	// Idempotent: a second run must not double-encrypt (values still decrypt to
	// the ORIGINAL plaintext, not to a nested envelope). The genuinely-encrypted
	// rows are now real envelopes, so v140's decrypt-verify skips them.
	runV140()
	assertEncrypted(`SELECT webhook_secret FROM agents WHERE id='ag1'`, "plain-agent-secret")
	assertEncrypted(`SELECT signing_secret FROM pipeline_webhooks WHERE id='wh1'`, "plain-signing-secret")
	assertEncrypted(`SELECT signing_secret FROM pipeline_webhooks WHERE id='wh2'`, "v2:looks-like-an-envelope-but-is-plaintext")
}

// TestMigrateV140_NoKey_LeavesPlaintext pins the fail-open path: with no key
// the backfill is a no-op (values stay plaintext), preserving key-less
// deployments.
func TestMigrateV140_NoKey_LeavesPlaintext(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	db := v140FreshDB(t)

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C','c')`)
	mustExec(t, db.DB, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, webhook_secret)
		VALUES ('ag1','cr1','ws1','A','a','plain-agent-secret')`)

	tx, _ := db.DB.Begin()
	if err := migrationEncryptWebhookSecrets(context.Background(), tx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migration: %v", err)
	}
	tx.Commit()

	var stored string
	if err := db.DB.QueryRow(`SELECT webhook_secret FROM agents WHERE id='ag1'`).Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored != "plain-agent-secret" {
		t.Errorf("no-key backfill changed the value: %q", stored)
	}
}
