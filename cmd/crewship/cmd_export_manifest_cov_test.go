package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRunExportWorkspace_EmptyWorkspaceToStdout(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return runExportWorkspace(exportWorkspaceCmd, nil)
	})
	if err != nil {
		t.Fatalf("runExportWorkspace: %v", err)
	}
	if !strings.Contains(out, "kind: Workspace") {
		t.Errorf("workspace manifest missing kind:\n%s", out)
	}
}

func TestRunExportWorkspace_WritesOutputFile(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	covSetupCli10(t, s.URL())
	outPath := filepath.Join(t.TempDir(), "ws.yaml")
	setFlagCovCli10(t, exportWorkspaceCmd, "output", outPath)

	stderr, err := captureStderrCov(t, func() error {
		return runExportWorkspace(exportWorkspaceCmd, nil)
	})
	if err != nil {
		t.Fatalf("runExportWorkspace: %v", err)
	}
	if !strings.Contains(stderr, "wrote "+outPath) {
		t.Errorf("wrote-banner missing: %q", stderr)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(b), "kind: Workspace") {
		t.Errorf("file content wrong:\n%s", b)
	}
}

func TestRunExportWorkspace_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := runExportWorkspace(exportWorkspaceCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestRunExportWorkspace_ListCrewsError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.ErrorResponse(500, "crews unavailable"))
	covSetupCli10(t, s.URL())
	err := runExportWorkspace(exportWorkspaceCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "export workspace:") {
		t.Errorf("expected wrapped export error, got %v", err)
	}
}

func TestRunExportCrew_CrewNotFound(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{}))
	covSetupCli10(t, s.URL())

	err := runExportCrew(exportCrewCmd, []string{"ghost-crew"})
	if err == nil || !strings.Contains(err.Error(), `export crew "ghost-crew"`) {
		t.Errorf("expected wrapped crew error, got %v", err)
	}
}

func TestRunExportCrew_EmptyCrewToStdout(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	desc := "API team"
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli10, "slug": "backend", "name": "Backend", "description": desc},
	}))
	// Crew exists but has no agents; integrations listing 404s and is
	// tolerated (servers=nil).
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return runExportCrew(exportCrewCmd, []string{"backend"})
	})
	if err != nil {
		t.Fatalf("runExportCrew: %v", err)
	}
	for _, want := range []string{"kind: Crew", "slug: backend", "API team"} {
		if !strings.Contains(out, want) {
			t.Errorf("crew manifest missing %q:\n%s", want, out)
		}
	}
}

func TestRunExportCrew_WritesOutputFile(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]any{
		{"id": covCrewIDCli10, "slug": "backend", "name": "Backend"},
	}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{}))
	covSetupCli10(t, s.URL())
	outPath := filepath.Join(t.TempDir(), "crew.yaml")
	setFlagCovCli10(t, exportCrewCmd, "output", outPath)

	if _, err := captureStderrCov(t, func() error {
		return runExportCrew(exportCrewCmd, []string{"backend"})
	}); err != nil {
		t.Fatalf("runExportCrew: %v", err)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(b), "kind: Crew") {
		t.Errorf("file content wrong:\n%s", b)
	}
}

func TestRunExportCrew_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := runExportCrew(exportCrewCmd, []string{"backend"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}
