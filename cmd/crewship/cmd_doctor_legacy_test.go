//go:build !clionly

package main

// `crewship doctor` surfaces the legacy-C1-resource status from the
// authenticated admin legacy-resources endpoint. Tested against the CLI stub
// server (covStub wires cliCfg + auth at the stub URL).

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRunCheckLegacyResources(t *testing.T) {
	cases := []struct {
		name       string
		handler    clitest.Handler
		wantStatus string
		wantDetail string
		wantHint   string
	}{
		{
			name:       "present warns with prune hint",
			handler:    clitest.JSONResponse(200, map[string]any{"present": true}),
			wantStatus: "WARN",
			wantDetail: "orphaned pre-C1",
			wantHint:   "prune-legacy",
		},
		{
			name:       "clean passes",
			handler:    clitest.JSONResponse(200, map[string]any{"present": false}),
			wantStatus: "PASS",
			wantDetail: "no orphaned",
		},
		{
			name:       "non-docker server is informational",
			handler:    clitest.ErrorResponse(503, "docker not configured"),
			wantStatus: "INFO",
			wantDetail: "no docker provider",
		},
		{
			name:       "forbidden is informational, not a duplicate FAIL",
			handler:    clitest.ErrorResponse(403, "admin role required"),
			wantStatus: "INFO",
			wantDetail: "not authorized",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := covStub(t)
			stub.OnGet("/api/v1/admin/legacy-resources", tc.handler)
			got := runCheckLegacyResources(context.Background())
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (detail: %s)", got.status, tc.wantStatus, got.detail)
			}
			if !strings.Contains(got.detail, tc.wantDetail) {
				t.Fatalf("detail %q does not contain %q", got.detail, tc.wantDetail)
			}
			if tc.wantHint != "" && !strings.Contains(got.hint, tc.wantHint) {
				t.Fatalf("hint %q does not contain %q", got.hint, tc.wantHint)
			}
		})
	}
}

func TestRunCheckLegacyResources_NotLoggedIn(t *testing.T) {
	stub := covStub(t)
	_ = stub
	cliCfg.Token = "" // simulate a CLI that hasn't logged in
	got := runCheckLegacyResources(context.Background())
	if got.status != "INFO" || !strings.Contains(got.detail, "not logged in") {
		t.Fatalf("want INFO 'not logged in', got %q / %q", got.status, got.detail)
	}
}
