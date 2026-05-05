package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/recipes"
)

func newRecipeHandler(t *testing.T) *RecipeHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewRecipeHandler(setupTestDB(t), logger)
}

// TestRecipes_List_Static validates the curated catalogue ships with
// the expected slugs (regression guard for accidental removal).
func TestRecipes_List_Static(t *testing.T) {
	t.Parallel()
	h := newRecipeHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/recipes", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got []recipes.Recipe
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]bool{"code-review-crew": false, "triage-crew": false, "research-crew": false}
	for _, r := range got {
		if _, ok := want[r.Slug]; ok {
			want[r.Slug] = true
		}
	}
	for slug, found := range want {
		if !found {
			t.Errorf("recipe %q missing from List", slug)
		}
	}
}

// TestRecipes_InstallSuffixedSlug exercises the happy path where a
// pre-existing crew with the same slug forces the slug-collision
// retry loop to suffix `-2`. Verifies install succeeds and DB state
// matches the request.
func TestRecipes_InstallSuffixedSlug(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('preexisting-crew', ?, 'Existing', 'code-review')`, wsID); err != nil {
		t.Fatalf("preseed crew: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewRecipeHandler(db, logger)

	body, _ := json.Marshal(installRecipeRequest{
		CredentialValues: map[string]string{
			"ANTHROPIC_API_KEY": "sk-ant-test",
			"GH_TOKEN":          "ghp_test",
		},
		AccountLabels: map[string]string{
			"ANTHROPIC_API_KEY": "Recipe install test",
		},
	})
	req := httptest.NewRequest("POST", "/api/v1/recipes/code-review-crew/install", bytes.NewReader(body))
	req.SetPathValue("slug", "code-review-crew")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Install(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got installRecipeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal install response: %v", err)
	}
	if got.CrewSlug != "code-review-2" {
		t.Errorf("crew slug = %q, want code-review-2", got.CrewSlug)
	}
	if len(got.CredentialsAdded) != 2 {
		t.Errorf("credentials added = %v, want 2", got.CredentialsAdded)
	}
	if len(got.MCPServersAdded) != 1 {
		t.Errorf("mcp servers added = %v, want 1 (github)", got.MCPServersAdded)
	}

	var label, provider string
	if err := db.QueryRow(`SELECT account_label, provider FROM credentials WHERE name = 'ANTHROPIC_API_KEY' AND workspace_id = ?`, wsID).Scan(&label, &provider); err != nil {
		t.Fatalf("read anthropic credential: %v", err)
	}
	if label != "Recipe install test" {
		t.Errorf("account_label = %q, want %q", label, "Recipe install test")
	}
	if provider != "ANTHROPIC" {
		t.Errorf("provider = %q, want ANTHROPIC", provider)
	}
}

// TestRecipes_InstallAtomicRollback is the central regression for the
// "all or nothing" promise. We force a deterministic failure inside
// the install transaction by violating the created_by FK on
// credentials (we drop the user row immediately before the install
// attempt). The handler MUST roll back: no crew, no credentials, no
// MCP servers must remain after the failed call.
func TestRecipes_InstallAtomicRollback(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// SQLite needs PRAGMA foreign_keys=ON to enforce FKs at runtime.
	// setupTestDB enables this; assert so the test is self-validating.
	var fkOn int
	_ = db.QueryRow("PRAGMA foreign_keys").Scan(&fkOn)
	if fkOn != 1 {
		t.Skip("FK enforcement disabled — atomic rollback test requires PRAGMA foreign_keys=ON")
	}

	// Snapshot pre-state.
	preCredCount := countRows(t, db, "credentials", wsID)
	preCrewCount := countRows(t, db, "crews", wsID)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewRecipeHandler(db, logger)

	// Body looks valid — but the failure will come from removing the
	// creating user row via DELETE before install runs.
	body, _ := json.Marshal(installRecipeRequest{
		CredentialValues: map[string]string{
			"ANTHROPIC_API_KEY": "sk-ant-fk-test",
			"GH_TOKEN":          "ghp_fk_test",
		},
	})

	// Use an AuthUser whose ID does NOT exist in the users table.
	// credentials.created_by REFERENCES users(id) — the INSERT will
	// fail with a FK constraint violation inside the tx.
	ghostUserID := "ghost-user-" + userID

	req := httptest.NewRequest("POST", "/api/v1/recipes/code-review-crew/install", bytes.NewReader(body))
	req.SetPathValue("slug", "code-review-crew")
	ctx := withUser(req.Context(), &AuthUser{ID: ghostUserID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Install(rr, req)

	if rr.Code == http.StatusCreated {
		t.Fatalf("install unexpectedly succeeded with non-existent user; body=%s", rr.Body.String())
	}
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}

	// Atomicity assertion: every counter must equal its pre-state.
	if got := countRows(t, db, "credentials", wsID); got != preCredCount {
		t.Errorf("credentials leaked: pre=%d post=%d", preCredCount, got)
	}
	if got := countRows(t, db, "crews", wsID); got != preCrewCount {
		t.Errorf("crews leaked: pre=%d post=%d", preCrewCount, got)
	}
	// crew_mcp_servers join through crews — if no crew leaked, no
	// orphaned MCP can either, but assert directly to be safe.
	var mcpCount int
	_ = db.QueryRow(`
		SELECT COUNT(*) FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id
		WHERE c.workspace_id = ?`, wsID).Scan(&mcpCount)
	if mcpCount != 0 {
		t.Errorf("mcp servers leaked: %d", mcpCount)
	}
}

func countRows(t *testing.T, db *sql.DB, table, wsID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestRecipes_InstallReusesExisting verifies that an env_var_name
// already present in the workspace is reused, not duplicated. This
// is the path where a user installs Recipe A and then Recipe B that
// shares ANTHROPIC_API_KEY — they shouldn't have to paste the key
// twice.
func TestRecipes_InstallReusesExisting(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Pre-seed an Anthropic credential. The recipe install must
	// reuse it (not insert a second row that would 409 on UNIQUE).
	seedCredentialEnc(t, db, wsID, userID, "preexisting-anthropic", "ANTHROPIC_API_KEY", "old-value")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewRecipeHandler(db, logger)

	// research-crew needs ANTHROPIC_API_KEY (pre-seeded → reuse) plus
	// BRAVE_API_KEY (must be supplied → added). Verifies the
	// reuse-when-already-present path doesn't UNIQUE-collide on the
	// shared credential.
	body, _ := json.Marshal(installRecipeRequest{
		CredentialValues: map[string]string{"BRAVE_API_KEY": "BSA-test-fake"},
	})
	req := httptest.NewRequest("POST", "/api/v1/recipes/research-crew/install", bytes.NewReader(body))
	req.SetPathValue("slug", "research-crew")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Install(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got installRecipeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !sliceContains(got.CredentialsReused, "ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY not reused: reused=%v added=%v", got.CredentialsReused, got.CredentialsAdded)
	}
	if !sliceContains(got.CredentialsAdded, "BRAVE_API_KEY") {
		t.Errorf("BRAVE_API_KEY not added: added=%v reused=%v", got.CredentialsAdded, got.CredentialsReused)
	}

	// And exactly one ANTHROPIC_API_KEY credential exists in the workspace.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE name = 'ANTHROPIC_API_KEY' AND workspace_id = ?`, wsID).Scan(&n)
	if n != 1 {
		t.Errorf("anthropic credential count = %d, want 1 (no duplicate)", n)
	}
}

// TestRecipes_InstallMissingCredentials returns 400 with the list of
// missing env var names so the FE can prompt the user.
func TestRecipes_InstallMissingCredentials(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewRecipeHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	// code-review-crew needs ANTHROPIC_API_KEY + GH_TOKEN.
	// Provide only one.
	body, _ := json.Marshal(installRecipeRequest{
		CredentialValues: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"},
	})
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	req.SetPathValue("slug", "code-review-crew")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Install(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "GH_TOKEN") {
		t.Errorf("error body should list GH_TOKEN as missing: %s", rr.Body.String())
	}

	// And no partial state — no crew, no credentials.
	var crewN, credN int
	_ = db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&crewN)
	_ = db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ?`, wsID).Scan(&credN)
	if crewN != 0 || credN != 0 {
		t.Errorf("partial state after rejected install: crews=%d, creds=%d", crewN, credN)
	}
}

// TestRecipes_Preview returns the dry-run summary used by the install
// Sheet to skip already-supplied credentials.
func TestRecipes_Preview(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "have-anthropic", "ANTHROPIC_API_KEY", "v")

	h := NewRecipeHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	req := httptest.NewRequest("GET", "/api/v1/recipes/code-review-crew/preview", nil)
	req.SetPathValue("slug", "code-review-crew")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Preview(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got previewRecipeResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if !got.ExistingCredentials["ANTHROPIC_API_KEY"] {
		t.Error("preview should mark ANTHROPIC_API_KEY as existing")
	}
	wantNeeded := map[string]bool{"GH_TOKEN": true}
	for _, n := range got.NeededCredentials {
		delete(wantNeeded, n)
	}
	if len(wantNeeded) != 0 {
		t.Errorf("preview missing needed entries: %v", wantNeeded)
	}
	if !got.CrewSlugAvailable || got.ResolvedCrewSlug != "code-review" {
		t.Errorf("crew slug resolution: avail=%v slug=%q", got.CrewSlugAvailable, got.ResolvedCrewSlug)
	}
}
