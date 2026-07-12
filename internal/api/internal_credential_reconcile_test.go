package api

// A2 (secret lifecycle hardening): a credential whose status transitions to
// REVOKED through the internal sidecar surface (PATCH
// /api/v1/internal/credentials/{id}) must trigger the same file reconciler as
// the public DELETE handler — otherwise a materialized /secrets file stays
// readable in running containers until the next run. The reconcile is
// asynchronous (the sidecar's status PATCH must not stall on a docker exec),
// so tests synchronize on the handler's reconcileWG.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestUpdateCredentialStatus_Revoked_ReconcilesSecretFiles(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	_, credID := seedFileMountCred(t, db, "SECRET")

	var calls []provider.ExecConfig
	h := NewInternalHandler(db, "tok", newTestLogger())
	h.SetContainer(newRecordingCtr(&calls, nil))

	// No workspace_id query param on purpose — the internal caller may omit
	// it, and the reconciler must resolve the workspace from the credential.
	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/"+credID+"/status",
		strings.NewReader(`{"status":"REVOKED"}`))
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	h.reconcileWG.Wait()

	if len(calls) != 1 {
		t.Fatalf("exec called %d times, want 1 (rm of the revoked credential's file)", len(calls))
	}
	c := calls[0]
	if c.User != "1001:1001" {
		t.Errorf("User = %q, want 1001:1001 (only the agent UID can unlink in the 0700 dir)", c.User)
	}
	if len(c.Cmd) != 3 || c.Cmd[0] != "sh" || c.Cmd[1] != "-c" ||
		!strings.Contains(c.Cmd[2], "rm -f '/secrets/writer/GH_TOKEN'") {
		t.Errorf("Cmd = %v, want sh -c rm of /secrets/writer/GH_TOKEN", c.Cmd)
	}
}

func TestUpdateCredentialStatus_NonRevoked_NoReconcile(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	wsID, credID := seedFileMountCred(t, db, "SECRET")

	var calls []provider.ExecConfig
	h := NewInternalHandler(db, "tok", newTestLogger())
	h.SetContainer(newRecordingCtr(&calls, nil))

	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/"+credID+"/status?workspace_id="+wsID,
		strings.NewReader(`{"status":"EXPIRED"}`))
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	h.reconcileWG.Wait()

	if len(calls) != 0 {
		t.Fatalf("EXPIRED must not remove files (the credential may recover); got %d exec calls", len(calls))
	}
}

func TestUpdateCredentialStatus_Revoked_NilContainer_NoPanic(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	_, credID := seedFileMountCred(t, db, "SECRET")

	h := NewInternalHandler(db, "tok", newTestLogger()) // no SetContainer

	req := httptest.NewRequest("PATCH", "/api/v1/internal/credentials/"+credID+"/status",
		strings.NewReader(`{"status":"REVOKED"}`))
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.UpdateCredentialStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	h.reconcileWG.Wait()
}
