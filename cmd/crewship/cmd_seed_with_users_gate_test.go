package main

import (
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"testing"
)

// An explicit administrative fixture works while public registration remains
// disabled; it never requires changing configuration or restarting the server.
func TestSeedRBACUsers_DisabledSignupDoesNotGateProvisioning(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]bool{"allow_signup": false}))
	stubSeedUserProvision(s, provisionMembersPath)
	s.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))
	if err := seedRBACUsers(t.Context(), newSeedClient(s)); err != nil {
		t.Fatal(err)
	}
	if len(s.CallsFor("GET", setupStatusPath)) != 0 || len(s.CallsFor("POST", signupPath)) != 0 {
		t.Fatal("registration is not part of admin provisioning")
	}
}
