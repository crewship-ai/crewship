package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// The server answers this endpoint with two independent facts:
//
//	created_user — did we have to create the account?
//	setup_url    — is there a link to hand over?
//
// They are NOT the same question. An account that exists but is unclaimed
// (no password, no linked provider, no verified address — the shape this
// endpoint itself creates) gets created_user=false AND a fresh setup_url,
// because re-issuing is the supported way to recover a link that went
// astray before it was used.
//
// Branching on created_user therefore drops the link on the floor and tells
// the operator the person "signs in with their existing password" — an
// account with no password at all. Found on dev3 against the real server.
func TestMemberInvite_PrintsTheLinkWheneverTheServerIssuesOne(t *testing.T) {
	cases := []struct {
		name        string
		createdUser bool
		setupURL    string
		wantLink    bool
		wantPhrase  string
	}{
		{
			name:        "new account",
			createdUser: true,
			setupURL:    "https://crewship.example.com/reset-password?token=aaa",
			wantLink:    true,
			wantPhrase:  "Created",
		},
		{
			// The regression. Existing row, nobody controls it, server
			// issues a link — the CLI must show it.
			name:        "existing but unclaimed account",
			createdUser: false,
			setupURL:    "https://crewship.example.com/reset-password?token=bbb",
			wantLink:    true,
			wantPhrase:  "Added",
		},
		{
			// The genuinely claimed case: no link, and saying so is right.
			name:        "claimed account",
			createdUser: false,
			setupURL:    "",
			wantLink:    false,
			wantPhrase:  "existing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			stub.OnPost("/api/v1/workspaces/"+covWS+"/members/provision",
				clitest.JSONResponse(201, map[string]any{
					"email":        "alice@example.com",
					"role":         "MEMBER",
					"created_user": tc.createdUser,
					"setup_url":    tc.setupURL,
					"expires_at":   "2026-08-03T00:00:00Z",
				}))
			setStubCLI(t, stub.URL())

			c := newFlagCmd(map[string]string{"role": "MEMBER"}, nil)
			out := captureStdoutCovCli2(t, func() {
				if err := workspaceMemberInviteCmd.RunE(c, []string{"alice@example.com"}); err != nil {
					t.Fatalf("RunE: %v", err)
				}
			})

			gotLink := strings.Contains(out, tc.setupURL) && tc.setupURL != ""
			if gotLink != tc.wantLink {
				t.Errorf("link shown = %v, want %v\noutput:\n%s", gotLink, tc.wantLink, out)
			}
			if !strings.Contains(out, tc.wantPhrase) {
				t.Errorf("missing %q\noutput:\n%s", tc.wantPhrase, out)
			}
			// Never claim a password exists unless the account is claimed.
			// This is the sentence that was false on dev3.
			if tc.setupURL != "" && strings.Contains(out, "existing password") {
				t.Errorf("told the operator about an 'existing password' for an account "+
					"that is being handed a password-setting link\noutput:\n%s", out)
			}
		})
	}
}
