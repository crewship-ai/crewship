package api

// oauth_token.go coverage top-up #2 — the request-construction error
// branches (control character in the token URL) and the
// refreshExpiringTokens query-error fork. The HTTP-response branches of
// exchangeOAuthCode / refreshOAuthToken stay uncovered on purpose: the
// ssrfSafeTransport blocks loopback addresses, so an httptest server
// cannot stand in for the token endpoint without production changes.
//
// All tests are prefixed TestCov2OT.

import (
	"context"
	"strings"
	"testing"
)

func TestCov2OTExchangeOAuthCode_BadURLRequestError(t *testing.T) {
	t.Parallel()
	_, err := exchangeOAuthCode(context.Background(),
		"http://exa\nmple.com/token", "cid", "", "code", "https://app/cb", "")
	if err == nil {
		t.Fatal("expected request-construction error for control-char URL")
	}
	if strings.Contains(err.Error(), "token request:") {
		t.Errorf("err = %v, want pre-dial NewRequest failure", err)
	}
}

func TestCov2OTRefreshOAuthToken_BadURLRequestError(t *testing.T) {
	t.Parallel()
	_, err := refreshOAuthToken(context.Background(),
		"http://exa\nmple.com/token", "cid", "", "rt")
	if err == nil {
		t.Fatal("expected request-construction error for control-char URL")
	}
	if strings.Contains(err.Error(), "refresh request:") {
		t.Errorf("err = %v, want pre-dial NewRequest failure", err)
	}
}

func TestCov2OTRefreshExpiring_QueryErrorLogsAndReturns(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE credentials`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// Must not panic; the query error is logged and the sweep returns.
	refreshExpiringTokens(context.Background(), db, nil, covOTLogger())
}
