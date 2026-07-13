package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// Dummy AES-256 test keys (64 hex chars), assembled via Repeat so secret
// scanners don't flag the literals.
var (
	reencKeyV1 = strings.Repeat("0123456789abcdef", 4)
	reencKeyV2 = strings.Repeat("fedcba9876543210", 4)
)

// reencSetV1 pins the process to the v1 key so seeding produces v1 envelopes.
func reencSetV1(t *testing.T) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", reencKeyV1)
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	os.Unsetenv("CREWSHIP_ENCRYPTION_KEY_VERSION")
	t.Setenv("ENCRYPTION_KEY_V2", "")
	os.Unsetenv("ENCRYPTION_KEY_V2")
}

// reencRotateToV2 simulates the operator's rotation: new key under
// ENCRYPTION_KEY_V2, version flipped to v2, old key kept for decryption.
func reencRotateToV2(t *testing.T) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", reencKeyV1)
	t.Setenv("ENCRYPTION_KEY_V2", reencKeyV2)
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2")
}

func reencMustEncrypt(t *testing.T, plain string) string {
	t.Helper()
	enc, err := encryption.Encrypt(plain)
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}
	return enc
}

func reencExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// seedReencryptFixtures inserts one representative row per inventoried
// envelope column (plus the defensive edge cases: empty value, legacy
// non-versioned envelope, undecryptable garbage, plaintext non-credential
// escalation resolution). Returns the expected plaintexts keyed by
// "table.column" checks done later.
func seedReencryptFixtures(t *testing.T, db *sql.DB) {
	t.Helper()

	reencExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'W', 'w1')`)
	reencExec(t, db, `INSERT INTO users (id, email) VALUES ('u1', 'u1@example.com')`)

	// credentials: all four envelope columns on one row.
	reencExec(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, encrypted_refresh_token, oauth_client_secret_enc, oauth_refresh_token_enc, created_by)
		VALUES ('c1', 'ws1', 'main', ?, ?, ?, ?, 'u1')`,
		reencMustEncrypt(t, "secret-A"),
		reencMustEncrypt(t, "refresh-A"),
		reencMustEncrypt(t, "client-secret-A"),
		reencMustEncrypt(t, "oauth-refresh-A"))
	// PENDING-OAuth style row: empty encrypted_value must be left alone.
	reencExec(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('c2', 'ws1', 'pending', '', 'u1')`)
	// Legacy pre-envelope value (raw base64, no version prefix) — still v1-keyed.
	legacy := strings.TrimPrefix(reencMustEncrypt(t, "legacy-B"), "v1:")
	reencExec(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('c3', 'ws1', 'legacy', ?, 'u1')`, legacy)
	// Garbage that decrypts with no key: counted failed, left untouched.
	reencExec(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('c4', 'ws1', 'garbage', 'not-an-envelope-at-all', 'u1')`)

	// credential_rotations: live envelope + scrubbed terminal row.
	reencExec(t, db, `INSERT INTO credential_rotations (id, credential_id, old_value, expires_at)
		VALUES ('r1', 'c1', ?, '2027-01-01T00:00:00Z')`, reencMustEncrypt(t, "old-value-A"))
	reencExec(t, db, `INSERT INTO credential_rotations (id, credential_id, old_value, expires_at, status)
		VALUES ('r2', 'c1', '', '2026-01-01T00:00:00Z', 'EXPIRED')`)

	// notification_channels: webhook secret + NULL email channel.
	reencExec(t, db, `INSERT INTO notification_channels (id, workspace_id, type, secret_enc)
		VALUES ('n1', 'ws1', 'webhook', ?)`, reencMustEncrypt(t, "hook-secret-A"))
	reencExec(t, db, `INSERT INTO notification_channels (id, workspace_id, type, secret_enc)
		VALUES ('n2', 'ws1', 'email', NULL)`)

	// composio_settings.
	reencExec(t, db, `INSERT INTO composio_settings (workspace_id, encrypted_api_key)
		VALUES ('ws1', ?)`, reencMustEncrypt(t, "composio-key-A"))

	// oauth_states PKCE verifier.
	reencExec(t, db, `INSERT INTO oauth_states (state, credential_id, workspace_id, redirect_uri, code_verifier)
		VALUES ('st1', 'c1', 'ws1', 'http://cb', ?)`, reencMustEncrypt(t, "verifier-A"))

	// escalations: CREDENTIAL resolution is an envelope; TEXT resolution is
	// operator plaintext and MUST NOT be touched.
	reencExec(t, db, `INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, resolution, created_at, type)
		VALUES ('e1', 'ws1', 'cr1', 'ch1', 'a1', 'need cred', 'RESOLVED', ?, '2026-01-01T00:00:00Z', 'CREDENTIAL')`,
		reencMustEncrypt(t, "escalated-cred-A"))
	reencExec(t, db, `INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, resolution, created_at, type)
		VALUES ('e2', 'ws1', 'cr1', 'ch1', 'a1', 'question', 'RESOLVED', 'plain human answer', '2026-01-01T00:00:00Z', 'TEXT')`)
}

func callReencrypt(t *testing.T, db *sql.DB, role string) (*httptest.ResponseRecorder, reencryptResponse) {
	t.Helper()
	h := NewReencryptHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	req := httptest.NewRequest("POST", "/api/v1/admin/reencrypt", nil)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, "ws1")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u1"})
	ctx = context.WithValue(ctx, ctxRole, role)
	rec := httptest.NewRecorder()
	h.Reencrypt(rec, req.WithContext(ctx))
	var out reencryptResponse
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal response: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, out
}

func TestReencrypt_RotatesEveryInventoriedColumn(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	seedReencryptFixtures(t, db)

	reencRotateToV2(t)
	rec, out := callReencrypt(t, db, "OWNER")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if out.KeyVersion != "v2" {
		t.Fatalf("key_version = %q, want v2", out.KeyVersion)
	}
	// 10 envelopes: c1×4, c3 legacy, r1, n1, composio, oauth_state, e1.
	if out.Reencrypted != 10 {
		t.Fatalf("reencrypted = %d, want 10 (%s)", out.Reencrypted, rec.Body.String())
	}
	if out.Failed != 1 { // c4 garbage
		t.Fatalf("failed = %d, want 1", out.Failed)
	}
	if out.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", out.Skipped)
	}

	// Every rotated value must now be a v2 envelope that decrypts to the
	// original plaintext.
	checks := []struct {
		query string
		args  []any
		want  string
	}{
		{`SELECT encrypted_value FROM credentials WHERE id='c1'`, nil, "secret-A"},
		{`SELECT encrypted_refresh_token FROM credentials WHERE id='c1'`, nil, "refresh-A"},
		{`SELECT oauth_client_secret_enc FROM credentials WHERE id='c1'`, nil, "client-secret-A"},
		{`SELECT oauth_refresh_token_enc FROM credentials WHERE id='c1'`, nil, "oauth-refresh-A"},
		{`SELECT encrypted_value FROM credentials WHERE id='c3'`, nil, "legacy-B"},
		{`SELECT old_value FROM credential_rotations WHERE id='r1'`, nil, "old-value-A"},
		{`SELECT secret_enc FROM notification_channels WHERE id='n1'`, nil, "hook-secret-A"},
		{`SELECT encrypted_api_key FROM composio_settings WHERE workspace_id='ws1'`, nil, "composio-key-A"},
		{`SELECT code_verifier FROM oauth_states WHERE state='st1'`, nil, "verifier-A"},
		{`SELECT resolution FROM escalations WHERE id='e1'`, nil, "escalated-cred-A"},
	}
	for _, c := range checks {
		var stored string
		if err := db.QueryRow(c.query, c.args...).Scan(&stored); err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		if !strings.HasPrefix(stored, "v2:") {
			t.Errorf("%s: expected v2 envelope, got %q", c.query, stored[:min(12, len(stored))])
			continue
		}
		plain, err := encryption.Decrypt(stored)
		if err != nil {
			t.Errorf("%s: decrypt after rotation: %v", c.query, err)
			continue
		}
		if plain != c.want {
			t.Errorf("%s: round-trip = %q, want %q", c.query, plain, c.want)
		}
	}

	// Untouchables.
	var v string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id='c2'`).Scan(&v); err != nil || v != "" {
		t.Errorf("empty pending value must stay empty, got %q (%v)", v, err)
	}
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id='c4'`).Scan(&v); err != nil || v != "not-an-envelope-at-all" {
		t.Errorf("undecryptable value must stay untouched, got %q (%v)", v, err)
	}
	if err := db.QueryRow(`SELECT old_value FROM credential_rotations WHERE id='r2'`).Scan(&v); err != nil || v != "" {
		t.Errorf("scrubbed rotation must stay empty, got %q (%v)", v, err)
	}
	if err := db.QueryRow(`SELECT resolution FROM escalations WHERE id='e2'`).Scan(&v); err != nil || v != "plain human answer" {
		t.Errorf("non-credential escalation resolution must stay plaintext, got %q (%v)", v, err)
	}

	// An audit row must land.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action = 'admin.reencrypt'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("expected 1 admin.reencrypt audit row, got %d (%v)", n, err)
	}
}

func TestReencrypt_Idempotent(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	seedReencryptFixtures(t, db)

	reencRotateToV2(t)
	if rec, _ := callReencrypt(t, db, "OWNER"); rec.Code != 200 {
		t.Fatalf("first run: %d", rec.Code)
	}
	rec, out := callReencrypt(t, db, "OWNER")
	if rec.Code != 200 {
		t.Fatalf("second run: %d", rec.Code)
	}
	if out.Reencrypted != 0 {
		t.Fatalf("second run reencrypted = %d, want 0", out.Reencrypted)
	}
	if out.Skipped != 10 {
		t.Fatalf("second run skipped = %d, want 10", out.Skipped)
	}
	if out.Failed != 1 {
		t.Fatalf("second run failed = %d, want 1", out.Failed)
	}
}

// TestReencrypt_NoOpOnV1: running the command without any rotation in
// progress is safe — everything already at the current version is skipped.
func TestReencrypt_NoOpOnV1(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	seedReencryptFixtures(t, db)

	rec, out := callReencrypt(t, db, "OWNER")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// The legacy raw-base64 value (c3) is normalized to a v1 envelope; the
	// 9 already-current envelopes are skipped.
	if out.Reencrypted != 1 || out.Skipped != 9 || out.Failed != 1 {
		t.Fatalf("got reencrypted=%d skipped=%d failed=%d, want 1/9/1", out.Reencrypted, out.Skipped, out.Failed)
	}
}

func TestReencrypt_RequiresAdmin(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	rec, _ := callReencrypt(t, db, "MEMBER")
	if rec.Code != 403 {
		t.Fatalf("expected 403 for MEMBER, got %d", rec.Code)
	}
}

// TestReencrypt_FailsClosedOnMisconfiguredKey: version flipped but the new
// key missing — the handler must refuse to start rather than partially
// process.
func TestReencrypt_FailsClosedOnMisconfiguredKey(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	seedReencryptFixtures(t, db)

	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2") // no ENCRYPTION_KEY_V2
	rec, _ := callReencrypt(t, db, "OWNER")
	if rec.Code != 500 {
		t.Fatalf("expected 500 on missing versioned key, got %d: %s", rec.Code, rec.Body.String())
	}
	// Nothing may have been touched.
	var stored string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id='c1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "v1:") {
		t.Fatalf("rows must be untouched on aborted run, got %q", stored[:8])
	}
}

// TestReencrypt_FailOpenWebhookColumns_BarePlaintextSkipped pins the #1072
// adversarial follow-up: bare (non-enveloped) webhook secrets are the expected
// key-less/legacy state, so master-key rotation must count them as Skipped, not
// Failed — otherwise the "failed=0 ⇒ retire old key" gate is poisoned forever.
func TestReencrypt_FailOpenWebhookColumns_BarePlaintextSkipped(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	reencExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w1')`)
	reencExec(t, db, `INSERT INTO users (id, email) VALUES ('u1','u1@example.com')`)
	// Bare plaintext webhook secrets — the fail-open key-less steady state.
	reencExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug, webhook_secret)
		VALUES ('ag1','ws1','A','a','barehexsecret')`)
	reencExec(t, db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('p1','ws1','p','P','{}','h')`)
	reencExec(t, db, `INSERT INTO pipeline_webhooks
		(id, workspace_id, name, target_pipeline_id, token, signing_secret, inputs_template, enabled, rate_limit_per_min, created_at, updated_at)
		VALUES ('wh1','ws1','n','p1','tok','baresig','{}',1,60,'t','t')`)

	reencRotateToV2(t)
	rec, out := callReencrypt(t, db, "OWNER")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	check := func(table, column string) {
		for _, c := range out.Columns {
			if c.Table == table && c.Column == column {
				if c.Failed != 0 {
					t.Errorf("%s.%s: Failed=%d, want 0 (bare fail-open value must be Skipped, not Failed)", table, column, c.Failed)
				}
				if c.Skipped < 1 {
					t.Errorf("%s.%s: Skipped=%d, want >=1", table, column, c.Skipped)
				}
				return
			}
		}
		t.Errorf("%s.%s missing from the reencrypt inventory", table, column)
	}
	check("agents", "webhook_secret")
	check("pipeline_webhooks", "signing_secret")

	// Bare values must be left untouched.
	var ws, sg string
	_ = db.QueryRow(`SELECT webhook_secret FROM agents WHERE id='ag1'`).Scan(&ws)
	_ = db.QueryRow(`SELECT signing_secret FROM pipeline_webhooks WHERE id='wh1'`).Scan(&sg)
	if ws != "barehexsecret" || sg != "baresig" {
		t.Errorf("bare values were mutated: webhook_secret=%q signing_secret=%q", ws, sg)
	}
}
