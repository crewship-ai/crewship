package api

// Coverage tests for the recipe install/preview flow (recipes.go) and the
// message-feedback API (message_feedback.go).
//
// recipes_get_test.go does not exist in this tree, so List/Get are not
// separately covered elsewhere; the tests here focus on Install and Preview
// (the heavy uncovered handlers) plus enough of List/Get to exercise the
// catalogue lookup. message_feedback.go Create/List are covered including
// validation, not-found, happy-path DB assertions and 500 fault injection.
//
// All test funcs are prefixed TestCovRec; new local helpers are prefixed
// covRec to avoid clashing with the shared harness in router_test.go /
// core_handlers_test.go.
//
// Skipped intentionally: no network branches exist in either handler — the
// recipe MCP servers are stored as rows (npx commands are not executed at
// install time), and feedback never makes outbound calls. Nothing was
// stubbed for network.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/recipes"
)

// ---------------------------------------------------------------------------
// recipes helpers
// ---------------------------------------------------------------------------

// covRecRealSlug returns a real recipe slug from recipes.All() so tests don't
// hard-code a value that could drift if the curated set changes.
func covRecRealSlug(t *testing.T) string {
	t.Helper()
	all := recipes.All()
	if len(all) == 0 {
		t.Fatal("recipes.All() returned no recipes — cannot exercise install/preview")
	}
	return all[0].Slug
}

// covRecInstallReq builds an Install/Preview request with the slug path value
// and the given workspace-scoped user context.
func covRecInstallReq(t *testing.T, method, slug, body, userID, wsID, role string) *http.Request {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, "/api/v1/recipes/"+slug+"/x", rdr)
	req.SetPathValue("slug", slug)
	return withWorkspaceUser(req, userID, wsID, role)
}

// ---------------------------------------------------------------------------
// recipes.go — List / Get
// ---------------------------------------------------------------------------

func TestCovRecList(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recipes", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("List: got %d, want 200", rec.Code)
	}
	var out []recipes.Recipe
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode List: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("List returned empty catalogue")
	}
}

func TestCovRecGet(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	slug := covRecRealSlug(t)

	t.Run("found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/recipes/"+slug, nil)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Get: got %d, want 200", rec.Code)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/recipes/nope", nil)
		req.SetPathValue("slug", "nope")
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("Get unknown: got %d, want 404", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// recipes.go — Preview
// ---------------------------------------------------------------------------

func TestCovRecPreview_Happy(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	slug := covRecRealSlug(t)

	req := covRecInstallReq(t, http.MethodGet, slug, "", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Preview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Preview: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var out previewRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode Preview: %v", err)
	}
	// Fresh workspace: every credential is needed, none exist, slug free.
	if len(out.NeededCredentials) == 0 {
		t.Error("expected needed credentials on a fresh workspace")
	}
	if !out.CrewSlugAvailable {
		t.Error("expected crew slug available on a fresh workspace")
	}
	if out.ResolvedCrewSlug == "" {
		t.Error("expected a resolved crew slug")
	}
}

func TestCovRecPreview_ExistingCredentialReused(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rec0 := recipes.All()[0]
	envVar := rec0.Credentials[0].EnvVarName

	// Pre-seed one of the recipe's credentials so Preview marks it existing.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, scope, type, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, '', 'WORKSPACE', 'API_KEY', 'ACTIVE', ?, '2026-01-01', '2026-01-01')`,
		"cred-existing", wsID, envVar, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	req := covRecInstallReq(t, http.MethodGet, rec0.Slug, "", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Preview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Preview: got %d, want 200", rec.Code)
	}
	var out previewRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.ExistingCredentials[envVar] {
		t.Errorf("expected %q to be marked existing", envVar)
	}
	for _, n := range out.NeededCredentials {
		if n == envVar {
			t.Errorf("%q should not be in needed list", envVar)
		}
	}
}

func TestCovRecPreview_SlugSuffixWhenTaken(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rec0 := recipes.All()[0]
	// Occupy the recipe's preferred crew slug.
	seedCrewRow(t, db, "crew-taken", wsID, rec0.Name, rec0.CrewSlug)

	req := covRecInstallReq(t, http.MethodGet, rec0.Slug, "", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Preview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Preview: got %d, want 200", rec.Code)
	}
	var out previewRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CrewSlugAvailable {
		t.Error("expected slug unavailable when base is taken")
	}
	if out.ResolvedCrewSlug == rec0.CrewSlug {
		t.Errorf("expected a suffixed slug, got base %q", out.ResolvedCrewSlug)
	}
}

func TestCovRecPreview_NotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := covRecInstallReq(t, http.MethodGet, "no-such-recipe", "", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Preview(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Preview unknown slug: got %d, want 404", rec.Code)
	}
}

func TestCovRecPreview_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	slug := covRecRealSlug(t)

	// Fault injection: drop the table the credential lookup reads from.
	if _, err := db.Exec(`DROP TABLE credentials`); err != nil {
		t.Fatalf("drop credentials: %v", err)
	}

	req := covRecInstallReq(t, http.MethodGet, slug, "", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Preview(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Preview with broken DB: got %d, want 500", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// recipes.go — Install
// ---------------------------------------------------------------------------

// covRecInstallBody builds a JSON install body that supplies a value for every
// credential the given recipe declares.
func covRecInstallBody(t *testing.T, rec recipes.Recipe) string {
	t.Helper()
	vals := map[string]string{}
	for _, c := range rec.Credentials {
		vals[c.EnvVarName] = "secret-" + c.EnvVarName
	}
	b, err := json.Marshal(installRecipeRequest{CredentialValues: vals})
	if err != nil {
		t.Fatalf("marshal install body: %v", err)
	}
	return string(b)
}

func TestCovRecInstall_Happy(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rec0 := recipes.All()[0]
	body := covRecInstallBody(t, rec0)

	req := covRecInstallReq(t, http.MethodPost, rec0.Slug, body, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("Install: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var out installRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode Install: %v", err)
	}
	if out.CrewID == "" || out.CrewSlug != rec0.CrewSlug {
		t.Errorf("unexpected crew identity: id=%q slug=%q (want slug %q)", out.CrewID, out.CrewSlug, rec0.CrewSlug)
	}
	if len(out.CredentialsAdded) != len(rec0.Credentials) {
		t.Errorf("CredentialsAdded=%d, want %d", len(out.CredentialsAdded), len(rec0.Credentials))
	}
	if len(out.MCPServersAdded) != len(rec0.MCPServers) {
		t.Errorf("MCPServersAdded=%d, want %d", len(out.MCPServersAdded), len(rec0.MCPServers))
	}

	// Assert DB rows actually landed.
	var crewCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE id = ?`, out.CrewID).Scan(&crewCount); err != nil {
		t.Fatalf("query crew: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew rows = %d, want 1", crewCount)
	}
	var credCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ?`, wsID).Scan(&credCount); err != nil {
		t.Fatalf("query credentials: %v", err)
	}
	if credCount != len(rec0.Credentials) {
		t.Errorf("credential rows = %d, want %d", credCount, len(rec0.Credentials))
	}
	var mcpCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = ?`, out.CrewID).Scan(&mcpCount); err != nil {
		t.Fatalf("query mcp servers: %v", err)
	}
	if mcpCount != len(rec0.MCPServers) {
		t.Errorf("mcp rows = %d, want %d", mcpCount, len(rec0.MCPServers))
	}
}

func TestCovRecInstall_ReuseExistingCredential(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rec0 := recipes.All()[0]
	reusedEnv := rec0.Credentials[0].EnvVarName

	// Pre-seed the first credential; the install body omits it, so it must be
	// reused rather than re-inserted.
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, scope, type, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, '', 'WORKSPACE', 'API_KEY', 'ACTIVE', ?, '2026-01-01', '2026-01-01')`,
		"cred-reuse", wsID, reusedEnv, userID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	vals := map[string]string{}
	for _, c := range rec0.Credentials[1:] {
		vals[c.EnvVarName] = "secret-" + c.EnvVarName
	}
	body, _ := json.Marshal(installRecipeRequest{CredentialValues: vals})

	req := covRecInstallReq(t, http.MethodPost, rec0.Slug, string(body), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("Install: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var out installRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, e := range out.CredentialsReused {
		if e == reusedEnv {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in CredentialsReused, got %v", reusedEnv, out.CredentialsReused)
	}
}

func TestCovRecInstall_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	slug := covRecRealSlug(t)

	// MEMBER lacks "manage".
	req := covRecInstallReq(t, http.MethodPost, slug, "{}", userID, wsID, "MEMBER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Install as MEMBER: got %d, want 403", rec.Code)
	}
}

func TestCovRecInstall_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	slug := covRecRealSlug(t)

	// Has the role but no user in context.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/recipes/"+slug+"/install", strings.NewReader("{}"))
	req.SetPathValue("slug", slug)
	req = req.WithContext(withWorkspace(req.Context(), "ws-x", "OWNER"))
	rec := httptest.NewRecorder()
	h.Install(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Install without user: got %d, want 401", rec.Code)
	}
}

func TestCovRecInstall_NotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := covRecInstallReq(t, http.MethodPost, "no-such-recipe", "{}", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Install unknown slug: got %d, want 404", rec.Code)
	}
}

func TestCovRecInstall_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	slug := covRecRealSlug(t)

	req := covRecInstallReq(t, http.MethodPost, slug, "{not json", userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Install bad JSON: got %d, want 400", rec.Code)
	}
}

func TestCovRecInstall_MissingCredentialValues(t *testing.T) {
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	slug := covRecRealSlug(t)

	// Empty credential_values — none of the recipe's credentials are
	// supplied and none exist, so all are "missing" → 400.
	req := covRecInstallReq(t, http.MethodPost, slug, `{"credential_values":{}}`, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Install missing creds: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["missing_credentials"]; !ok {
		t.Errorf("expected missing_credentials key, got %v", out)
	}
}

func TestCovRecInstall_DBError(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	h := NewRecipeHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	rec0 := recipes.All()[0]
	body := covRecInstallBody(t, rec0)

	// Fault injection: drop crews so the crew INSERT inside the tx errors
	// with something other than a UNIQUE collision → 500.
	if _, err := db.Exec(`DROP TABLE crews`); err != nil {
		t.Fatalf("drop crews: %v", err)
	}

	req := covRecInstallReq(t, http.MethodPost, rec0.Slug, body, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Install(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Install with broken DB: got %d, want 500", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// message_feedback.go — Create
// ---------------------------------------------------------------------------

func covRecFeedbackReq(t *testing.T, body, userID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", strings.NewReader(body))
	return req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"}))
}

func TestCovRecFeedbackCreate_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feedback", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Create no user: got %d, want 401", rec.Code)
	}
}

func TestCovRecFeedbackCreate_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, "{bad", userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Create bad JSON: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackCreate_MissingMessageID(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, `{"signal":"helpful"}`, userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Create missing message_id: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackCreate_BadSignal(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, `{"message_id":"m1","signal":"bogus"}`, userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Create bad signal: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackCreate_IDTooLong(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	long := strings.Repeat("x", maxFeedbackIDChars+1)
	body := `{"message_id":"` + long + `","signal":"helpful"}`
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, body, userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Create oversized id: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackCreate_ReasonTooLong(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	reason := strings.Repeat("y", maxFeedbackReasonChars+1)
	body := `{"message_id":"m1","signal":"helpful","reason":"` + reason + `"}`
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, body, userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Create oversized reason: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackCreate_NoWorkspaceMembership(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	// User exists but has no workspace membership → 403 on fallback path.
	userID := seedTestUser(t, db)
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, `{"message_id":"m1","signal":"helpful"}`, userID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Create no membership: got %d, want 403 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestCovRecFeedbackCreate_ChatNotFound(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	body := `{"message_id":"m1","signal":"helpful","chat_id":"ghost"}`
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, body, userID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Create unknown chat: got %d, want 404", rec.Code)
	}
}

func TestCovRecFeedbackCreate_HappyWorkspaceFallback(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	body := `{"message_id":"msg-1","signal":"helpful","reason":"nice"}`
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, body, userID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create happy: got %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["id"] == "" {
		t.Error("expected an id in the response")
	}

	// Row landed in the fallback workspace.
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM message_feedback WHERE message_id = 'msg-1' AND user_id = ? AND workspace_id = ?`,
		userID, wsID).Scan(&count); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if count != 1 {
		t.Errorf("feedback rows = %d, want 1", count)
	}
}

func TestCovRecFeedbackCreate_Upsert(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	first := `{"message_id":"msg-up","signal":"helpful","reason":"first"}`
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, first, userID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first Create: got %d, want 201", rec.Code)
	}

	// Same (message_id, user, signal) with a new reason — UPSERT, no new row.
	second := `{"message_id":"msg-up","signal":"helpful","reason":"second"}`
	rec2 := httptest.NewRecorder()
	h.Create(rec2, covRecFeedbackReq(t, second, userID))
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second Create: got %d, want 201", rec2.Code)
	}

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM message_feedback WHERE message_id = 'msg-up' AND user_id = ?`, userID).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("rows after upsert = %d, want 1", count)
	}
	var reason string
	if err := db.QueryRow(
		`SELECT reason FROM message_feedback WHERE message_id = 'msg-up' AND user_id = ?`, userID).Scan(&reason); err != nil {
		t.Fatalf("query reason: %v", err)
	}
	if reason != "second" {
		t.Errorf("reason = %q, want updated to 'second'", reason)
	}
}

func TestCovRecFeedbackCreate_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	// Fault injection: drop the insert target table → 500.
	if _, err := db.Exec(`DROP TABLE message_feedback`); err != nil {
		t.Fatalf("drop message_feedback: %v", err)
	}
	body := `{"message_id":"m1","signal":"helpful"}`
	rec := httptest.NewRecorder()
	h.Create(rec, covRecFeedbackReq(t, body, userID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Create with broken DB: got %d, want 500", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// message_feedback.go — List
// ---------------------------------------------------------------------------

func covRecListReq(t *testing.T, query, userID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback?"+query, nil)
	return req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"}))
}

func TestCovRecFeedbackList_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feedback?message_id=m1", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("List no user: got %d, want 401", rec.Code)
	}
}

func TestCovRecFeedbackList_MissingFilter(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	rec := httptest.NewRecorder()
	h.List(rec, covRecListReq(t, "", userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("List no filter: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackList_IDTooLong(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	long := strings.Repeat("z", maxFeedbackIDChars+1)
	rec := httptest.NewRecorder()
	h.List(rec, covRecListReq(t, "message_id="+long, userID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("List oversized id: got %d, want 400", rec.Code)
	}
}

func TestCovRecFeedbackList_HappyByMessageAndTrace(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Seed two feedback rows for the same message with a trace_id.
	if _, err := db.Exec(`
		INSERT INTO message_feedback (id, workspace_id, message_id, trace_id, signal, user_id)
		VALUES ('f1', ?, 'msg-list', 'trace-1', 'helpful', ?)`, wsID, userID); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	t.Run("by_message", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covRecListReq(t, "message_id=msg-list", userID))
		if rec.Code != http.StatusOK {
			t.Fatalf("List by message: got %d, want 200", rec.Code)
		}
		var out struct {
			Feedback []feedbackRow `json:"feedback"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Feedback) != 1 {
			t.Fatalf("got %d rows, want 1", len(out.Feedback))
		}
		if out.Feedback[0].MessageID != "msg-list" {
			t.Errorf("message_id = %q", out.Feedback[0].MessageID)
		}
	})

	t.Run("by_trace", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.List(rec, covRecListReq(t, "trace_id=trace-1", userID))
		if rec.Code != http.StatusOK {
			t.Fatalf("List by trace: got %d, want 200", rec.Code)
		}
		var out struct {
			Feedback []feedbackRow `json:"feedback"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out.Feedback) != 1 {
			t.Fatalf("got %d rows, want 1", len(out.Feedback))
		}
	})
}

func TestCovRecFeedbackList_DBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewMessageFeedbackHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(`DROP TABLE message_feedback`); err != nil {
		t.Fatalf("drop message_feedback: %v", err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, covRecListReq(t, "message_id=m1", userID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("List with broken DB: got %d, want 500", rec.Code)
	}
}
