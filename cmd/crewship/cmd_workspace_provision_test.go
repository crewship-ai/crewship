package main

import (
	"encoding/json"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
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
	guardCLIState(t)
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

// Both provisioning commands must remain scriptable: machine output includes
// every response field and never mixes success prose into either stream.
func TestWorkspaceProvision_MachineFormats(t *testing.T) {
	guardCLIState(t)
	for _, format := range []string{"json", "yaml", "ndjson", "quiet"} {
		for _, scenario := range []string{"new member", "unclaimed member", "claimed member", "workspace"} {
			t.Run(format+"/"+scenario, func(t *testing.T) {
				stub := clitest.NewStubServer()
				defer stub.Close()
				setStubCLI(t, stub.URL())
				flagFormat = format
				want := map[string]any{
					"email": "alice@example.com", "role": "MEMBER",
					"created_user": scenario == "new member",
					"setup_url":    "https://crewship.example.com/reset-password?token=test-only",
					"expires_at":   "2026-09-08T00:00:00Z",
				}
				quiet := "alice@example.com"
				path := "/api/v1/workspaces/" + covWS + "/members/provision"
				command := workspaceMemberInviteCmd
				flags := map[string]string{"role": "MEMBER"}
				args := []string{"alice@example.com"}
				if scenario == "claimed member" {
					want["setup_url"] = ""
					want["expires_at"] = ""
				}
				if scenario == "workspace" {
					want = map[string]any{"id": "cworkspace-full-id", "name": "CLI testing", "slug": "cli-testing"}
					quiet = "cworkspace-full-id"
					path = "/api/v1/workspaces"
					command = workspaceCreateCmd
					flags = map[string]string{"name": "CLI testing", "slug": "cli-testing", "language": "en"}
					args = nil
				}
				stub.OnPost(path, clitest.JSONResponse(201, want))
				out := captureStdoutCovCli2(t, func() {
					if err := command.RunE(newFlagCmd(flags, nil), args); err != nil {
						t.Fatalf("RunE: %v", err)
					}
				})
				if format == "quiet" {
					if out != quiet+"\n" {
						t.Fatalf("quiet output = %q, want identifier only", out)
					}
					return
				}
				var got map[string]any
				var err error
				if format == "yaml" {
					err = yaml.Unmarshal([]byte(out), &got)
				} else {
					err = json.Unmarshal([]byte(out), &got)
				}
				if err != nil {
					t.Fatalf("%s output cannot be parsed: %v\n%s", format, err, out)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("output = %#v, want %#v", got, want)
				}
				if format == "ndjson" && strings.Count(out, "\n") != 1 {
					t.Fatalf("NDJSON must be exactly one line: %q", out)
				}
			})
		}
	}
}

func TestMemberProvisionCreateOnlyFlag(t *testing.T) {
	guardCLIState(t)
	stub := clitest.NewStubServer()
	stub.OnGet("/openapi.json", clitest.JSONResponse(200, map[string]any{"components": map[string]any{"schemas": map[string]any{"FinalCoreProvisionMemberRequest": map[string]any{"properties": map[string]any{"create_only": map[string]any{"type": "boolean"}}}}}}))
	defer stub.Close()
	setStubCLI(t, stub.URL())
	path := "/api/v1/workspaces/" + covWS + "/members/provision"
	stub.OnPost(path, clitest.JSONResponse(201, map[string]any{"email": "demo@example.test", "role": "MEMBER", "created_user": true, "setup_url": ""}))
	captureStdoutCovCli2(t, func() {
		if err := workspaceMemberInviteCmd.RunE(newFlagCmd(map[string]string{"role": "MEMBER"}, map[string]bool{"create-only": true}), []string{"demo@example.test"}); err != nil {
			t.Fatal(err)
		}
	})
	found := false
	for _, call := range stub.Calls() {
		if call.Path == path && call.Method == "POST" {
			var body map[string]any
			if err := json.Unmarshal(call.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body["create_only"] != true {
				t.Fatal("create-only guard missing")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("provision request missing")
	}
}

func TestMemberProvisionCreateOnlyRefusesOldServer(t *testing.T) {
	guardCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())
	err := workspaceMemberInviteCmd.RunE(newFlagCmd(map[string]string{"role": "MEMBER"}, map[string]bool{"create-only": true}), []string{"demo@example.test"})
	if err == nil {
		t.Fatal("unsupported guard accepted")
	}
	for _, call := range stub.Calls() {
		if call.Method == "POST" {
			t.Fatal("provisioning attempted against unsupported server")
		}
	}
}
