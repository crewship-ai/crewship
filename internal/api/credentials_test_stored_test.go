package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStored (POST /api/v1/credentials/{credentialId}/test) probes a
// stored credential's validity. The live-probe happy path makes a real
// network call, so here we cover the auth + lookup gates that run
// before the probe: 403 below the "update" tier, 404 for unknown ids.

func TestCredentialTestStored_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/c1/test", nil)
	req.SetPathValue("credentialId", "c1")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCredentialTestStored_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/ghost/test", nil)
	req.SetPathValue("credentialId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// #5045: a probe sends the value to the provider's fixed host, or to a stored
// endpoint, which a person never approved for a host-bound credential. The
// probe is refused before the value is even decrypted. The stored value here
// is not decryptable, so reaching decryption would answer 500.
func TestCredentialTestStored_HostBoundIsNotProbed(t *testing.T) {
	tests := []struct {
		name     string
		hosts    string
		wantCode int
	}{
		{"bound to another host", `["llm-proxy.example.com"]`, http.StatusConflict},
		{"bound, even to the provider host", `["api.anthropic.com"]`, http.StatusConflict},
		{"unreadable list", `not json`, http.StatusConflict},
		{"unbound reaches decryption", `[]`, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			execOrFatal(t, db, `INSERT INTO credentials
				(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, allowed_hosts, created_at, updated_at)
				VALUES ('c-bound', ?, 'k', 'not-a-ciphertext', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, ?, datetime('now'), datetime('now'))`,
				wsID, userID, tc.hosts)
			h := NewCredentialHandler(db, newTestLogger())
			req := httptest.NewRequest("POST", "/api/v1/credentials/c-bound/test", nil)
			req.SetPathValue("credentialId", "c-bound")
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.TestStored(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status=%d want %d; body=%s", rr.Code, tc.wantCode, rr.Body.String())
			}
		})
	}
}
