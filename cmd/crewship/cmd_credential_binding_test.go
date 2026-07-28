package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity for the binding routes (project rule #3). The tests below assert
// the two things a wrapper can get wrong in a way no server-side test would
// catch: the REQUEST it builds, and whether a refusal reaches the user.

const covCredIDBinding = "ccred0000000000000000aaa"

// covStubBindingCredential makes the credential name→id resolve succeed.
func covStubBindingCredential(s *clitest.StubServer) {
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": covCredIDBinding, "name": "github-acme"},
	}))
}

// TestCredBindCmd_SendsScopeAndSlot pins the request body. A binding that
// silently lost its crew_id would be written at WORKSPACE scope — every agent
// in the tenant would get the crew's GitHub account, and the command would
// still print success.
func TestCredBindCmd_SendsScopeAndSlot(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credBindCmd)
	covStubBindingCredential(stub)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew0000000000000000aaa", "slug": "acme"},
	}))
	stub.OnPost("/api/v1/credentials/bindings", clitest.JSONResponse(201, map[string]any{
		"id": "cbind0000000000000000aaa", "credential_id": covCredIDBinding,
		"credential_name": "github-acme", "scope": "CREW",
		"crew_id": "ccrew0000000000000000aaa", "slot": "GH_TOKEN",
	}))

	_ = credBindCmd.Flags().Set("slot", "GH_TOKEN")
	_ = credBindCmd.Flags().Set("crew", "acme")
	out := covCaptureStdoutCli3(t, func() {
		if err := credBindCmd.RunE(credBindCmd, []string{"github-acme"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("POST", "/api/v1/credentials/bindings")
	if len(calls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["scope"] != "CREW" || body["crew_id"] != "ccrew0000000000000000aaa" {
		t.Errorf("body = %v, want CREW scope carrying the resolved crew id", body)
	}
	if body["slot"] != "GH_TOKEN" || body["credential_id"] != covCredIDBinding {
		t.Errorf("body = %v, want slot GH_TOKEN on the resolved credential", body)
	}
	if body["agent_id"] != "" {
		t.Errorf("body = %v, want no agent_id on a crew binding", body)
	}
	if !strings.Contains(out, "GH_TOKEN") || !strings.Contains(out, "github-acme") {
		t.Errorf("output should name the slot and the account, got:\n%s", out)
	}
}

// TestCredBindCmd_ConflictSurfaces is the reason the server returns 409 rather
// than replacing the row: the user has to SEE it. A wrapper that swallowed the
// status would turn a refused write into a silent no-op, which is worse than
// the last-write-wins we were avoiding.
func TestCredBindCmd_ConflictSurfaces(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credBindCmd)
	covStubBindingCredential(stub)
	stub.OnPost("/api/v1/credentials/bindings",
		clitest.ErrorResponse(409, "slot GH_TOKEN is already bound in this scope — delete the existing binding first"))

	_ = credBindCmd.Flags().Set("slot", "GH_TOKEN")
	err := credBindCmd.RunE(credBindCmd, []string{"github-acme"})
	if err == nil {
		t.Fatal("a 409 must reach the user as an error, not a success")
	}
	if !strings.Contains(err.Error(), "already bound") {
		t.Errorf("error = %v, want the server's explanation preserved", err)
	}
}

// TestCredBindCmd_RejectsTwoScopes catches the flag combination the schema's
// CHECK would reject anyway — locally, before a round trip, with a sentence
// instead of a constraint name.
func TestCredBindCmd_RejectsTwoScopes(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credBindCmd)
	covStubBindingCredential(stub)

	_ = credBindCmd.Flags().Set("slot", "GH_TOKEN")
	_ = credBindCmd.Flags().Set("crew", "acme")
	_ = credBindCmd.Flags().Set("agent", "bot")
	if err := credBindCmd.RunE(credBindCmd, []string{"github-acme"}); err == nil {
		t.Fatal("--crew and --agent together must be refused: a binding has exactly one scope")
	}
	if n := len(stub.CallsFor("POST", "/api/v1/credentials/bindings")); n != 0 {
		t.Errorf("made %d create calls for an invalid scope combination, want 0", n)
	}
}

// TestCredBindCmd_RequiresSlot guards the flag that carries the entire point of
// the feature. Defaulting it to the credential name would quietly reinstate the
// bug: name and env var fused again.
func TestCredBindCmd_RequiresSlot(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credBindCmd)
	covStubBindingCredential(stub)

	if err := credBindCmd.RunE(credBindCmd, []string{"github-acme"}); err == nil {
		t.Fatal("bind without --slot must fail rather than guessing an env var name")
	}
	if n := len(stub.CallsFor("POST", "/api/v1/credentials/bindings")); n != 0 {
		t.Errorf("made %d create calls without a slot, want 0", n)
	}
}

// TestCredBindingsCmd_ListsAndFilters covers the read path and the empty case.
// The empty case matters: "no bindings" must not read as "no credentials
// reach your agents", because unbound credentials still deliver under their own
// name — that is the compatibility guarantee, and the CLI is where a user would
// first doubt it.
func TestCredBindingsCmd_ListsAndFilters(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credBindingsCmd)
	stub.OnGet("/api/v1/credentials/bindings", clitest.JSONResponse(200, map[string]any{
		"bindings": []map[string]any{{
			"id": "cbind0000000000000000aaa", "credential_id": covCredIDBinding,
			"credential_name": "github-acme", "scope": "CREW",
			"crew_id": "ccrew0000000000000000aaa", "slot": "GH_TOKEN",
		}},
	}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credBindingsCmd.RunE(credBindingsCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"GH_TOKEN", "github-acme", "crew"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCredBindingsCmd_EmptyExplainsTheDefault(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credBindingsCmd)
	stub.OnGet("/api/v1/credentials/bindings",
		clitest.JSONResponse(200, map[string]any{"bindings": []map[string]any{}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credBindingsCmd.RunE(credBindingsCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "own name") {
		t.Errorf("an empty list must say what happens WITHOUT a binding, got:\n%s", out)
	}
}

// TestCredUnbindCmd_ResolvesSlotToBindingID covers the ergonomic path: a human
// knows "crew acme's GH_TOKEN", not a binding id. The lookup relies on the
// invariant — one slot, one binding, per scope — so it may take the first row
// without ambiguity.
func TestCredUnbindCmd_ResolvesSlotToBindingID(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUnbindCmd)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew0000000000000000aaa", "slug": "acme"},
	}))
	stub.OnGet("/api/v1/credentials/bindings", clitest.JSONResponse(200, map[string]any{
		"bindings": []map[string]any{{
			"id": "cbind0000000000000000aaa", "credential_id": covCredIDBinding,
			"credential_name": "github-acme", "scope": "CREW",
			"crew_id": "ccrew0000000000000000aaa", "slot": "GH_TOKEN",
		}},
	}))
	stub.OnDelete("/api/v1/credentials/bindings/cbind0000000000000000aaa", clitest.EmptyResponse(204))

	_ = credUnbindCmd.Flags().Set("slot", "GH_TOKEN")
	_ = credUnbindCmd.Flags().Set("crew", "acme")
	covCaptureStdoutCli3(t, func() {
		if err := credUnbindCmd.RunE(credUnbindCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	lookups := stub.CallsFor("GET", "/api/v1/credentials/bindings")
	if len(lookups) != 1 {
		t.Fatalf("expected 1 lookup, got %d", len(lookups))
	}
	for _, want := range []string{"scope=CREW", "slot=GH_TOKEN", "crew_id=ccrew0000000000000000aaa"} {
		if !strings.Contains(lookups[0].Query, want) {
			t.Errorf("lookup query %q missing %q — a broader query could delete another scope's binding",
				lookups[0].Query, want)
		}
	}
	if n := len(stub.CallsFor("DELETE", "/api/v1/credentials/bindings/cbind0000000000000000aaa")); n != 1 {
		t.Errorf("expected 1 delete of the resolved id, got %d", n)
	}
}

func TestCredUnbindCmd_NeedsAnIDOrASlot(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUnbindCmd)
	if err := credUnbindCmd.RunE(credUnbindCmd, nil); err == nil {
		t.Fatal("unbind with neither an id nor a slot must fail rather than guess which binding to remove")
	}
}

// TestCredResolveCmd_ShowsSourceAndNeverValues is the debugging command, and
// the assertion that matters most is the negative one: it must report WHICH
// account fills a slot without ever handing back the secret.
func TestCredResolveCmd_ShowsSourceAndNeverValues(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/credential-bindings", clitest.JSONResponse(200, map[string]any{
		"agent_id": covAgentIDCli3,
		"slots": []map[string]string{
			{"slot": "GH_TOKEN", "credential_id": covCredIDBinding, "credential_name": "github-acme", "source": "crew_binding"},
			{"slot": "PLAIN_TOKEN", "credential_id": "cred-plain", "credential_name": "PLAIN_TOKEN", "source": "crew_link"},
		},
	}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credResolveCmd.RunE(credResolveCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"GH_TOKEN", "github-acme", "crew_binding", "PLAIN_TOKEN", "crew_link"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCredResolveCmd_EmptyIsExplicit(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/credential-bindings", clitest.JSONResponse(200, map[string]any{
		"agent_id": covAgentIDCli3, "slots": []map[string]string{},
	}))

	out := covCaptureStdoutCli3(t, func() {
		if err := credResolveCmd.RunE(credResolveCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "no credentials") {
		t.Errorf("an agent with no credentials must be stated, not rendered as an empty table:\n%s", out)
	}
}
