package api

// Second coverage pass for credentials_test_endpoint.go — TestStored's
// audit-record warn branch. (The per-provider "Failed to create request"
// returns are unreachable: the request URLs are hard-coded valid.)

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestCTE2_TestStored_AuditFailureStillReturnsResult(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	enc, err := encryption.Encrypt("some-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Unknown provider → probeProvider's offline default branch, no network.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-cte2', ?, 'Custom', ?, 'API_KEY', 'CUSTOM', 'ACTIVE', ?)`, wsID, enc, userID); err != nil {
		t.Fatalf("seed cred: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TRIGGER cte2_block_audit BEFORE INSERT ON credential_audit
		BEGIN SELECT RAISE(ABORT, 'cte2 no audit'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/credentials/cr-cte2/test", nil)
	req.SetPathValue("credentialId", "cr-cte2")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite audit failure; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"valid":true`) ||
		!strings.Contains(rr.Body.String(), "No validation available") {
		t.Errorf("body = %q", rr.Body.String())
	}
}
