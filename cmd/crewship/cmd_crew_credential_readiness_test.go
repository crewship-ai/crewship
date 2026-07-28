package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covCrewIDReadiness = "ccrew0000000000000000aaa"

// covStubReadinessCrew makes the slug→id resolve succeed against the stub.
func covStubReadinessCrew(s *clitest.StubServer) {
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDReadiness, "slug": "ops"},
	}))
}

// The whole point of the command: name the credential AND the feature
// that would fix it. A report that only said "something is missing"
// would leave the user exactly where they started.
func TestCrewCredentialReadinessCmd_ReportsGap(t *testing.T) {
	stub := covStub(t)
	covStubReadinessCrew(stub)
	stub.OnGet("/api/v1/crews/"+covCrewIDReadiness+"/credential-readiness",
		clitest.JSONResponse(200, map[string]any{
			"crew_id":   covCrewIDReadiness,
			"crew_slug": "ops",
			"tools":     []string{"terraform"},
			"checked":   1,
			"gaps": []map[string]string{{
				"credential_id":   "cred-gh",
				"credential_name": "gh-pat",
				"provider":        "GITHUB",
				"tool":            "gh",
				"feature":         "ghcr.io/devcontainers/features/github-cli:1",
				"feature_id":      "github-cli",
			}},
		}))

	out := covCaptureStdoutCli3(t, func() {
		if err := crewCredentialReadinessCmd.RunE(crewCredentialReadinessCmd, []string{"ops"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"gh-pat", "GITHUB", "gh", "ghcr.io/devcontainers/features/github-cli:1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The command must not imply it fixed anything — adding the feature
	// is a separate, user-confirmed action.
	if !strings.Contains(out, "crew config") {
		t.Errorf("output should point at `crewship crew config`, got:\n%s", out)
	}

	calls := stub.CallsFor("GET", "/api/v1/crews/"+covCrewIDReadiness+"/credential-readiness")
	if len(calls) != 1 {
		t.Fatalf("expected 1 readiness call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "workspace_id") {
		t.Errorf("workspace_id should be sent: %q", calls[0].Query)
	}
}

// The clean case must read as clean, not as an empty table the user has
// to interpret.
func TestCrewCredentialReadinessCmd_NoGaps(t *testing.T) {
	stub := covStub(t)
	covStubReadinessCrew(stub)
	stub.OnGet("/api/v1/crews/"+covCrewIDReadiness+"/credential-readiness",
		clitest.JSONResponse(200, map[string]any{
			"crew_id":   covCrewIDReadiness,
			"crew_slug": "ops",
			"tools":     []string{"gh"},
			"checked":   1,
			"gaps":      []map[string]string{},
		}))

	out := covCaptureStdoutCli3(t, func() {
		if err := crewCredentialReadinessCmd.RunE(crewCredentialReadinessCmd, []string{"ops"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "ops") || !strings.Contains(strings.ToLower(out), "credential") {
		t.Errorf("expected an explicit all-clear line, got:\n%s", out)
	}
	if strings.Contains(out, "FEATURE") {
		t.Errorf("no gaps should mean no table, got:\n%s", out)
	}
}

func TestCrewCredentialReadinessCmd_Errors(t *testing.T) {
	t.Run("crew not found", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
		err := crewCredentialReadinessCmd.RunE(crewCredentialReadinessCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "crew not found") {
			t.Fatalf("expected crew-not-found, got %v", err)
		}
	})

	t.Run("server error surfaces", func(t *testing.T) {
		stub := covStub(t)
		covStubReadinessCrew(stub)
		stub.OnGet("/api/v1/crews/"+covCrewIDReadiness+"/credential-readiness",
			clitest.ErrorResponse(500, "readiness broke"))
		err := crewCredentialReadinessCmd.RunE(crewCredentialReadinessCmd, []string{"ops"})
		if err == nil || !strings.Contains(err.Error(), "readiness broke") {
			t.Fatalf("expected server error, got %v", err)
		}
	})

	t.Run("malformed body surfaces", func(t *testing.T) {
		stub := covStub(t)
		covStubReadinessCrew(stub)
		stub.OnGet("/api/v1/crews/"+covCrewIDReadiness+"/credential-readiness",
			clitest.TextResponse(200, "{broken"))
		if err := crewCredentialReadinessCmd.RunE(crewCredentialReadinessCmd, []string{"ops"}); err == nil {
			t.Fatal("expected decode error")
		}
	})
}
