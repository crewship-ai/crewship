package api

// Coverage tests for internal_credentials.go — maybeRecordSidecarUse
// debounce/CAS branches, ListCredentials decrypt-failure tolerance,
// requestIsLoopback host-only parsing, UpdateCredentialStatus error
// branch, and GetWebhookSecret crew scoping.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func covICRig(t *testing.T) (*InternalHandler, *sql.DB, string, string) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewInternalHandler(db, "test-token", newTestLogger()), db, userID, wsID
}

// covICSeedAICred inserts an AI_CLI_TOKEN credential with the given raw
// (possibly invalid) encrypted columns so decrypt branches can be driven.
func covICSeedAICred(t *testing.T, db *sql.DB, wsID, userID, credID, encValue string, encRefresh any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, encrypted_refresh_token,
			type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'AI_CLI_TOKEN', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		credID, wsID, "ai-"+credID, encValue, encRefresh, userID); err != nil {
		t.Fatalf("seed ai credential: %v", err)
	}
}

// --- maybeRecordSidecarUse -----------------------------------------------

func TestCovICMaybeRecordSidecarUse(t *testing.T) {
	h, db, userID, wsID := covICRig(t)
	_ = h

	t.Run("empty cred id no-op", func(t *testing.T) {
		maybeRecordSidecarUse(context.Background(), db, newTestLogger(), "", "")
		// nothing to assert beyond "does not panic / does not write"
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM credential_audit`).Scan(&n)
		if n != 0 {
			t.Errorf("audit rows = %d, want 0", n)
		}
	})

	t.Run("missing row skips", func(t *testing.T) {
		maybeRecordSidecarUse(context.Background(), db, newTestLogger(), "no-such-cred", "")
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM credential_audit`).Scan(&n)
		if n != 0 {
			t.Errorf("audit rows = %d, want 0", n)
		}
	})

	t.Run("debounce: second call within window skipped", func(t *testing.T) {
		seedCredentialEnc(t, db, wsID, userID, "cred-use", "K", "v")
		maybeRecordSidecarUse(context.Background(), db, newTestLogger(), "cred-use", "")
		maybeRecordSidecarUse(context.Background(), db, newTestLogger(), "cred-use", "")
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = 'cred-use' AND event_type = 'USE'`).Scan(&n); err != nil {
			t.Fatalf("query: %v", err)
		}
		if n != 1 {
			t.Errorf("USE audit rows = %d, want exactly 1 (debounced)", n)
		}
	})

	t.Run("record failure tolerated", func(t *testing.T) {
		seedCredentialEnc(t, db, wsID, userID, "cred-use2", "K2", "v")
		if _, err := db.Exec(`DROP TABLE credential_audit`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		// Must not panic; failure only logs.
		maybeRecordSidecarUse(context.Background(), db, newTestLogger(), "cred-use2", "")
	})

	t.Run("CAS db error tolerated", func(t *testing.T) {
		closed := setupTestDB(t)
		closed.Close()
		maybeRecordSidecarUse(context.Background(), closed, newTestLogger(), "cred-x", "")
	})
}

// --- ListCredentials include_values --------------------------------------

func TestCovICListCredentials_DecryptFailureSkipsRow(t *testing.T) {
	h, db, userID, wsID := covICRig(t)

	// One credential with a garbage encrypted value (undecryptable) and one
	// healthy credential whose refresh token is garbage (refresh decrypt
	// branch logs and omits refresh only).
	covICSeedAICred(t, db, wsID, userID, "cred-bad", "not-decryptable", nil)
	goodEnc, err := encryption.Encrypt("sk-ant-good")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	covICSeedAICred(t, db, wsID, userID, "cred-good", goodEnc, "garbage-refresh")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/credentials?include_values=true&workspace_id="+wsID, nil)
	req.RemoteAddr = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	h.ListCredentials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var out []struct {
		ID           string  `json:"id"`
		AccessToken  *string `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].ID != "cred-good" {
		t.Fatalf("out = %+v, want only cred-good (bad row skipped)", out)
	}
	if out[0].AccessToken == nil || *out[0].AccessToken != "sk-ant-good" {
		t.Errorf("access_token = %v", out[0].AccessToken)
	}
	if out[0].RefreshToken != nil {
		t.Errorf("refresh_token = %v, want omitted (undecryptable)", *out[0].RefreshToken)
	}
}

func TestCovICListCredentials_NonLoopbackValuesStripped(t *testing.T) {
	h, db, userID, wsID := covICRig(t)
	goodEnc, err := encryption.Encrypt("sk-ant-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	covICSeedAICred(t, db, wsID, userID, "cred-remote", goodEnc, nil)

	req := httptest.NewRequest(http.MethodGet, "/x?include_values=true&workspace_id="+wsID, nil)
	req.RemoteAddr = "10.1.2.3:4444" // non-loopback
	rec := httptest.NewRecorder()
	h.ListCredentials(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-ant-secret") {
		t.Error("plaintext token leaked to non-loopback caller")
	}
}

// --- requestIsLoopback ------------------------------------------------------

func TestCovICRequestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:1234", true},
		{"127.0.0.1", true}, // SplitHostPort error fallback
		{"[::1]:80", true},
		{"10.0.0.1:80", false},
		{"not-an-ip", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.addr
		if got := requestIsLoopback(req); got != tc.want {
			t.Errorf("requestIsLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// --- UpdateCredentialStatus ----------------------------------------------------

func TestCovICUpdateCredentialStatus_ExecError500(t *testing.T) {
	h, db, _, _ := covICRig(t)
	if _, err := db.Exec(`DROP TABLE credentials`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader(`{"status":"ACTIVE"}`))
	req.SetPathValue("credentialId", "c1")
	rec := httptest.NewRecorder()
	h.UpdateCredentialStatus(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- GetWebhookSecret ------------------------------------------------------------

func TestCovICGetWebhookSecret_CrewScope(t *testing.T) {
	h, db, _, wsID := covICRig(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-wh', ?, 'WH', 'wh')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, webhook_secret)
		VALUES ('ag-wh', ?, 'crew-wh', 'A', 'a-wh', 'whsec_123')`, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	t.Run("matching crew scope", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x?crew_id=crew-wh", nil)
		req.SetPathValue("agentId", "ag-wh")
		rec := httptest.NewRecorder()
		h.GetWebhookSecret(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "whsec_123") {
			t.Errorf("body = %q", rec.Body.String())
		}
	})

	t.Run("mismatched crew scope 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x?crew_id=other-crew", nil)
		req.SetPathValue("agentId", "ag-wh")
		rec := httptest.NewRecorder()
		h.GetWebhookSecret(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (no tenant leak)", rec.Code)
		}
	})
}
