package main

// Coverage tests for the cmd_hire.go gaps left by cmd_hire_test.go:
// the rehire RunE flow, the 202 pending-review branch of hire, the
// optional-flag bodies (model / parent-lead), and printHireResponse's
// pointer-field branches. Serial; cov* helpers in cmd_skill_cov_test.go.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestHireRunECov_MissingTemplate(t *testing.T) {
	covSetup(t)
	covSetFlag(t, hireCmd, "crew", "docs")
	err := hireCmd.RunE(hireCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--template is required") {
		t.Errorf("want template-required; got %v", err)
	}
}

func TestHireRunECov_MissingReason(t *testing.T) {
	covSetup(t)
	covSetFlag(t, hireCmd, "crew", "docs")
	covSetFlag(t, hireCmd, "template", "docs-writer")
	err := hireCmd.RunE(hireCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Errorf("want reason-required; got %v", err)
	}
}

func TestHireRunECov_AcceptedPendingReview(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/agents/hire", clitest.JSONResponse(http.StatusAccepted, map[string]any{
		"id": covAgentIDCli1, "workspace_id": covWorkspaceIDCli1,
		"slug": "docs-writer-eph-1", "name": "Docs Writer", "status": "STAGED",
		"ephemeral": true, "expires_at": nil,
		"hire_reason": "ship section 7", "pending_review": true,
		"inbox_item_id": "cinbox0123456789abcdefgh", "decision": "queued_inbox_blocking",
	}))
	covSetFlag(t, hireCmd, "yes", "true")
	covSetFlag(t, hireCmd, "crew", "docs")
	covSetFlag(t, hireCmd, "template", "docs-writer")
	covSetFlag(t, hireCmd, "reason", "ship section 7")
	covSetFlag(t, hireCmd, "model", "claude-haiku-4-5")
	covSetFlag(t, hireCmd, "parent-lead", "clead0123456789abcdefghi")

	out, err := covCaptureStdout(t, func() error {
		return hireCmd.RunE(hireCmd, nil)
	})
	if err != nil {
		t.Fatalf("202 is informational, not an error: %v", err)
	}
	if !strings.Contains(out, "awaiting inbox approval") {
		t.Errorf("output must explain pending state:\n%s", out)
	}
	if !strings.Contains(out, "PENDING APPROVAL (inbox cinbox0123456789abcdefgh)") {
		t.Errorf("output missing inbox pointer:\n%s", out)
	}
	body := covJSONBody(t, s.CallsFor("POST", "/api/v1/agents/hire")[0].Body)
	if body["model"] != "claude-haiku-4-5" {
		t.Errorf("model = %v", body["model"])
	}
	if body["parent_lead_id"] != "clead0123456789abcdefghi" {
		t.Errorf("parent_lead_id = %v", body["parent_lead_id"])
	}
	if _, has := body["ttl_minutes"]; has {
		t.Errorf("ttl_minutes must be omitted when --ttl not set; got %v", body)
	}
}

func TestHireRunECov_ServerError(t *testing.T) {
	s := covSetup(t)
	s.OnPost("/api/v1/agents/hire", clitest.ErrorResponse(403, "autonomy_level=strict rejects hires"))
	covSetFlag(t, hireCmd, "yes", "true")
	covSetFlag(t, hireCmd, "crew", "docs")
	covSetFlag(t, hireCmd, "template", "docs-writer")
	covSetFlag(t, hireCmd, "reason", "x")

	err := hireCmd.RunE(hireCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "strict") {
		t.Errorf("want server rejection surfaced; got %v", err)
	}
}

// ─── rehire ──────────────────────────────────────────────────────────────

func TestRehireRunECov_MissingReason(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "docs-writer-eph-1"},
	}))
	err := rehireCmd.RunE(rehireCmd, []string{"docs-writer-eph-1"})
	if err == nil || !strings.Contains(err.Error(), "--reason is required") {
		t.Errorf("want reason-required; got %v", err)
	}
}

func TestRehireRunECov_Happy(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "docs-writer-eph-1"},
	}))
	rehirePath := "/api/v1/agents/" + covAgentIDCli1 + "/rehire"
	s.OnPost(rehirePath, clitest.JSONResponse(200, map[string]any{
		"id": covAgentIDCli1, "workspace_id": covWorkspaceIDCli1,
		"slug": "docs-writer-eph-1", "name": "Docs Writer", "status": "IDLE",
		"ephemeral": true, "expires_at": "2026-06-12T15:00:00Z",
		"hire_reason": "[2026-06-12] rehire: finish section 8",
		"decision":    "auto_log_journal",
	}))
	covSetFlag(t, rehireCmd, "yes", "true")
	covSetFlag(t, rehireCmd, "reason", "finish section 8")
	covSetFlag(t, rehireCmd, "ttl", "90")

	out, err := covCaptureStdout(t, func() error {
		return rehireCmd.RunE(rehireCmd, []string{"docs-writer-eph-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Agent rehired", "2026-06-12T15:00:00Z", "auto_log_journal"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	calls := s.CallsFor("POST", rehirePath)
	if len(calls) != 1 {
		t.Fatalf("want 1 rehire POST, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["reason"] != "finish section 8" {
		t.Errorf("reason = %v", body["reason"])
	}
	if body["ttl_minutes"].(float64) != 90 {
		t.Errorf("ttl_minutes = %v, want 90", body["ttl_minutes"])
	}
}

func TestRehireRunECov_AgentNotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	err := rehireCmd.RunE(rehireCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
		t.Errorf("want not-found error; got %v", err)
	}
}

// ─── printHireResponse ───────────────────────────────────────────────────

func TestPrintHireResponseCov_BadJSON(t *testing.T) {
	saveCLIState(t)
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("not json at all")),
	}
	if err := printHireResponse(resp, "headline"); err == nil {
		t.Error("want decode error for malformed body; got nil")
	}
}
