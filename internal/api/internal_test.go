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
	"github.com/crewship-ai/crewship/internal/ws"
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

func TestCreateRun_UpdatesAgentStatus(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a1', ?, 'Bot', 'bot', 'IDLE')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('c1', 'a1', ?, 'CHAT', 'ACTIVE')`, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	// Hub is nil — broadcasts are skipped, but DB updates still happen
	body := strings.NewReader(`{"id":"run1","agent_id":"a1","chat_id":"c1","workspace_id":"` + wsID + `","trigger_type":"USER"}`)
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", body)
	rr := httptest.NewRecorder()
	handler.CreateRun(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify agent status was updated to RUNNING
	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id = 'a1'").Scan(&status); err != nil {
		t.Fatalf("query agent status: %v", err)
	}
	if status != "RUNNING" {
		t.Errorf("agent status = %q, want RUNNING", status)
	}
}

func TestUpdateRun_UpdatesAgentStatusOnCompletion(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a1', ?, 'Bot', 'bot', 'RUNNING')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	now := "2026-01-01T00:00:00Z"
	_, err = db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run1', 'a1', ?, 'USER', 'RUNNING', ?, ?)`, wsID, now, now)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	// Use real hub for broadcast testing
	hub := ws.NewHub(logger, nil)
	handler.SetHub(hub)

	body := strings.NewReader(`{"status":"COMPLETED","exit_code":0}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run1", body)
	req.SetPathValue("runId", "run1")
	rr := httptest.NewRecorder()
	handler.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify agent status was updated to IDLE
	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id = 'a1'").Scan(&status); err != nil {
		t.Fatalf("query agent status: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agent status = %q, want IDLE", status)
	}

	// Verify run was marked completed
	var runStatus string
	if err := db.QueryRow("SELECT status FROM agent_runs WHERE id = 'run1'").Scan(&runStatus); err != nil {
		t.Fatalf("query run status: %v", err)
	}
	if runStatus != "COMPLETED" {
		t.Errorf("run status = %q, want COMPLETED", runStatus)
	}
}

func TestUpdateRun_FailedSetsAgentError(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a1', ?, 'Bot', 'bot', 'RUNNING')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	now := "2026-01-01T00:00:00Z"
	_, err = db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run1', 'a1', ?, 'USER', 'RUNNING', ?, ?)`, wsID, now, now)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	hub := ws.NewHub(logger, nil)
	handler.SetHub(hub)

	body := strings.NewReader(`{"status":"FAILED","error_message":"OOM killed"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run1", body)
	req.SetPathValue("runId", "run1")
	rr := httptest.NewRecorder()
	handler.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify agent status was set to ERROR on failure
	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id = 'a1'").Scan(&status); err != nil {
		t.Fatalf("query agent status: %v", err)
	}
	if status != "ERROR" {
		t.Errorf("agent status = %q, want ERROR", status)
	}
}

func TestUpdateRun_StaysRunningIfOtherRunActive(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a1', ?, 'Bot', 'bot', 'RUNNING')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	now := "2026-01-01T00:00:00Z"
	// Two running runs for the same agent
	if _, err := db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run1', 'a1', ?, 'USER', 'RUNNING', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("insert run1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run2', 'a1', ?, 'ASSIGNMENT', 'RUNNING', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("insert run2: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	hub := ws.NewHub(logger, nil)
	handler.SetHub(hub)

	// Complete run1, but run2 is still active
	body := strings.NewReader(`{"status":"COMPLETED","exit_code":0}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run1", body)
	req.SetPathValue("runId", "run1")
	rr := httptest.NewRecorder()
	handler.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Agent should stay RUNNING since run2 is still active
	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id = 'a1'").Scan(&status); err != nil {
		t.Fatalf("query agent status: %v", err)
	}
	if status != "RUNNING" {
		t.Errorf("agent status = %q, want RUNNING (other run still active)", status)
	}
}

func TestUpdateRun_FailedStaysRunningIfOtherRunActive(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a1', ?, 'Bot', 'bot', 'RUNNING')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	now := "2026-01-01T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run1', 'a1', ?, 'USER', 'RUNNING', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("insert run1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run2', 'a1', ?, 'ASSIGNMENT', 'RUNNING', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("insert run2: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	hub := ws.NewHub(logger, nil)
	handler.SetHub(hub)

	// Fail run1, but run2 is still active — agent should stay RUNNING, not ERROR
	body := strings.NewReader(`{"status":"FAILED","error_message":"crash"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run1", body)
	req.SetPathValue("runId", "run1")
	rr := httptest.NewRecorder()
	handler.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id = 'a1'").Scan(&status); err != nil {
		t.Fatalf("query agent status: %v", err)
	}
	if status != "RUNNING" {
		t.Errorf("agent status = %q, want RUNNING (other run still active despite failure)", status)
	}
}

func TestUpdateRun_CancelledSetsAgentIdle(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a1', ?, 'Bot', 'bot', 'RUNNING')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	now := "2026-01-01T00:00:00Z"
	_, err = db.Exec(`INSERT INTO agent_runs (id, agent_id, workspace_id, trigger_type, status, started_at, created_at)
		VALUES ('run1', 'a1', ?, 'USER', 'RUNNING', ?, ?)`, wsID, now, now)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	hub := ws.NewHub(logger, nil)
	handler.SetHub(hub)

	body := strings.NewReader(`{"status":"CANCELLED"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/runs/run1", body)
	req.SetPathValue("runId", "run1")
	rr := httptest.NewRecorder()
	handler.UpdateRun(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var status string
	if err := db.QueryRow("SELECT status FROM agents WHERE id = 'a1'").Scan(&status); err != nil {
		t.Fatalf("query agent status: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agent status = %q, want IDLE", status)
	}

	var runStatus string
	if err := db.QueryRow("SELECT status FROM agent_runs WHERE id = 'run1'").Scan(&runStatus); err != nil {
		t.Fatalf("query run status: %v", err)
	}
	if runStatus != "CANCELLED" {
		t.Errorf("run status = %q, want CANCELLED", runStatus)
	}
}

// TestResolveCoordinatorCrews_SameNameCrews guards against a grouping bug in
// the N+1 refactor: the schema only has UNIQUE(workspace_id, slug) on crews,
// so two crews CAN share a name. The SELECT must tie-break ORDER BY on c.id
// or same-named crews will interleave their agent rows and the streaming
// grouping loop will produce duplicate crew entries.
func TestResolveCoordinatorCrews_SameNameCrews(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Two crews with the SAME name but different slugs (schema allows this).
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES
		('crew-a', ?, 'Ops', 'ops-a'),
		('crew-b', ?, 'Ops', 'ops-b')`, wsID, wsID)
	if err != nil {
		t.Fatalf("insert crews: %v", err)
	}

	// Interleave agent names across crews so that a naive ORDER BY c.name, a.name
	// would shuffle rows between crews:
	//   (Ops, alice, crew-a)
	//   (Ops, bob,   crew-b)
	//   (Ops, carol, crew-a)
	//   (Ops, dave,  crew-b)
	_, err = db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES
		('a1', 'crew-a', ?, 'alice', 'alice'),
		('a2', 'crew-b', ?, 'bob',   'bob'),
		('a3', 'crew-a', ?, 'carol', 'carol'),
		('a4', 'crew-b', ?, 'dave',  'dave')`, wsID, wsID, wsID, wsID)
	if err != nil {
		t.Fatalf("insert agents: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)

	data := &agentConfigData{
		wsID:      wsID,
		agentRole: sql.NullString{String: "COORDINATOR", Valid: true},
	}
	req := httptest.NewRequest("GET", "/unused", nil)
	crews := handler.resolveCoordinatorCrews(req, data)

	if len(crews) != 2 {
		t.Fatalf("got %d crews, want 2 (bug: same-named crews produced duplicate entries)", len(crews))
	}

	// Each of the two crews must own exactly its own two agents, not a mix.
	byID := map[string]crewInfoEntry{}
	for _, c := range crews {
		byID[c.ID] = c
	}
	crewA, okA := byID["crew-a"]
	crewB, okB := byID["crew-b"]
	if !okA || !okB {
		t.Fatalf("missing expected crew IDs; got: %+v", byID)
	}

	memberIDs := func(c crewInfoEntry) []string {
		out := make([]string, 0, len(c.Members))
		for _, m := range c.Members {
			out = append(out, m.ID)
		}
		return out
	}

	// The SQL orders agents deterministically by a.name ASC within each crew,
	// so both crews' members arrive in a known order: crew-a is [a1=alice,
	// a3=carol] and crew-b is [a2=bob, a4=dave]. Use strict positional checks
	// (|| not &&) — the original && form short-circuited if either position
	// was correct, so a result like [a1, a4] would have passed.
	gotA := memberIDs(crewA)
	gotB := memberIDs(crewB)
	if len(gotA) != 2 || gotA[0] != "a1" || gotA[1] != "a3" {
		t.Errorf("crew-a members = %v, want [a1 a3]", gotA)
	}
	if len(gotB) != 2 || gotB[0] != "a2" || gotB[1] != "a4" {
		t.Errorf("crew-b members = %v, want [a2 a4]", gotB)
	}
}

// TestResolveCoordinatorCrews_EmptyCrew verifies the LEFT JOIN semantic: a
// crew with zero members still appears in the result with an empty Members
// slice. The streaming loop has a NULL-agent guard for this exact case.
func TestResolveCoordinatorCrews_EmptyCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES
		('c-empty', ?, 'Empty', 'empty'),
		('c-full',  ?, 'Full',  'full')`, wsID, wsID)
	if err != nil {
		t.Fatalf("insert crews: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES
		('solo', 'c-full', ?, 'solo', 'solo')`, wsID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	data := &agentConfigData{
		wsID:      wsID,
		agentRole: sql.NullString{String: "COORDINATOR", Valid: true},
	}
	req := httptest.NewRequest("GET", "/unused", nil)
	crews := handler.resolveCoordinatorCrews(req, data)

	if len(crews) != 2 {
		t.Fatalf("got %d crews, want 2", len(crews))
	}
	var empty, full *crewInfoEntry
	for i := range crews {
		if crews[i].ID == "c-empty" {
			empty = &crews[i]
		}
		if crews[i].ID == "c-full" {
			full = &crews[i]
		}
	}
	if empty == nil || full == nil {
		t.Fatalf("missing crew entries: %+v", crews)
	}
	if len(empty.Members) != 0 {
		t.Errorf("empty crew Members = %v, want empty", empty.Members)
	}
	if len(full.Members) != 1 || full.Members[0].ID != "solo" {
		t.Errorf("full crew Members = %+v, want [solo]", full.Members)
	}
}

// TestResolveCoordinatorCrews_NonCoordinatorReturnsNil keeps the role guard
// explicit so a future refactor doesn't accidentally leak crew data to
// non-COORDINATOR agents.
func TestResolveCoordinatorCrews_NonCoordinatorReturnsNil(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c', ?, 'C', 'c')`, wsID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", logger)
	req := httptest.NewRequest("GET", "/unused", nil)

	for _, role := range []string{"AGENT", "LEAD", ""} {
		data := &agentConfigData{
			wsID:      wsID,
			agentRole: sql.NullString{String: role, Valid: role != ""},
		}
		if got := handler.resolveCoordinatorCrews(req, data); got != nil {
			t.Errorf("role=%q: expected nil, got %+v", role, got)
		}
	}
}
