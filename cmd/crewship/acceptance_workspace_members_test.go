package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for the member operations Settings › Members is built on: change
// a role, grant and revoke capabilities, apply a preset, remove. The UI's bulk
// bar issues these one member at a time, so each is pinned here as a single
// request. The ladder itself (grant-below-own, no-modify-superior, last owner)
// is enforced by the server (internal/api/workspaces_member_role.go); the
// CLI must surface its refusal as an error, never as success.

const membersListJSON = `[{"id":"mem_1","user_id":"user_1","email":"ada@example.com","role":"MEMBER"},` +
	`{"id":"mem_2","user_id":"user_2","email":"bob@example.com","role":"ADMIN"}]`

func membersStub(t *testing.T) (*settingsStub, string) {
	t.Helper()
	stub := &settingsStub{routes: map[string]func(map[string]any) (int, string){
		"GET /api/v1/workspaces/ws_test/members": func(map[string]any) (int, string) { return 200, membersListJSON },
	}}
	return stub, profileStubConfig(t, stub.start(t).URL)
}

func TestAcceptance_WorkspaceMembers_RoleChangeResolvesUserIDToMembership(t *testing.T) {
	stub, cfg := membersStub(t)
	stub.routes["PATCH /api/v1/workspaces/ws_test/members/mem_1"] = func(map[string]any) (int, string) {
		return 200, `{"status":"updated","role":"MANAGER"}`
	}
	// The UI addresses a person by membership id; an agent has the user id.
	// Both must land on the membership row.
	for _, id := range []string{"mem_1", "user_1"} {
		out, err := runProfileCLI(t, cfg, "workspace", "member", "role", id, "manager")
		if err != nil {
			t.Fatalf("member role %s: %v\n%s", id, err, out)
		}
	}
	calls := stub.called("PATCH", "/api/v1/workspaces/ws_test/members/mem_1")
	if len(calls) != 2 {
		t.Fatalf("want 2 PATCHes on mem_1, got %d", len(calls))
	}
	for _, c := range calls {
		if c.Body["role"] != "MANAGER" || len(c.Body) != 1 {
			t.Errorf("PATCH body = %v, want only role=MANAGER", c.Body)
		}
	}
}

func TestAcceptance_WorkspaceMembers_LadderRefusalIsAnError(t *testing.T) {
	stub, cfg := membersStub(t)
	stub.routes["PATCH /api/v1/workspaces/ws_test/members/mem_2"] = func(map[string]any) (int, string) {
		return 403, `{"error":"cannot modify a member above your own role"}`
	}
	out, err := runProfileCLI(t, cfg, "workspace", "member", "role", "mem_2", "member")
	if err == nil {
		t.Fatalf("a 403 from the ladder must fail the command; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitAuth {
		t.Errorf("exit code = %d, want ExitAuth (%d)", got, cli.ExitAuth)
	}
	if strings.Contains(out, "Member role changed") {
		t.Errorf("success message printed after a refusal:\n%s", out)
	}
}

func TestAcceptance_WorkspaceMembers_CapabilitiesGrantRevokePreset(t *testing.T) {
	stub, cfg := membersStub(t)
	capPath := "/api/v1/workspaces/ws_test/members/user_1/capabilities"
	stub.routes["PATCH "+capPath] = func(map[string]any) (int, string) {
		return 200, `{"user_id":"user_1","role":"MEMBER","capabilities":["chat","skill.create"]}`
	}
	stub.routes["GET "+capPath] = func(map[string]any) (int, string) {
		return 200, `{"user_id":"user_1","role":"MEMBER","capabilities":["chat","skill.create"]}`
	}

	if out, err := runProfileCLI(t, cfg, "workspace", "member", "capabilities", "grant", "user_1", "skill.create"); err != nil {
		t.Fatalf("grant: %v\n%s", err, out)
	}
	if out, err := runProfileCLI(t, cfg, "workspace", "member", "capabilities", "revoke", "user_1", "skill.create"); err != nil {
		t.Fatalf("revoke: %v\n%s", err, out)
	}
	if out, err := runProfileCLI(t, cfg, "workspace", "member", "capabilities", "preset", "user_1", "power"); err != nil {
		t.Fatalf("preset: %v\n%s", err, out)
	}
	out, err := runProfileCLI(t, cfg, "workspace", "member", "capabilities", "list", "user_1", "-f", "json")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"skill.create"`) {
		t.Errorf("list does not show the capability:\n%s", out)
	}

	patches := stub.called("PATCH", capPath)
	if len(patches) != 3 {
		t.Fatalf("want 3 PATCHes, got %d", len(patches))
	}
	if !reflect.DeepEqual(patches[0].Body["grant"], []any{"skill.create"}) {
		t.Errorf("grant body = %v", patches[0].Body)
	}
	if !reflect.DeepEqual(patches[1].Body["revoke"], []any{"skill.create"}) {
		t.Errorf("revoke body = %v", patches[1].Body)
	}
	if patches[2].Body["preset"] != "power" {
		t.Errorf("preset body = %v", patches[2].Body)
	}
}

func TestAcceptance_WorkspaceMembers_RemoveUsesMembershipID(t *testing.T) {
	stub, cfg := membersStub(t)
	stub.routes["DELETE /api/v1/workspaces/ws_test/members/mem_1"] = func(map[string]any) (int, string) { return 200, `{"status":"removed"}` }
	out, err := runProfileCLI(t, cfg, "workspace", "member", "remove", "user_1", "--yes")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if len(stub.called("DELETE", "/api/v1/workspaces/ws_test/members/mem_1")) != 1 {
		t.Errorf("remove did not DELETE the membership row; calls: %+v", stub.calls)
	}
}
