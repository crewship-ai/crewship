package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func setTestEncryptionKey(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(key))
}

func TestResolveChat_MemoryEnabled(t *testing.T) {
	setTestEncryptionKey(t)

	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Seed user, workspace, crew, agent (with memory_enabled=1), and chat
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Test Crew', 'test-crew')`, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, memory_enabled, system_prompt)
		VALUES ('agent1', 'crew1', ?, 'Test Agent', 'test-agent', 1, 'You are helpful.')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chat1', 'agent1', ?, 'CHAT', 'ACTIVE')`, wsID)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)

	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chat1/resolve", nil)
	req.SetPathValue("chatId", "chat1")
	w := httptest.NewRecorder()

	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify memory_enabled is present and true
	memEnabled, ok := resp["memory_enabled"]
	if !ok {
		t.Fatal("missing memory_enabled in response")
	}
	if memEnabled != true {
		t.Errorf("expected memory_enabled=true, got %v", memEnabled)
	}

	// Verify other fields still work
	if resp["agent_slug"] != "test-agent" {
		t.Errorf("expected agent_slug='test-agent', got %v", resp["agent_slug"])
	}
	if resp["crew_slug"] != "test-crew" {
		t.Errorf("expected crew_slug='test-crew', got %v", resp["crew_slug"])
	}
}

func TestResolveChat_MemoryDisabled(t *testing.T) {
	setTestEncryptionKey(t)

	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew2', ?, 'Crew 2', 'crew-2')`, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	// Agent with memory_enabled=0 (default)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, memory_enabled)
		VALUES ('agent2', 'crew2', ?, 'Agent 2', 'agent-2', 0)`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chat2', 'agent2', ?, 'CHAT', 'ACTIVE')`, wsID)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)

	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chat2/resolve", nil)
	req.SetPathValue("chatId", "chat2")
	w := httptest.NewRecorder()

	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	memEnabled, ok := resp["memory_enabled"]
	if !ok {
		t.Fatal("missing memory_enabled in response")
	}
	if memEnabled != false {
		t.Errorf("expected memory_enabled=false, got %v", memEnabled)
	}
}

func TestResolveChat_WithCredentials(t *testing.T) {
	setTestEncryptionKey(t)

	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew3', ?, 'Crew 3', 'crew-3')`, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, memory_enabled)
		VALUES ('agent3', 'crew3', ?, 'Agent 3', 'agent-3', 1)`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Add a credential
	encValue, err := encryption.Encrypt("test-credential-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
		VALUES ('cred1', ?, 'Test Key', ?, 'API_KEY', 'ANTHROPIC', ?)`, wsID, encValue, userID)
	if err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('ac1', 'agent3', 'cred1', 'ANTHROPIC_API_KEY', 1)`)
	if err != nil {
		t.Fatalf("insert agent_credential: %v", err)
	}

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chat3', 'agent3', ?, 'CHAT', 'ACTIVE')`, wsID)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)

	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chat3/resolve", nil)
	req.SetPathValue("chatId", "chat3")
	w := httptest.NewRecorder()

	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	// Verify memory is enabled
	if resp["memory_enabled"] != true {
		t.Errorf("expected memory_enabled=true, got %v", resp["memory_enabled"])
	}

	// Verify credentials are present
	creds, ok := resp["credentials"].([]interface{})
	if !ok || len(creds) == 0 {
		t.Error("expected at least one credential in response")
	}
}
