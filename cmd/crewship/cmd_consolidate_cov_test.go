package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// setConsolidateFlags sets --crew/--since and registers cleanup back to
// the defaults so the global cobra command stays pristine.
func setConsolidateFlags(t *testing.T, crew, since string) {
	t.Helper()
	if err := consolidateRunCmd.Flags().Set("crew", crew); err != nil {
		t.Fatal(err)
	}
	if err := consolidateRunCmd.Flags().Set("since", since); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = consolidateRunCmd.Flags().Set("crew", "")
		_ = consolidateRunCmd.Flags().Set("since", "")
	})
}

func TestConsolidateRunRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := consolidateRunCmd.RunE(consolidateRunCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("got %v; want not-logged-in error", err)
	}
}

func TestConsolidateRunRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "tok"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := consolidateRunCmd.RunE(consolidateRunCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("got %v; want workspace error", err)
	}
}

func TestConsolidateRunRunE_PostError(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	deadURL := stub.URL()
	stub.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: deadURL}

	if err := consolidateRunCmd.RunE(consolidateRunCmd, nil); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestConsolidateRunRunE_ServerError(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/consolidate/run", clitest.ErrorResponse(http.StatusInternalServerError, "summarizer exploded"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	err := consolidateRunCmd.RunE(consolidateRunCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "summarizer exploded") {
		t.Errorf("got %v; want server error surfaced", err)
	}
}

func TestConsolidateRunRunE_BadJSON(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/consolidate/run", clitest.TextResponse(http.StatusOK, "not json"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := consolidateRunCmd.RunE(consolidateRunCmd, nil); err == nil {
		t.Error("want decode error; got nil")
	}
}

func TestConsolidateRunRunE_TriggeredWithScope(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/consolidate/run", clitest.JSONResponse(http.StatusOK, map[string]any{
		"triggered": true,
		"worker_id": "w-42",
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}
	// A CUID passes through resolveCrewID untouched (no /crews lookup).
	const crewCUID = "ccrew0123456789012345"
	setConsolidateFlags(t, crewCUID, "24h")

	out, err := captureStdout(t, func() error {
		return consolidateRunCmd.RunE(consolidateRunCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Consolidation triggered (worker_id=w-42)") {
		t.Errorf("output: got %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/consolidate/run")
	if len(calls) != 1 {
		t.Fatalf("want 1 POST, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["crew_id"] != crewCUID {
		t.Errorf("crew_id: got %q", body["crew_id"])
	}
	if body["since"] != "24h" {
		t.Errorf("since: got %q", body["since"])
	}
}

// TestConsolidateRunRunE_ResolvesCrewSlug verifies issue #616: a --crew slug
// is resolved to its CUID via /api/v1/crews before the consolidate POST,
// matching every other crew-scoped command.
func TestConsolidateRunRunE_ResolvesCrewSlug(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(http.StatusOK, []map[string]any{
		{"id": "ccrewbackendteam0001234", "slug": "backend-team"},
	}))
	stub.OnPost("/api/v1/consolidate/run", clitest.JSONResponse(http.StatusOK, map[string]any{
		"triggered": true,
		"worker_id": "w-7",
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}
	setConsolidateFlags(t, "backend-team", "")

	_, err := captureStdout(t, func() error {
		return consolidateRunCmd.RunE(consolidateRunCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/consolidate/run")
	if len(calls) != 1 {
		t.Fatalf("want 1 POST, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["crew_id"] != "ccrewbackendteam0001234" {
		t.Errorf("crew_id: got %q, want resolved CUID", body["crew_id"])
	}
}

func TestConsolidateRunRunE_OmitsEmptyScope(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/consolidate/run", clitest.JSONResponse(http.StatusOK, map[string]any{}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}
	setConsolidateFlags(t, "", "")

	out, err := captureStdout(t, func() error {
		return consolidateRunCmd.RunE(consolidateRunCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Neither triggered nor accepted → fallback message.
	if !strings.Contains(out, "Consolidation request submitted.") {
		t.Errorf("output: got %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/consolidate/run")
	if len(calls) != 1 {
		t.Fatalf("want 1 POST, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if _, ok := body["crew_id"]; ok {
		t.Errorf("crew_id must be omitted when flag is empty; body=%v", body)
	}
	if _, ok := body["since"]; ok {
		t.Errorf("since must be omitted when flag is empty; body=%v", body)
	}
}

func TestConsolidateRunRunE_AcceptedWithNote(t *testing.T) {
	saveCLIState(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/consolidate/run", clitest.JSONResponse(http.StatusAccepted, map[string]any{
		"accepted": true,
		"note":     "no summarizer configured",
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	out, err := captureStdout(t, func() error {
		return consolidateRunCmd.RunE(consolidateRunCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Accepted, but skipped: no summarizer configured") {
		t.Errorf("output: got %q", out)
	}
}

func TestConsolidateRunRunE_JSONAndYAMLFormats(t *testing.T) {
	saveCLIState(t)
	origFormat := flagFormat
	t.Cleanup(func() { flagFormat = origFormat })

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/consolidate/run", clitest.JSONResponse(http.StatusOK, map[string]any{
		"triggered": true,
		"worker_id": "w-9",
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	flagFormat = "json"
	out, err := captureStdout(t, func() error {
		return consolidateRunCmd.RunE(consolidateRunCmd, nil)
	})
	if err != nil {
		t.Fatalf("json RunE: %v", err)
	}
	if !strings.Contains(out, `"worker_id": "w-9"`) && !strings.Contains(out, `"worker_id":"w-9"`) {
		t.Errorf("json output: got %q", out)
	}

	flagFormat = "yaml"
	out, err = captureStdout(t, func() error {
		return consolidateRunCmd.RunE(consolidateRunCmd, nil)
	})
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	if !strings.Contains(out, "workerid: w-9") && !strings.Contains(out, "worker_id: w-9") {
		t.Errorf("yaml output: got %q", out)
	}
}
