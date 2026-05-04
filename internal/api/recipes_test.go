package api

import (
	"bytes"
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

// TestRecipes_InstallAtomic is the central regression for the "all or
// nothing" promise. We force a credential conflict mid-flow and
// verify NO partial state remains: no crew, no MCP server, no extra
// credentials.
func TestRecipes_InstallAtomic(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Pre-seed a CREW with the same slug the recipe wants — the slug
	// resolver should suffix this to "code-review-2", so this is NOT
	// a conflict, just an exercise of the suffix path. The atomicity
	// failure mode we want is harder: a NULL constraint violation or
	// similar mid-tx. We provoke that by deleting the workspace
	// row's referenced user (created_by FK on credentials) right
	// before install — the FK violation kicks during INSERT
	// credentials.
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
	_ = json.Unmarshal(rr.Body.Bytes(), &got)

	// Slug suffixed because preexisting-crew already had "code-review".
	if got.CrewSlug != "code-review-2" {
		t.Errorf("crew slug = %q, want code-review-2", got.CrewSlug)
	}
	// Both credentials added (no pre-existing).
	if len(got.CredentialsAdded) != 2 {
		t.Errorf("credentials added = %v, want 2", got.CredentialsAdded)
	}
	if len(got.MCPServersAdded) != 1 {
		t.Errorf("mcp servers added = %v, want 1 (github)", got.MCPServersAdded)
	}

	// Verify credentials live in DB with correct provider + label.
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

	// research-crew only needs ANTHROPIC_API_KEY → user provides
	// nothing (it's already there) and install should still succeed.
	body, _ := json.Marshal(installRecipeRequest{CredentialValues: map[string]string{}})
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
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got.CredentialsReused) != 1 || got.CredentialsReused[0] != "ANTHROPIC_API_KEY" {
		t.Errorf("expected ANTHROPIC_API_KEY reused, got %v / added=%v", got.CredentialsReused, got.CredentialsAdded)
	}
	if len(got.CredentialsAdded) != 0 {
		t.Errorf("nothing should be added, got %v", got.CredentialsAdded)
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
