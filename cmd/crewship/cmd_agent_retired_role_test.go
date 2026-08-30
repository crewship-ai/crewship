package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// COORDINATOR is retired, and until #2189 the CLI said the opposite.
//
// `warnCoordinatorDeprecated` printed "Setting role anyway" and let the
// request through; the server then answered 400 "agent_role must be AGENT or
// LEAD" (internal/api/agents.go:186, pinned by agents_test.go:36 as "retired
// in v0.1"). The one audience for that path — somebody applying a v1 template
// that used COORDINATOR — was told their value would be honoured and got a
// generic server refusal that mentioned neither the template nor the
// deprecation they had just been warned about.
//
// These tests pin the local refusal instead: the CLI must reject the retired
// role before the request, and say what to use.

func retiredRolePreRun(t *testing.T, cmd *cobra.Command, role string) error {
	t.Helper()
	setFlagCovCli10(t, cmd, "role", role)
	if cmd.PreRunE == nil {
		t.Fatalf("%s has no PreRunE — a retired role cannot be refused before the request", cmd.Name())
	}
	return cmd.PreRunE(cmd, nil)
}

func TestRetiredRole_CreateRefusesCoordinatorBeforeTheRequest(t *testing.T) {
	err := retiredRolePreRun(t, agentCreateCmd, "COORDINATOR")
	if err == nil {
		t.Fatal("COORDINATOR must be refused locally; the server rejects it with 400")
	}
	// The message has to carry the fix, not just the refusal: the generic
	// server 400 already said "must be AGENT or LEAD" and that was not enough
	// to tell a template author what to change.
	if !strings.Contains(strings.ToUpper(err.Error()), "LEAD") {
		t.Errorf("refusal must name LEAD as the replacement, got %q", err)
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "COORDINATOR") {
		t.Errorf("refusal must name the role the user passed, got %q", err)
	}
	// "Setting role anyway" was the specific false promise. It must not
	// survive in any casing.
	if strings.Contains(strings.ToLower(err.Error()), "anyway") {
		t.Errorf("refusal must not claim the role was set, got %q", err)
	}
}

func TestRetiredRole_UpdateRefusesCoordinator(t *testing.T) {
	if err := retiredRolePreRun(t, agentUpdateCmd, "COORDINATOR"); err == nil {
		t.Fatal("agent update must refuse COORDINATOR too; the same handler rejects it")
	}
}

func TestRetiredRole_RefusalIsCaseInsensitive(t *testing.T) {
	for _, v := range []string{"coordinator", "Coordinator", "CoOrDiNaToR"} {
		if err := retiredRolePreRun(t, agentCreateCmd, v); err == nil {
			t.Errorf("role %q must be refused; the API compares case-insensitively too", v)
		}
	}
}

func TestRetiredRole_SupportedRolesStillPass(t *testing.T) {
	// The guard must not become a second, stricter role validator: the server
	// owns which roles are valid, and this only removes the one retired value
	// the CLI used to promise. An empty value is the flag default path and
	// must stay untouched.
	for _, v := range []string{"AGENT", "LEAD", "agent", "lead", ""} {
		if err := retiredRolePreRun(t, agentCreateCmd, v); err != nil {
			t.Errorf("role %q must pass through to the server, got %v", v, err)
		}
	}
}

// The refusal replaced a server 400, and a 400 maps to ExitValidation (2).
// Returning a bare error would have quietly demoted this failure to
// ExitGeneric (1) — invisible in the message, but a different answer for any
// script that distinguishes "the request was invalid" from "something broke".
// Moving a check from the server to the client must not change what the shell
// sees.
func TestRetiredRole_RefusalKeepsTheValidationExitCode(t *testing.T) {
	err := retiredRolePreRun(t, agentCreateCmd, "COORDINATOR")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if got := cli.ExitCodeFor(err); got != cli.ExitValidation {
		t.Errorf("exit code = %d, want %d (ExitValidation, what the server 400 produced)", got, cli.ExitValidation)
	}
}
