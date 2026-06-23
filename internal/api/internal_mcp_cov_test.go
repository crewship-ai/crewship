package api

// Coverage for internal_mcp.go — ensureFreshOAuthToken's refresh
// decision tree and resolveOneEnvVar's OAUTH2 field-mapping branches.
//
// refreshOAuthToken builds an SSRF-guarded client internally, so its
// network success can't be faked against a loopback server; we use the
// invalid-URL trick ("://bad") to fail the refresh deterministically and
// pin the "return current value unchanged" contract on every error edge.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func covMCPRig(t *testing.T) (db *sql.DB, wsID, userID string) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	return
}

func covEnc(t *testing.T, plain string) string {
	t.Helper()
	enc, err := encryption.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

// ---- ensureFreshOAuthToken ----

func TestEnsureFreshOAuthToken_StillFresh_NoRefresh(t *testing.T) {
	db, _, _ := covMCPRig(t)
	current := covEnc(t, "access-token")
	expiry := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	got := ensureFreshOAuthToken(context.Background(), db, newTestLogger(),
		"cred-x", current, "cid", "", "https://token.example", covEnc(t, "refresh"), expiry)
	if got != current {
		t.Errorf("fresh token must be returned unchanged")
	}
}

func TestEnsureFreshOAuthToken_BadClientSecret_ReturnsCurrent(t *testing.T) {
	db, _, _ := covMCPRig(t)
	current := covEnc(t, "access-token")
	// Expired, so the refresh path is taken; the garbage client secret
	// ciphertext aborts it before any network call.
	expired := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	got := ensureFreshOAuthToken(context.Background(), db, newTestLogger(),
		"cred-x", current, "cid", "garbage-not-encrypted", "https://token.example", covEnc(t, "refresh"), expired)
	if got != current {
		t.Errorf("decrypt failure must return current value")
	}
}

func TestEnsureFreshOAuthToken_BadRefreshToken_ReturnsCurrent(t *testing.T) {
	db, _, _ := covMCPRig(t)
	current := covEnc(t, "access-token")
	got := ensureFreshOAuthToken(context.Background(), db, newTestLogger(),
		"cred-x", current, "cid", "", "https://token.example", "garbage-refresh", "")
	if got != current {
		t.Errorf("refresh decrypt failure must return current value")
	}
}

func TestEnsureFreshOAuthToken_RefreshCallFails_ReturnsCurrent(t *testing.T) {
	db, _, _ := covMCPRig(t)
	current := covEnc(t, "access-token")
	// Invalid token URL → http.NewRequest fails immediately, no network.
	got := ensureFreshOAuthToken(context.Background(), db, newTestLogger(),
		"cred-x", current, "cid", covEnc(t, "secret"), "://bad", covEnc(t, "refresh"), "")
	if got != current {
		t.Errorf("refresh failure must return current value")
	}
}

func TestEnsureFreshOAuthToken_UnparseableExpiryStillRefreshes(t *testing.T) {
	db, _, _ := covMCPRig(t)
	current := covEnc(t, "access-token")
	// Bad expiry string → treated as "needs refresh"; refresh then fails
	// on the invalid URL, so current comes back. Pins the fail-open parse.
	got := ensureFreshOAuthToken(context.Background(), db, newTestLogger(),
		"cred-x", current, "cid", "", "://bad", covEnc(t, "refresh"), "not-a-timestamp")
	if got != current {
		t.Errorf("got different value after failed refresh")
	}
}

// ---- resolveOneEnvVar ----

func covSeedMCPCred(t *testing.T, db *sql.DB, wsID, userID, id, name, credType, encValue string, oauthCols map[string]string) {
	t.Helper()
	cols := map[string]string{
		"oauth_client_id":         "",
		"oauth_client_secret_enc": "",
		"oauth_token_url":         "",
		"oauth_refresh_token_enc": "",
		"oauth_token_expires_at":  "",
	}
	for k, v := range oauthCols {
		cols[k] = v
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, type, encrypted_value, status,
			oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc, oauth_token_expires_at,
			created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'ACTIVE', ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		id, wsID, name, credType, encValue,
		cols["oauth_client_id"], cols["oauth_client_secret_enc"], cols["oauth_token_url"],
		cols["oauth_refresh_token_enc"], cols["oauth_token_expires_at"], userID); err != nil {
		t.Fatalf("seed credential %s: %v", id, err)
	}
}

func TestResolveOneEnvVar_NoMatch(t *testing.T) {
	db, wsID, _ := covMCPRig(t)
	_, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "NOPE_TOKEN")
	if ok {
		t.Error("expected no match")
	}
}

func TestResolveOneEnvVar_PlainSecretByNamePrefix(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	covSeedMCPCred(t, db, wsID, userID, "cred-slack", "slack-bot-token-1", "SECRET", covEnc(t, "xoxb-123"), nil)
	entry, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "SLACK_BOT_TOKEN")
	if !ok {
		t.Fatal("expected match")
	}
	if entry.Value != "xoxb-123" || entry.EnvVar != "SLACK_BOT_TOKEN" || entry.ID != "cred-slack" {
		t.Errorf("entry = %+v", entry)
	}
}

func TestResolveOneEnvVar_PlainSecretDecryptFailure(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	covSeedMCPCred(t, db, wsID, userID, "cred-bad", "badenc-token", "SECRET", "garbage", nil)
	_, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "BADENC_TOKEN")
	if ok {
		t.Error("undecryptable credential must not resolve")
	}
}

func TestResolveOneEnvVar_OAuthClientID(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	covSeedMCPCred(t, db, wsID, userID, "cred-g", "google-oauth-abc", "OAUTH2", covEnc(t, "access"),
		map[string]string{"oauth_client_id": "google-client-id.apps"})
	entry, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "GOOGLE_CLIENT_ID")
	if !ok {
		t.Fatal("expected match")
	}
	if entry.Value != "google-client-id.apps" {
		t.Errorf("value = %q, want client id (entry=%+v)", entry.Value, entry)
	}
}

func TestResolveOneEnvVar_OAuthClientSecret(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	covSeedMCPCred(t, db, wsID, userID, "cred-g2", "google-oauth-def", "OAUTH2", covEnc(t, "access"),
		map[string]string{
			"oauth_client_id":         "cid",
			"oauth_client_secret_enc": covEnc(t, "the-secret"),
		})
	entry, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "GOOGLE_CLIENT_SECRET")
	if !ok {
		t.Fatal("expected match")
	}
	if entry.Value != "the-secret" {
		t.Errorf("value = %q, want decrypted client secret", entry.Value)
	}
}

func TestResolveOneEnvVar_OAuthClientSecret_DecryptFails(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	covSeedMCPCred(t, db, wsID, userID, "cred-g3", "github-oauth-x", "OAUTH2", covEnc(t, "access"),
		map[string]string{
			"oauth_client_id":         "cid",
			"oauth_client_secret_enc": "garbage",
		})
	_, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "GITHUB_CLIENT_SECRET")
	if ok {
		t.Error("undecryptable client secret must not resolve")
	}
}

func TestResolveOneEnvVar_OAuthAccessToken_FreshSkipsRefresh(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	expiry := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	covSeedMCPCred(t, db, wsID, userID, "cred-at", "linear-access-token-1", "OAUTH2", covEnc(t, "tok-fresh"),
		map[string]string{
			"oauth_client_id":         "cid",
			"oauth_token_url":         "https://token.example",
			"oauth_refresh_token_enc": covEnc(t, "refresh"),
			"oauth_token_expires_at":  expiry,
		})
	entry, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "LINEAR_ACCESS_TOKEN")
	if !ok {
		t.Fatal("expected match")
	}
	if entry.Value != "tok-fresh" {
		t.Errorf("value = %q, want tok-fresh", entry.Value)
	}
}

func TestResolveOneEnvVar_OAuthAccessToken_FailedRefreshFallsBack(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	expired := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	covSeedMCPCred(t, db, wsID, userID, "cred-at2", "notion-access-token-1", "OAUTH2", covEnc(t, "tok-stale"),
		map[string]string{
			"oauth_client_id":         "cid",
			"oauth_token_url":         "://bad", // refresh attempt fails fast offline
			"oauth_refresh_token_enc": covEnc(t, "refresh"),
			"oauth_token_expires_at":  expired,
		})
	entry, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "NOTION_ACCESS_TOKEN")
	if !ok {
		t.Fatal("expected match")
	}
	if entry.Value != "tok-stale" {
		t.Errorf("value = %q, want stale token after failed refresh", entry.Value)
	}
}

// autoResolveMCPCredentials end-to-end: reserved refs refused, covered
// refs skipped, resolvable refs appended.
func TestAutoResolveMCPCredentials_EndToEnd(t *testing.T) {
	db, wsID, userID := covMCPRig(t)
	covSeedMCPCred(t, db, wsID, userID, "cred-r", "resolvable-token-1", "SECRET", covEnc(t, "val-1"), nil)

	cfg := `{"mcpServers":{"s":{"env":{
		"RESOLVABLE_TOKEN":"${RESOLVABLE_TOKEN}",
		"INTERNAL_TOKEN":"${INTERNAL_TOKEN}",
		"ALREADY_COVERED":"${ALREADY_COVERED}",
		"UNKNOWN_REF":"${UNKNOWN_REF}"
	}}}}`
	existing := []mcpCredEntry{{ID: "explicit", EnvVar: "ALREADY_COVERED", Value: "x", Type: "SECRET"}}
	out := autoResolveMCPCredentials(context.Background(), db, newTestLogger(), wsID, existing, cfg)
	if len(out) != 2 {
		t.Fatalf("entries = %d, want 2 (explicit + resolved): %+v", len(out), out)
	}
	var found bool
	for _, e := range out {
		if e.EnvVar == "RESOLVABLE_TOKEN" && e.Value == "val-1" {
			found = true
		}
		if e.EnvVar == "INTERNAL_TOKEN" {
			t.Error("reserved namespace must never resolve")
		}
	}
	if !found {
		t.Errorf("RESOLVABLE_TOKEN not resolved: %+v", out)
	}
}
