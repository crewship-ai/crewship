package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSkillAdapter_Internal_CarriesWorkspaceInContext pins the sidecar half of
// the skills/generate path-vs-query fix.
//
// Generate used to read r.PathValue("workspaceId"), and this adapter stamped
// the query's workspace onto the path to suit it. Moving the read to the
// context (the only value RequireWorkspace validates membership against) closed
// a cross-tenant hole on the PUBLIC route — but it also silently moved which
// line makes the INTERNAL route work. SetPathValue is now decoration; the
// ctxWorkspaceID value is load-bearing.
//
// Nothing caught that. Removing the ctxWorkspaceID injection leaves the whole
// internal/api suite green while every sidecar `skill generate` fails with
// "workspace_id is required", because the existing adapter tests
// (internal_mirrors_dualpath_test.go and friends) all assert a 403 from the
// capability gate, which runs BEFORE Generate reads the workspace.
//
// So this asserts the one thing those cannot: the request reaches Generate with
// a workspace resolved, and gets past the workspace check to fail on its own
// payload validation instead.
func TestSkillAdapter_Internal_CarriesWorkspaceInContext(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	adapter := NewSkillInternalAdapter(&SkillGenerateHandler{db: db, logger: slog.Default()})

	// No X-Caller-User-Id: the capability gate is skipped, so the request
	// travels all the way into Generate rather than stopping at 403.
	r := httptest.NewRequest(http.MethodPost, "/?workspace_id="+wsID,
		strings.NewReader(`{"prompt":"x"}`))
	w := httptest.NewRecorder()
	adapter.Generate(w, r)

	if strings.Contains(w.Body.String(), "workspace_id is required") {
		t.Fatalf("Generate did not receive the workspace from the request context — "+
			"the adapter's ctxWorkspaceID injection is what makes the internal route work, "+
			"not its SetPathValue call. status=%d body=%s", w.Code, strings.TrimSpace(w.Body.String()))
	}

	// Positive control: the probe must actually be reaching Generate's own
	// validation, or the assertion above would pass for the wrong reason
	// (e.g. a 403 from a gate that runs earlier).
	if !strings.Contains(w.Body.String(), "slug and prompt are required") {
		t.Errorf("expected to reach Generate's payload validation; "+
			"got status=%d body=%s", w.Code, strings.TrimSpace(w.Body.String()))
	}
}
