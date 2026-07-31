package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity for the evaluator credential (project rule #3, issue #1554).
//
// The two things a wrapper can get wrong that no server-side test would catch:
// the request it builds, and whether a refusal reaches the person who typed the
// command. Both matter more than usual here — the flag takes a NAME, because
// "which of my three Anthropic keys" is a question an operator answers by name
// and never by CUID, and the name→id resolution happens on this side.

const covCredIDAux = "ccred0000000000000000aux"

func auxStubConfig() map[string]any {
	return map[string]any{
		"slots": []map[string]any{{
			"slot": "behavior", "label": "Tool-call behaviour monitor", "applies_at": "immediately",
			"provider":      map[string]any{"value": "anthropic", "source": "default", "editable": true},
			"model":         map[string]any{"value": "claude-haiku-4-5", "source": "default", "editable": true},
			"timeout_ms":    map[string]any{"value": 30000, "source": "default", "editable": true},
			"credential_id": map[string]any{"value": covCredIDAux, "source": "instance", "editable": true},
			"overridden":    true,
		}},
		"providers":      []string{"anthropic", "openai", "ollama"},
		"judge_provider": "ollama", "judge_model": "qwen2.5:7b",
		"any_overridden": true,
	}
}

// The flag takes a NAME and the server stores an ID, so the resolution has to
// happen here. Sending the name through would store a string that matches no
// credential — accepted by nothing, and the slot would go on spending the
// process env's key while the console claimed a key was pinned.
func TestKeeperAuxSet_ResolvesTheCredentialNameToAnID(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, keeperAuxSetCmd)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": covCredIDAux, "name": "prod-anthropic"},
	}))
	stub.OnPut("/api/v1/admin/keeper/aux/behavior", clitest.JSONResponse(200, auxStubConfig()))

	_ = keeperAuxSetCmd.Flags().Set("credential", "prod-anthropic")
	out := covCaptureStdoutCli3(t, func() {
		if err := keeperAuxSetCmd.RunE(keeperAuxSetCmd, []string{"behavior"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("PUT", "/api/v1/admin/keeper/aux/behavior")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["credential_id"] != covCredIDAux {
		t.Errorf("body = %v, want the resolved credential id", body)
	}
	// Only what was asked for: a --credential-only call must not also rewrite the
	// model, which would pin a slot to whatever the CLI's zero value happens to be.
	if _, ok := body["model"]; ok {
		t.Errorf("body = %v, want no model on a credential-only set", body)
	}
	if !strings.Contains(out, "behavior") {
		t.Errorf("output should name the slot, got:\n%s", out)
	}
}

// "" is the documented clear everywhere else in this command, so it has to be
// the clear here too — and it must NOT go looking for a credential named "".
func TestKeeperAuxSet_EmptyCredentialClearsWithoutALookup(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, keeperAuxSetCmd)
	stub.OnPut("/api/v1/admin/keeper/aux/behavior", clitest.JSONResponse(200, auxStubConfig()))

	_ = keeperAuxSetCmd.Flags().Set("credential", "")
	keeperAuxSetCmd.Flags().Lookup("credential").Changed = true
	covCaptureStdoutCli3(t, func() {
		if err := keeperAuxSetCmd.RunE(keeperAuxSetCmd, []string{"behavior"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	if n := len(stub.CallsFor("GET", "/api/v1/credentials")); n != 0 {
		t.Errorf("looked up %d credential(s) while clearing", n)
	}
	calls := stub.CallsFor("PUT", "/api/v1/admin/keeper/aux/behavior")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if v, ok := body["credential_id"]; !ok || v != "" {
		t.Errorf("body = %v, want an explicit empty credential_id", body)
	}
}

// A name that resolves to nothing must stop before the PUT. Forwarding it would
// turn a typo into a 400 from a server that has no idea what the operator typed.
func TestKeeperAuxSet_UnknownCredentialNameStopsBeforeTheWrite(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, keeperAuxSetCmd)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": covCredIDAux, "name": "prod-anthropic"},
	}))

	_ = keeperAuxSetCmd.Flags().Set("credential", "staging-anthropic")
	err := keeperAuxSetCmd.RunE(keeperAuxSetCmd, []string{"behavior"})
	if err == nil {
		t.Fatal("an unresolvable credential name was accepted")
	}
	if !strings.Contains(err.Error(), "staging-anthropic") {
		t.Errorf("err = %q, want it to name what could not be found", err.Error())
	}
	if n := len(stub.CallsFor("PUT", "/api/v1/admin/keeper/aux/behavior")); n != 0 {
		t.Errorf("wrote %d time(s) despite an unresolvable credential", n)
	}
}

// The server's refusal has to reach the user verbatim: "that key belongs to
// another workspace" and "that credential is an ENDPOINT_URL" send an operator
// to completely different fixes.
func TestKeeperAuxSet_ServerRefusalSurfaces(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, keeperAuxSetCmd)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": covCredIDAux, "name": "prod-anthropic"},
	}))
	stub.OnPut("/api/v1/admin/keeper/aux/behavior",
		clitest.ErrorResponse(400, "credential is a ENDPOINT_URL — an evaluator needs an API_KEY"))

	_ = keeperAuxSetCmd.Flags().Set("credential", "prod-anthropic")
	err := keeperAuxSetCmd.RunE(keeperAuxSetCmd, []string{"behavior"})
	if err == nil {
		t.Fatal("a refused write reported success")
	}
	if !strings.Contains(err.Error(), "API_KEY") {
		t.Errorf("err = %q, want the server's reason", err.Error())
	}
}

// The list view has to name the key. An operator holding three Anthropic
// subscriptions cannot otherwise tell which one a sweep is billing.
func TestKeeperAuxList_NamesThePinnedKey(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/admin/keeper/aux", clitest.JSONResponse(200, auxStubConfig()))

	out := covCaptureStdoutCli3(t, func() {
		if err := keeperAuxListCmd.RunE(keeperAuxListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, covCredIDAux) {
		t.Errorf("list does not say which key the slot spends, got:\n%s", out)
	}
}
