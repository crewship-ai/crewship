package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const policyTestCrewCUID = "cabcdefghijklmnopqrstuv" // ≥21 chars → no slug resolution

func resetPolicySetFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range []string{"crew", "level", "reason"} {
			_ = policySetCmd.Flags().Set(name, "")
		}
		_ = policySetCmd.Flags().Set("behavior", "warn")
		_ = policySetCmd.Flags().Set("yes", "false")
	})
}

func resetPolicyGetFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = policyGetCmd.Flags().Set("crew", "") })
}

// ─── policy get error paths ──────────────────────────────────────────────

func TestPolicyGetRunE_CrewResolutionFails(t *testing.T) {
	saveCLIState(t)
	resetPolicyGetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(http.StatusOK, []any{}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}
	_ = policyGetCmd.Flags().Set("crew", "ghost")

	err := policyGetCmd.RunE(policyGetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("got %v; want crew-not-found", err)
	}
}

func TestPolicyGetRunE_TransportError(t *testing.T) {
	saveCLIState(t)
	resetPolicyGetFlags(t)

	stub := clitest.NewStubServer()
	deadURL := stub.URL()
	stub.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: deadURL}
	_ = policyGetCmd.Flags().Set("crew", policyTestCrewCUID)

	if err := policyGetCmd.RunE(policyGetCmd, nil); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestPolicyGetRunE_ServerError(t *testing.T) {
	saveCLIState(t)
	resetPolicyGetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/crews/"+policyTestCrewCUID+"/policy",
		clitest.ErrorResponse(http.StatusForbidden, "Forbidden: requires OWNER role"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}
	_ = policyGetCmd.Flags().Set("crew", policyTestCrewCUID)

	err := policyGetCmd.RunE(policyGetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("got %v; want forbidden error surfaced", err)
	}
}

func TestPolicyGetRunE_BadJSON(t *testing.T) {
	saveCLIState(t)
	resetPolicyGetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/crews/"+policyTestCrewCUID+"/policy",
		clitest.TextResponse(http.StatusOK, "not json"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}
	_ = policyGetCmd.Flags().Set("crew", policyTestCrewCUID)

	if err := policyGetCmd.RunE(policyGetCmd, nil); err == nil {
		t.Error("want decode error; got nil")
	}
}

// ─── policy set error paths ──────────────────────────────────────────────

func TestPolicySetRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("got %v; want not-logged-in", err)
	}
}

func TestPolicySetRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "tok"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("got %v; want workspace error", err)
	}
}

func TestPolicySetRunE_MissingCrewAndLevel(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs"}

	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("got %v; want --crew required", err)
	}

	_ = policySetCmd.Flags().Set("crew", "engineering")
	err = policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--level is required") {
		t.Errorf("got %v; want --level required", err)
	}
}

func TestPolicySetRunE_ConfirmationAborts(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs"}

	// Feed an explicit "n" so the non-TTY confirm path deterministically
	// aborts before any network traffic.
	feedStdin(t, "n\n")

	_ = policySetCmd.Flags().Set("crew", "engineering")
	_ = policySetCmd.Flags().Set("level", "trusted")
	// --yes stays false → confirmAction runs → stdin says no → aborted.
	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("got %v; want aborted", err)
	}
}

func TestPolicySetRunE_CrewResolutionFails(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(http.StatusOK, []any{}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	_ = policySetCmd.Flags().Set("crew", "ghost")
	_ = policySetCmd.Flags().Set("level", "strict") // not loose → no prompt
	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("got %v; want crew-not-found", err)
	}
}

func TestPolicySetRunE_EmptyBehaviorDefaultsToWarn(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPut("/api/v1/crews/"+policyTestCrewCUID+"/policy",
		clitest.JSONResponse(http.StatusOK, policyWire{
			CrewID: policyTestCrewCUID, AutonomyLevel: "strict", BehaviorMode: "warn",
		}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	_ = policySetCmd.Flags().Set("crew", policyTestCrewCUID)
	_ = policySetCmd.Flags().Set("level", "STRICT") // exercises lowercase normalisation too
	_ = policySetCmd.Flags().Set("behavior", "")    // explicit empty → server gets "warn"

	if err := policySetCmd.RunE(policySetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("PUT", "/api/v1/crews/"+policyTestCrewCUID+"/policy")
	if len(calls) != 1 {
		t.Fatalf("want 1 PUT, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["autonomy_level"] != "strict" {
		t.Errorf("autonomy_level: got %q", body["autonomy_level"])
	}
	if body["behavior_mode"] != "warn" {
		t.Errorf("behavior_mode: got %q want warn default", body["behavior_mode"])
	}
	if _, ok := body["reason"]; ok {
		t.Errorf("reason must be omitted when empty; body=%v", body)
	}
}

func TestPolicySetRunE_PutTransportError(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	deadURL := stub.URL()
	stub.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: deadURL}

	_ = policySetCmd.Flags().Set("crew", policyTestCrewCUID)
	_ = policySetCmd.Flags().Set("level", "strict")
	if err := policySetCmd.RunE(policySetCmd, nil); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestPolicySetRunE_PutRejected(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPut("/api/v1/crews/"+policyTestCrewCUID+"/policy",
		clitest.ErrorResponse(http.StatusUnprocessableEntity, "reason required"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	_ = policySetCmd.Flags().Set("crew", policyTestCrewCUID)
	_ = policySetCmd.Flags().Set("level", "strict")
	err := policySetCmd.RunE(policySetCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "reason required") {
		t.Errorf("got %v; want server rejection surfaced", err)
	}
}

func TestPolicySetRunE_PutBadJSON(t *testing.T) {
	saveCLIState(t)
	resetPolicySetFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPut("/api/v1/crews/"+policyTestCrewCUID+"/policy",
		clitest.TextResponse(http.StatusOK, "not json"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	_ = policySetCmd.Flags().Set("crew", policyTestCrewCUID)
	_ = policySetCmd.Flags().Set("level", "strict")
	if err := policySetCmd.RunE(policySetCmd, nil); err == nil {
		t.Error("want decode error; got nil")
	}
}

// ─── policy list error + enrichment paths ────────────────────────────────

func TestPolicyListRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "tok"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := policyListCmd.RunE(policyListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("got %v; want workspace error", err)
	}
}

func TestPolicyListRunE_TransportError(t *testing.T) {
	saveCLIState(t)

	stub := clitest.NewStubServer()
	deadURL := stub.URL()
	stub.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: deadURL}

	if err := policyListCmd.RunE(policyListCmd, nil); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestPolicyListRunE_BadJSON(t *testing.T) {
	saveCLIState(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/policies", clitest.TextResponse(http.StatusOK, "not json"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := policyListCmd.RunE(policyListCmd, nil); err == nil {
		t.Error("want decode error; got nil")
	}
}

func TestPolicyListRunE_EnrichmentFailureIsNonFatal(t *testing.T) {
	saveCLIState(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/policies", clitest.JSONResponse(http.StatusOK, []policyWire{
		{CrewID: "c-unnamed-1", AutonomyLevel: "guided", BehaviorMode: "warn"},
	}))
	// Crew listing fails → names degrade to CrewID, list still renders.
	stub.OnGet("/api/v1/crews", clitest.ErrorResponse(http.StatusInternalServerError, "boom"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	out, err := captureStdout(t, func() error {
		return policyListCmd.RunE(policyListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v (enrichment failure must be non-fatal)", err)
	}
	if !strings.Contains(out, "c-unnamed-1") {
		t.Errorf("output should fall back to CrewID; got %q", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("missing-name placeholder expected; got %q", out)
	}
}

func TestPolicyListRunE_SortsAndEnrichesNames(t *testing.T) {
	saveCLIState(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/policies", clitest.JSONResponse(http.StatusOK, []policyWire{
		{CrewID: "c-z-unnamed", AutonomyLevel: "full", BehaviorMode: "block",
			SetAt: "2026-06-01T00:00:00Z", Reason: strings.Repeat("long reason ", 8)},
		{CrewID: "c-named", AutonomyLevel: "strict", BehaviorMode: "warn"},
		{CrewID: "c-a-unnamed", AutonomyLevel: "guided", BehaviorMode: "warn"},
	}))
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(http.StatusOK, []map[string]string{
		{"id": "c-named", "name": "Backend Team", "slug": "backend"},
		{"id": "c-slug-only", "name": "", "slug": "frontend"}, // name falls back to slug
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	out, err := captureStdout(t, func() error {
		return policyListCmd.RunE(policyListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// Sort is case-insensitive by display name with CrewID fallback:
	// "Backend Team" < "c-a-unnamed" < "c-z-unnamed".
	iNamed := strings.Index(out, "Backend Team")
	iA := strings.Index(out, "c-a-unnamed")
	iZ := strings.Index(out, "c-z-unnamed")
	if iNamed == -1 || iA == -1 || iZ == -1 {
		t.Fatalf("output missing rows: %q", out)
	}
	if !(iNamed < iA && iA < iZ) {
		t.Errorf("rows not sorted by display name: named=%d a=%d z=%d\n%s", iNamed, iA, iZ, out)
	}
	// Long reason is truncated to 48 chars (with ellipsis).
	if strings.Contains(out, strings.Repeat("long reason ", 8)) {
		t.Errorf("reason should be truncated; got %q", out)
	}
}
