package main

// #1378 reverse API↔CLI parity: crews.max_ephemeral_agents (PATCH
// /api/v1/crews/{id}) had UI + API but no CLI flag. `policy set
// --max-ephemeral` closes the gap. These assert the flag is reachable and
// drives the crews PATCH — on its own and alongside --level.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestPolicySet_MaxEphemeralFlagExists is the pure parity guard: the flag must
// be wired so an agent driving the CLI can reach the quota at all.
func TestPolicySet_MaxEphemeralFlagExists(t *testing.T) {
	if policySetCmd.Flags().Lookup("max-ephemeral") == nil {
		t.Fatal("policy set is missing the --max-ephemeral flag (API↔CLI parity, #1378)")
	}
}

// TestPolicySet_MaxEphemeralOnly proves --max-ephemeral works standalone (no
// --level): it PATCHes the crew with max_ephemeral_agents and does NOT touch
// the policy route.
func TestPolicySet_MaxEphemeralOnly(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPatch("/api/v1/crews/"+policyTestCrewCUID,
		clitest.JSONResponse(http.StatusOK, map[string]any{"id": policyTestCrewCUID, "max_ephemeral_agents": 20}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	_ = policySetCmd.Flags().Set("crew", policyTestCrewCUID)
	_ = policySetCmd.Flags().Set("max-ephemeral", "20")

	if err := policySetCmd.RunE(policySetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	patches := stub.CallsFor("PATCH", "/api/v1/crews/"+policyTestCrewCUID)
	if len(patches) != 1 {
		t.Fatalf("want 1 crews PATCH, got %d", len(patches))
	}
	var body map[string]int
	if err := json.Unmarshal(patches[0].Body, &body); err != nil {
		t.Fatalf("decode PATCH body: %v", err)
	}
	if body["max_ephemeral_agents"] != 20 {
		t.Errorf("max_ephemeral_agents = %d, want 20", body["max_ephemeral_agents"])
	}
	// No policy PUT when --level is omitted.
	if puts := stub.CallsFor("PUT", "/api/v1/crews/"+policyTestCrewCUID+"/policy"); len(puts) != 0 {
		t.Errorf("want 0 policy PUTs when only --max-ephemeral is set, got %d", len(puts))
	}
}

// TestPolicySet_LevelAndMaxEphemeral proves the combined form drives both the
// policy PUT and the crews PATCH.
func TestPolicySet_LevelAndMaxEphemeral(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPut("/api/v1/crews/"+policyTestCrewCUID+"/policy",
		clitest.JSONResponse(http.StatusOK, policyWire{
			CrewID: policyTestCrewCUID, AutonomyLevel: "strict", BehaviorMode: "warn",
		}))
	stub.OnPatch("/api/v1/crews/"+policyTestCrewCUID,
		clitest.JSONResponse(http.StatusOK, map[string]any{"id": policyTestCrewCUID}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	_ = policySetCmd.Flags().Set("crew", policyTestCrewCUID)
	_ = policySetCmd.Flags().Set("level", "strict") // not loose → no prompt
	_ = policySetCmd.Flags().Set("max-ephemeral", "5")

	if err := policySetCmd.RunE(policySetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if puts := stub.CallsFor("PUT", "/api/v1/crews/"+policyTestCrewCUID+"/policy"); len(puts) != 1 {
		t.Fatalf("want 1 policy PUT, got %d", len(puts))
	}
	if patches := stub.CallsFor("PATCH", "/api/v1/crews/"+policyTestCrewCUID); len(patches) != 1 {
		t.Fatalf("want 1 crews PATCH, got %d", len(patches))
	}
}

// TestPolicySet_MaxEphemeralOutOfRange rejects an out-of-range value before any
// network traffic (mirrors the server's 0-100 validation).
func TestPolicySet_MaxEphemeralOutOfRange(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs"}

	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("max-ephemeral", "500")

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "max-ephemeral") {
		t.Errorf("got %v; want out-of-range --max-ephemeral error", err)
	}
}
