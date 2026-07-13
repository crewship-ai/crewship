package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecCred_LoadAgentCredentials_SkipsNonActive is the #1051 regression. The
// delegation/hire loader (AssignmentHandler.loadAgentCredentials) selected
// `WHERE ac.agent_id = ? AND c.deleted_at IS NULL` with no status filter, so a
// PENDING credential (manifest slot / OAuth mid-handshake / mid-rotation) whose
// body is a sentinel placeholder would be decrypted and injected as a real env
// value at the sub-agent boundary. The fix mirrors resolveAgentCredentials:
// status='ACTIVE' in SQL + a sentinel guard in the decrypt loop.
func TestSecCred_LoadAgentCredentials_SkipsNonActive(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('lc-crew', ?, 'C', 'lc-c')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('lc-ag', 'lc-crew', ?, 'A', 'lc-a')`, wsID)

	// One ACTIVE credential (should inject) and one PENDING one carrying the
	// OAuth sentinel body (must NOT inject).
	seedCredentialEnc(t, db, wsID, userID, "lc-active", "lc-active", "real-token")
	seedCredentialEnc(t, db, wsID, userID, "lc-pending", "lc-pending", pendingSentinelOAuth)
	execOrFatal(t, db, `UPDATE credentials SET status='PENDING' WHERE id='lc-pending'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('lc-ac1','lc-ag','lc-active','TOK_A',0,datetime('now'))`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('lc-ac2','lc-ag','lc-pending','TOK_P',1,datetime('now'))`)

	h := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger())
	creds, err := h.loadAgentCredentials(context.Background(), "lc-ag")
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].EnvVarName != "TOK_A" {
		t.Fatalf("creds = %+v, want only the ACTIVE TOK_A (PENDING sentinel must not leak)", creds)
	}
	for _, c := range creds {
		if isPendingSentinel(c.PlainValue) {
			t.Fatalf("PENDING sentinel leaked as an injected env value: %+v", c)
		}
	}
}

// TestSecCred_UpdateStatus_RejectsSoftDeleted is the #1061 regression.
// UpdateCredentialStatus filtered only `id = ?` (+ optional workspace), with no
// deleted_at check, so a status write (e.g. the OAuth refresh worker) against a
// soft-deleted credential returned 200 and flipped its status back to ACTIVE on
// a dead row. The fix adds `deleted_at IS NULL` so the n==0 → 404 branch rejects
// deleted credentials.
func TestSecCred_UpdateStatus_RejectsSoftDeleted(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "del-cred", "del", "v")
	execOrFatal(t, db, `UPDATE credentials SET status='REVOKED', deleted_at=datetime('now') WHERE id='del-cred'`)

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/del-cred/status?workspace_id="+wsID,
		strings.NewReader(`{"status":"ACTIVE"}`))
	req.SetPathValue("credentialId", "del-cred")
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)
	h.reconcileWG.Wait()

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s; want 404 (must not mutate a soft-deleted credential)",
			rr.Code, rr.Body.String())
	}
	// The dead row must be unchanged (still REVOKED, not flipped to ACTIVE).
	var status string
	if err := db.QueryRow(`SELECT status FROM credentials WHERE id='del-cred'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "REVOKED" {
		t.Errorf("status = %q, want REVOKED unchanged (soft-deleted row was mutated)", status)
	}
}

// TestSecCred_Delete_RemovesAgentAssignments is the #1050 regression. Delete is
// a SOFT delete (deleted_at), so the agent_credentials ON DELETE CASCADE FK
// never fires and the handler left assignments in place — keeping the credential
// listed as "assigned" and inflating per-agent counts. The fix removes the
// agent_credentials rows on delete.
func TestSecCred_Delete_RemovesAgentAssignments(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('dc-crew', ?, 'C', 'dc-c')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('dc-ag', 'dc-crew', ?, 'A', 'dc-a')`, wsID)
	seedCredentialEnc(t, db, wsID, userID, "dc-cred", "dc", "v")
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('dc-ac','dc-ag','dc-cred','TOK',0,datetime('now'))`)

	req := httptest.NewRequest("DELETE", "/api/v1/credentials/dc-cred", nil)
	req.SetPathValue("credentialId", "dc-cred")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", rr.Code, rr.Body.String())
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE credential_id='dc-cred'`).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if n != 0 {
		t.Fatalf("agent_credentials rows = %d after delete, want 0 (assignments must be removed)", n)
	}
}
