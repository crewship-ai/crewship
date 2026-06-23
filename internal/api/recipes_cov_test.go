package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recipes_cov_test.go covers the failure branches of the recipe
// install flow: Preview/Install DB errors (closed DB or renamed
// tables), encryption failures, write failures forced via RAISE
// triggers (including the disguised-UNIQUE exhaustion path), plus the
// resolveCrewSlug and nullableJSON helpers. Uses the built-in
// "code-review-crew" recipe (2 credentials + 1 MCP server). Helpers
// are prefixed covRec.

const covRecSlug = "code-review-crew"

func covRecHandler(t *testing.T) (*RecipeHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewRecipeHandler(db, newTestLogger()), userID, wsID
}

func covRecPreview(h *RecipeHandler, userID, wsID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/recipes/"+covRecSlug+"/preview", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("slug", covRecSlug)
	rr := httptest.NewRecorder()
	h.Preview(rr, req)
	return rr
}

func covRecInstall(h *RecipeHandler, userID, wsID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/recipes/"+covRecSlug+"/install", jsonBody(map[string]any{
			"credential_values": map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
				"GH_TOKEN":          "ghp_test",
			},
		})),
		userID, wsID, "OWNER")
	req.SetPathValue("slug", covRecSlug)
	rr := httptest.NewRecorder()
	h.Install(rr, req)
	return rr
}

func TestCovRec_Preview_CredentialLookupDBError_500(t *testing.T) {
	h, userID, wsID := covRecHandler(t)
	h.db.Close()
	rr := covRecPreview(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRec_Preview_ResolveSlugDBError_500(t *testing.T) {
	h, userID, wsID := covRecHandler(t)
	// credentials lookups still work; only the crews slug probe fails.
	execOrFatal(t, h.db, `ALTER TABLE crews RENAME TO crews_broken`)
	rr := covRecPreview(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRec_Install_PreloadDBError_500(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID := covRecHandler(t)
	h.db.Close()
	rr := covRecInstall(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRec_Install_EncryptFailure_500(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "not-a-valid-hex-key")
	h, userID, wsID := covRecHandler(t)
	rr := covRecInstall(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Failed to encrypt credential") {
		t.Errorf("body = %s, want encrypt failure", rr.Body.String())
	}
}

func TestCovRec_Install_CredentialInsertFailure_500(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID := covRecHandler(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covrec_block_cred BEFORE INSERT ON credentials
		BEGIN SELECT RAISE(ABORT, 'covrec forced'); END`)
	rr := covRecInstall(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// Nothing must have leaked out of the rolled-back tx.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&n); err != nil || n != 0 {
		t.Errorf("crews after failed install = %d err=%v, want 0", n, err)
	}
}

func TestCovRec_Install_CrewInsertHardFailure_500(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID := covRecHandler(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covrec_block_crew BEFORE INSERT ON crews
		BEGIN SELECT RAISE(ABORT, 'covrec disk exploded'); END`)
	rr := covRecInstall(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovRec_Install_SlugRetriesExhausted_500 — every crew INSERT
// fails with a message that LOOKS like a UNIQUE collision, so the
// retry loop runs out of suffixes and reports the allocation failure.
func TestCovRec_Install_SlugRetriesExhausted_500(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID := covRecHandler(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covrec_fake_unique BEFORE INSERT ON crews
		BEGIN SELECT RAISE(ABORT, 'UNIQUE constraint failed: crews.slug'); END`)
	rr := covRecInstall(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Could not allocate crew slug") {
		t.Errorf("body = %s, want slug allocation failure", rr.Body.String())
	}
}

func TestCovRec_Install_MCPServerInsertFailure_500(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID := covRecHandler(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covrec_block_mcp BEFORE INSERT ON crew_mcp_servers
		BEGIN SELECT RAISE(ABORT, 'covrec forced'); END`)
	rr := covRecInstall(h, userID, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRec_ResolveCrewSlug_FreeTakenAndExhausted(t *testing.T) {
	h, _, wsID := covRecHandler(t)
	ctx := context.Background()

	// Free slug: returned untouched, flagged available.
	got, avail, err := resolveCrewSlug(ctx, h.db, wsID, "covrec-base")
	if err != nil || got != "covrec-base" || !avail {
		t.Fatalf("free slug = (%q, %v, %v), want (covrec-base, true, nil)", got, avail, err)
	}

	// Taken base: suffixes to -2.
	seedCrewRow(t, h.db, "covrec-c0", wsID, "C0", "covrec-base")
	got, avail, err = resolveCrewSlug(ctx, h.db, wsID, "covrec-base")
	if err != nil || got != "covrec-base-2" || avail {
		t.Fatalf("taken slug = (%q, %v, %v), want (covrec-base-2, false, nil)", got, avail, err)
	}

	// Exhausted: every candidate up to -99 is taken.
	for i := 2; i < 100; i++ {
		seedCrewRow(t, h.db, fmt.Sprintf("covrec-c%d", i), wsID, "C", fmt.Sprintf("covrec-base-%d", i))
	}
	if _, _, err = resolveCrewSlug(ctx, h.db, wsID, "covrec-base"); err == nil {
		t.Fatalf("expected exhaustion error after 100 taken candidates")
	}
}

func TestCovRec_NullableJSON(t *testing.T) {
	cases := []struct{ raw, fallback, want string }{
		{"", "[]", "[]"},
		{"null", "{}", "{}"},
		{`{"a":1}`, "{}", `{"a":1}`},
	}
	for _, c := range cases {
		if got := nullableJSON([]byte(c.raw), c.fallback); got != c.want {
			t.Errorf("nullableJSON(%q, %q) = %q, want %q", c.raw, c.fallback, got, c.want)
		}
	}
}
