package api

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// covOTLogger returns a quiet logger for refresh-worker tests.
func covOTLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// covOTSeedExpiring inserts an ACTIVE OAUTH2 credential whose token is
// expiring soon, with caller-supplied (already-encrypted-or-not) values
// for the client-secret and refresh-token columns. This lets each test
// exercise a specific branch of refreshExpiringTokens.
func covOTSeedExpiring(t *testing.T, db *sql.DB, wsID, userID, credID, clientSecretEnc, refreshTokenEnc, tokenURL string) {
	t.Helper()
	encAccess, err := encryption.Encrypt("old-access")
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	expiresSoon := time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, status,
			oauth_client_id, oauth_client_secret_enc, oauth_token_url, oauth_refresh_token_enc,
			oauth_token_expires_at, created_by, created_at, updated_at, scope, provider)
		VALUES (?, ?, ?, ?, 'OAUTH2', 'ACTIVE',
			'client-id', ?, ?, ?, ?, ?, datetime('now'), datetime('now'), 'WORKSPACE', 'NONE')`,
		credID, wsID, "expiring-"+credID, encAccess, clientSecretEnc, tokenURL, refreshTokenEnc, expiresSoon, userID); err != nil {
		t.Fatalf("seed expiring credential: %v", err)
	}
}

func covOTCredentialStatus(t *testing.T, db *sql.DB, credID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow("SELECT status FROM credentials WHERE id = ?", credID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	return status
}

// TestCovOTRefreshExpiring_BadRefreshTokenMarksExpired drives the
// decrypt-refresh-token failure branch: an undecryptable
// oauth_refresh_token_enc must flip the credential to EXPIRED and not
// attempt any network call.
func TestCovOTRefreshExpiring_BadRefreshTokenMarksExpired(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// "not-base64-ciphertext" is not a valid encryption.Decrypt input.
	covOTSeedExpiring(t, db, wsID, userID, "cred-bad-rt", "", "not-a-valid-ciphertext", "http://192.0.2.1:1/token")

	refreshExpiringTokens(context.Background(), db, nil, covOTLogger())

	if got := covOTCredentialStatus(t, db, "cred-bad-rt"); got != "EXPIRED" {
		t.Errorf("status = %q, want EXPIRED (undecryptable refresh token should expire credential)", got)
	}
}

// TestCovOTRefreshExpiring_EmptyRefreshTokenSkips drives the branch where
// the refresh token decrypts to an empty string: the row is skipped with
// no status change and no network call.
func TestCovOTRefreshExpiring_EmptyRefreshTokenSkips(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Valid ciphertext that decrypts to "" — the query filter requires a
	// non-empty enc column, so we encrypt an empty string here.
	encEmpty, err := encryption.Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	covOTSeedExpiring(t, db, wsID, userID, "cred-empty-rt", "", encEmpty, "http://192.0.2.1:1/token")

	refreshExpiringTokens(context.Background(), db, nil, covOTLogger())

	// Empty refresh token => continue, no refresh attempt, status untouched.
	if got := covOTCredentialStatus(t, db, "cred-empty-rt"); got != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE (empty refresh token should be skipped untouched)", got)
	}
}

// TestCovOTRefreshExpiring_BadClientSecretSkips drives the
// decrypt-client-secret failure branch: a non-empty but undecryptable
// oauth_client_secret_enc causes the row to be skipped (continue) before
// the refresh token is touched, leaving status unchanged.
func TestCovOTRefreshExpiring_BadClientSecretSkips(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	encRefresh, err := encryption.Encrypt("rt-valid")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	covOTSeedExpiring(t, db, wsID, userID, "cred-bad-secret", "garbage-secret-enc", encRefresh, "http://192.0.2.1:1/token")

	refreshExpiringTokens(context.Background(), db, nil, covOTLogger())

	if got := covOTCredentialStatus(t, db, "cred-bad-secret"); got != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE (undecryptable client secret should skip without expiring)", got)
	}
}

// TestCovOTRefreshExpiring_RefreshFailsMarksExpired drives the path where
// the refresh token decrypts cleanly (with a valid client secret) but the
// network refresh fails (unroutable token URL) — the credential is marked
// EXPIRED and a workspace event would be broadcast (nil hub is a no-op).
func TestCovOTRefreshExpiring_RefreshFailsMarksExpired(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	encRefresh, err := encryption.Encrypt("rt-valid")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	encSecret, err := encryption.Encrypt("client-secret")
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	// 192.0.2.0/24 is RFC 5737 TEST-NET-1 — never routable, so
	// refreshOAuthToken returns a dial error.
	covOTSeedExpiring(t, db, wsID, userID, "cred-refresh-fail", encSecret, encRefresh, "http://192.0.2.1:1/token")

	refreshExpiringTokens(context.Background(), db, nil, covOTLogger())

	if got := covOTCredentialStatus(t, db, "cred-refresh-fail"); got != "EXPIRED" {
		t.Errorf("status = %q, want EXPIRED (failed refresh should expire credential)", got)
	}
}

// TestCovOTExchangeOAuthCode_SetsOptionalParams exercises the branches that
// add client_secret and code_verifier to the request body. The endpoint is
// unroutable so the call still errors, but the optional-param branches are
// executed before the network dial.
func TestCovOTExchangeOAuthCode_SetsOptionalParams(t *testing.T) {
	t.Parallel()
	_, err := exchangeOAuthCode(context.Background(),
		"http://192.0.2.1:1/token", "client-id", "client-secret", "auth-code", "https://app/cb", "pkce-verifier")
	if err == nil {
		t.Error("expected connection error to unroutable token endpoint")
	}
}

// TestCovOTRefreshOAuthToken_SetsClientSecret exercises the client_secret
// branch of refreshOAuthToken before the (failing) network dial.
func TestCovOTRefreshOAuthToken_SetsClientSecret(t *testing.T) {
	t.Parallel()
	_, err := refreshOAuthToken(context.Background(),
		"http://192.0.2.1:1/token", "client-id", "client-secret", "refresh-token")
	if err == nil {
		t.Error("expected connection error to unroutable token endpoint")
	}
}
