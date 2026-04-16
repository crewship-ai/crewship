package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// seedAgentCredEnv inserts an agent and a credential, returns their IDs.
func seedAgentCredEnv(t *testing.T, db *sql.DB) (userID, wsID, agentID, credID string) {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	agentID = "agent-1"
	credID = "cred-1"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'A', 'a')`, agentID, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	seedCredentialEnc(t, db, wsID, userID, credID, "test-cred", "v")
	return
}

func newAgentHandlerForCred(t *testing.T, db *sql.DB) *AgentHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewAgentHandler(db, logger)
}

func TestAgentCred_List_AgentNotFound(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := newAgentHandlerForCred(t, db)

	req := httptest.NewRequest("GET", "/api/v1/agents/missing/credentials", nil)
	req.SetPathValue("agentId", "missing")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListCredentials(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestAgentCred_List_Empty(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, _ := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

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
	if rr.Body.String() != "[]\n" {
		t.Errorf("expected [], got %s", rr.Body.String())
	}
}

func TestAgentCred_Add_Forbidden(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestAgentCred_Add_AgentNotFound(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"x","env_var_name":"X"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/missing/credentials", body)
	req.SetPathValue("agentId", "missing")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestAgentCred_Add_BadRequest(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, _ := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	tests := []string{`{}`, `{"credential_id":"x"}`, `{"env_var_name":"Y"}`, `bad json`}
	for _, body := range tests {
		req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", strings.NewReader(body))
		req.SetPathValue("agentId", agentID)
		ctx := withWorkspace(req.Context(), wsID, "OWNER")
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()
		h.AddCredential(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body=%q status=%d, want 400", body, rr.Code)
		}
	}
}

func TestAgentCred_Add_CredentialNotFound(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, _ := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"missing","env_var_name":"X"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestAgentCred_Add_Success(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN","priority":1}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ? AND credential_id = ?", agentID, credID).Scan(&count)
	if count != 1 {
		t.Errorf("agent_credentials count = %d, want 1", count)
	}
}

func TestAgentCred_Add_DuplicateConflict(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	body1 := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body1)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first add status = %d", rr.Code)
	}

	// Duplicate
	body2 := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"OTHER"}`)
	req = httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body2)
	req.SetPathValue("agentId", agentID)
	ctx = withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr = httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestAgentCred_List_AfterAdd(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-1', ?, ?, 'X', 0, datetime('now'))`, agentID, credID); err != nil {
		t.Fatalf("seed: %v", err)
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
	var result []agentCredentialResponse
	json.Unmarshal(rr.Body.Bytes(), &result)
	if len(result) != 1 || result[0].EnvVarName != "X" {
		t.Errorf("result = %+v", result)
	}
}

func TestAgentCred_Remove_Forbidden(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, _ := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/credentials/x", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("assignmentId", "x")
	ctx := withWorkspace(req.Context(), wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.RemoveCredential(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestAgentCred_Remove_NotFound(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, _ := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/credentials/missing-assignment", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("assignmentId", "missing-assignment")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.RemoveCredential(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestAgentCred_Remove_Success(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-rm', ?, ?, 'X', 0, datetime('now'))`, agentID, credID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/credentials/ac-rm", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("assignmentId", "ac-rm")
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.RemoveCredential(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM agent_credentials WHERE id = 'ac-rm'").Scan(&count)
	if count != 0 {
		t.Errorf("expected delete, got count=%d", count)
	}
}

func TestAgentCred_Remove_CrossWorkspaceDenied(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, _, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at) VALUES ('ac-x', ?, ?, 'X', 0, datetime('now'))`, agentID, credID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/agents/"+agentID+"/credentials/ac-x", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("assignmentId", "ac-x")
	ctx := withWorkspace(req.Context(), "other-ws", "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.RemoveCredential(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-ws delete status = %d, want 404", rr.Code)
	}
}
