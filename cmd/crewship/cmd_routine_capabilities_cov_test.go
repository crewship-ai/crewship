package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// A CUID-shaped arg short-circuits resolveCrewID so only the capabilities GET
// is exercised (no /api/v1/crews list round-trip to stub).
const covCapCrewID = "clcapcrew0000000000001"
const covCapPath = "/api/v1/crews/" + covCapCrewID + "/capabilities"

func capsStubBody() cli.CrewCapabilities {
	var c cli.CrewCapabilities
	c.CrewSlug = "acct"
	c.Container.Tools = []struct {
		Name string `json:"name"`
	}{{Name: "terraform"}}
	c.Integrations = []struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"display_name"`
		Tools       []string `json:"tools"`
	}{{Name: "gmail", DisplayName: "Gmail", Tools: []string{"GMAIL_FETCH_EMAIL"}}}
	c.Agents = []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}{{Slug: "parse", Name: "Parser"}}
	c.Runtimes.Code.Wired = []string{"cel", "expr"}
	c.Runtimes.Code.ReservedUnwired = []string{"python"}
	c.Runtimes.ScriptInterpreters = map[string]string{".py": "python3"}
	c.Schema = []byte(`{"type":"object"}`)
	return c
}

func TestRoutineCapabilitiesRunE_HumanSummary(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covCapPath, clitest.JSONResponse(200, capsStubBody()))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return routineCapabilitiesCmd.RunE(routineCapabilitiesCmd, []string{covCapCrewID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"crew acct", "parse", "terraform", "Gmail: GMAIL_FETCH_EMAIL", "cel, expr", ".py→python3"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	// Reserved runtimes flagged as do-not-use.
	if !strings.Contains(out, "reserved") || !strings.Contains(out, "python") {
		t.Errorf("reserved runtimes not flagged:\n%s", out)
	}
}

func TestRoutineCapabilitiesRunE_FormatJSONIncludesSchema(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covCapPath, clitest.JSONResponse(200, capsStubBody()))
	covSetupCli10(t, s.URL())
	defer func() { flagFormat = "" }()
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return routineCapabilitiesCmd.RunE(routineCapabilitiesCmd, []string{covCapCrewID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Full bundle includes the schema (omitted from the human view).
	if !strings.Contains(out, `"schema"`) || !strings.Contains(out, `"crew_slug": "acct"`) {
		t.Errorf("json bundle missing schema/slug:\n%s", out)
	}
}

func TestRoutineCapabilitiesRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet(covCapPath, clitest.ErrorResponse(404, "Crew not found"))
	covSetupCli10(t, s.URL())
	if err := routineCapabilitiesCmd.RunE(routineCapabilitiesCmd, []string{covCapCrewID}); err == nil {
		t.Error("expected error from 404")
	}
}
