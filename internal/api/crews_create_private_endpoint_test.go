package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// #974 S4: enabling allow_private_endpoints is security-sensitive (private-
// network egress from the sandbox). Crew *update* gates it at "manage" (ADMIN);
// creation must match so a MANAGER can't self-grant private egress via Create.
func TestCreateCrew_AllowPrivateEndpoints_RBAC(t *testing.T) {
	h, userID, wsID := covCCHandler(t)

	post := func(role, slug string, flag bool) *httptest.ResponseRecorder {
		req := withWorkspaceUser(
			httptest.NewRequest("POST", "/api/v1/crews", jsonBody(map[string]any{
				"name":                    "Crew " + slug,
				"slug":                    slug,
				"allow_private_endpoints": flag,
			})), userID, wsID, role)
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}

	// MANAGER can "create" but not "manage" → setting the flag true is forbidden.
	if rr := post("MANAGER", "priv-mgr-true", true); rr.Code != http.StatusForbidden {
		t.Errorf("MANAGER create flag=true: status = %d, want 403 (body=%s)", rr.Code, rr.Body.String())
	}
	// MANAGER creating with the flag false (or unset) stays allowed.
	if rr := post("MANAGER", "priv-mgr-false", false); rr.Code != http.StatusCreated {
		t.Errorf("MANAGER create flag=false: status = %d, want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	// ADMIN (manage) may set it.
	rr := post("ADMIN", "priv-admin-true", true)
	if rr.Code != http.StatusCreated {
		t.Fatalf("ADMIN create flag=true: status = %d, want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	// And it persisted.
	var got int
	if err := h.db.QueryRow(`SELECT allow_private_endpoints FROM crews WHERE workspace_id = ? AND slug = ?`, wsID, "priv-admin-true").Scan(&got); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if got != 1 {
		t.Errorf("allow_private_endpoints = %d, want 1", got)
	}
}
