package api

// keeper_governance_govmodel_test.go — M2a (#1001): the governance-model
// selection fields on PUT /api/v1/admin/keeper/governance (provider + model +
// vault credential ref), their validation, and partial-update isolation.

import (
	"net/http"
	"strings"
	"testing"
)

func TestKeeperGovernance_GovModelRoundTrips(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	body := `{"gov_model_provider": "ollama", "gov_model_id": "qwen2.5:3b-instruct"}`
	rr := doGovernanceReq(t, h, http.MethodPut, body, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}

	rr = doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.GovModelProvider != "ollama" || res.GovModelID != "qwen2.5:3b-instruct" {
		t.Fatalf("round-trip = %+v", res)
	}
}

func TestKeeperGovernance_GovModelRejectsUnknownProvider(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"gov_model_provider": "gemini", "gov_model_id": "x"}`, wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "gov_model_provider") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_GovModelRejectsProviderWithoutModel(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut, `{"gov_model_provider": "ollama"}`, wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "gov_model_id is required") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_GovModelRejectsCredentialWithoutProvider(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut, `{"gov_model_credential_id": "some-cred"}`, wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "gov_model_provider is required") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_GovModelRejectsUnknownCredential(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"gov_model_provider": "anthropic", "gov_model_id": "claude-haiku-4-5", "gov_model_credential_id": "ghost"}`,
		wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not a credential in this workspace") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_GovModelRejectsWrongCredentialType(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by) VALUES ('c-secret', ?, 'a-secret', 'enc', 'SECRET', ?)`,
		wsID, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"gov_model_provider": "anthropic", "gov_model_id": "claude-haiku-4-5", "gov_model_credential_id": "c-secret"}`,
		wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ENDPOINT_URL or API_KEY") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_GovModelAcceptsValidCredential(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, created_by) VALUES ('c-endpoint', ?, 'llm-endpoint', 'enc', 'ENDPOINT_URL', ?)`,
		wsID, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"gov_model_provider": "openai_compat", "gov_model_id": "gpt-4o-mini", "gov_model_credential_id": "c-endpoint"}`,
		wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	rr = doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.GovModelCredentialID != "c-endpoint" || res.GovModelProvider != "openai_compat" {
		t.Fatalf("round-trip = %+v", res)
	}
}

// A watch-only PUT must not clobber a previously-set governance model — the
// partial-update contract extends to the new fields.
func TestKeeperGovernance_GovModelPartialUpdateIsolation(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"gov_model_provider": "ollama", "gov_model_id": "qwen2.5:3b-instruct"}`, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT 1 status = %d; body=%s", rr.Code, rr.Body.String())
	}
	rr = doGovernanceReq(t, h, http.MethodPut, `{"watch_spec": "flag egress"}`, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT 2 status = %d; body=%s", rr.Code, rr.Body.String())
	}
	rr = doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.GovModelProvider != "ollama" || res.GovModelID != "qwen2.5:3b-instruct" {
		t.Errorf("watch-only PUT clobbered gov model: %+v", res)
	}
	if res.WatchSpec != "flag egress" {
		t.Errorf("watch_spec not applied: %q", res.WatchSpec)
	}
}
