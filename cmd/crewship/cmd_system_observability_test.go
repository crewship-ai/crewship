package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSystemLogLevelSet_SendsLevelAndTTL(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut("/api/v1/admin/log-level", clitest.JSONResponse(200, map[string]any{
		"level": "debug", "baseline": "info", "expires_at": "2026-07-03T10:00:00Z",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, systemLogLevelSetCmd, "level", "debug")
	setFlagCovCli10(t, systemLogLevelSetCmd, "ttl", "15m")
	systemLogLevelSetCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return systemLogLevelSetCmd.RunE(systemLogLevelSetCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "debug") {
		t.Errorf("output missing new level: %q", out)
	}
	calls := s.CallsFor("PUT", "/api/v1/admin/log-level")
	if len(calls) != 1 {
		t.Fatalf("PUT calls = %d, want 1", len(calls))
	}
	var body struct {
		Level      string `json:"level"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.Level != "debug" || body.TTLSeconds != 900 {
		t.Errorf("body = %+v, want level=debug ttl_seconds=900", body)
	}
}

func TestSystemLogLevelSet_RequiresLevel(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCli10(t, s.URL())
	// Reset the flag left set by a sibling test in this file.
	setFlagCovCli10(t, systemLogLevelSetCmd, "level", "")
	systemLogLevelSetCmd.SetContext(context.Background())

	_, err := captureStdoutCovCli10(t, func() error {
		return systemLogLevelSetCmd.RunE(systemLogLevelSetCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "--level is required") {
		t.Errorf("want --level required error, got %v", err)
	}
	if got := len(s.CallsFor("PUT", "/api/v1/admin/log-level")); got != 0 {
		t.Errorf("PUT calls = %d, want 0 (validation before request)", got)
	}
}

func TestSystemHealth_ParsesDiskAndLevel(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/admin/health", clitest.JSONResponse(200, map[string]any{
		"uptime_seconds": 1234,
		"log_level":      map[string]any{"level": "info", "baseline": "info"},
		"disk":           map[string]any{"path": "/data", "total_bytes": 100, "free_bytes": 40, "used_bytes": 60, "used_pct": 60.0},
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"
	systemHealthCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return systemHealthCmd.RunE(systemHealthCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "used_pct") || !strings.Contains(out, "uptime_seconds") {
		t.Errorf("health output missing fields: %q", out)
	}
}
