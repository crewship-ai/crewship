package api

import (
	"net/http"
	"strings"
	"testing"
)

// escalation_falsesuccess_test.go — the credential-escalation "false success"
// class: when the agent proposes a credential but it CANNOT be staged, the
// server must not record a PENDING escalation that falsely claims a proposal is
// waiting for one-click approval while the secret has already been discarded.
//
// Hard failures (no workspace owner to approve; vault/encrypt error) must fail
// LOUD (503) with NO escalation row. Recoverable mismatches (name collision,
// unknown type) become a plain escalation carrying a human-readable note, with
// no phantom credential link.

func escCount(t *testing.T, h *QueryHandler, wsID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE workspace_id=?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count escalations: %v", err)
	}
	return n
}

func pendingCredCount(t *testing.T, h *QueryHandler, wsID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM credentials WHERE workspace_id=? AND status='PENDING_APPROVAL'`, wsID).Scan(&n); err != nil {
		t.Fatalf("count pending credentials: %v", err)
	}
	return n
}

// No workspace OWNER → the credential cannot be attributed/approved. The agent
// must get a hard 503, and NOTHING may be recorded (no phantom escalation, no
// pending credential). On current main this returns 201 with a phantom
// escalation row → RED.
func TestEscalation_NoOwner_NoPhantomPending(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	// Remove the workspace OWNER so createPendingCredential's owner lookup fails.
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND role='OWNER'`, wsID)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "store pg pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","provider":"NONE","value":"s3cret-noowner"}`, //gitleaks:allow — fake fixture
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	if n := escCount(t, h, wsID); n != 0 {
		t.Errorf("escalation count = %d, want 0 (no phantom escalation on hard failure)", n)
	}
	if n := pendingCredCount(t, h, wsID); n != 0 {
		t.Errorf("pending credential count = %d, want 0", n)
	}
}

// Encrypt/vault failure → hard 503, no phantom escalation. On current main the
// escalation is inserted as success (201) → RED.
func TestEscalation_VaultError_NoPhantomPending(t *testing.T) {
	// A non-hex ENCRYPTION_KEY makes encryption.Encrypt fail inside
	// createPendingCredential (same trick as TestCovEsc_Resolve_CredentialEncryptFailure_500).
	t.Setenv("ENCRYPTION_KEY", "definitely-not-hex")
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "store pg pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","provider":"NONE","value":"s3cret-vaulterr"}`, //gitleaks:allow — fake fixture
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	if n := escCount(t, h, wsID); n != 0 {
		t.Errorf("escalation count = %d, want 0 (no phantom escalation on vault error)", n)
	}
}

// A live credential already uses the proposed name → we never auto-rename. This
// is recoverable: record a PLAIN escalation carrying a note so a human is
// notified, but with NO credential link (nothing was staged) and NO phantom
// pending credential. The agent still gets 201 (a real escalation exists).
func TestEscalation_NameConflict_PlainEscalationWithNote(t *testing.T) {
	ensureEncryptionKey(t)
	h, ownerID, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	execOrFatal(t, h.db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('existing', ?, 'PG_PASSWORD', 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		wsID, ownerID)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "store pg pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","provider":"NONE","value":"s3cret-dup"}`, //gitleaks:allow — fake fixture
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var storedContext, storedReason string
	var credID *string
	if err := h.db.QueryRow(`SELECT COALESCE(context,''), reason, credential_id FROM escalations
		WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&storedContext, &storedReason, &credID); err != nil {
		t.Fatalf("load escalation: %v", err)
	}
	if credID != nil && *credID != "" {
		t.Errorf("name-conflict escalation must NOT link a phantom credential, got %q", *credID)
	}
	// A human must be told why no credential was staged.
	note := storedContext + " " + storedReason
	if !strings.Contains(strings.ToLower(note), "already") {
		t.Errorf("escalation should carry a name-conflict note, got context=%q reason=%q", storedContext, storedReason)
	}
	// The pre-existing credential is untouched; no new PENDING_APPROVAL row.
	if n := pendingCredCount(t, h, wsID); n != 0 {
		t.Errorf("pending credential count = %d, want 0 on name conflict", n)
	}
}
