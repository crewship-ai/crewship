package api

// Tests for the onboarding setup crew: onboarding_setup_crew.go, plus the
// Status/Complete/Setup wiring in onboarding.go and the List filter in
// crews_query.go. See docs/prd/conversational-onboarding.md §4, §5.3, §8.1.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// ---- ensureOnboardingSetupCrew: slug, network mode, kind ----

func TestEnsureOnboardingSetupCrew_FixedSlugRestrictedNetworkAndKind(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}
	if info.CrewID == "" || info.AgentID == "" || info.ChatID == "" {
		t.Fatalf("expected non-empty ids, got %+v", info)
	}

	var slug, networkMode, kind string
	if err := db.QueryRow("SELECT slug, network_mode, kind FROM crews WHERE id = ?", info.CrewID).
		Scan(&slug, &networkMode, &kind); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if slug != setupCrewSlug {
		t.Errorf("slug = %q, want %q", slug, setupCrewSlug)
	}
	if networkMode != database.DefaultCrewNetworkMode {
		t.Errorf("network_mode = %q, want %q", networkMode, database.DefaultCrewNetworkMode)
	}
	if kind != setupCrewKindSetup {
		t.Errorf("kind = %q, want %q", kind, setupCrewKindSetup)
	}

	// The crew gets its devcontainer from the chokepoint default, not a
	// value written at INSERT — devcontainer_config must stay NULL here.
	var devcontainer sql.NullString
	if err := db.QueryRow("SELECT devcontainer_config FROM crews WHERE id = ?", info.CrewID).Scan(&devcontainer); err != nil {
		t.Fatalf("read devcontainer_config: %v", err)
	}
	if devcontainer.Valid {
		t.Errorf("devcontainer_config = %q, want NULL (chokepoint default applies at read time)", devcontainer.String)
	}

	var chatAgentID string
	if err := db.QueryRow("SELECT agent_id FROM chats WHERE id = ?", info.ChatID).Scan(&chatAgentID); err != nil {
		t.Fatalf("read chat: %v", err)
	}
	if chatAgentID != info.AgentID {
		t.Errorf("chat.agent_id = %q, want %q", chatAgentID, info.AgentID)
	}
}

// ---- Slug collision proof: the fixed slug is unreachable through the
//      validation every user-facing slug (crew or agent) must pass. ----

func TestSetupSlugs_RejectedByPublicSlugValidation(t *testing.T) {
	if validSlugFormat(setupCrewSlug) {
		t.Errorf("setupCrewSlug %q passes validSlugFormat — a user could create a colliding crew", setupCrewSlug)
	}
	if validSlugFormat(setupAgentSlug) {
		t.Errorf("setupAgentSlug %q passes validSlugFormat — a user could create a colliding agent", setupAgentSlug)
	}
	// And the two slug-generating helpers (onboarding's own makeSlug, and
	// crew_templates.go's slugify) can never emit a leading underscore
	// either, so a *derived* slug cannot collide even without going
	// through validSlugFormat.
	if strings.HasPrefix(makeSlug("_crewship-setup"), "_") {
		t.Error("makeSlug can produce a leading underscore — the collision proof no longer holds")
	}
	if strings.HasPrefix(slugify("_crewship-setup"), "_") {
		t.Error("slugify can produce a leading underscore — the collision proof no longer holds")
	}
}

// ---- Idempotency: a second call (a second onboarding "session") does not
//      create a second setup crew. ----

func TestEnsureOnboardingSetupCrew_SecondCallConvergesOnSameRows(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	first, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if first.CrewID != second.CrewID || first.AgentID != second.AgentID || first.ChatID != second.ChatID {
		t.Errorf("second call produced different rows: first=%+v second=%+v", first, second)
	}

	var crewCount, agentCount, chatCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND slug = ?", wsID, setupAgentSlug).Scan(&agentCount); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM chats WHERE agent_id = ?", first.AgentID).Scan(&chatCount); err != nil {
		t.Fatalf("count chats: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew count = %d, want 1", crewCount)
	}
	if agentCount != 1 {
		t.Errorf("agent count = %d, want 1", agentCount)
	}
	if chatCount != 1 {
		t.Errorf("chat count = %d, want 1", chatCount)
	}
}

// ---- Visibility: CrewHandler.List excludes the setup crew ----

func TestCrewList_ExcludesSetupCrew(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "real-crew-1", wsID, "Real Crew", "real-crew")

	if _, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID); err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	h := &CrewHandler{db: db, logger: testLogger()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("crew list = %d entries, want 1 (setup crew must be excluded): %+v", len(got), got)
	}
	if got[0]["slug"] != "real-crew" {
		t.Errorf("crew list returned %v, want only the real crew", got[0]["slug"])
	}
	for _, c := range got {
		if c["slug"] == setupCrewSlug {
			t.Fatalf("setup crew %q leaked into crew list", setupCrewSlug)
		}
	}
}

// ---- System prompt carries the [LANGUAGE PREFERENCE] block, injected the
//      same way it is for any other agent (agent_config.go), plus this
//      file's own authored text. ----

func TestEnsureOnboardingSetupCrew_SystemPromptCarriesLanguageBlock(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.ExecContext(context.Background(),
		"UPDATE workspaces SET preferred_language = ? WHERE id = ?", "Czech", wsID); err != nil {
		t.Fatalf("set preferred_language: %v", err)
	}

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	handler := NewInternalHandler(db, "test-token", testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/chats/"+info.ChatID+"/resolve", nil)
	req.SetPathValue("chatId", info.ChatID)
	w := httptest.NewRecorder()
	handler.ResolveChat(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sysPrompt, _ := resp["system_prompt"].(string)

	if !strings.Contains(sysPrompt, "[LANGUAGE PREFERENCE]") {
		t.Error("system prompt missing [LANGUAGE PREFERENCE] block")
	}
	if !strings.Contains(sysPrompt, "Always respond in: Czech") {
		t.Error("system prompt missing the Czech language directive")
	}
	if !strings.Contains(sysPrompt, "Crewship Guide") {
		t.Error("system prompt missing the guide's own authored text")
	}
	if !strings.Contains(sysPrompt, "Ask for explicit confirmation") {
		t.Error("system prompt missing the confirmation-before-mutation contract")
	}
	if !strings.Contains(sysPrompt, "marker only renders a review card") ||
		!strings.Contains(sysPrompt, "SAME response as the concrete proposal") {
		t.Error("system prompt does not distinguish proposal-card rendering from mutation confirmation")
	}
}

// ---- Setup agent's prompt names at least one builtin crew template. ----

func TestBuildSetupAgentSystemPrompt_ListsBuiltinTemplates(t *testing.T) {
	db := setupTestDB(t)
	prompt, err := buildSetupAgentSystemPrompt(context.Background(), db, testLogger(), "ANTHROPIC", setupAgentDefaultModel)
	if err != nil {
		t.Fatalf("buildSetupAgentSystemPrompt: %v", err)
	}
	if !strings.Contains(prompt, "Software Development") {
		t.Errorf("prompt missing a known builtin template name; prompt=%s", prompt)
	}
	if !strings.Contains(prompt, "Security Audit") {
		t.Errorf("prompt missing another known builtin template name; prompt=%s", prompt)
	}
	for _, want := range []string{
		"built-in Crewship product specialist",
		"status.v1",
		"save_routine",
		"apiVersion",
		"CrewTemplate",
		"dedicated Crewship apply tool",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("guide prompt missing product contract %q", want)
		}
	}
}

// ---- Status wiring: creates the setup crew once workspace + credential
//      exist, and a repeat poll does not create a second one. ----

func TestOnboardingStatus_CreatesSetupCrewOnceCredentialExists(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))

	// No credential yet: Status must not create a setup crew.
	w := httptest.NewRecorder()
	h.Status(w, req)
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("setup crew created before a credential existed")
	}

	// Seed a credential the way onboarding would, then poll again.
	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "API Key", "ANTHROPIC", "ANTHROPIC_API_KEY", "sk-ant-oat01-fake", isoMillisNow()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	w = httptest.NewRecorder()
	h.Status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var statusResp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode first status response: %v", err)
	}
	if statusResp["completed"] {
		t.Fatal("setup crew creation must not itself complete onboarding")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("setup crew count after first poll with credential = %d, want 1", n)
	}

	// A second poll ("a second onboarding") must not create a second one.
	w = httptest.NewRecorder()
	h.Status(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("decode second status response: %v", err)
	}
	if statusResp["completed"] {
		t.Fatal("setup agent was counted as a real crew agent on the next status poll")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("setup crew count after second poll = %d, want 1 (must not duplicate)", n)
	}
}

// ---- Persistence: Complete() keeps the system crew, guide, and chat live. ----

func TestOnboardingComplete_RetainsCrewshipGuide(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Complete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var crewDeletedAt, agentDeletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", info.CrewID).Scan(&crewDeletedAt); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if crewDeletedAt.Valid {
		t.Error("Crewship Guide crew was deleted when onboarding completed")
	}
	if err := db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", info.AgentID).Scan(&agentDeletedAt); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if agentDeletedAt.Valid {
		t.Error("Crewship Guide agent was deleted when onboarding completed")
	}
	var chatExists int
	if err := db.QueryRow("SELECT COUNT(*) FROM chats WHERE id = ?", info.ChatID).Scan(&chatExists); err != nil {
		t.Fatalf("read chat: %v", err)
	}
	if chatExists != 1 {
		t.Error("Crewship Guide chat row should remain available")
	}

	var name, roleTitle, suggested string
	var memoryEnabled int
	if err := db.QueryRow(`
		SELECT name, role_title, memory_enabled, suggested_prompts
		FROM agents WHERE id = ?`, info.AgentID).
		Scan(&name, &roleTitle, &memoryEnabled, &suggested); err != nil {
		t.Fatalf("read retained guide configuration: %v", err)
	}
	if name != "Crewship Guide" || roleTitle != "Crewship Specialist" {
		t.Fatalf("retained identity = %q / %q", name, roleTitle)
	}
	if memoryEnabled != 1 {
		t.Error("Crewship Guide must retain workspace memory after onboarding")
	}
	for _, want := range []string{"Design a crew", "Create a routine", "Crewship Page", "YAML manifest"} {
		if !strings.Contains(suggested, want) {
			t.Errorf("suggested prompts missing %q: %q", want, suggested)
		}
	}
}

func TestOnboardingStatus_RetainsSetupCrewForAlreadyCompletedUser(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}
	if _, err := db.Exec("UPDATE users SET onboarding_completed = 1, onboarding_skipped_at = ? WHERE id = ?", "2026-08-22T00:00:00Z", userID); err != nil {
		t.Fatalf("mark completed: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var crewDeletedAt, agentDeletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", info.CrewID).Scan(&crewDeletedAt); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if err := db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", info.AgentID).Scan(&agentDeletedAt); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if crewDeletedAt.Valid || agentDeletedAt.Valid {
		t.Fatalf("completed-user status deleted guide rows: crew=%v agent=%v", crewDeletedAt.Valid, agentDeletedAt.Valid)
	}
}

func TestOnboardingStatus_ReopensCompletedUserWithNoRealAgent(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensure setup crew: %v", err)
	}
	// Simulate a workspace upgraded from a build that reclaimed the temporary
	// setup rows when onboarding completed. Status must revive them as the
	// permanent Crewship Guide.
	if _, err := db.Exec("UPDATE agents SET deleted_at = '2026-08-22T00:00:00Z' WHERE id = ?", info.AgentID); err != nil {
		t.Fatalf("tombstone legacy setup agent: %v", err)
	}
	if _, err := db.Exec("UPDATE crews SET deleted_at = '2026-08-22T00:00:00Z' WHERE id = ?", info.CrewID); err != nil {
		t.Fatalf("tombstone legacy setup crew: %v", err)
	}
	if _, err := db.Exec("UPDATE users SET onboarding_completed = 1 WHERE id = ?", userID); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials
			(id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('resume-cred', ?, 'ANTHROPIC_API_KEY', 'encrypted', 'AI_CLI_TOKEN', 'ANTHROPIC', 'ACTIVE', ?)`,
		wsID, userID); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body["completed"] {
		t.Fatal("interrupted onboarding with no real agent remained completed")
	}

	var completed int
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("read completed flag: %v", err)
	}
	if completed != 0 {
		t.Fatalf("onboarding_completed = %d, want 0", completed)
	}
	var crewDeletedAt, agentDeletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", info.CrewID).Scan(&crewDeletedAt); err != nil {
		t.Fatalf("read setup crew: %v", err)
	}
	if err := db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", info.AgentID).Scan(&agentDeletedAt); err != nil {
		t.Fatalf("read setup agent: %v", err)
	}
	if crewDeletedAt.Valid || agentDeletedAt.Valid {
		t.Fatalf("setup rows were not revived: crew_deleted=%v agent_deleted=%v", crewDeletedAt.Valid, agentDeletedAt.Valid)
	}
}

// ---- Credential-aware model choice: the setup agent's provider follows
//      whatever credential the workspace already has, so autoAssignCredentials
//      can actually wire it up. ----

func TestEnsureOnboardingSetupCrew_FollowsWorkspaceCredentialProvider(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "API Key", "OPENAI", "OPENAI_API_KEY", "test-value", isoMillisNow()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("ensureOnboardingSetupCrew: %v", err)
	}

	var adapter, provider, model, prompt string
	if err := db.QueryRow("SELECT cli_adapter, llm_provider, llm_model, system_prompt_legacy FROM agents WHERE id = ?", info.AgentID).
		Scan(&adapter, &provider, &model, &prompt); err != nil {
		t.Fatalf("read agent provider: %v", err)
	}
	if adapter != "CODEX_CLI" {
		t.Errorf("agent cli_adapter = %q, want CODEX_CLI", adapter)
	}
	if provider != "OPENAI" {
		t.Errorf("agent llm_provider = %q, want OPENAI (matching the workspace's only credential)", provider)
	}
	if model != "gpt-5.5" {
		t.Errorf("agent llm_model = %q, want gpt-5.5", model)
	}
	if !strings.Contains(prompt, `"llm_provider":"OPENAI","llm_model":"gpt-5.5"`) {
		t.Error("proposal marker in setup prompt does not follow the selected runtime")
	}

	var assigned int
	if err := db.QueryRow("SELECT COUNT(*) FROM agent_credentials WHERE agent_id = ?", info.AgentID).Scan(&assigned); err != nil {
		t.Fatalf("count agent_credentials: %v", err)
	}
	if assigned != 1 {
		t.Errorf("agent_credentials rows for setup agent = %d, want 1", assigned)
	}
}

func TestEnsureOnboardingSetupCrew_RefreshesExistingAgentPrompt(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	info, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE agents
		SET system_prompt_legacy = 'obsolete prompt', cli_adapter = 'CODEX_CLI',
			llm_provider = 'OPENAI', llm_model = 'obsolete-model'
		WHERE id = ?`, info.AgentID); err != nil {
		t.Fatalf("make setup agent stale: %v", err)
	}

	second, err := ensureOnboardingSetupCrew(context.Background(), db, testLogger(), wsID, userID)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.AgentID != info.AgentID {
		t.Fatalf("refresh replaced the stable setup agent: first=%s second=%s", info.AgentID, second.AgentID)
	}

	var adapter, provider, model, prompt string
	if err := db.QueryRow(`
		SELECT cli_adapter, llm_provider, llm_model, system_prompt_legacy
		FROM agents WHERE id = ?`, info.AgentID).Scan(&adapter, &provider, &model, &prompt); err != nil {
		t.Fatalf("read refreshed setup agent: %v", err)
	}
	if adapter != "CLAUDE_CODE" || provider != "ANTHROPIC" || model != setupAgentDefaultModel {
		t.Errorf("refreshed runtime = %s/%s/%s, want CLAUDE_CODE/ANTHROPIC/%s", adapter, provider, model, setupAgentDefaultModel)
	}
	if !strings.Contains(prompt, "Crewship Guide") || !strings.Contains(prompt, "crewship:onboarding-proposal") {
		t.Error("existing setup agent did not receive the current authored prompt")
	}
}

func TestWorkspaceHasCredential_RejectsUnusableRows(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "GitHub token", "GITHUB", "GITHUB_TOKEN", "fake", isoMillisNow()); err != nil {
		t.Fatalf("insert unrelated credential: %v", err)
	}
	if h.workspaceHasCredential(context.Background(), wsID) {
		t.Fatal("unrelated credential opened the model setup chat")
	}

	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "Anthropic", "ANTHROPIC", "ANTHROPIC_API_KEY", "fake", isoMillisNow()); err != nil {
		t.Fatalf("insert model credential: %v", err)
	}
	if !h.workspaceHasCredential(context.Background(), wsID) {
		t.Fatal("active Anthropic model credential was not recognized")
	}
	if _, err := db.Exec(`UPDATE credentials SET status = 'REVOKED' WHERE workspace_id = ? AND provider = 'ANTHROPIC'`, wsID); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if h.workspaceHasCredential(context.Background(), wsID) {
		t.Fatal("revoked model credential opened the setup chat")
	}
}
