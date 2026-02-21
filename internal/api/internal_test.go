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

func TestResolveChat_LanguagePreference(t *testing.T) {
	tests := []struct {
		name     string
		lang     *string
		wantBlock bool
		wantLang  string
	}{
		{
			name:      "WithLanguage",
			lang:      strPtr("Czech"),
			wantBlock: true,
			wantLang:  "Czech",
		},
		{
			name:      "NoLanguage",
			lang:      nil,
			wantBlock: false,
		},
		{
			name:      "EmptyLanguage",
			lang:      strPtr(""),
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestEncryptionKey(t)
			db := setupTestDB(t)
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			if tt.lang != nil {
				if *tt.lang == "" {
					if _, err := db.ExecContext(context.Background(),
						"UPDATE workspaces SET preferred_language = NULL WHERE id = ?", wsID); err != nil {
						t.Fatalf("update preferred_language: %v", err)
					}
				} else {
					if _, err := db.ExecContext(context.Background(),
						"UPDATE workspaces SET preferred_language = ? WHERE id = ?", *tt.lang, wsID); err != nil {
						t.Fatalf("update preferred_language: %v", err)
					}
				}
			}

			_, err := db.ExecContext(context.Background(),
				`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Test Crew', 'test-crew')`, wsID)
			if err != nil {
				t.Fatalf("insert crew: %v", err)
			}
			_, err = db.ExecContext(context.Background(),
				`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
				VALUES ('agent1', 'crew1', ?, 'Test Agent', 'test-agent')`, wsID)
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
			sysPrompt, _ := resp["system_prompt"].(string)

			if tt.wantBlock {
				if !strings.Contains(sysPrompt, "[LANGUAGE PREFERENCE]") {
					t.Error("system_prompt missing [LANGUAGE PREFERENCE] block")
				}
				if !strings.Contains(sysPrompt, "Always respond in: "+tt.wantLang) {
					t.Errorf("system_prompt missing language directive for %s", tt.wantLang)
				}
				if !strings.Contains(sysPrompt, "[END LANGUAGE PREFERENCE]") {
					t.Error("system_prompt missing [END LANGUAGE PREFERENCE]")
				}
			} else {
				if strings.Contains(sysPrompt, "[LANGUAGE PREFERENCE]") {
					t.Error("system_prompt should not contain [LANGUAGE PREFERENCE] when language is not set")
				}
			}
		})
	}
}

func strPtr(s string) *string { return &s }

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

func TestResolveChat_Skills(t *testing.T) {
	tests := []struct {
		name   string
		seed   func(t *testing.T, db *sql.DB, wsID, userID string)
		assert func(t *testing.T, sysPrompt string)
	}{
		{
			name: "WithSkills",
			seed: func(t *testing.T, db *sql.DB, wsID, userID string) {
				seedTestSkill(t, db, "sk1", "GitHub Integration", "github-integration",
					"GitHub Integration", "DEVELOPMENT",
					"# GitHub Integration\n\n## Instructions\nUse GitHub API.", "[]")
				seedTestAgentSkill(t, db, "as1", "agent1", "sk1", 1)
			},
			assert: func(t *testing.T, sysPrompt string) {
				if !strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
					t.Error("system_prompt missing [SKILLS AVAILABLE] header")
				}
				if !strings.Contains(sysPrompt, `<skill name="GitHub Integration"`) {
					t.Error("system_prompt missing skill XML tag")
				}
				if !strings.Contains(sysPrompt, "# GitHub Integration") {
					t.Error("system_prompt missing skill content")
				}
				if !strings.Contains(sysPrompt, "[END SKILLS AVAILABLE]") {
					t.Error("system_prompt missing [END SKILLS AVAILABLE] footer")
				}
			},
		},
		{
			name: "SkillDisabled",
			seed: func(t *testing.T, db *sql.DB, wsID, userID string) {
				seedTestSkill(t, db, "sk1", "Disabled Skill", "disabled-skill",
					"Disabled Skill", "CUSTOM", "# Disabled Skill", "[]")
				seedTestAgentSkill(t, db, "as1", "agent1", "sk1", 0) // enabled=0
			},
			assert: func(t *testing.T, sysPrompt string) {
				if strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
					t.Error("disabled skill should not produce [SKILLS AVAILABLE] block")
				}
			},
		},
		{
			name: "SkillNoContent",
			seed: func(t *testing.T, db *sql.DB, wsID, userID string) {
				if _, err := db.ExecContext(context.Background(),
					`INSERT INTO skills (id, name, slug, display_name, category, source, content)
					VALUES ('sk1', 'Empty Skill', 'empty-skill', 'Empty Skill', 'CUSTOM', 'CUSTOM', NULL)`); err != nil {
					t.Fatalf("insert skill: %v", err)
				}
				seedTestAgentSkill(t, db, "as1", "agent1", "sk1", 1)
			},
			assert: func(t *testing.T, sysPrompt string) {
				if strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
					t.Error("skill with NULL content should not produce [SKILLS AVAILABLE] block")
				}
			},
		},
		{
			name: "MultipleSkills",
			seed: func(t *testing.T, db *sql.DB, wsID, userID string) {
				seedTestSkill(t, db, "sk1", "Alpha Skill", "alpha-skill",
					"Alpha Skill", "CODING", "# Alpha Skill", "[]")
				seedTestSkill(t, db, "sk2", "Zeta Skill", "zeta-skill",
					"Zeta Skill", "RESEARCH", "# Zeta Skill", "[]")
				seedTestAgentSkill(t, db, "as1", "agent1", "sk1", 1)
				seedTestAgentSkill(t, db, "as2", "agent1", "sk2", 1)
			},
			assert: func(t *testing.T, sysPrompt string) {
				if !strings.Contains(sysPrompt, `<skill name="Alpha Skill"`) {
					t.Error("missing Alpha Skill in prompt")
				}
				if !strings.Contains(sysPrompt, `<skill name="Zeta Skill"`) {
					t.Error("missing Zeta Skill in prompt")
				}
				alphaIdx := strings.Index(sysPrompt, `<skill name="Alpha Skill"`)
				zetaIdx := strings.Index(sysPrompt, `<skill name="Zeta Skill"`)
				if alphaIdx > zetaIdx {
					t.Error("skills should be in alphabetical order: Alpha before Zeta")
				}
			},
		},
		{
			name: "ZeroSkills",
			seed: func(t *testing.T, db *sql.DB, wsID, userID string) {
				// No skills assigned to agent
			},
			assert: func(t *testing.T, sysPrompt string) {
				if strings.Contains(sysPrompt, "[SKILLS AVAILABLE]") {
					t.Error("agent with no skills should not have [SKILLS AVAILABLE] block")
				}
			},
		},
		{
			name: "SkillCredentialStatus",
			seed: func(t *testing.T, db *sql.DB, wsID, userID string) {
				seedTestSkill(t, db, "sk1", "GitHub Skill", "github-skill",
					"GitHub Skill", "DEVELOPMENT", "# GitHub Skill",
					`["GITHUB_TOKEN","SLACK_TOKEN"]`)
				seedTestAgentSkill(t, db, "as1", "agent1", "sk1", 1)

				encValue, _ := encryption.Encrypt("ghp_test")
				if _, err := db.ExecContext(context.Background(),
					`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
					VALUES ('cred1', ?, 'GitHub Token', ?, 'API_KEY', 'NONE', ?)`, wsID, encValue, userID); err != nil {
					t.Fatalf("insert credential: %v", err)
				}
				if _, err := db.ExecContext(context.Background(),
					`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
					VALUES ('ac1', 'agent1', 'cred1', 'GITHUB_TOKEN', 1)`); err != nil {
					t.Fatalf("insert agent_credential: %v", err)
				}
			},
			assert: func(t *testing.T, sysPrompt string) {
				if !strings.Contains(sysPrompt, "GITHUB_TOKEN: configured") {
					t.Errorf("expected GITHUB_TOKEN configured status, got prompt: %s", sysPrompt)
				}
				if !strings.Contains(sysPrompt, "SLACK_TOKEN: NOT CONFIGURED") {
					t.Errorf("expected SLACK_TOKEN not configured status, got prompt: %s", sysPrompt)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestEncryptionKey(t)
			db := setupTestDB(t)
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			if _, err := db.ExecContext(context.Background(),
				`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', ?, 'Test Crew', 'test-crew')`, wsID); err != nil {
				t.Fatalf("insert crew: %v", err)
			}
			if _, err := db.ExecContext(context.Background(),
				`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
				VALUES ('agent1', 'crew1', ?, 'Test Agent', 'test-agent')`, wsID); err != nil {
				t.Fatalf("insert agent: %v", err)
			}
			if _, err := db.ExecContext(context.Background(),
				`INSERT INTO chats (id, agent_id, workspace_id, mode, status)
				VALUES ('chat1', 'agent1', ?, 'CHAT', 'ACTIVE')`, wsID); err != nil {
				t.Fatalf("insert chat: %v", err)
			}

			tt.seed(t, db, wsID, userID)

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
			sysPrompt, _ := resp["system_prompt"].(string)

			tt.assert(t, sysPrompt)
		})
	}
}
