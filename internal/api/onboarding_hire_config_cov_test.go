package api

// Additional branch coverage for onboarding.go, agents_hire.go, and
// agent_config.go. Mirrors the harness in router_test.go /
// core_handlers_test.go / agents_hire_test.go / onboarding_validate_test.go:
// in-memory SQLite via setupTestDB, table-light handler invocations,
// context-injected auth + workspace, httptest recorders.
//
// Scope: the auth / validation / 404 / happy / 500 arms the existing
// cov suites leave uncovered.
//
//   onboarding.go     — Status (404/500/smart-detect), Complete (404/500),
//                       Setup (unauth/bad-json/no-workspace/bad-cred/
//                       missing-name/unknown-provider/500/happy),
//                       setupFromTemplate (happy/unknown-provider-reject/
//                       template-not-found/CAS-conflict).
//   agents_hire.go    — Hire validation arms (mutually-exclusive,
//                       missing template_slug, crew_slug lookup, parent
//                       lead arms), full-autonomy journal-only path,
//                       Rehire (RBAC/404/not-ephemeral/bad-json/missing
//                       reason/happy/ghost-rehire).
//   agent_config.go   — resolveNetworkPolicy edge arms, container
//                       resource defaults/overrides, resolveOAuthAccess
//                       Tokens, MCP cred attachment, skills truncation,
//                       reconstructSKILLMD / yamlQuote.
//
// Skipped (documented, not covered here):
//   - probeAnthropicOAuthToken live HTTP path (network; gated by
//     skipTokenProbe, already exercised in onboarding_probe_test.go).
//   - Hire's DecisionAutoLogInbox arm: unreachable for ActionEphemeralSpawn
//     via the concrete *policy.Resolver (resolver never yields
//     auto_log_inbox for that action; would need an interface seam to stub).

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/services"
)

// ---------------------------------------------------------------------------
// Helpers (covOHC-prefixed to avoid clashing with existing test helpers)
// ---------------------------------------------------------------------------

// covOHCOnboardingHandler builds an OnboardingHandler backed by a real
// OnboardingService so the blank single-agent fork exercises the service
// layer, while the template fork stays in-handler.
func covOHCOnboardingHandler(db *sql.DB) *OnboardingHandler {
	svc := services.NewOnboardingService(db, newTestLogger(), generateCUID)
	return NewOnboardingHandler(db, svc, newTestLogger())
}

// postOnboardingSetup drives OnboardingHandler.Setup with an auth'd user.
func covOHCPostSetup(t *testing.T, h *OnboardingHandler, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case string:
		rdr = bytes.NewReader([]byte(b))
	default:
		raw, _ := json.Marshal(b)
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest("POST", "/api/v1/onboarding/setup", rdr)
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	}
	rr := httptest.NewRecorder()
	h.Setup(rr, req)
	return rr
}

// covOHCSeedBuiltinTemplate inserts a deployable built-in crew template
// with a single agent so the template fork of Setup succeeds.
func covOHCSeedBuiltinTemplate(t *testing.T, db *sql.DB, slug, name string) {
	t.Helper()
	agentsJSON := `[{"name":"Lead","slug":"lead","role_title":"Lead","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE","llm_provider":"ANTHROPIC","llm_model":"claude-3-5-sonnet","tool_profile":"CODING","system_prompt":"You lead."}]`
	if _, err := db.Exec(
		`INSERT INTO crew_templates (id, name, slug, agents_json, is_builtin) VALUES (?, ?, ?, ?, 1)`,
		"tmpl-"+slug, name, slug, agentsJSON); err != nil {
		t.Fatalf("seed builtin template %s: %v", slug, err)
	}
}

// ---------------------------------------------------------------------------
// onboarding.go — Status
// ---------------------------------------------------------------------------

func TestCovOHCOnboardingStatus_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovOHCOnboardingStatus_UserNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "ghost"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for missing user", rr.Code)
	}
}

func TestCovOHCOnboardingStatus_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := covOHCOnboardingHandler(db)
	db.Close() // force a query failure on the first SELECT
	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 on closed db", rr.Code)
	}
}

func TestCovOHCOnboardingStatus_SmartDetectViaAgents(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// onboarding_completed defaults to 0; seeding an agent in the user's
	// workspace should flip the response to completed=true and persist it.
	seedAgentRow(t, db, "smart-agent", wsID, "", "Solo", "solo", "AGENT")

	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]bool
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp["completed"] {
		t.Errorf("completed = false; smart-detect should mark completed when agents exist")
	}
	// Flag must have been persisted.
	var flag int
	_ = db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&flag)
	if flag != 1 {
		t.Errorf("onboarding_completed = %d, want persisted 1", flag)
	}
}

func TestCovOHCOnboardingStatus_AlreadyCompleted(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	_, _ = db.Exec(`UPDATE users SET onboarding_completed = 1 WHERE id = ?`, userID)
	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("GET", "/api/v1/onboarding/status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp map[string]bool
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if !resp["completed"] {
		t.Errorf("completed = false; want true for already-completed user")
	}
}

// ---------------------------------------------------------------------------
// onboarding.go — Complete
// ---------------------------------------------------------------------------

func TestCovOHCOnboardingComplete_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("POST", "/api/v1/onboarding/complete", nil)
	rr := httptest.NewRecorder()
	h.Complete(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovOHCOnboardingComplete_UserNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("POST", "/api/v1/onboarding/complete", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "ghost"}))
	rr := httptest.NewRecorder()
	h.Complete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (0 rows affected)", rr.Code)
	}
}

func TestCovOHCOnboardingComplete_Happy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := covOHCOnboardingHandler(db)
	req := httptest.NewRequest("POST", "/api/v1/onboarding/complete", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Complete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var flag int
	_ = db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&flag)
	if flag != 1 {
		t.Errorf("onboarding_completed = %d, want 1", flag)
	}
}

func TestCovOHCOnboardingComplete_DBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := covOHCOnboardingHandler(db)
	db.Close()
	req := httptest.NewRequest("POST", "/api/v1/onboarding/complete", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	rr := httptest.NewRecorder()
	h.Complete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 on closed db", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// onboarding.go — Setup (blank / single-agent fork + guards)
// ---------------------------------------------------------------------------

func TestCovOHCOnboardingSetup_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, "", map[string]any{"crew_name": "C", "agent_name": "A"})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_BadJSON(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, "{not-json")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid JSON", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_NoWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db) // user exists but is in NO workspace
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{"crew_name": "C", "agent_name": "A"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no workspace found)", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_BadCredentialRejected(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_name":        "C",
		"agent_name":       "A",
		"llm_provider":     "ANTHROPIC",
		"credential_value": "sk-ant-api-raw-key", // wrong shape → 400
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for raw API key", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_MissingCrewName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{"agent_name": "A"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (crew_name required)", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_MissingAgentName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{"crew_name": "C"})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (agent_name required)", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_UnknownProvider(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_name":    "C",
		"agent_name":   "A",
		"llm_provider": "BOGUS",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown llm_provider)", rr.Code)
	}
}

func TestCovOHCOnboardingSetup_BlankHappyWithPreferredLanguage(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_name":          "Engineering",
		"agent_name":         "Builder",
		"preferred_language": "Czech",
		"llm_provider":       "OLLAMA", // empty env var → skips credential
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	// preferred_language is persisted before the branch.
	var lang string
	_ = db.QueryRow(`SELECT COALESCE(preferred_language,'') FROM workspaces WHERE id = ?`, wsID).Scan(&lang)
	if lang != "Czech" {
		t.Errorf("preferred_language = %q, want Czech", lang)
	}
	var crewCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&crewCount)
	if crewCount != 1 {
		t.Errorf("crews = %d, want 1", crewCount)
	}
}

func TestCovOHCOnboardingSetup_AlreadyCompletedConflict(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	_, _ = db.Exec(`UPDATE users SET onboarding_completed = 1 WHERE id = ?`, userID)
	h := covOHCOnboardingHandler(db)
	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_name": "C", "agent_name": "A", "llm_provider": "OLLAMA",
	})
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already completed)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// onboarding.go — setupFromTemplate fork
// ---------------------------------------------------------------------------

func TestCovOHCOnboardingSetup_TemplateHappy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covOHCSeedBuiltinTemplate(t, db, "docs-crew", "Docs Crew")
	h := covOHCOnboardingHandler(db)

	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_template_slug": "docs-crew",
		"workspace_name":     "Renamed WS",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["crew_id"] == "" || resp["crew_id"] == nil {
		t.Errorf("crew_id missing in template deploy response")
	}
	// Workspace was renamed.
	var name string
	_ = db.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, wsID).Scan(&name)
	if name != "Renamed WS" {
		t.Errorf("workspace name = %q, want Renamed WS", name)
	}
	// onboarding flag claimed.
	var flag int
	_ = db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&flag)
	if flag != 1 {
		t.Errorf("onboarding_completed = %d, want 1", flag)
	}
}

func TestCovOHCOnboardingSetup_TemplateWithCredential(t *testing.T) {
	setTestEncryptionKey(t)
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	covOHCSeedBuiltinTemplate(t, db, "secure-crew", "Secure Crew")
	h := covOHCOnboardingHandler(db)

	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_template_slug": "secure-crew",
		"llm_provider":       "ANTHROPIC",
		"credential_value":   "sk-ant-oat01-valid-shape",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	// Credential row stored as AI_CLI_TOKEN.
	var credType string
	if err := db.QueryRow(`SELECT type FROM credentials WHERE workspace_id = ?`, wsID).Scan(&credType); err != nil {
		t.Fatalf("credential not stored: %v", err)
	}
	if credType != "AI_CLI_TOKEN" {
		t.Errorf("credential type = %q, want AI_CLI_TOKEN", credType)
	}
}

func TestCovOHCOnboardingSetup_TemplateUnknownProviderRejected(t *testing.T) {
	setTestEncryptionKey(t)
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	covOHCSeedBuiltinTemplate(t, db, "tmpl-x", "Tmpl X")
	h := covOHCOnboardingHandler(db)

	// Unknown provider + a credential value → reject 400 AND roll back
	// the completion flag so the user can retry.
	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_template_slug": "tmpl-x",
		"llm_provider":       "BOGUS",
		"credential_value":   "sk-ant-oat01-something",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown provider); body: %s", rr.Code, rr.Body.String())
	}
	var flag int
	_ = db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&flag)
	if flag != 0 {
		t.Errorf("onboarding_completed = %d, want rolled-back 0 after reject", flag)
	}
}

func TestCovOHCOnboardingSetup_TemplateNotFound(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := covOHCOnboardingHandler(db)

	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_template_slug": "no-such-template",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown crew template); body: %s", rr.Code, rr.Body.String())
	}
	// Completion flag rolled back so a retry with a valid slug works.
	var flag int
	_ = db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&flag)
	if flag != 0 {
		t.Errorf("onboarding_completed = %d, want rolled-back 0", flag)
	}
}

func TestCovOHCOnboardingSetup_TemplateAlreadyCompletedConflict(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	covOHCSeedBuiltinTemplate(t, db, "dup-crew", "Dup Crew")
	_, _ = db.Exec(`UPDATE users SET onboarding_completed = 1 WHERE id = ?`, userID)
	h := covOHCOnboardingHandler(db)

	rr := covOHCPostSetup(t, h, userID, map[string]any{
		"crew_template_slug": "dup-crew",
	})
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (CAS guard, already completed)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// onboarding.go — resolveLLMProvider + makeSlug pure helpers
// ---------------------------------------------------------------------------

func TestCovOHCResolveLLMProvider(t *testing.T) {
	cases := []struct {
		in      string
		wantOK  bool
		wantPrv string
		wantEnv string
	}{
		{"", true, "ANTHROPIC", "ANTHROPIC_API_KEY"},
		{"anthropic", true, "ANTHROPIC", "ANTHROPIC_API_KEY"},
		{"openai", true, "OPENAI", "OPENAI_API_KEY"},
		{"google", true, "GOOGLE", "GOOGLE_API_KEY"},
		{"cursor", true, "CURSOR", "CURSOR_API_KEY"},
		{"factory", true, "FACTORY", "FACTORY_API_KEY"},
		{"ollama", true, "OLLAMA", ""},
		{"nope", false, "", ""},
	}
	for _, tc := range cases {
		got, ok := resolveLLMProvider(tc.in)
		if ok != tc.wantOK {
			t.Errorf("resolveLLMProvider(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if ok && (got.provider != tc.wantPrv || got.envVarName != tc.wantEnv) {
			t.Errorf("resolveLLMProvider(%q) = %+v, want provider=%q env=%q", tc.in, got, tc.wantPrv, tc.wantEnv)
		}
	}
}

func TestCovOHCMakeSlug(t *testing.T) {
	cases := map[string]string{
		"Hello World":   "hello-world",
		"  Trim  Me  ":  "trim-me",
		"a//b__c":       "a-b-c",
		"":              "default",
		"---":           "default",
		"Über Café 123": "ber-caf-123",
	}
	for in, want := range cases {
		if got := makeSlug(in); got != want {
			t.Errorf("makeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// agents_hire.go — Hire validation arms
// ---------------------------------------------------------------------------

func TestCovOHCHire_MutuallyExclusiveCrewRefs(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "both refs set",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (crew_id + crew_slug exclusive)", rr.Code)
	}
}

func TestCovOHCHire_MissingTemplateSlug(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id": crewID,
		"reason":  "no template",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (template_slug required)", rr.Code)
	}
}

func TestCovOHCHire_MissingCrewRef(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"template_slug": "docs-writer",
		"reason":        "no crew ref",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (crew_id or crew_slug required)", rr.Code)
	}
}

func TestCovOHCHire_BadJSON(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	req := httptest.NewRequest("POST", "/api/v1/agents/hire", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Hire(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON)", rr.Code)
	}
}

func TestCovOHCHire_BySlugAndTTLClamp(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	// crew_slug lookup branch + over-max TTL clamp arm.
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "by slug",
		"ttl_minutes":   99999, // above maxHireTTLMinutes → clamped
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCovOHCHire_TemplateNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "no-such-template",
		"reason":        "missing template",
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (template not found)", rr.Code)
	}
}

func TestCovOHCHire_FullAutonomyJournalOnly(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "full", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "full autonomy journal-only",
		"model":         "claude-3-5-sonnet",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Decision != string(policy.DecisionAutoJournal) {
		t.Errorf("decision = %q, want %q (full autonomy)", resp.Decision, policy.DecisionAutoJournal)
	}
	// Journal-only → no inbox row.
	var inboxCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`, resp.ID).Scan(&inboxCount)
	if inboxCount != 0 {
		t.Errorf("inbox rows = %d on full autonomy; want 0 (journal-only)", inboxCount)
	}
	// llm_provider inferred from model.
	var prov string
	_ = db.QueryRow(`SELECT COALESCE(llm_provider,'') FROM agents WHERE id = ?`, resp.ID).Scan(&prov)
	if prov != "ANTHROPIC" {
		t.Errorf("llm_provider = %q, want ANTHROPIC (inferred)", prov)
	}
}

func TestCovOHCHire_ParentLeadNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "bad parent",
		"parent_lead_id": "no-such-lead",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (parent_lead_id not found)", rr.Code)
	}
}

func TestCovOHCHire_ParentLeadWrongCrew(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	// LEAD in a *different* crew within the same workspace.
	seedCrewRow(t, db, "other-crew", wsID, "Other", "other")
	seedAgentRow(t, db, "lead-other", wsID, "other-crew", "Lead", "lead-other", "LEAD")
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "cross-crew parent",
		"parent_lead_id": "lead-other",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (parent lead belongs to different crew)", rr.Code)
	}
}

func TestCovOHCHire_ParentLeadHappy(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	seedAgentRow(t, db, "lead-ok", wsID, crewID, "Lead", "lead-ok", "LEAD")
	h := newHireHandler(t, db)
	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_id":        crewID,
		"template_slug":  "docs-writer",
		"reason":         "good parent",
		"parent_lead_id": "lead-ok",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var resp hireResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.ParentLeadID == nil || *resp.ParentLeadID != "lead-ok" {
		t.Errorf("parent_lead_id = %v, want lead-ok", resp.ParentLeadID)
	}
}

// ---------------------------------------------------------------------------
// agents_hire.go — Rehire
// ---------------------------------------------------------------------------

func covOHCPostRehire(t *testing.T, h *AgentHandler, userID, wsID, role, agentID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/rehire", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agentId", agentID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Rehire(rr, req)
	return rr
}

func TestCovOHCRehire_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "VIEWER", "any", map[string]any{"reason": "x"})
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (RBAC)", rr.Code)
	}
}

func TestCovOHCRehire_MissingReason(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	aid := seedEphemeralAgent(t, db, wsID, crewID, "eph-r", nil, nil, "[ts] hire: orig")
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "MANAGER", aid, map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (reason required)", rr.Code)
	}
}

func TestCovOHCRehire_AgentNotFound(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "MANAGER", "ghost-agent", map[string]any{"reason": "extend"})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (agent not found)", rr.Code)
	}
}

func TestCovOHCRehire_NotEphemeral(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	// Permanent agent (ephemeral defaults to 0).
	seedAgentRow(t, db, "perm-1", wsID, crewID, "Perm", "perm", "AGENT")
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "MANAGER", "perm-1", map[string]any{"reason": "extend"})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (not ephemeral)", rr.Code)
	}
}

func TestCovOHCRehire_GhostHappy(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	expired := "2024-01-01T00:00:00Z"
	aid := seedEphemeralAgent(t, db, wsID, crewID, "eph-ghost", nil, &expired, "[2024-01-01T00:00:00Z] hire: orig")
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "MANAGER", aid, map[string]any{
		"reason":      "bring back",
		"ttl_minutes": 60,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	// expired_at cleared (un-ghosted), reason appended.
	var expiredAt, reason string
	_ = db.QueryRow(`SELECT COALESCE(expired_at,''), COALESCE(hire_reason,'') FROM agents WHERE id = ?`, aid).Scan(&expiredAt, &reason)
	if expiredAt != "" {
		t.Errorf("expired_at = %q, want cleared after rehire", expiredAt)
	}
	if !strings.Contains(reason, "rehire: bring back") {
		t.Errorf("hire_reason = %q, want appended rehire line", reason)
	}
}

func TestCovOHCRehire_StrictRejected(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "strict", 5)
	aid := seedEphemeralAgent(t, db, wsID, crewID, "eph-strict", nil, nil, "[ts] hire: orig")
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "MANAGER", aid, map[string]any{"reason": "try extend"})
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (strict policy reject)", rr.Code)
	}
}

func TestCovOHCRehire_GhostQuotaExceeded(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 1) // max 1 live
	// One live ephemeral already fills the quota.
	seedEphemeralAgent(t, db, wsID, crewID, "eph-live", nil, nil, "[ts] hire: live")
	// A ghost we try to rehire — should 429 because the slot is taken.
	expired := "2024-01-01T00:00:00Z"
	ghost := seedEphemeralAgent(t, db, wsID, crewID, "eph-ghost2", nil, &expired, "[ts] hire: ghost")
	h := newHireHandler(t, db)
	rr := covOHCPostRehire(t, h, userID, wsID, "MANAGER", ghost, map[string]any{"reason": "bring back"})
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 (ghost rehire over quota)", rr.Code)
	}
}

func TestCovOHCRehire_BadJSON(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	aid := seedEphemeralAgent(t, db, wsID, crewID, "eph-bj", nil, nil, "[ts] hire: orig")
	h := newHireHandler(t, db)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+aid+"/rehire", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agentId", aid)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Rehire(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// agents_hire.go — pure helpers
// ---------------------------------------------------------------------------

func TestCovOHCAppendRehireReason(t *testing.T) {
	// Empty prior → bare rehire line.
	if got := appendRehireReason("", "first", "2026-01-01T00:00:00Z"); got != "[2026-01-01T00:00:00Z] rehire: first" {
		t.Errorf("empty prior = %q", got)
	}
	// Non-empty prior → appended with newline, trailing newline trimmed.
	prior := "[2026-01-01T00:00:00Z] hire: orig\n"
	got := appendRehireReason(prior, "more", "2026-01-02T00:00:00Z")
	want := "[2026-01-01T00:00:00Z] hire: orig\n[2026-01-02T00:00:00Z] rehire: more"
	if got != want {
		t.Errorf("appended = %q, want %q", got, want)
	}
}

func TestCovOHCHireInboxBodyAndTitle(t *testing.T) {
	if got := hireInboxTitle("Bot", 30); !strings.Contains(got, "Bot") || !strings.Contains(got, "30m") {
		t.Errorf("hireInboxTitle = %q", got)
	}
	// Empty model → "(template default)".
	body := hireInboxBody("ship it", "docs-writer", 60, "")
	if !strings.Contains(body, "(template default)") {
		t.Errorf("hireInboxBody empty model = %q, want template-default marker", body)
	}
	body2 := hireInboxBody("ship it", "docs-writer", 60, "claude-3")
	if !strings.Contains(body2, "claude-3") {
		t.Errorf("hireInboxBody = %q, want model echoed", body2)
	}
}

// ---------------------------------------------------------------------------
// agent_config.go — pure / unit-level resolver edges
// ---------------------------------------------------------------------------

func TestCovOHCResolveNetworkPolicy(t *testing.T) {
	h := &InternalHandler{logger: newTestLogger()}

	// Default (no crew network mode) → free, empty domains.
	mode, domains := h.resolveNetworkPolicy(&agentConfigData{})
	if mode != "free" || len(domains) != 0 {
		t.Errorf("default = (%q, %v), want (free, [])", mode, domains)
	}

	// Explicit restricted + valid domains JSON.
	d := &agentConfigData{}
	d.crewNetworkMode.Valid, d.crewNetworkMode.String = true, "restricted"
	d.crewAllowedDomains.Valid, d.crewAllowedDomains.String = true, `["github.com","npm.org"]`
	mode, domains = h.resolveNetworkPolicy(d)
	if mode != "restricted" || len(domains) != 2 {
		t.Errorf("restricted = (%q, %v), want (restricted, 2 domains)", mode, domains)
	}

	// Unknown mode in DB → fail closed to restricted.
	d2 := &agentConfigData{}
	d2.crewNetworkMode.Valid, d2.crewNetworkMode.String = true, "yolo"
	mode, _ = h.resolveNetworkPolicy(d2)
	if mode != "restricted" {
		t.Errorf("unknown mode = %q, want restricted (fail-closed)", mode)
	}

	// Malformed domains JSON → empty slice, no panic.
	d3 := &agentConfigData{}
	d3.crewNetworkMode.Valid, d3.crewNetworkMode.String = true, "restricted"
	d3.crewAllowedDomains.Valid, d3.crewAllowedDomains.String = true, "{not-json"
	_, domains = h.resolveNetworkPolicy(d3)
	if len(domains) != 0 {
		t.Errorf("malformed domains = %v, want empty", domains)
	}
}

func TestCovOHCResolveContainerResources(t *testing.T) {
	h := &InternalHandler{logger: newTestLogger()}

	// Defaults when all NULL.
	mem, cpus, ttl := h.resolveContainerResources(&agentConfigData{})
	if mem != 4096 || cpus != 2.0 || ttl != 0 {
		t.Errorf("defaults = (%d, %v, %d), want (4096, 2.0, 0)", mem, cpus, ttl)
	}

	// Overrides.
	d := &agentConfigData{}
	d.crewMemoryMB.Valid, d.crewMemoryMB.Int64 = true, 8192
	d.crewCPUs.Valid, d.crewCPUs.Float64 = true, 4.0
	d.crewTTLHours.Valid, d.crewTTLHours.Int64 = true, 12
	mem, cpus, ttl = h.resolveContainerResources(d)
	if mem != 8192 || cpus != 4.0 || ttl != 12 {
		t.Errorf("overrides = (%d, %v, %d), want (8192, 4.0, 12)", mem, cpus, ttl)
	}
}

func TestCovOHCReconstructSKILLMD(t *testing.T) {
	// Body already has frontmatter → returned verbatim.
	withFM := "---\nname: foo\n---\nbody"
	if got := reconstructSKILLMD("foo", "anthropic", "Foo", "desc", withFM); got != withFM {
		t.Errorf("frontmatter body should pass through verbatim, got %q", got)
	}

	// Body without frontmatter → synthesised header.
	got := reconstructSKILLMD("rev", "custom", "Review", "Line1\nLine2", "## content")
	if !strings.HasPrefix(got, "---\n") {
		t.Errorf("synthesised SKILL.md missing frontmatter: %q", got)
	}
	if !strings.Contains(got, `name: "rev"`) {
		t.Errorf("missing name field: %q", got)
	}
	if !strings.Contains(got, `display_name: "Review"`) {
		t.Errorf("missing display_name: %q", got)
	}
	// Description newlines collapsed to spaces.
	if strings.Contains(got, "Line1\nLine2") {
		t.Errorf("description newlines not collapsed: %q", got)
	}
	if !strings.Contains(got, `vendor: "custom"`) {
		t.Errorf("missing vendor: %q", got)
	}
	if !strings.HasSuffix(got, "## content") {
		t.Errorf("body not appended: %q", got)
	}
}

func TestCovOHCYamlQuote(t *testing.T) {
	if got := yamlQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("yamlQuote = %q, want escaped quote + backslash", got)
	}
	if got := yamlQuote("plain"); got != `"plain"` {
		t.Errorf("yamlQuote(plain) = %q", got)
	}
}

// ---------------------------------------------------------------------------
// agent_config.go — DB-backed resolver paths
// ---------------------------------------------------------------------------

func TestCovOHCResolveAgentConfig_NotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	h.resolveAgentConfig(w, req, "no-such-agent")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestCovOHCResolveOAuthAccessTokens_Append(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	enc, _ := encryption.Encrypt("oauth-token-bytes")
	_, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-oa', ?, 'Oauth', ?, 'OAUTH2', 'GOOGLE', 'ACTIVE', ?)`, wsID, enc, userID)
	if err != nil {
		t.Fatalf("cred: %v", err)
	}
	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)

	// Only a _CLIENT_ID entry → append access token.
	out := h.resolveOAuthAccessTokens(req, []mcpCredEntry{{ID: "cr-oa", EnvVar: "GOOGLE_CLIENT_ID", Type: "OAUTH2"}})
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (orig + access token)", len(out))
	}
	// No OAUTH2 cred → unchanged.
	out2 := h.resolveOAuthAccessTokens(req, []mcpCredEntry{{ID: "x", EnvVar: "API_KEY", Type: "API_KEY"}})
	if len(out2) != 1 {
		t.Errorf("non-oauth len = %d, want 1 (unchanged)", len(out2))
	}
}

func TestCovOHCBuildKeeperBlock(t *testing.T) {
	h := &InternalHandler{logger: newTestLogger()}
	// No SECRET creds → empty block.
	if got := h.buildKeeperBlock("bot", []mcpCredEntry{{EnvVar: "X", Type: "API_KEY"}}); got != "" {
		t.Errorf("non-secret creds should yield empty keeper block, got %q", got)
	}
	// One SECRET cred → block lists it AND the value is scrubbed in-place.
	creds := []mcpCredEntry{{EnvVar: "DB_PW", Type: "SECRET", Value: "supersecret"}}
	block := h.buildKeeperBlock("bot", creds)
	if !strings.Contains(block, "DB_PW") {
		t.Errorf("keeper block missing secret env name: %q", block)
	}
	if !strings.Contains(block, "agent_slug\":\"bot\"") {
		t.Errorf("keeper block missing agent_slug: %q", block)
	}
	if creds[0].Value != "" {
		t.Errorf("SECRET cred value not scrubbed, still = %q", creds[0].Value)
	}
}

func TestCovOHCResolveSkillsBlock_Truncation(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-sk", wsID, "SK", "sk")
	seedAgentRow(t, db, "agent-sk", wsID, "crew-sk", "SK", "sk", "AGENT")

	// One oversized skill (> 20k budget) forces the truncation branch.
	big := strings.Repeat("x", 25000)
	_, err := db.Exec(`INSERT INTO skills (id, name, slug, display_name, category, source, content, credential_requirements)
		VALUES ('sk-big', 'Big', 'big', 'Big', 'CODING', 'CUSTOM', ?, '["GH_TOKEN"]')`, big)
	if err != nil {
		t.Fatalf("skill: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('asb', 'agent-sk', 'sk-big', 1)`)
	if err != nil {
		t.Fatalf("agent_skills: %v", err)
	}

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	// GH_TOKEN configured → "configured ✓" credential line branch.
	creds := []mcpCredEntry{{EnvVar: "GH_TOKEN", Type: "API_KEY", Value: "t"}}
	block, err := h.resolveSkillsBlock(req, creds, "agent-sk")
	if err != nil {
		t.Fatalf("resolveSkillsBlock: %v", err)
	}
	if !strings.Contains(block, "[SKILLS AVAILABLE]") {
		t.Errorf("missing skills header: %q", block[:min(80, len(block))])
	}
	if !strings.Contains(block, "truncated") {
		t.Errorf("oversized skill should be truncated; block lacks marker")
	}
	if !strings.Contains(block, "configured ✓") {
		t.Errorf("configured credential line missing")
	}
}

func TestCovOHCResolveSkillsBlock_NotConfiguredCred(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-nc", wsID, "NC", "nc")
	seedAgentRow(t, db, "agent-nc", wsID, "crew-nc", "NC", "nc", "AGENT")
	_, err := db.Exec(`INSERT INTO skills (id, name, slug, display_name, category, source, content, credential_requirements)
		VALUES ('sk-nc', 'NC', 'nc', 'NC', 'CODING', 'CUSTOM', '## small', '["MISSING_TOKEN"]')`)
	if err != nil {
		t.Fatalf("skill: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('asnc', 'agent-nc', 'sk-nc', 1)`)
	if err != nil {
		t.Fatalf("agent_skills: %v", err)
	}
	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	block, err := h.resolveSkillsBlock(req, nil, "agent-nc")
	if err != nil {
		t.Fatalf("resolveSkillsBlock: %v", err)
	}
	if !strings.Contains(block, "NOT CONFIGURED") {
		t.Errorf("missing NOT CONFIGURED line: %q", block)
	}
}

func TestCovOHCResolveAgentMCPServers_CredAttachment(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-mcp", wsID, "MCP", "mcp")
	seedAgentRow(t, db, "agent-mcp", wsID, "crew-mcp", "MCP", "mcp", "AGENT")

	// Workspace MCP server (http transport).
	_, err := db.Exec(`INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport, endpoint, enabled)
		VALUES ('ws-srv', ?, 'gh', 'GitHub', 'streamable_http', 'https://mcp.example.com', 1)`, wsID)
	if err != nil {
		t.Fatalf("ws server: %v", err)
	}
	// A credential to attach.
	enc, _ := encryption.Encrypt("mcp-token")
	_, err = db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-mcp', ?, 'McpTok', ?, 'API_KEY', 'GITHUB', 'ACTIVE', ?)`, wsID, enc, userID)
	if err != nil {
		t.Fatalf("cred: %v", err)
	}
	// Agent binding enables the server and attaches the credential.
	_, err = db.Exec(`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled, cred_type, cred_header)
		VALUES ('bnd', 'agent-mcp', 'ws-srv', 'workspace', 'cr-mcp', 1, '', 'Authorization')`)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}

	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	d := &agentConfigData{wsID: wsID}
	d.crewID.Valid, d.crewID.String = true, "crew-mcp"
	servers := h.resolveAgentMCPServers(req, d, "agent-mcp")
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(servers))
	}
	if servers[0].CredToken != "mcp-token" {
		t.Errorf("cred token = %q, want decrypted mcp-token", servers[0].CredToken)
	}
	// cred_type defaulted to bearer when empty.
	if servers[0].CredType != "bearer" {
		t.Errorf("cred_type = %q, want defaulted bearer", servers[0].CredType)
	}
}

func TestCovOHCResolveAgentMCPServers_OptOut(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-oo", wsID, "OO", "oo")
	seedAgentRow(t, db, "agent-oo", wsID, "crew-oo", "OO", "oo", "AGENT")
	_, err := db.Exec(`INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport, enabled)
		VALUES ('ws-oo', ?, 'srv', 'Srv', 'stdio', 1)`, wsID)
	if err != nil {
		t.Fatalf("ws server: %v", err)
	}
	// Binding present but disabled → server opted out.
	_, err = db.Exec(`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, enabled)
		VALUES ('bnd-oo', 'agent-oo', 'ws-oo', 'workspace', 0)`)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	h := NewInternalHandler(db, "tok", newTestLogger())
	req := httptest.NewRequest("GET", "/x", nil)
	d := &agentConfigData{wsID: wsID}
	d.crewID.Valid, d.crewID.String = true, "crew-oo"
	servers := h.resolveAgentMCPServers(req, d, "agent-oo")
	if len(servers) != 0 {
		t.Errorf("servers = %d, want 0 (opted out)", len(servers))
	}
}
