package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const revealCredID = "ccred00000000000000revl"

// withStubbedRevealTTY forces the terminal check for the duration of a test.
// The real check reads os.Stdin, which `go test` does not give us.
func withStubbedRevealTTY(t *testing.T, isTTY bool) {
	t.Helper()
	prev := revealTTYCheck
	revealTTYCheck = func() bool { return isTTY }
	t.Cleanup(func() { revealTTYCheck = prev })
}

func runReveal(t *testing.T, args []string, flags map[string]string) (string, string, error) {
	t.Helper()
	covResetFlags(t, credRevealCmd)
	var stdout, stderr bytes.Buffer
	credRevealCmd.SetOut(&stdout)
	credRevealCmd.SetErr(&stderr)
	t.Cleanup(func() {
		credRevealCmd.SetOut(nil)
		credRevealCmd.SetErr(nil)
	})
	if flags != nil {
		covSetFlags(t, credRevealCmd, flags)
	}
	err := credRevealCmd.RunE(credRevealCmd, args)
	return stdout.String(), stderr.String(), err
}

// The reason is checked client-side before anything else happens, so an
// operator learns their reason is unusable before typing a password — and a
// script that forgot the flag never reaches the network.
func TestCredReveal_RequiresReason(t *testing.T) {
	covStub(t)
	withStubbedRevealTTY(t, true)

	_, _, err := runReveal(t, []string{"gh-token"}, nil)
	if err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Fatalf("err = %v, want the missing-reason error", err)
	}
}

func TestCredReveal_RejectsShortReason(t *testing.T) {
	covStub(t)
	withStubbedRevealTTY(t, true)

	_, _, err := runReveal(t, []string{"gh-token"}, map[string]string{"reason": "need it"})
	if err == nil || !strings.Contains(err.Error(), "at least 20 characters") {
		t.Fatalf("err = %v, want the short-reason error", err)
	}
}

// The client-side half of §2.6 L9. The server is the authority and refuses a
// CLI token regardless, but failing here means a CI job gets a message that
// says what to do instead, rather than a bare 403 after a password prompt it
// could never satisfy.
func TestCredReveal_RefusesWithoutATerminal(t *testing.T) {
	covStub(t)
	withStubbedRevealTTY(t, false)

	_, _, err := runReveal(t, []string{"gh-token"},
		map[string]string{"reason": "Handing the deploy key to the migration runbook"})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err = %v, want the non-interactive refusal", err)
	}
	// And it must point at the safer alternative rather than dead-ending.
	if !strings.Contains(err.Error(), "rotate") {
		t.Errorf("refusal should point at `crewship credential rotate`, got %q", err)
	}
}

// ─── reveal-policy ──────────────────────────────────────────────────────

func TestCredRevealPolicy_ShowsDisabledStateWithGuidance(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials/reveal-policy",
		clitest.JSONResponse(200, map[string]any{"workspace_id": "ws1", "enabled": false}))

	out, err := runPolicy(t, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("output = %q, want it to say reveal is disabled", out)
	}
	if !strings.Contains(out, "--enable") {
		t.Errorf("output = %q, want it to name the command that turns it on", out)
	}
}

func TestCredRevealPolicy_EnableSendsTheSwitch(t *testing.T) {
	stub := covStub(t)
	stub.OnPut("/api/v1/credentials/reveal-policy",
		clitest.JSONResponse(200, map[string]any{"workspace_id": "ws1", "enabled": true}))

	if _, err := runPolicy(t, map[string]string{"enable": "true"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := stub.CallsFor("PUT", "/api/v1/credentials/reveal-policy")
	if len(calls) != 1 {
		t.Fatalf("got %d PUTs, want 1", len(calls))
	}
	var body map[string]bool
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body["enabled"] {
		t.Fatalf("body = %v, want enabled=true", body)
	}
}

func TestCredRevealPolicy_EnableAndDisableAreExclusive(t *testing.T) {
	covStub(t)
	_, err := runPolicy(t, map[string]string{"enable": "true", "disable": "true"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want the exclusivity error", err)
	}
}

func runPolicy(t *testing.T, flags map[string]string) (string, error) {
	t.Helper()
	covResetFlags(t, credRevealPolicyCmd)
	var stdout bytes.Buffer
	credRevealPolicyCmd.SetOut(&stdout)
	t.Cleanup(func() { credRevealPolicyCmd.SetOut(nil) })
	if flags != nil {
		covSetFlags(t, credRevealPolicyCmd, flags)
	}
	err := credRevealPolicyCmd.RunE(credRevealPolicyCmd, nil)
	return stdout.String(), err
}

// ─── sensitivity ────────────────────────────────────────────────────────

// The CLI's accepted classes are derived from the server's vocabulary, so
// there is no second list to drift. A class the server does not know must be
// rejected locally with a message naming the real set.
func TestCredSensitivity_RejectsUnknownClass(t *testing.T) {
	covStub(t)
	_, err := runSensitivity(t, []string{"gh-token", "TOP_SECRET"})
	if err == nil || !strings.Contains(err.Error(), "sensitivity must be one of") {
		t.Fatalf("err = %v, want the unknown-class error", err)
	}
	for _, class := range api.AllSensitivities() {
		if !strings.Contains(err.Error(), class) {
			t.Errorf("error should name %s; got %q", class, err)
		}
	}
}

func TestCredSensitivity_AcceptsLowercaseAndSendsUppercase(t *testing.T) {
	stub := covStub(t)
	stub.OnPut("/api/v1/credentials/"+revealCredID+"/sensitivity",
		clitest.JSONResponse(200, map[string]any{
			"credential_id": revealCredID, "sensitivity": "SEALED", "previous": "STANDARD",
		}))

	if _, err := runSensitivity(t, []string{revealCredID, "sealed"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := stub.CallsFor("PUT", "/api/v1/credentials/"+revealCredID+"/sensitivity")
	if len(calls) != 1 {
		t.Fatalf("got %d PUTs, want 1", len(calls))
	}
	var body map[string]string
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["sensitivity"] != api.SensitivitySealed {
		t.Fatalf("sent %q, want SEALED — the server's CHECK only accepts the canonical spelling", body["sensitivity"])
	}
}

// Sealing tells the operator the thing they will otherwise discover as a 403
// later: the value is now unreachable, and rotation is the only way to a
// usable one.
func TestCredSensitivity_SealingExplainsTheConsequence(t *testing.T) {
	stub := covStub(t)
	stub.OnPut("/api/v1/credentials/"+revealCredID+"/sensitivity",
		clitest.JSONResponse(200, map[string]any{
			"credential_id": revealCredID, "sensitivity": "SEALED", "previous": "RESTRICTED",
		}))

	out, err := runSensitivity(t, []string{revealCredID, "SEALED"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "no longer be revealed") || !strings.Contains(out, "Rotate") {
		t.Errorf("output = %q, want it to explain that reveal is gone and rotation is the way out", out)
	}
}

func runSensitivity(t *testing.T, args []string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	credSensitivityCmd.SetOut(&stdout)
	t.Cleanup(func() { credSensitivityCmd.SetOut(nil) })
	err := credSensitivityCmd.RunE(credSensitivityCmd, args)
	return stdout.String(), err
}

// Every route added in this change has a command, and every command is
// reachable from `crewship credential` — project rule #3 is otherwise easy to
// satisfy by writing the file and forgetting the AddCommand.
func TestCredRevealCommandsAreRegistered(t *testing.T) {
	want := map[string]bool{"reveal": false, "reveal-policy": false, "sensitivity": false}
	for _, c := range credentialCmd.Commands() {
		name := strings.Fields(c.Use)[0]
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("`crewship credential %s` is not registered", name)
		}
	}
}
