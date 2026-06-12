package main

// Final error-branch pass for cmd_hire.go: workspace gates, confirmation
// aborts (non-TTY without --yes), and transport/API failures on the hire
// and rehire POSTs. Helpers in cmd_skill_cov_test.go / cov2.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestHireRunECov2_NoWorkspace(t *testing.T) {
	covAuthGates(t, hireCmd, nil, true)
}

func TestRehireRunECov2_AuthGates(t *testing.T) {
	covAuthGates(t, rehireCmd, []string{"docs-writer-eph-1"}, true)
}

func TestHireRunECov2_ConfirmAborted(t *testing.T) {
	s := covSetup(t)
	covSetFlag(t, hireCmd, "crew", "docs")
	covSetFlag(t, hireCmd, "template", "docs-writer")
	covSetFlag(t, hireCmd, "reason", "x")
	// --yes NOT set: the test binary has no TTY, the plain-stdin fallback
	// reads EOF and must abort before any API call.
	_, err := covCaptureStdout(t, func() error {
		return hireCmd.RunE(hireCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want abort; got %v", err)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("aborted hire must not call the API; got %d calls", n)
	}
}

func TestHireRunECov2_TransportError(t *testing.T) {
	covSetupDead(t)
	covSetFlag(t, hireCmd, "yes", "true")
	covSetFlag(t, hireCmd, "crew", "docs")
	covSetFlag(t, hireCmd, "template", "docs-writer")
	covSetFlag(t, hireCmd, "reason", "x")
	if err := hireCmd.RunE(hireCmd, nil); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestRehireRunECov2_ConfirmAborted(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "docs-writer-eph-1"},
	}))
	covSetFlag(t, rehireCmd, "reason", "extend")
	_, err := covCaptureStdout(t, func() error {
		return rehireCmd.RunE(rehireCmd, []string{"docs-writer-eph-1"})
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want abort; got %v", err)
	}
	if n := len(s.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli1+"/rehire")); n != 0 {
		t.Errorf("aborted rehire must not POST; got %d calls", n)
	}
}

func TestRehireRunECov2_TransportError(t *testing.T) {
	covSetupDead(t)
	covSetFlag(t, rehireCmd, "yes", "true")
	covSetFlag(t, rehireCmd, "reason", "extend")
	// CUID arg short-circuits agent resolution; the rehire POST then
	// dies at transport level.
	if err := rehireCmd.RunE(rehireCmd, []string{covAgentIDCli1}); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestRehireRunECov2_ServerRejects(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "docs-writer-eph-1"},
	}))
	s.OnPost("/api/v1/agents/"+covAgentIDCli1+"/rehire",
		clitest.ErrorResponse(409, "ephemeral quota exhausted"))
	covSetFlag(t, rehireCmd, "yes", "true")
	covSetFlag(t, rehireCmd, "reason", "extend")
	err := rehireCmd.RunE(rehireCmd, []string{"docs-writer-eph-1"})
	if err == nil || !strings.Contains(err.Error(), "ephemeral quota exhausted") {
		t.Errorf("want server rejection surfaced; got %v", err)
	}
}
