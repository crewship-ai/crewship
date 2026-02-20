package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

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
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

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

// seedTestSkill inserts a skill and returns its ID.
func seedTestSkill(t *testing.T, db interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}, id, name, slug, displayName, category, content, credReqs string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO skills (id, name, slug, display_name, category, source, content, credential_requirements)
		VALUES (?, ?, ?, ?, ?, 'CUSTOM', ?, ?)`,
		id, name, slug, displayName, category, content, credReqs)
	if err != nil {
		t.Fatalf("insert skill %q: %v", id, err)
	}
}

// seedTestAgentSkill links a skill to an agent.
func seedTestAgentSkill(t *testing.T, db interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}, asID, agentID, skillID string, enabled int) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES (?, ?, ?, ?)`,
		asID, agentID, skillID, enabled)
	if err != nil {
		t.Fatalf("insert agent_skill: %v", err)
	}
}

func TestResolveChat_WithSkills(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewS1', ?, 'Skill Crew', 'skill-crew')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		VALUES ('agentS1', 'crewS1', ?, 'Skill Agent', 'skill-agent')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chatS1', 'agentS1', ?, 'CHAT', 'ACTIVE')`, wsID)

	seedTestSkill(t, db, "skillS1", "GitHub Integration", "github-integration",
		"GitHub Integration", "DEVELOPMENT",
		"# GitHub Integration\n\n## Instructions\nUse GitHub API.", "[]")
	seedTestAgentSkill(t, db, "asS1", "agentS1", "skillS1", 1)

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chatS1/resolve", nil)
	req.SetPathValue("chatId", "chatS1")
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	sysPrompt, _ := resp["system_prompt"].(string)
	if !strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
		t.Error("system_prompt missing [SKILLS AVAILABLE] header")
	}
	if !strings.Contains(sysPrompt, `<skill name="GitHub Integration"`) {
		t.Errorf("system_prompt missing skill XML tag, got: %s", sysPrompt)
	}
	if !strings.Contains(sysPrompt, "# GitHub Integration") {
		t.Error("system_prompt missing skill content")
	}
	if !strings.Contains(sysPrompt, "[END SKILLS AVAILABLE]") {
		t.Error("system_prompt missing [END SKILLS AVAILABLE] footer")
	}
}

func TestResolveChat_SkillDisabled(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewS2', ?, 'Crew S2', 'crew-s2')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		VALUES ('agentS2', 'crewS2', ?, 'Agent S2', 'agent-s2')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chatS2', 'agentS2', ?, 'CHAT', 'ACTIVE')`, wsID)

	seedTestSkill(t, db, "skillS2", "Disabled Skill", "disabled-skill",
		"Disabled Skill", "CUSTOM", "# Disabled Skill", "[]")
	seedTestAgentSkill(t, db, "asS2", "agentS2", "skillS2", 0) // enabled=0

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chatS2/resolve", nil)
	req.SetPathValue("chatId", "chatS2")
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	sysPrompt, _ := resp["system_prompt"].(string)

	if strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
		t.Error("disabled skill should not produce [SKILLS AVAILABLE] block")
	}
}

func TestResolveChat_SkillNoContent(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewS3', ?, 'Crew S3', 'crew-s3')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		VALUES ('agentS3', 'crewS3', ?, 'Agent S3', 'agent-s3')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chatS3', 'agentS3', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Skill with NULL content
	db.ExecContext(context.Background(),
		`INSERT INTO skills (id, name, slug, display_name, category, source, content)
		VALUES ('skillS3', 'Empty Skill', 'empty-skill', 'Empty Skill', 'CUSTOM', 'CUSTOM', NULL)`)
	seedTestAgentSkill(t, db, "asS3", "agentS3", "skillS3", 1)

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chatS3/resolve", nil)
	req.SetPathValue("chatId", "chatS3")
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	sysPrompt, _ := resp["system_prompt"].(string)

	if strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
		t.Error("skill with NULL content should not produce [SKILLS AVAILABLE] block")
	}
}

func TestResolveChat_MultipleSkills(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewS4', ?, 'Crew S4', 'crew-s4')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		VALUES ('agentS4', 'crewS4', ?, 'Agent S4', 'agent-s4')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chatS4', 'agentS4', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Two skills - names chosen so "Alpha Skill" sorts before "Zeta Skill"
	seedTestSkill(t, db, "skillS4a", "Alpha Skill", "alpha-skill",
		"Alpha Skill", "CODING", "# Alpha Skill", "[]")
	seedTestSkill(t, db, "skillS4b", "Zeta Skill", "zeta-skill",
		"Zeta Skill", "RESEARCH", "# Zeta Skill", "[]")
	seedTestAgentSkill(t, db, "asS4a", "agentS4", "skillS4a", 1)
	seedTestAgentSkill(t, db, "asS4b", "agentS4", "skillS4b", 1)

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chatS4/resolve", nil)
	req.SetPathValue("chatId", "chatS4")
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	sysPrompt, _ := resp["system_prompt"].(string)

	if !strings.Contains(sysPrompt, `<skill name="Alpha Skill"`) {
		t.Error("missing Alpha Skill in prompt")
	}
	if !strings.Contains(sysPrompt, `<skill name="Zeta Skill"`) {
		t.Error("missing Zeta Skill in prompt")
	}
	// Verify alphabetical order
	alphaIdx := strings.Index(sysPrompt, `<skill name="Alpha Skill"`)
	zetaIdx := strings.Index(sysPrompt, `<skill name="Zeta Skill"`)
	if alphaIdx > zetaIdx {
		t.Error("skills should be in alphabetical order: Alpha before Zeta")
	}
}

func TestResolveChat_ZeroSkills(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewS5', ?, 'Crew S5', 'crew-s5')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		VALUES ('agentS5', 'crewS5', ?, 'Agent S5', 'agent-s5')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chatS5', 'agentS5', ?, 'CHAT', 'ACTIVE')`, wsID)

	// No skills assigned to agent

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chatS5/resolve", nil)
	req.SetPathValue("chatId", "chatS5")
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	sysPrompt, _ := resp["system_prompt"].(string)

	if strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
		t.Error("agent with no skills should not have [SKILLS AVAILABLE] block")
	}
}

func TestResolveChat_SkillCredentialStatus(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewS6', ?, 'Crew S6', 'crew-s6')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		VALUES ('agentS6', 'crewS6', ?, 'Agent S6', 'agent-s6')`, wsID)
	db.ExecContext(context.Background(),
		`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
		VALUES ('chatS6', 'agentS6', ?, 'CHAT', 'ACTIVE')`, wsID)

	// Skill requires GITHUB_TOKEN and SLACK_TOKEN; agent has GITHUB_TOKEN configured
	seedTestSkill(t, db, "skillS6", "GitHub Skill", "github-skill",
		"GitHub Skill", "DEVELOPMENT", "# GitHub Skill",
		`["GITHUB_TOKEN","SLACK_TOKEN"]`)
	seedTestAgentSkill(t, db, "asS6", "agentS6", "skillS6", 1)

	// Add GITHUB_TOKEN as agent credential
	encValue, _ := encryption.Encrypt("ghp_test")
	db.ExecContext(context.Background(),
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
		VALUES ('credS6', ?, 'GitHub Token', ?, 'API_KEY', 'NONE', ?)`, wsID, encValue, userID)
	db.ExecContext(context.Background(),
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('acS6', 'agentS6', 'credS6', 'GITHUB_TOKEN', 1)`)

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/api/v1/internal/chats/chatS6/resolve", nil)
	req.SetPathValue("chatId", "chatS6")
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	sysPrompt, _ := resp["system_prompt"].(string)

	// GITHUB_TOKEN should be marked configured, SLACK_TOKEN should be NOT CONFIGURED
	if !strings.Contains(sysPrompt, "GITHUB_TOKEN: configured") {
		t.Errorf("expected GITHUB_TOKEN configured status, got prompt: %s", sysPrompt)
	}
	if !strings.Contains(sysPrompt, "SLACK_TOKEN: NOT CONFIGURED") {
		t.Errorf("expected SLACK_TOKEN not configured status, got prompt: %s", sysPrompt)
	}
}
