package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func stubCostEndpoints(s *clitest.StubServer) {
	s.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []topSpenderRow{
			{ScopeKind: "agent", ScopeID: "viktor", CostUSD: 1.5, CallCount: 12},
		},
	}))
	s.OnGet("/api/v1/paymaster/spend/by-crew", clitest.JSONResponse(200, map[string]any{
		"rows": []crewSpendRow{
			{CrewID: "backend", CostUSD: 0.5, CallCount: 3, InTokens: 100, OutTokens: 50},
			{CrewID: "frontend", CostUSD: 2.5, CallCount: 7, InTokens: 400, OutTokens: 80},
		},
	}))
	s.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{
		"rows": []subUsageRow{
			{Plan: "max", Provider: "anthropic", CallCount: 4, InTokens: 10, OutTokens: 5},
		},
	}))
}

func TestCostRunE_TablePrintsAllSections(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCostEndpoints(s)
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return costCmd.RunE(costCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Cost summary", "Top spenders", "agent/viktor", "By crew", "Subscription plans", "max/anthropic"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// printCostByCrew sorts descending by cost — frontend ($2.5) must
	// appear before backend ($0.5).
	if fi, bi := strings.Index(out, "frontend"), strings.Index(out, "backend"); fi == -1 || bi == -1 || fi > bi {
		t.Errorf("by-crew rows not sorted by cost desc: frontend@%d backend@%d", fi, bi)
	}
	// Header totals: 0.5 + 2.5 = 3.0 across 10 calls and 2 crews.
	if !strings.Contains(out, "calls=10") || !strings.Contains(out, "crews=2") {
		t.Errorf("header totals wrong:\n%s", out)
	}
}

func TestCostRunE_JSONFormat(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCostEndpoints(s)
	covSetupCli10(t, s.URL())
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return costCmd.RunE(costCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{`"range"`, `"top"`, `"crews"`, `"subscriptions"`, `"viktor"`} {
		if !strings.Contains(out, want) {
			t.Errorf("json missing %q:\n%s", want, out)
		}
	}
}

func TestCostRunE_YAMLFormat(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubCostEndpoints(s)
	covSetupCli10(t, s.URL())
	flagFormat = "yaml"

	out, err := captureStdoutCovCli10(t, func() error {
		return costCmd.RunE(costCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "crews:") || !strings.Contains(out, "viktor") {
		t.Errorf("yaml output missing keys:\n%s", out)
	}
}

func TestCostRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := costCmd.RunE(costCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestCostRunE_TopSpendersError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/paymaster/top-spenders", clitest.ErrorResponse(502, "rollup down"))
	covSetupCli10(t, s.URL())
	if err := costCmd.RunE(costCmd, nil); err == nil {
		t.Error("expected error when top-spenders fails")
	}
}

func TestCostRunE_CrewSpendError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{"rows": []topSpenderRow{}}))
	s.OnGet("/api/v1/paymaster/spend/by-crew", clitest.ErrorResponse(500, "no crew rollup"))
	covSetupCli10(t, s.URL())
	if err := costCmd.RunE(costCmd, nil); err == nil {
		t.Error("expected error when by-crew fails")
	}
}

func TestFetchTopSpendersCov_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/paymaster/top-spenders", clitest.ErrorResponse(500, "boom"))
	if _, err := fetchTopSpenders(cli.NewClient(s.URL(), "t", covWorkspaceIDCli10), "24h", 5); err == nil {
		t.Error("expected error")
	}
}

func TestFetchCrewSpendCov_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/paymaster/spend/by-crew", clitest.ErrorResponse(500, "boom"))
	if _, err := fetchCrewSpend(cli.NewClient(s.URL(), "t", covWorkspaceIDCli10), "7d"); err == nil {
		t.Error("expected error")
	}
}

func TestPrintCostHelpers_EmptyRowsPrintNothing(t *testing.T) {
	out, _ := captureStdoutCovCli10(t, func() error {
		printCostTopSpenders(nil)
		printCostByCrew(nil)
		printCostSubscriptions(nil)
		return nil
	})
	if out != "" {
		t.Errorf("expected silence for empty rows, got %q", out)
	}
}

func TestPrintCostHeader_DefaultsRange(t *testing.T) {
	out, _ := captureStdoutCovCli10(t, func() error {
		printCostHeader("", []crewSpendRow{{CrewID: "x", CostUSD: 1.25, CallCount: 4}})
		return nil
	})
	if !strings.Contains(out, "range=24h") {
		t.Errorf("empty range should default to 24h: %q", out)
	}
	if !strings.Contains(out, "calls=4") || !strings.Contains(out, "crews=1") {
		t.Errorf("totals missing: %q", out)
	}
}

func TestPrintCostSubscriptions_LastUsed(t *testing.T) {
	last := "2026-06-01T00:00:00Z"
	out, _ := captureStdoutCovCli10(t, func() error {
		printCostSubscriptions([]subUsageRow{
			{Plan: "max", Provider: "anthropic", CallCount: 2, InTokens: 5, OutTokens: 5, LastUsedAt: &last},
		})
		return nil
	})
	if !strings.Contains(out, "last="+last) {
		t.Errorf("last_used_at not rendered: %q", out)
	}
}
