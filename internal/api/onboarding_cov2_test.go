package api

// onboarding.go coverage top-up #2 — the failure-injection branches the
// first cov file left out: Status flag-persist warn, Setup's DB-error
// forks (find workspace, preferred_language, telemetry consent), the
// service error mapping (ErrWorkspaceNotFound / generic 500), and
// setupFromTemplate's lock/rename/credential/rollback failure paths.
//
// Failure injection uses SQLite triggers (RAISE(ABORT, …)) and dropped
// tables, never production-code changes. The live Anthropic probe is
// always routed around via withTokenProbeSkipped.
//
// All tests are prefixed TestCov2Onb.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/services"
)

func cov2OnbAbortTrigger(t *testing.T, db *sql.DB, name, opAndTable string) {
	t.Helper()
	stmt := `CREATE TRIGGER ` + name + ` BEFORE ` + opAndTable + `
		BEGIN SELECT RAISE(ABORT, 'cov2 injected failure'); END`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

func cov2OnbSetupReq(userID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/setup", strings.NewReader(body))
	return req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
}

func cov2OnbCompleted(t *testing.T, db *sql.DB, userID string) bool {
	t.Helper()
	var completed bool
	if err := db.QueryRow("SELECT onboarding_completed FROM users WHERE id = ?", userID).Scan(&completed); err != nil {
		t.Fatalf("read onboarding_completed: %v", err)
	}
	return completed
}

// --- Status: smart-detect persist failure is a warn, not an error ---

func TestCov2OnbStatus_PersistFlagFailureStillReportsCompleted(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-c2s", wsID, "Crew", "crew-c2s")
	seedAgentRow(t, db, "agent-c2s", wsID, crewID, "Agent", "agent-c2s", "AGENT")
	// Block the UPDATE that persists the smart-detected flag.
	cov2OnbAbortTrigger(t, db, "cov2_onb_users_upd", "UPDATE ON users")

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Status(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"completed":true`) {
		t.Errorf("body = %s, want completed:true despite persist failure", w.Body.String())
	}
	if cov2OnbCompleted(t, db, userID) {
		t.Error("flag persisted although the UPDATE was trigger-blocked")
	}
}

// --- Complete: UPDATE failure → 500 ---

func TestCov2OnbComplete_UpdateFailure500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	cov2OnbAbortTrigger(t, db, "cov2_onb_cpl_upd", "UPDATE ON users")
	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Complete(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
}

// --- Setup: find-workspace query error (not ErrNoRows) → 500 ---

func TestCov2OnbSetup_WorkspaceQueryError500(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	if _, err := db.Exec(`DROP TABLE workspace_members`); err != nil {
		t.Fatalf("drop workspace_members: %v", err)
	}
	h := NewOnboardingHandler(db, nil, testLogger())
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_name":"X","agent_name":"Y"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (no such table), body=%s", w.Code, w.Body.String())
	}
}

// --- Setup: preferred_language persist failure is soft (201 still) ---

func TestCov2OnbSetup_PreferredLanguageWriteFailureIsSoft(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Blocks the preferred_language UPDATE; workspace_name stays empty so
	// the service never updates workspaces again.
	cov2OnbAbortTrigger(t, db, "cov2_onb_ws_upd", "UPDATE ON workspaces")

	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())
	body := `{"crew_name":"Crew","agent_name":"Eva","preferred_language":"Czech"}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (language write is soft-fail), body=%s", w.Code, w.Body.String())
	}
	var lang sql.NullString
	if err := db.QueryRow("SELECT preferred_language FROM workspaces WHERE id = ?", wsID).Scan(&lang); err != nil {
		t.Fatalf("read preferred_language: %v", err)
	}
	if lang.String == "Czech" {
		t.Error("preferred_language persisted although the UPDATE was blocked")
	}
}

// --- Setup: telemetry consent persist failure is soft ---

func TestCov2OnbSetup_TelemetryPersistFailureIsSoft(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`DROP TABLE app_settings`); err != nil {
		t.Fatalf("drop app_settings: %v", err)
	}
	h := NewOnboardingHandler(db, nil, testLogger())
	// crew_name missing → flow stops with 400 AFTER the telemetry branch,
	// proving the SetOptIn failure didn't abort the request.
	body := `{"telemetry_opt_in":true}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (crew_name required), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "crew_name") {
		t.Errorf("body = %s, want crew_name validation error", w.Body.String())
	}
}

// --- Setup: service maps ErrWorkspaceNotFound → 400 ---

func TestCov2OnbSetup_ServiceWorkspaceNotFound400(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	// The service runs against a SECOND database that has no membership
	// rows — its in-tx membership re-check fails with ErrWorkspaceNotFound
	// even though the handler's own lookup succeeded.
	otherDB := setupTestDB(t)
	svc := services.NewOnboardingService(otherDB, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_name":"X","agent_name":"Y"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No workspace found") {
		t.Errorf("body = %s, want \"No workspace found\"", w.Body.String())
	}
}

// --- Setup: generic service failure → 500 ---

func TestCov2OnbSetup_ServiceGenericError500(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	cov2OnbAbortTrigger(t, db, "cov2_onb_crews_ins", "INSERT ON crews")
	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_name":"X","agent_name":"Y"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (crew insert blocked), body=%s", w.Code, w.Body.String())
	}
	// The service tx rolled back, so onboarding stays claimable.
	if cov2OnbCompleted(t, db, userID) {
		t.Error("onboarding_completed must roll back with the failed service tx")
	}
}

// --- setupFromTemplate: CAS lock UPDATE failure → 500 ---

func TestCov2OnbTemplate_LockFailure500(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	cov2OnbAbortTrigger(t, db, "cov2_onb_tpl_lock", "UPDATE ON users")
	h := NewOnboardingHandler(db, nil, testLogger())

	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_template_slug":"software-development"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (CAS lock blocked), body=%s", w.Code, w.Body.String())
	}
}

// --- setupFromTemplate: workspace rename failure is soft; deploy error
//     still surfaces (unknown template → 400 + rollback) ---

func TestCov2OnbTemplate_RenameFailureSoftThenUnknownTemplate(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	cov2OnbAbortTrigger(t, db, "cov2_onb_tpl_ws_upd", "UPDATE ON workspaces")
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{"crew_template_slug":"no-such-template-c2","workspace_name":"New Name"}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown template), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Unknown crew template") {
		t.Errorf("body = %s, want \"Unknown crew template\"", w.Body.String())
	}
	// Rename never landed (soft failure) ...
	var name string
	if err := db.QueryRow("SELECT name FROM workspaces WHERE id = ?", wsID).Scan(&name); err != nil {
		t.Fatalf("read workspace name: %v", err)
	}
	if name == "New Name" {
		t.Error("workspace rename persisted although UPDATE was blocked")
	}
	// ... and the completion flag rolled back so the user can retry.
	if cov2OnbCompleted(t, db, userID) {
		t.Error("onboarding_completed not rolled back after deploy failure")
	}
}

// --- setupFromTemplate: rollback UPDATE itself failing is logged, the
//     client still gets the 400 ---

func TestCov2OnbTemplate_RollbackFailureStillAnswers400(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	// Only the compensating UPDATE (back to 0) is blocked — the CAS
	// claim (sets 1) passes the WHEN clause.
	if _, err := db.Exec(`CREATE TRIGGER cov2_onb_tpl_rb BEFORE UPDATE ON users
		WHEN NEW.onboarding_completed = 0
		BEGIN SELECT RAISE(ABORT, 'cov2 rollback blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := NewOnboardingHandler(db, nil, testLogger())

	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_template_slug":"no-such-template-c2"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 despite rollback failure, body=%s", w.Code, w.Body.String())
	}
	// The flag stays claimed because the rollback was blocked.
	if !cov2OnbCompleted(t, db, userID) {
		t.Error("expected onboarding_completed to remain 1 (rollback blocked)")
	}
}

// --- setupFromTemplate: provider-reject rollback failure → still 400 ---

func TestCov2OnbTemplate_ProviderRejectRollbackFailureStill400(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`CREATE TRIGGER cov2_onb_tpl_rb2 BEFORE UPDATE ON users
		WHEN NEW.onboarding_completed = 0
		BEGIN SELECT RAISE(ABORT, 'cov2 rollback blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{"crew_template_slug":"software-development","llm_provider":"BOGUS","credential_value":"sk-ant-oat01-x"}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown provider), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Unknown llm_provider") {
		t.Errorf("body = %s, want \"Unknown llm_provider\"", w.Body.String())
	}
}

// --- setupFromTemplate: credential store failure → 500 + flag rollback ---

func TestCov2OnbTemplate_CredentialStoreFailure500RollsBack(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	cov2OnbAbortTrigger(t, db, "cov2_onb_tpl_cred", "INSERT ON credentials")
	h := NewOnboardingHandler(db, nil, testLogger())

	// OLLAMA passes validateOnboardingCredential (non-Anthropic falls
	// through) and resolveLLMProvider, so the flow reaches the blocked
	// credentials INSERT.
	body := `{"crew_template_slug":"software-development","llm_provider":"OLLAMA","credential_value":"some-token"}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (credential insert blocked), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Failed to store credential") {
		t.Errorf("body = %s, want \"Failed to store credential\"", w.Body.String())
	}
	if cov2OnbCompleted(t, db, userID) {
		t.Error("onboarding_completed not rolled back after credential failure")
	}
}

// --- setupFromTemplate: builtin seeding failure → warn + deploy 500 ---

func TestCov2OnbTemplate_SeedFailureFallsToDeploy500(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`DROP TABLE crew_templates`); err != nil {
		t.Fatalf("drop crew_templates: %v", err)
	}
	h := NewOnboardingHandler(db, nil, testLogger())

	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_template_slug":"software-development"}`))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (deploy hits missing table), body=%s", w.Code, w.Body.String())
	}
	if cov2OnbCompleted(t, db, userID) {
		t.Error("onboarding_completed not rolled back after deploy failure")
	}
}

// --- setupFromTemplate: crew slug conflict → 409 + rollback ---

func TestCov2OnbTemplate_CrewSlugConflict409(t *testing.T) {
	withTokenProbeSkipped(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Pre-existing crew whose slug collides with the deploy's derived
	// slug ("Software Development" → software-development).
	seedCrewRow(t, db, "crew-conflict", wsID, "Existing", "software-development")
	h := NewOnboardingHandler(db, nil, testLogger())

	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, `{"crew_template_slug":"software-development"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (crew slug conflict), body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Crew slug already exists") {
		t.Errorf("body = %s, want \"Crew slug already exists\"", w.Body.String())
	}
	if cov2OnbCompleted(t, db, userID) {
		t.Error("onboarding_completed not rolled back after slug conflict")
	}
}
