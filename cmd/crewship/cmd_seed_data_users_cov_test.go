package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	signupPath           = "/api/v1/auth/signup"
	invitationsPath      = "/api/v1/workspaces/" + covWS + "/invitations"
	membersPath          = "/api/v1/workspaces/" + covWS + "/members"
	adminUsers           = "/api/v1/admin/users"
	provisionMembersPath = membersPath + "/provision"
	setupStatusPath      = "/api/v1/system/setup-status"
)

func fixtureRoster() []map[string]any {
	var rows []map[string]any
	for i, u := range demoUsers {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("u%d", i), "email": u.Email, "role": u.Role})
	}
	return rows
}
func stubSeedUserProvision(s *clitest.StubServer, path string) {
	s.OnPost(path, clitest.JSONResponse(201, map[string]any{"user_id": "new-user", "created_user": true, "setup_url": "https://untrusted.invalid/reset-password?token=setup%2Btoken"}))
	s.OnPost("/api/v1/auth/reset", clitest.JSONResponse(200, map[string]bool{"ok": true}))
}
func TestSeedRBACUsers_RequiresWorkspace(t *testing.T) {
	if err := seedRBACUsers(context.Background(), cli.NewClient("http://127.0.0.1:1", "tok", "")); err == nil || !strings.Contains(err.Error(), "workspace_id") {
		t.Fatal(err)
	}
}
func TestSeedRBACUsers_Canceled(t *testing.T) {
	if err := seedRBACUsers(canceledCtx(), cli.NewClient("http://127.0.0.1:1", "tok", covWS)); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestSeedRBACUsers_NewAccountsWithoutSignup(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]bool{"allow_signup": false}))
	stubSeedUserProvision(s, provisionMembersPath)
	s.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))
	if err := seedRBACUsers(t.Context(), newSeedClient(s)); err != nil {
		t.Fatal(err)
	}
	if len(s.CallsFor("POST", signupPath)) != 0 {
		t.Fatal("signup dependency returned")
	}
	for _, call := range s.CallsFor("POST", provisionMembersPath) {
		var b map[string]any
		_ = json.Unmarshal(call.Body, &b)
		if b["create_only"] != true || b["password"] != nil {
			t.Fatalf("unsafe provisioning: %s", call.Body)
		}
	}
	resets := s.CallsFor("POST", "/api/v1/auth/reset")
	if len(resets) != len(demoUsers) {
		t.Fatalf("reset requests=%d", len(resets))
	}
	for i, call := range resets {
		var b map[string]string
		_ = json.Unmarshal(call.Body, &b)
		if b["token"] != "setup+token" || b["new_password"] != demoUsers[i].Password {
			t.Fatalf("incorrect setup redemption")
		}
	}
}
func TestSeedRBACUsers_ExistingMembersRequireProof(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost(provisionMembersPath, clitest.ErrorResponse(409, "existing account"))
	s.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))
	if err := seedRBACUsers(t.Context(), newSeedClient(s)); err == nil {
		t.Fatal("existing member without working fixture login accepted")
	}
	if len(s.CallsFor("POST", "/api/v1/auth/reset")) != 0 {
		t.Fatal("reset existing password")
	}
}
func TestSeedRBACUsers_RejectIncompleteFixture(t *testing.T) {
	for _, which := range []string{"partial", "forbidden", "role-drift", "unsafe-response", "missing-token"} {
		t.Run(which, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			stubSeedUserProvision(s, provisionMembersPath)
			rows := fixtureRoster()
			switch which {
			case "partial":
				n := 0
				s.OnPost(provisionMembersPath, func(r *http.Request, b []byte) (int, []byte, string) {
					n++
					if n == 1 {
						return clitest.ErrorResponse(500, "broken")(r, b)
					}
					return clitest.JSONResponse(201, map[string]any{"user_id": "new", "created_user": true, "setup_url": "/reset-password?token=t"})(r, b)
				})
			case "forbidden":
				s.OnPost(provisionMembersPath, clitest.ErrorResponse(403, "Forbidden"))
			case "role-drift":
				rows[0]["role"] = "VIEWER"
			case "unsafe-response":
				s.OnPost(provisionMembersPath, clitest.JSONResponse(201, map[string]any{"user_id": "existing", "created_user": false, "setup_url": "/reset-password?token=t"}))
			case "missing-token":
				s.OnPost(provisionMembersPath, clitest.JSONResponse(201, map[string]any{"user_id": "new", "created_user": true, "setup_url": "/reset-password"}))
			}
			s.OnGet(adminUsers, clitest.JSONResponse(200, rows))
			if err := seedRBACUsers(t.Context(), newSeedClient(s)); err == nil || !strings.Contains(err.Error(), "incomplete RBAC fixture") {
				t.Fatal(err)
			}
			if (which == "unsafe-response" || which == "missing-token") && len(s.CallsFor("POST", "/api/v1/auth/reset")) != 0 {
				t.Fatal("unsafe reset")
			}
		})
	}
}
