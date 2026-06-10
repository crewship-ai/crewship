package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/services"
)

// ---------------------------------------------------------------------------
// onboarding.go — handler-level coverage for the branches the existing
// internal_handlers_test.go / onboarding_validate_test.go suites leave
// uncovered: the Status smart-detect path, Complete's user-not-found
// branch, Setup's workspace / credential / provider validation forks,
// and the full setupFromTemplate flow (CAS guard, provider reject,
// template-not-found, and the happy 201 deploy).
//
// All tests are prefixed TestCovOnb so `go test -run TestCovOnb` selects
// exactly this file. The live Anthropic probe in probeAnthropicOAuthToken
// is intentionally NOT exercised here — it makes a real network call; the
// onboarding_probe_test.go skip-gate test owns that contract, and every
// Setup test below routes around it via withTokenProbeSkipped.
// ---------------------------------------------------------------------------

// ---- Status: smart-detect (user has agents but flag is 0 → completed) ----

func TestCovOnbStatus_SmartDetectFlipsCompleted(t *testing.T) {
	// Source: "if user already has agents (e.g. provisioned via CLI),
	// treat as completed" — and persist the flag. This is the uncovered
	// branch of Status: onboarding_completed=0 AND agentCount>0.
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-smart", wsID, "Crew", "crew")
	seedAgentRow(t, db, "agent-smart", wsID, crewID, "Agent", "agent", "AGENT")

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Status(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"completed":true`) {
		t.Errorf("body = %s, want completed:true (smart-detect)", w.Body.String())
	}
	// The flag must have been persisted so the next call short-circuits.
	var completed bool
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !completed {
		t.Error("onboarding_completed not persisted after smart-detect")
	}
}

// ---- Complete: user-not-found (auth passes, no row updated → 404) ----

func TestCovOnbComplete_UserNotFound(t *testing.T) {
	// rows==0 branch: a context user whose id has no matching users row.
	db := setupTestDB(t)
	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "ghost-user"}))
	w := httptest.NewRecorder()
	h.Complete(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no row updated)", w.Code)
	}
}

// ---- Setup: workspace-not-found (user exists but no membership) ----

func TestCovOnbSetup_NoWorkspace(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db) // user, but NO workspace_members row
	h := NewOnboardingHandler(db, nil, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"crew_name":"X","agent_name":"Y"}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no workspace), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No workspace found") {
		t.Errorf("body = %s, want \"No workspace found\"", w.Body.String())
	}
}

// ---- Setup: credential shape rejection (raw API key → 400) ----

func TestCovOnbSetup_InvalidCredentialShape(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{"crew_name":"X","agent_name":"Y","credential_value":"sk-ant-api-WRONG"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (bad credential shape), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Claude Code CLI token") {
		t.Errorf("body = %s, want fix-it hint", w.Body.String())
	}
}

// ---- Setup: unknown llm_provider in single-agent branch → 400 ----

func TestCovOnbSetup_UnknownProvider(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{"crew_name":"X","agent_name":"Y","llm_provider":"BOGUS"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown provider), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "llm_provider must be") {
		t.Errorf("body = %s, want provider list", w.Body.String())
	}
}

// ---- Setup: single-agent happy path through the service (201 Created) ----

func TestCovOnbSetup_SingleAgentSuccess(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	// preferred_language exercises the pre-branch UPDATE; cli_adapter
	// empty exercises the CLAUDE_CODE default; a valid OAuth-shaped token
	// exercises the credential-name default + persistence.
	body := `{
		"workspace_name":"Renamed WS",
		"crew_name":"Build Crew",
		"agent_name":"Eva",
		"preferred_language":"Czech",
		"credential_value":"sk-ant-oat01-fake-but-valid-shape"
	}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	// preferred_language was persisted before the branch.
	var lang string
	if err := db.QueryRow("SELECT preferred_language FROM workspaces WHERE id = ?", wsID).Scan(&lang); err != nil {
		t.Fatalf("read preferred_language: %v", err)
	}
	if lang != "Czech" {
		t.Errorf("preferred_language = %q, want Czech", lang)
	}
	// A credential row landed (default name "API Key").
	var credName string
	if err := db.QueryRow("SELECT name FROM credentials WHERE workspace_id = ?", wsID).Scan(&credName); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if credName != "API Key" {
		t.Errorf("credential name = %q, want default \"API Key\"", credName)
	}
}

// ---- Setup: explicit telemetry consent rides the wizard submission ----

func TestCovOnbSetup_TelemetryConsentPersisted(t *testing.T) {
	// The onboarding wizard (web + `crewship setup`) carries an explicit
	// telemetry consent answer in `telemetry_opt_in`. The handler must
	// persist it via crashreport.SetOptIn so the choice survives in
	// app_settings exactly like `crewship telemetry on|off` would write it.
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)

	for _, tc := range []struct {
		name    string
		consent string // JSON literal for the field
		want    string // expected app_settings value
	}{
		{"opt-in", "true", "1"},
		{"opt-out", "false", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			seedTestWorkspace(t, db, userID)

			svc := services.NewOnboardingService(db, testLogger(), generateCUID)
			h := NewOnboardingHandler(db, svc, testLogger())

			body := `{
				"crew_name":"Build Crew",
				"agent_name":"Eva",
				"telemetry_opt_in":` + tc.consent + `
			}`
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
			w := httptest.NewRecorder()
			h.Setup(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
			}
			var val string
			if err := db.QueryRow(
				"SELECT value FROM app_settings WHERE key = 'telemetry_opt_in'").Scan(&val); err != nil {
				t.Fatalf("read telemetry_opt_in: %v", err)
			}
			if val != tc.want {
				t.Errorf("telemetry_opt_in = %q, want %q", val, tc.want)
			}
		})
	}
}

func TestCovOnbSetup_TelemetryOmittedLeavesDefault(t *testing.T) {
	// Backwards compatibility: a wizard submission WITHOUT the field
	// (older frontend, scripted CLI without --telemetry) must not touch
	// the consent row — the version-based default keeps ruling.
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	body := `{"crew_name":"C","agent_name":"A"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}

	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM app_settings WHERE key = 'telemetry_opt_in'").Scan(&n); err != nil {
		t.Fatalf("count telemetry_opt_in rows: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no consent row when telemetry_opt_in omitted, found %d", n)
	}
}

// ---- Setup: second call after success → service returns
//      ErrOnboardingAlreadyCompleted → 409 ----

func TestCovOnbSetup_AlreadyCompletedConflict(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	body := `{"crew_name":"C","agent_name":"A"}`
	first := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	first = first.WithContext(withUser(first.Context(), &AuthUser{ID: userID}))
	fw := httptest.NewRecorder()
	h.Setup(fw, first)
	if fw.Code != http.StatusCreated {
		t.Fatalf("first setup status = %d, body=%s", fw.Code, fw.Body.String())
	}

	second := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	second = second.WithContext(withUser(second.Context(), &AuthUser{ID: userID}))
	sw := httptest.NewRecorder()
	h.Setup(sw, second)
	if sw.Code != http.StatusConflict {
		t.Errorf("second setup status = %d, want 409 (already completed)", sw.Code)
	}
}

// ---- setupFromTemplate: dispatched from Setup, unknown llm_provider with a
//      credential value → 400 + completion-flag rollback ----

func TestCovOnbSetupTemplate_UnknownProviderRollsBack(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	// crew_template_slug routes into setupFromTemplate. credential_value
	// is a valid OAuth shape (passes validateOnboardingCredential since
	// the provider is unknown → falls through), but llm_provider is
	// unknown → the credential-store branch rejects it and rolls back.
	body := `{
		"crew_template_slug":"software-development",
		"llm_provider":"BOGUS",
		"credential_value":"sk-ant-oat01-fake"
	}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown provider), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Unknown llm_provider") {
		t.Errorf("body = %s, want \"Unknown llm_provider\"", w.Body.String())
	}
	// Rollback: onboarding_completed must be back to 0 so the user can retry.
	var completed bool
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if completed {
		t.Error("onboarding_completed not rolled back after provider reject")
	}
}

// ---- setupFromTemplate: unknown template slug → deploy returns
//      errTemplateNotFound → 400 + rollback ----

func TestCovOnbSetupTemplate_UnknownTemplate(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{"crew_template_slug":"no-such-template-xyz"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown template), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Unknown crew template") {
		t.Errorf("body = %s, want \"Unknown crew template\"", w.Body.String())
	}
	var completed bool
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if completed {
		t.Error("onboarding_completed not rolled back after deploy failure")
	}
}

// ---- setupFromTemplate: full happy path — seeds builtins, deploys a real
//      builtin template, stores the pasted credential, returns 201 ----

func TestCovOnbSetupTemplate_Success(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{
		"crew_template_slug":"software-development",
		"workspace_name":"Templated WS",
		"credential_name":"My Token",
		"credential_value":"sk-ant-oat01-fake-but-valid-shape"
	}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, `"crew_id"`) || !strings.Contains(bodyStr, `"agent_ids"`) {
		t.Errorf("body = %s, want crew_id + agent_ids in template response", bodyStr)
	}
	// Workspace was renamed.
	var name string
	if err := db.QueryRow("SELECT name FROM workspaces WHERE id = ?", wsID).Scan(&name); err != nil {
		t.Fatalf("read workspace name: %v", err)
	}
	if name != "Templated WS" {
		t.Errorf("workspace name = %q, want Templated WS", name)
	}
	// The pasted credential was stored under the explicit name.
	var credName string
	if err := db.QueryRow("SELECT name FROM credentials WHERE workspace_id = ? AND name = 'My Token'", wsID).Scan(&credName); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	// A crew + at least one agent were created by the deploy.
	var agentCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM agents WHERE workspace_id = ?", wsID).Scan(&agentCount); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if agentCount == 0 {
		t.Error("template deploy created no agents")
	}
	// onboarding_completed stayed claimed on success.
	var completed bool
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !completed {
		t.Error("onboarding_completed should remain 1 after successful template deploy")
	}
}

// ---- setupFromTemplate: CAS guard — a second template submit after the
//      first claimed onboarding returns 409 ----

func TestCovOnbSetupTemplate_AlreadyCompletedConflict(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	// First submit claims onboarding (and deploys the template).
	body := `{"crew_template_slug":"research-analysis"}`
	first := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	first = first.WithContext(withUser(first.Context(), &AuthUser{ID: userID}))
	fw := httptest.NewRecorder()
	h.Setup(fw, first)
	if fw.Code != http.StatusCreated {
		t.Fatalf("first template setup status = %d, body=%s", fw.Code, fw.Body.String())
	}

	// Second submit hits the CAS guard (rows==0) → 409.
	second := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	second = second.WithContext(withUser(second.Context(), &AuthUser{ID: userID}))
	sw := httptest.NewRecorder()
	h.Setup(sw, second)
	if sw.Code != http.StatusConflict {
		t.Errorf("second template setup status = %d, want 409 (CAS guard)", sw.Code)
	}
}
