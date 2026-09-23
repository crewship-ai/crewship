package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type accessMeResponse struct {
	Actions map[string]accessMeDecision `json:"actions"`
}

func TestMyRoutineAccessMatchesRunRoleCapabilityAndStatusGates(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, p := seedPinnable(t, h, user, ws, "access-me-routine")
	read := func(role string) (int, accessMeResponse) {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/access/me", nil), user, ws, role)
		req.SetPathValue("slug", p.Slug)
		rr := httptest.NewRecorder()
		h.MyRoutineAccess(rr, req)
		var result accessMeResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &result)
		return rr.Code, result
	}
	if code, got := read("OWNER"); code != 200 || got.Actions["run"].State != "conditional" {
		t.Fatalf("owner = %d %#v", code, got)
	}
	if code, got := read("VIEWER"); code != 200 || got.Actions["run"].Reason != "missing_role_or_capability" {
		t.Fatalf("viewer = %d %#v", code, got)
	}
	_, err := h.db.Exec(`UPDATE workspace_members SET role='VIEWER', capabilities='["routine.run"]' WHERE workspace_id=? AND user_id=?`, ws, user)
	if err != nil {
		t.Fatal(err)
	}
	InvalidateCapabilityCache(ws, user)
	if code, got := read("VIEWER"); code != 200 || got.Actions["run"].State != "conditional" {
		t.Fatalf("capability viewer = %d %#v", code, got)
	}
	if _, err := h.db.Exec(`UPDATE pipelines SET status='disabled' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if code, got := read("VIEWER"); code != 200 || got.Actions["run"].Reason != "routine_disabled" {
		t.Fatalf("disabled = %d %#v", code, got)
	}
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/access/me", nil), user, "another-workspace", "OWNER")
	req.SetPathValue("slug", p.Slug)
	rr := httptest.NewRecorder()
	h.MyRoutineAccess(rr, req)
	if rr.Code != 404 {
		t.Fatalf("cross-workspace = %d", rr.Code)
	}
}

func TestMyRoutineAccessRoleCapabilityStatusMatrix(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, p := seedPinnable(t, h, user, ws, "access-matrix")
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		for _, cap := range []string{"none", CapabilityRoutineRun, CapabilityCredentialReveal} {
			for _, status := range []string{"active", "proposed", "disabled"} {
				t.Run(role+"/"+cap+"/"+status, func(t *testing.T) {
					caps := "[]"
					if cap != "none" {
						caps = `["` + cap + `"]`
					}
					if _, err := h.db.Exec(`UPDATE workspace_members SET role=?, capabilities=? WHERE workspace_id=? AND user_id=?`, role, caps, ws, user); err != nil {
						t.Fatal(err)
					}
					InvalidateCapabilityCache(ws, user)
					if _, err := h.db.Exec(`UPDATE pipelines SET status=? WHERE id=?`, status, p.ID); err != nil {
						t.Fatal(err)
					}
					req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/access/me", nil), user, ws, role)
					req.SetPathValue("slug", p.Slug)
					rr := httptest.NewRecorder()
					h.MyRoutineAccess(rr, req)
					if rr.Code != http.StatusOK {
						t.Fatalf("access status = %d: %s", rr.Code, rr.Body.String())
					}
					var result accessMeResponse
					if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					eligible := role == "OWNER" || role == "ADMIN" || role == "MANAGER" || cap == CapabilityRoutineRun
					want := "denied"
					if eligible && status == "active" {
						want = "conditional"
					}
					if got := result.Actions["run"].State; got != want {
						t.Fatalf("run access = %s, want %s: %#v", got, want, result)
					}
					if result.Actions["edit"].State == "allowed" != (role == "OWNER" || role == "ADMIN" || role == "MANAGER") {
						t.Fatalf("edit drift: %#v", result.Actions["edit"])
					}
					// Run's layered gate shares roleOrCapability with access/me.
					// With no runner wired, an admitted request returns 503 before
					// preflight, while a refused one returns 403.
					runReq := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/run", nil), user, ws, role)
					runReq.SetPathValue("slug", p.Slug)
					runRR := httptest.NewRecorder()
					h.Run(runRR, runReq)
					if eligible && runRR.Code == http.StatusForbidden || !eligible && runRR.Code != http.StatusForbidden {
						t.Fatalf("run auth = %d, eligible=%t", runRR.Code, eligible)
					}
				})
			}
		}
	}
}

func TestMyCredentialAccessVisibilityAndRevealGates(t *testing.T) {
	rig := newRevealRig(t)
	ws, owner := "ws-access-me", "user-access-owner"
	rig.seedWorkspace(t, ws, true)
	rig.seedMember(t, ws, owner, "OWNER", []string{CapabilityCredentialReveal})
	rig.seedCredential(t, ws, owner, "cred-access", "Test credential", "SECRET_SENTINEL", "STANDARD")
	h := NewCredentialHandler(rig.db, revealTestLogger())
	read := func(id, user, role, kind, workspace string) (int, accessMeResponse, string) {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/access/me", nil), user, workspace, role)
		req = req.WithContext(withAuthKind(req.Context(), kind))
		req.SetPathValue("credentialId", id)
		rr := httptest.NewRecorder()
		h.MyCredentialAccess(rr, req)
		var result accessMeResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &result)
		return rr.Code, result, rr.Body.String()
	}
	if code, got, body := read("cred-access", owner, "OWNER", AuthKindSession, ws); code != 200 || got.Actions["reveal"].State != "conditional" || got.Actions["edit"].State != "allowed" || got.Actions["manage_bindings"].State != "allowed" || containsSecret(body) {
		t.Fatalf("owner = %d %#v body=%s", code, got, body)
	}
	if code, got, _ := read("cred-access", owner, "OWNER", AuthKindCLIToken, ws); code != 200 || got.Actions["reveal"].Reason != "non_interactive_auth" {
		t.Fatalf("CLI token = %d %#v", code, got)
	}
	viewer := "user-access-viewer"
	rig.seedMember(t, ws, viewer, "VIEWER", []string{"chat"})
	if code, got, _ := read("cred-access", viewer, "VIEWER", AuthKindSession, ws); code != 200 || got.Actions["reveal"].Reason != "below_role_floor" || got.Actions["edit"].State != "denied" {
		t.Fatalf("viewer = %d %#v", code, got)
	}
	if code, _, _ := read("cred-access", owner, "OWNER", AuthKindSession, "other-workspace"); code != 404 {
		t.Fatalf("cross-workspace = %d", code)
	}
	rig.seedCredential(t, ws, owner, "cred-sealed-access", "Sealed", "OTHER_SECRET", SensitivitySealed)
	if code, got, _ := read("cred-sealed-access", owner, "OWNER", AuthKindSession, ws); code != 200 || got.Actions["reveal"].Reason != "sealed" {
		t.Fatalf("sealed = %d %#v", code, got)
	}
	manager := "user-access-manager"
	rig.seedMember(t, ws, manager, "MANAGER", nil)
	if code, got, _ := read("cred-access", manager, "MANAGER", AuthKindSession, ws); code != 200 || got.Actions["reveal"].Reason != "missing_capability" || got.Actions["edit"].State != "allowed" || got.Actions["rotate"].State != "denied" {
		t.Fatalf("manager without reveal grant = %d %#v", code, got)
	}
	if _, err := rig.db.Exec(`UPDATE workspaces SET credential_reveal_enabled=0 WHERE id=?`, ws); err != nil {
		t.Fatal(err)
	}
	if code, got, _ := read("cred-access", owner, "OWNER", AuthKindSession, ws); code != 200 || got.Actions["reveal"].Reason != "workspace_switch_off" {
		t.Fatalf("workspace switch = %d %#v", code, got)
	}
	if _, err := rig.db.Exec(`UPDATE credentials SET scope='CREW' WHERE id='cred-access'`); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := read("cred-access", viewer, "VIEWER", AuthKindSession, ws); code != 404 {
		t.Fatalf("hidden crew credential = %d", code)
	}
}

func containsSecret(body string) bool {
	return strings.Contains(body, "SECRET_SENTINEL") || strings.Contains(body, "OTHER_SECRET")
}
