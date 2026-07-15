package api

// TestResolveAgentConfig_PrivilegedCrew_FailClosedCredentials is issue
// #1032: a --privileged crew container drops no-new-privileges +
// CapDrop:ALL, collapsing the UID 1001 (agent) / 1002 (sidecar) boundary
// that normally keeps a compromised agent from reading the sidecar's
// process memory — and with it any credentials loaded into its CredStore.
// The agent-config resolver must fail CLOSED (omit credentials entirely)
// for a privileged crew unless the workspace has explicitly opted in via
// workspaces.allow_privileged_credentials.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestResolveAgentConfig_PrivilegedCrew_FailClosedCredentials(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-priv", wsID, "Privileged Crew", "privileged-crew")
	if _, err := db.Exec(`UPDATE crews SET cached_requirements = '{"privileged":true}' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("mark crew privileged: %v", err)
	}
	agentID := seedAgentRow(t, db, "ag-priv", wsID, crewID, "Ada", "ada", "AGENT")
	covCfgEncCred(t, db, wsID, userID, "cred1", "Anthropic Key", "ANTHROPIC_API_KEY", "", "sk-test-secret")
	covCfgAssignCred(t, db, "ac1", agentID, "cred1", "ANTHROPIC_API_KEY", 0)

	resolve := func() []any {
		req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
		w := httptest.NewRecorder()
		h.resolveAgentConfig(w, req, agentID)
		if w.Code != 200 {
			t.Fatalf("resolveAgentConfig status = %d, body=%s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		creds, _ := resp["credentials"].([]any)
		return creds
	}

	t.Run("workspace_not_opted_in_creds_empty", func(t *testing.T) {
		if creds := resolve(); len(creds) != 0 {
			t.Fatalf("expected 0 credentials for a privileged crew without opt-in, got %d: %+v", len(creds), creds)
		}
	})

	t.Run("workspace_opted_in_creds_present", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE workspaces SET allow_privileged_credentials = 1 WHERE id = ?`, wsID); err != nil {
			t.Fatalf("opt in workspace: %v", err)
		}
		if creds := resolve(); len(creds) == 0 {
			t.Fatalf("expected credentials once the workspace opted in, got 0")
		}
	})
}

// TestResolveAgentConfig_NonPrivilegedCrew_CredentialsUnaffected pins the
// non-regression: a normal (non-privileged) crew must keep resolving
// credentials exactly as before, regardless of the workspace flag.
func TestResolveAgentConfig_NonPrivilegedCrew_CredentialsUnaffected(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := covCfgHandler(db)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-normal", wsID, "Normal Crew", "normal-crew")
	agentID := seedAgentRow(t, db, "ag-normal", wsID, crewID, "Ada", "ada", "AGENT")
	covCfgEncCred(t, db, wsID, userID, "cred1", "Anthropic Key", "ANTHROPIC_API_KEY", "", "sk-test-secret")
	covCfgAssignCred(t, db, "ac1", agentID, "cred1", "ANTHROPIC_API_KEY", 0)

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	w := httptest.NewRecorder()
	h.resolveAgentConfig(w, req, agentID)
	if w.Code != 200 {
		t.Fatalf("resolveAgentConfig status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	creds, _ := resp["credentials"].([]any)
	if len(creds) == 0 {
		t.Fatalf("expected credentials for a non-privileged crew, got 0")
	}
}
