package api

// Tests for the PR-D F5 internal hire adapter (sidecar → server
// round-trip). The adapter injects workspace + MANAGER role from
// query params + internal-token elevation; we verify the downstream
// AgentHandler.Hire policy gate still fires (strict crews still 403)
// so a malicious / buggy sidecar can't bypass the autonomy_level.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalHireAdapter_RoutesToPublicHire(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID := seedHireCrew(t, db, "trusted", 5)
	agents := newHireHandler(t, db)

	adapter := NewHireInternalAdapter(agents)

	body, _ := json.Marshal(map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "internal hire round-trip",
	})
	req := httptest.NewRequest("POST",
		"/api/v1/internal/agents/hire?workspace_id="+wsID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	adapter.Hire(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalHireAdapter_StrictPolicyStillRejects(t *testing.T) {
	// Critical security test: the sidecar-elevated path MUST still
	// honour autonomy_level. A regression that bypassed the policy
	// gate would let a buggy LEAD spawn into a strict crew.
	db := setupTestDB(t)
	_, wsID, crewID := seedHireCrew(t, db, "strict", 5)
	agents := newHireHandler(t, db)
	adapter := NewHireInternalAdapter(agents)

	body, _ := json.Marshal(map[string]any{
		"crew_id":       crewID,
		"template_slug": "docs-writer",
		"reason":        "should bounce on strict",
	})
	req := httptest.NewRequest("POST",
		"/api/v1/internal/agents/hire?workspace_id="+wsID, bytes.NewReader(body))
	rr := httptest.NewRecorder()
	adapter.Hire(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 on strict crew; body: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalHireAdapter_MissingWorkspaceIs400(t *testing.T) {
	db := setupTestDB(t)
	agents := newHireHandler(t, db)
	adapter := NewHireInternalAdapter(agents)

	req := httptest.NewRequest("POST", "/api/v1/internal/agents/hire", nil)
	rr := httptest.NewRecorder()
	adapter.Hire(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalHireAdapter_NilHandlerIs500(t *testing.T) {
	adapter := NewHireInternalAdapter(nil)
	req := httptest.NewRequest("POST", "/api/v1/internal/agents/hire?workspace_id=x", nil)
	rr := httptest.NewRecorder()
	adapter.Hire(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}
