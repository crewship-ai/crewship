package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestKeeperHandleRequest_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	h := NewKeeperHandler(db, "internal-token", nil, logger)

	bodies := []map[string]string{
		{},
		{"requesting_agent_id": "a1"},
		{"requesting_agent_id": "a1", "requesting_crew_id": "c1"},
		{"requesting_agent_id": "a1", "requesting_crew_id": "c1", "workspace_id": "ws1"},
		{"requesting_agent_id": "a1", "requesting_crew_id": "c1", "workspace_id": "ws1", "credential_id": "cred1"},
	}

	for _, body := range bodies {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.HandleRequest(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for body %v, got %d", body, w.Code)
		}
	}
}

func TestKeeperHandleRequest_CredentialNotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Seed the agent so the agent validation passes — only the credential is missing
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Crew', 'crew')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agent1', 'crew1', ?, 'Bot', 'bot')`, wsID)

	h := NewKeeperHandler(db, "internal-token", nil, logger)

	body := keeperRequestBody{
		RequestingAgentID: "agent1",
		RequestingCrewID:  "crew1",
		WorkspaceID:       wsID,
		CredentialID:      "nonexistent-cred",
		Intent:            "I need the credential to deploy",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestKeeperHandleRequest_DenyByDefault(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Seed workspace, user, agent, credential
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-k1', ?, 'Keeper Crew', 'keeper-crew')`, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES ('agent-k1', 'crew-k1', ?, 'KeeperBot', 'keeper-bot')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Create a credential with a placeholder encrypted value.
	// The KeeperHandler only reads name+security_level from DB (no decryption here).
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('cred-k1', ?, 'prod-ssh', 'SECRET', 3, 'v1:aW52YWxpZA==', ?)`, wsID, userID)

	// Handler with no gatekeeper → deny-all
	h := NewKeeperHandler(db, "internal-token", nil, logger)

	body := keeperRequestBody{
		RequestingAgentID: "agent-k1",
		RequestingCrewID:  "crew-k1",
		WorkspaceID:       wsID,
		CredentialID:      "cred-k1",
		Intent:            "I want to SSH into prod",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionDeny {
		t.Errorf("expected DENY (no gatekeeper), got %s", result.Decision)
	}
	if result.RequestID == "" {
		t.Error("expected non-empty request_id")
	}
}

func TestKeeperGetRequest_NotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewKeeperHandler(db, "internal-token", nil, logger)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/keeper/request/nonexistent", nil)
	req.SetPathValue("requestId", "nonexistent")
	w := httptest.NewRecorder()
	h.GetRequest(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
