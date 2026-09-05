package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// `crewship escalation supply` (#2376) — the one CLI path a human-supplied
// secret takes. The value comes from stdin and only from stdin: the command
// has no --value flag, so it can never land in argv, the process table or
// shell history.

func TestEscalationSupply_HasNoValueFlag(t *testing.T) {
	for _, name := range []string{"value", "resolution"} {
		if f := escalationSupplyCmd.Flags().Lookup(name); f != nil {
			t.Errorf("escalation supply must not accept --%s: a secret on the command line is visible to anything that can read the process table", name)
		}
	}
}

func TestEscalationSupply_ReadsValueFromStdin(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/escalations/esc-1/supply", clitest.JSONResponse(200, map[string]any{
		"id": "esc-1", "status": "RESOLVED", "action": "approve",
		"credential":          map[string]any{"name": "PG_PASSWORD", "handle_only": true, "granted": true, "security_level": 3},
		"agent_still_waiting": true,
	}))
	// A pipe: the whole stream is the value, minus the single trailing
	// newline `echo` appends.
	escalationSupplyCmd.SetIn(bytes.NewBufferString("s3cret-from-stdin\n"))
	t.Cleanup(func() { escalationSupplyCmd.SetIn(nil) })
	covSetFlagCli8(t, escalationSupplyCmd, "security-level", "3")

	if err := escalationSupplyCmd.RunE(escalationSupplyCmd, []string{"esc-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/escalations/esc-1/supply")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["value"] != "s3cret-from-stdin" {
		t.Errorf("value = %v, want the stdin content without its trailing newline", body["value"])
	}
	if body["security_level"] != float64(3) {
		t.Errorf("security_level = %v, want 3", body["security_level"])
	}
	if _, has := body["name"]; has {
		t.Errorf("name must be omitted when not given: %v", body)
	}
}

func TestEscalationSupply_LegacyAskCarriesNameAndType(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/escalations/esc-2/supply", clitest.JSONResponse(200, map[string]any{
		"credential": map[string]any{"name": "GH_TOKEN"},
	}))
	// A multi-line value (a private key) arrives intact through a redirect.
	escalationSupplyCmd.SetIn(bytes.NewBufferString("-----BEGIN KEY-----\nabc\n-----END KEY-----\n"))
	t.Cleanup(func() { escalationSupplyCmd.SetIn(nil) })
	covSetFlagCli8(t, escalationSupplyCmd, "name", "GH_TOKEN")
	covSetFlagCli8(t, escalationSupplyCmd, "type", "cli_token")

	if err := escalationSupplyCmd.RunE(escalationSupplyCmd, []string{"esc-2"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/escalations/esc-2/supply")[0].Body, &body)
	if body["name"] != "GH_TOKEN" || body["type"] != "CLI_TOKEN" {
		t.Errorf("name/type = %v/%v", body["name"], body["type"])
	}
	if body["value"] != "-----BEGIN KEY-----\nabc\n-----END KEY-----" {
		t.Errorf("multi-line value mangled: %q", body["value"])
	}
}

func TestEscalationSupply_EmptyStdinRefused(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	escalationSupplyCmd.SetIn(bytes.NewBufferString(""))
	t.Cleanup(func() { escalationSupplyCmd.SetIn(nil) })

	err := escalationSupplyCmd.RunE(escalationSupplyCmd, []string{"esc-3"})
	if err == nil || !strings.Contains(err.Error(), "no value on stdin") {
		t.Fatalf("expected a no-value error, got %v", err)
	}
	if n := len(stub.CallsFor("POST", "/api/v1/escalations/esc-3/supply")); n != 0 {
		t.Errorf("an empty value must not reach the server, got %d calls", n)
	}
}

func TestEscalationSupply_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/escalations/esc-4/supply", clitest.ErrorResponse(403,
		"a second approver is required (workspace policy)"))
	escalationSupplyCmd.SetIn(bytes.NewBufferString("v\n"))
	t.Cleanup(func() { escalationSupplyCmd.SetIn(nil) })

	err := escalationSupplyCmd.RunE(escalationSupplyCmd, []string{"esc-4"})
	if err == nil || !strings.Contains(err.Error(), "second approver") {
		t.Fatalf("expected the server's refusal to surface, got %v", err)
	}
}
