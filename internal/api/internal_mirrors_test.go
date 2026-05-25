package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutineAdapter_RejectsEmptyWorkspace asserts the workspace_id
// query param is mandatory. Without it the public CreateSchedule
// handler would explode on an empty workspace and the failure
// surface would be confusing (a 5xx from deep in the stack instead
// of a clear 400 at the boundary).
func TestRoutineAdapter_RejectsEmptyWorkspace(t *testing.T) {
	adapter := NewRoutineInternalAdapter(&PipelineHandler{})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	adapter.CreateSchedule(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for missing workspace_id", w.Code)
	}
}

// TestRoutineAdapter_NilHandlerReturns500 covers the nil-safe init
// path — registerInternalRoutes guards with `if pipes != nil` so
// in practice this branch only fires on a future construction bug,
// but the adapter must degrade gracefully.
func TestRoutineAdapter_NilHandlerReturns500(t *testing.T) {
	adapter := NewRoutineInternalAdapter(nil)
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-1", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	adapter.CreateSchedule(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500 for nil pipes", w.Code)
	}
}

// TestSkillAdapter_StampsPathValue verifies the adapter sets
// workspaceId as a path value before calling the public Generate
// handler — without that step the public handler reads
// r.PathValue("workspaceId") = "" and 400s.
//
// CodeRabbit nitpick: previously this test reproduced the adapter
// logic inline and asserted only the recorder default — it never
// touched SkillInternalAdapter.Generate, so an adapter regression
// would have slipped through. Now we call the adapter directly
// against a real (empty-state) SkillGenerateHandler whose downstream
// Generate will fail on the empty JSON body. The 400 response is
// the signal that adapter.Generate ran end-to-end AND propagated
// the workspaceId path value (without it the downstream would
// produce a different error).
func TestSkillAdapter_StampsPathValue(t *testing.T) {
	db := setupTestDB(t)
	gen := &SkillGenerateHandler{db: db, logger: nil} // logger fine to be nil — handler guards
	adapter := NewSkillInternalAdapter(gen)

	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-42", strings.NewReader(``))
	w := httptest.NewRecorder()
	adapter.Generate(w, r)

	// PathValue ws-42 was stamped by the adapter — downstream
	// SkillGenerateHandler.Generate now sees it via r.PathValue and
	// progresses past the workspace-required guard. The empty body
	// then fails JSON decode → 400. Without the SetPathValue, the
	// downstream would 400 with "workspace_id required" instead.
	if r.PathValue("workspaceId") != "ws-42" {
		t.Errorf("PathValue after adapter call = %q, want ws-42 (stamp dropped)", r.PathValue("workspaceId"))
	}
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 (adapter passed workspace, downstream rejected body)", w.Code)
	}
}

// TestSkillAdapter_RejectsEmptyWorkspace mirrors the routine variant
// — boundary 400 instead of deep-stack 500.
func TestSkillAdapter_RejectsEmptyWorkspace(t *testing.T) {
	adapter := NewSkillInternalAdapter(&SkillGenerateHandler{})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	adapter.Generate(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

// TestCredentialAdapter_RejectsMissingCallerUserID asserts the
// X-Caller-User-Id requirement that distinguishes this adapter
// from the routine + skill ones: credential mutation MUST be
// user-attributed for audit; autonomous-agent path is intentionally
// rejected at the boundary.
func TestCredentialAdapter_RejectsMissingCallerUserID(t *testing.T) {
	adapter := NewCredentialInternalAdapter(&CredentialHandler{})
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-1", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	adapter.Create(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401 when X-Caller-User-Id missing", w.Code)
	}
}

// TestCredentialAdapter_RejectsEmptyWorkspace — workspace_id query
// param required even with a valid caller header.
func TestCredentialAdapter_RejectsEmptyWorkspace(t *testing.T) {
	adapter := NewCredentialInternalAdapter(&CredentialHandler{})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", "ludmila")
	w := httptest.NewRecorder()
	adapter.Create(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 when workspace_id missing", w.Code)
	}
}

// TestCredentialAdapter_InjectsAuthUser verifies the context
// injection produces a non-nil UserFromContext with the caller's
// id — the public Create handler would 401 without it.
func TestCredentialAdapter_InjectsAuthUser(t *testing.T) {
	adapter := NewCredentialInternalAdapter(&CredentialHandler{})
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id=ws-1", strings.NewReader(`{}`))
	r.Header.Set("X-Caller-User-Id", "ludmila")

	// Replicate the adapter's injection logic and verify the
	// resulting context — we can't easily run the full Create path
	// without a DB, but the injection step is what this adapter
	// adds on top of the public handler.
	injected := adapter.injectContext(r, "ws-1", "ludmila")
	user := UserFromContext(injected.Context())
	if user == nil {
		t.Fatal("UserFromContext returned nil after injection")
	}
	if user.ID != "ludmila" {
		t.Errorf("injected user.ID = %q, want ludmila", user.ID)
	}
	if WorkspaceIDFromContext(injected.Context()) != "ws-1" {
		t.Errorf("workspace not injected")
	}
	// Credential adapter injects ADMIN (not MANAGER like routine /
	// skill) because CredentialHandler.Rotate gates on canRole("manage"),
	// which requires ADMIN+. See internal_credentials_mutate.go:injectContext.
	if RoleFromContext(injected.Context()) != "ADMIN" {
		t.Errorf("role not injected as ADMIN")
	}
}
