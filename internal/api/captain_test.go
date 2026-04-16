package api

// File: captain_test.go — tests for the deprecated Captain feature.
//
// DEPRECATED (2026-04-16): See captain.go file header for context.
// Tests retained for regression safety while the deprecated code remains in
// the tree. Do not add new Captain tests here.

import (
	"context"
	"strings"
	"testing"
)

// TestDetectLanguageFromMessage — unit tests for Czech detection heuristic.
func TestDetectLanguageFromMessage(t *testing.T) {
	tests := []struct {
		msg      string
		wantLang string
	}{
		{"Jak mohu začít?", "Czech"},
		{"Ahoj, jak se máš?", "Czech"},
		{"Co je to za problém?", "Czech"},
		{"Hello, how are you?", ""},
		{"What is the status of my missions?", ""},
		{"", ""},
		{"díky za pomoc, prosím pokračuj", "Czech"},
	}
	for _, tc := range tests {
		got := detectLanguageFromMessage(tc.msg)
		if got != tc.wantLang {
			t.Errorf("detectLanguageFromMessage(%q) = %q, want %q", tc.msg, got, tc.wantLang)
		}
	}
}

// TestBuildCaptainSystemPrompt_Phase1_Empty — empty workspace.
func TestBuildCaptainSystemPrompt_Phase1_Empty(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "EMPTY")
	assertContains(t, prompt, "apply_crew_template")
	assertContains(t, prompt, "create_crew")
	assertContains(t, prompt, "[IDENTITY]")
	assertContains(t, prompt, "[RULES]")
}

// TestBuildCaptainSystemPrompt_Phase2_NoAgents — crew exists but no agents.
func TestBuildCaptainSystemPrompt_Phase2_NoAgents(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1', ?, 'Alpha Team', 'alpha-team')`, wsID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "SETUP")
	assertContains(t, prompt, "Alpha Team")
	assertContains(t, prompt, "create_agent")
}

// TestBuildCaptainSystemPrompt_Phase3_NoCredentials — agents exist but no credentials.
func TestBuildCaptainSystemPrompt_Phase3_NoCredentials(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1', ?, 'Crew', 'crew')`, wsID)
	db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'c1', ?, 'Bot', 'bot')`, wsID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "CREDENTIALS_NEEDED")
	assertContains(t, prompt, "Settings")
	assertContains(t, prompt, "Credentials")
}

// TestBuildCaptainSystemPrompt_Phase4_Operational — fully set up workspace.
func TestBuildCaptainSystemPrompt_Phase4_Operational(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1', ?, 'Crew', 'crew')`, wsID)
	db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'c1', ?, 'Bot', 'bot')`, wsID)
	db.Exec(`INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by) VALUES ('cred1', ?, 'Anthropic', 'API_KEY', 'ACTIVE', 'enc', ?)`, wsID, userID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "OPERATIONAL")
	assertContains(t, prompt, "list_missions")
	assertContains(t, prompt, "active mission")
}

// TestBuildCaptainSystemPrompt_LanguageFromDB — preferred_language in workspace overrides message detection.
func TestBuildCaptainSystemPrompt_LanguageFromDB(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Exec(`UPDATE workspaces SET preferred_language = 'Czech' WHERE id = ?`, wsID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello in english")

	assertContains(t, prompt, "[LANGUAGE]")
	assertContains(t, prompt, "Czech")
}

// TestBuildCaptainSystemPrompt_LanguageDetectedFromMessage — no DB lang, detect from message.
func TestBuildCaptainSystemPrompt_LanguageDetectedFromMessage(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "Ahoj, jak se mám začít?")

	assertContains(t, prompt, "[LANGUAGE]")
	assertContains(t, prompt, "Czech")
}

// TestBuildCaptainSystemPrompt_EnglishMessageNoLangBlock — English message should not inject language block.
func TestBuildCaptainSystemPrompt_EnglishMessageNoLangBlock(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "What is the status of my workspace?")

	if strings.Contains(prompt, "[LANGUAGE]") {
		t.Error("expected no [LANGUAGE] block for English message, but found one")
	}
}

// TestBuildCaptainSystemPrompt_CredentialProtectionRule — [RULES] must forbid revealing credential values.
func TestBuildCaptainSystemPrompt_CredentialProtectionRule(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "NEVER reveal API keys")
	assertContains(t, prompt, "[RULES]")
}

// TestBuildCaptainSystemPrompt_DynamicContextCounts — DYNAMIC CONTEXT shows live counts.
func TestBuildCaptainSystemPrompt_DynamicContextCounts(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1', ?, 'Crew', 'crew')`, wsID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "[DYNAMIC CONTEXT]")
	assertContains(t, prompt, "Crews: 1")
	assertContains(t, prompt, "Agents: 0")
}

// TestBuildCaptainSystemPrompt_MissionCountInOperational — active mission count appears in OPERATIONAL text.
func TestBuildCaptainSystemPrompt_MissionCountInOperational(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1', ?, 'Crew', 'crew')`, wsID)
	db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('a1', 'c1', ?, 'Bot', 'bot')`, wsID)
	db.Exec(`INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by) VALUES ('cred1', ?, 'Key', 'API_KEY', 'ACTIVE', 'enc', ?)`, wsID, userID)
	db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status) VALUES ('m1', ?, 'c1', 'a1', 'trace-1', 'Deploy', 'IN_PROGRESS')`, wsID)
	db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status) VALUES ('m2', ?, 'c1', 'a1', 'trace-2', 'Monitor', 'IN_PROGRESS')`, wsID)

	prompt := buildCaptainSystemPrompt(context.Background(), db, wsID, "hello")

	assertContains(t, prompt, "2 active mission")
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected prompt to contain %q\n\nFull prompt:\n%s", substr, s)
	}
}

// --- Multi-provider getProvider tests ---

// Note: getProvider tests require real DB with credentials seeded.
// These test the selection logic indirectly via buildCaptainSystemPrompt which is already tested.
