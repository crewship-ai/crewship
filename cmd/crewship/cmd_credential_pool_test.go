package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCredentialPoolCLI(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/provider-logins/pools", clitest.JSONResponse(201, map[string]any{"id": "pool", "name": "Team", "provider": "OPENAI", "mode": "api_key", "member_count": 1}))
	cmd := newCredentialPoolCmd()
	cmd.SetArgs([]string{"create", "--name", "Team", "--provider", "openai", "--mode", "api_key", "--member", "account=3"})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "do not grant access") {
		t.Fatalf("missing definition-only warning: %s", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/provider-logins/pools")
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	var body struct {
		Provider string          `json:"provider"`
		Allow    bool            `json:"allow_cross_owner"`
		Members  []poolMemberOut `json:"members"`
	}
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Provider != "OPENAI" || body.Allow || len(body.Members) != 1 || body.Members[0].CredentialID != "account" || body.Members[0].Priority != 3 {
		t.Fatalf("request=%s", calls[0].Body)
	}
	stub.OnGet("/api/v1/provider-logins/pools", clitest.JSONResponse(200, map[string]any{"items": []any{}, "next_cursor": "next"}))
	cmd = newCredentialPoolCmd()
	cmd.SetArgs([]string{"list", "--after", "previous"})
	out, err = captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "--after next") {
		t.Fatalf("pagination hidden: %s", out)
	}
	reads := stub.CallsFor("GET", "/api/v1/provider-logins/pools")
	if len(reads) != 1 || !strings.Contains(reads[0].Query, "after=previous") {
		t.Fatalf("cursor not forwarded: %+v", reads)
	}
	originalFormat := flagFormat
	flagFormat = "json"
	t.Cleanup(func() { flagFormat = originalFormat })
	cmd = newCredentialPoolCmd()
	cmd.SetArgs([]string{"list"})
	out, err = captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil || page.NextCursor != "next" {
		t.Fatalf("JSON pagination lost: %s, %v", out, err)
	}
	flagFormat = originalFormat
	stub.OnGet("/api/v1/provider-logins/pools/pool", clitest.ErrorResponse(403, "Forbidden"))
	cmd = newCredentialPoolCmd()
	cmd.SetArgs([]string{"get", "pool"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("server refusal swallowed")
	}
}

func TestCredentialPoolCLIValidation(t *testing.T) {
	stub := covStub(t)
	for _, extra := range [][]string{{}, {"--member", "account=x"}, {"--member", "account", "--member", "account"}} {
		cmd := newCredentialPoolCmd()
		args := []string{"create", "--name", "Team", "--provider", "OPENAI", "--mode", "api_key"}
		cmd.SetArgs(append(args, extra...))
		if err := cmd.Execute(); err == nil {
			t.Fatal("invalid members accepted")
		}
	}
	if len(stub.CallsFor("POST", "/api/v1/provider-logins/pools")) != 0 {
		t.Fatal("invalid request sent")
	}
}
