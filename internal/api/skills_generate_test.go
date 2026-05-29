package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// skills_generate.go — POST /api/v1/workspaces/{wsID}/skills/generate
//
// The happy path requires a real LLM call against the Anthropic Messages API
// and is therefore out of scope for hermetic tests. These cover the pre-LLM
// gates plus the helper functions:
//
//   - Generate: role gate, missing/invalid body, missing credential
//   - resolveAnthropicProvider: lookup contract (filters by ACTIVE, by type,
//     by workspace, by soft-delete)
//   - generateGeneratedSkillID: shape + uniqueness contract
//   - nullableString / nullableStrIfc / firstNonEmpty / truncateForError:
//     small pure helpers used by the insert path
// ---------------------------------------------------------------------------

func newSkillGenHandler(t *testing.T) *SkillGenerateHandler {
	t.Helper()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	return NewSkillGenerateHandler(db, newTestLogger())
}

func TestSkillGenerate_RoleForbidden(t *testing.T) {
	h := newSkillGenHandler(t)
	ownerID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, ownerID)

	// Seed a DISTINCT VIEWER user with chat-only capabilities — the
	// layered capability gate (PRD-SLASH-CAPABILITIES-2026 §6) does a
	// DB lookup, so using the OWNER's id with a faked VIEWER ctx would
	// grant via the capability path. We want both role + capability
	// dimensions to deny.
	viewerID := "viewer-skill-deny"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'V')`,
		viewerID, viewerID+"@x"); err != nil {
		t.Fatalf("seed viewer user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, 'VIEWER', '["chat"]')`,
		"m-"+viewerID, wsID, viewerID); err != nil {
		t.Fatalf("seed viewer membership: %v", err)
	}
	InvalidateCapabilityCache(wsID, viewerID)

	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/generate",
		strings.NewReader(`{"slug":"x","prompt":"y"}`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, viewerID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("VIEWER code = %d, want 403", rr.Code)
	}
}

func TestSkillGenerate_MissingWorkspaceID(t *testing.T) {
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("POST", "/api/v1/workspaces//skills/generate",
		strings.NewReader(`{"slug":"x","prompt":"y"}`))
	// Intentionally do not call SetPathValue("workspaceId", ...) — the
	// handler reads r.PathValue and must 400 when it's empty even though
	// the role context is OWNER (we need to pass the role gate first).
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing workspaceId code = %d, want 400", rr.Code)
	}
}

func TestSkillGenerate_InvalidJSON_400(t *testing.T) {
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`not-json`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON code = %d, want 400", rr.Code)
	}
}

func TestSkillGenerate_MissingSlugOrPrompt_400(t *testing.T) {
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	cases := []struct{ name, body string }{
		{"missing-slug", `{"prompt":"do thing"}`},
		{"missing-prompt", `{"slug":"x"}`},
		{"both-whitespace", `{"slug":"   ","prompt":"   "}`},
		{"both-empty", `{"slug":"","prompt":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
			req.SetPathValue("workspaceId", wsID)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Generate(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: code = %d, want 400", tc.name, rr.Code)
			}
		})
	}
}

func TestSkillGenerate_NoAnthropicCredential_412(t *testing.T) {
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"slug":"my-skill","prompt":"do thing"}`))
	req.SetPathValue("workspaceId", wsID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Generate(rr, req)
	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("no-credential code = %d, want 412 body=%s", rr.Code, rr.Body.String())
	}
	// Body must include the actionable hint about API_KEY vs AI_CLI_TOKEN —
	// that's the whole point of the precondition-failed status here.
	body := rr.Body.String()
	if !strings.Contains(body, "Anthropic API key") {
		t.Errorf("response body should mention 'Anthropic API key', got %s", body)
	}
	if !strings.Contains(body, "AI_CLI_TOKEN") {
		t.Errorf("response body should call out the AI_CLI_TOKEN pitfall, got %s", body)
	}
}

// ---- resolveAnthropicProvider ----

func TestResolveAnthropicProvider_NoCredential_ReturnsSentinel(t *testing.T) {
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	_, err := h.resolveAnthropicProvider(t.Context(), wsID)
	if !errors.Is(err, errNoActiveAnthropicCredential) {
		t.Errorf("err = %v, want errNoActiveAnthropicCredential", err)
	}
}

func TestResolveAnthropicProvider_FiltersStrictly(t *testing.T) {
	// Seed a variety of credentials; only ONE should match the predicate
	// (provider=ANTHROPIC AND type=API_KEY AND status=ACTIVE AND
	// deleted_at IS NULL AND workspace_id = wsID).
	h := newSkillGenHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	otherWS := "ws-other-skillgen"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o-skill')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}

	// Wrong provider — skipped.
	seedCredentialEnc(t, h.db, wsID, userID, "c-openai", "OPENAI_KEY", "ignored")
	if _, err := h.db.Exec(`UPDATE credentials SET provider = 'OPENAI' WHERE id = 'c-openai'`); err != nil {
		t.Fatalf("twist provider: %v", err)
	}
	// Wrong type — skipped (AI_CLI_TOKEN is the documented pitfall).
	seedCredentialEnc(t, h.db, wsID, userID, "c-oauth", "ANTHROPIC_OAUTH", "oauth-bearer")
	if _, err := h.db.Exec(`UPDATE credentials SET provider = 'ANTHROPIC', type = 'AI_CLI_TOKEN' WHERE id = 'c-oauth'`); err != nil {
		t.Fatalf("twist type: %v", err)
	}
	// Inactive — skipped.
	seedCredentialEnc(t, h.db, wsID, userID, "c-inactive", "ANTHROPIC_OLD", "old-key")
	if _, err := h.db.Exec(`UPDATE credentials SET provider = 'ANTHROPIC', status = 'INACTIVE' WHERE id = 'c-inactive'`); err != nil {
		t.Fatalf("twist status: %v", err)
	}
	// Soft-deleted — skipped.
	seedCredentialEnc(t, h.db, wsID, userID, "c-deleted", "ANTHROPIC_DEL", "del-key")
	if _, err := h.db.Exec(`UPDATE credentials SET provider = 'ANTHROPIC', deleted_at = datetime('now') WHERE id = 'c-deleted'`); err != nil {
		t.Fatalf("twist deleted: %v", err)
	}
	// Cross-workspace — skipped.
	seedCredentialEnc(t, h.db, otherWS, userID, "c-foreign", "ANTHROPIC_FOREIGN", "foreign-key")
	if _, err := h.db.Exec(`UPDATE credentials SET provider = 'ANTHROPIC' WHERE id = 'c-foreign'`); err != nil {
		t.Fatalf("twist foreign: %v", err)
	}

	// Nothing eligible — must fail with the sentinel even though several
	// nearly-matching rows exist.
	if _, err := h.resolveAnthropicProvider(t.Context(), wsID); !errors.Is(err, errNoActiveAnthropicCredential) {
		t.Fatalf("with no eligible cred err = %v, want sentinel", err)
	}

	// Now add the matching cred — must succeed.
	seedCredentialEnc(t, h.db, wsID, userID, "c-good", "ANTHROPIC_GOOD", "sk-ant-good")
	if _, err := h.db.Exec(`UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'c-good'`); err != nil {
		t.Fatalf("twist good: %v", err)
	}

	provider, err := h.resolveAnthropicProvider(t.Context(), wsID)
	if err != nil {
		t.Fatalf("with eligible cred err = %v, want nil", err)
	}
	if provider == nil {
		t.Error("provider = nil, want non-nil")
	}
}

// ---- generateGeneratedSkillID ----

func TestGenerateGeneratedSkillID_ShapeAndUniqueness(t *testing.T) {
	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		id := generateGeneratedSkillID()
		if !strings.HasPrefix(id, "sk_") {
			t.Fatalf("id = %q, want sk_-prefixed", id)
		}
		// "sk_" + 12 bytes hex = 3 + 24 = 27 chars
		if len(id) != 27 {
			t.Fatalf("id = %q (len %d), want length 27", id, len(id))
		}
		if seen[id] {
			t.Fatalf("collision after %d generations: %s", i, id)
		}
		seen[id] = true
	}
}

// ---- nullableString / nullableStrIfc / firstNonEmpty / truncateForError ----

func TestNullableString(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"nil", nil, ""},
		{"empty-string", "", ""},
		{"non-string", 42, ""},
		{"string", "hello", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nullableString(tc.in); got != tc.want {
				t.Errorf("nullableString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNullableStrIfc(t *testing.T) {
	if got := nullableStrIfc(""); got != nil {
		t.Errorf("empty → %v, want nil (so DB stores NULL not '')", got)
	}
	if got := nullableStrIfc("x"); got != "x" {
		t.Errorf("non-empty → %v, want \"x\"", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"x", "y", "x"},
		{"", "y", "y"},
		{"", "", ""},
		{"x", "", "x"},
	}
	for _, tc := range cases {
		if got := firstNonEmpty(tc.a, tc.b); got != tc.want {
			t.Errorf("firstNonEmpty(%q,%q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestTruncateForError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"under-limit", "hi", 10, "hi"},
		{"exact-limit", "hello", 5, "hello"},
		{"over-limit", "hello world", 5, "hello…"},
		{"empty", "", 5, ""},
		{"zero-limit-with-content", "abc", 0, "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateForError(tc.in, tc.n); got != tc.want {
				t.Errorf("truncateForError(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}
