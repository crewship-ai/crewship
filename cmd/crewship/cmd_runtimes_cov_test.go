package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func stubRuntimeCatalog(s *clitest.StubServer) {
	s.OnGet("/api/v1/runtimes/catalog", clitest.JSONResponse(200, map[string]any{
		"runtimes": []runtimeEntry{
			{Name: "Node.js", Tool: "node", Description: "JS runtime", Category: "languages",
				Icon: "node-icon", Versions: []string{"20", "22"}, DefaultVersion: "22", Backends: []string{"core"}},
			{Name: "Terraform", Tool: "terraform", Category: "tools"},
		},
	}))
}

func TestFetchRuntimeCatalogCov_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runtimes/catalog", clitest.ErrorResponse(502, "catalog down"))
	covSetupCli10(t, s.URL())
	_, err := fetchRuntimeCatalog("")
	if err == nil || !strings.Contains(err.Error(), "catalog down") {
		t.Errorf("expected 502 surfaced, got %v", err)
	}
}

func TestRuntimesListRunE_CategoryFilter(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRuntimeCatalog(s)
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, runtimesListCmd, "category", "LANGUAGES")

	out, err := captureStdoutCovCli10(t, func() error {
		return runtimesListCmd.RunE(runtimesListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "node") {
		t.Errorf("languages entry missing:\n%s", out)
	}
	if strings.Contains(out, "terraform") {
		t.Errorf("category filter must drop tools entries (case-insensitive):\n%s", out)
	}
}

func TestRuntimesInfoRunE_FoundCaseInsensitive(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRuntimeCatalog(s)
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return runtimesInfoCmd.RunE(runtimesInfoCmd, []string{"NODE"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Node.js", "JS runtime", "20, 22", "22", "core"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestRuntimesInfoRunE_DashPlaceholders(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRuntimeCatalog(s)
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return runtimesInfoCmd.RunE(runtimesInfoCmd, []string{"terraform"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// terraform has no versions/default/backends/description → all —.
	if strings.Count(out, "—") < 4 {
		t.Errorf("expected — placeholders for empty fields:\n%s", out)
	}
}

func TestRuntimesInfoRunE_NotFound(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRuntimeCatalog(s)
	covSetupCli10(t, s.URL())

	err := runtimesInfoCmd.RunE(runtimesInfoCmd, []string{"cobol"})
	if err == nil || !strings.Contains(err.Error(), "runtime not found: cobol") {
		t.Errorf("expected not-found, got %v", err)
	}
}

func TestRuntimesInfoRunE_CatalogError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runtimes/catalog", clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := runtimesInfoCmd.RunE(runtimesInfoCmd, []string{"node"}); err == nil {
		t.Error("expected catalog error to propagate")
	}
}
