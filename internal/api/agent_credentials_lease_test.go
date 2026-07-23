package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAgentCred_Add_WithTTL_PersistsExpiry proves that assigning a credential
// to an agent with a ttl_seconds lease persists a non-null expires_at roughly
// ttl_seconds in the future.
func TestAgentCred_Add_WithTTL_PersistsExpiry(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN","ttl_seconds":3600}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var expiresAt string
	if err := db.QueryRow(
		`SELECT COALESCE(expires_at,'') FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		agentID, credID).Scan(&expiresAt); err != nil {
		t.Fatalf("read back expires_at: %v", err)
	}
	if expiresAt == "" {
		t.Fatal("expected expires_at to be persisted for a TTL lease, got empty")
	}
	parsed, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatalf("expires_at not RFC3339: %q (%v)", expiresAt, err)
	}
	delta := time.Until(parsed)
	if delta < 55*time.Minute || delta > 65*time.Minute {
		t.Errorf("expires_at ~1h expected, got %v from now", delta)
	}
}

// TestAgentCred_Add_NoTTL_NullExpiry proves the default remains a standing
// grant (NULL expires_at) — leases are strictly opt-in and backward compatible.
func TestAgentCred_Add_NoTTL_NullExpiry(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var isNull int
	if err := db.QueryRow(
		`SELECT expires_at IS NULL FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`,
		agentID, credID).Scan(&isNull); err != nil {
		t.Fatalf("read back expires_at null-check: %v", err)
	}
	if isNull != 1 {
		t.Error("expected NULL expires_at for a grant with no TTL")
	}
}

// TestAgentCred_List_ReportsLeaseState proves the agent-credentials listing
// surfaces the lease expires_at and an `expired` flag so the CLI/UI can show
// lease state.
func TestAgentCred_List_ReportsLeaseState(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, expires_at)
		 VALUES ('ac-lease', ?, ?, 'GH_TOKEN', 0, ?)`,
		agentID, credID, past); err != nil {
		t.Fatalf("seed leased assignment: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/credentials", nil)
	req.SetPathValue("agentId", agentID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var got []agentCredentialResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(got))
	}
	if got[0].ExpiresAt == "" {
		t.Error("expected ExpiresAt to be reported")
	}
	if !got[0].Expired {
		t.Error("expected Expired=true for a past lease")
	}
}
