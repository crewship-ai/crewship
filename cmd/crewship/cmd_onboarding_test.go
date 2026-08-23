package main

// CLI acceptance tests for `crewship onboarding proposal create|get|apply`
// (CLAUDE.md: "Every API endpoint gets a CLI command, and its acceptance
// test drives the CLI binary"). Follows the clitest stub-server pattern in
// cmd_parity_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestOnboardingProposalCreate_TableAndJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/onboarding/proposals", clitest.JSONResponse(201, map[string]any{
		"id": "prop_123", "workspace_id": covWorkspaceIDCli10, "created_by": "u1",
		"created_at": "2026-08-22T00:00:00Z", "applied_at": nil, "status": "PENDING",
		"payload": map[string]any{
			"crew_name": "Support Crew", "crew_slug": "support-crew",
			"template_slug": "support", "llm_provider": "ANTHROPIC", "llm_model": "claude-override",
			"agents": []map[string]any{
				{"name": "Lead", "slug": "lead-support-crew", "role_title": "Lead", "llm_provider": "ANTHROPIC", "llm_model": "claude-override", "system_prompt": "lead"},
			},
		},
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, onboardingProposalCreateCmd, "crew-name", "Support Crew")
	setFlagCovCli10(t, onboardingProposalCreateCmd, "template-slug", "support")
	setFlagCovCli10(t, onboardingProposalCreateCmd, "llm-model", "claude-override")
	onboardingProposalCreateCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return onboardingProposalCreateCmd.RunE(onboardingProposalCreateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "prop_123") || !strings.Contains(out, "support-crew") {
		t.Errorf("output missing expected fields: %q", out)
	}

	calls := s.CallsFor("POST", "/api/v1/onboarding/proposals")
	if len(calls) != 1 {
		t.Fatalf("create calls = %d, want 1", len(calls))
	}
	var body struct {
		CrewName     string `json:"crew_name"`
		TemplateSlug string `json:"template_slug"`
		LLMModel     string `json:"llm_model"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.CrewName != "Support Crew" || body.TemplateSlug != "support" || body.LLMModel != "claude-override" {
		t.Errorf("request body = %+v", body)
	}
}

func TestOnboardingProposalCreate_RequiresFlags(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCli10(t, s.URL())
	onboardingProposalCreateCmd.SetContext(context.Background())

	if err := onboardingProposalCreateCmd.RunE(onboardingProposalCreateCmd, nil); err == nil {
		t.Fatal("expected an error when --crew-name/--template-slug are missing")
	}
}

func TestOnboardingProposalGet_PrintsStoredProposal(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/onboarding/proposals/prop_123", clitest.JSONResponse(200, map[string]any{
		"id": "prop_123", "workspace_id": covWorkspaceIDCli10, "created_by": "u1",
		"created_at": "2026-08-22T00:00:00Z", "status": "PENDING",
		"payload": map[string]any{
			"crew_name": "Support Crew", "crew_slug": "support-crew", "template_slug": "support",
			"agents": []map[string]any{{"name": "Lead", "slug": "lead-support-crew"}},
		},
	}))
	covSetupCli10(t, s.URL())
	onboardingProposalGetCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return onboardingProposalGetCmd.RunE(onboardingProposalGetCmd, []string{"prop_123"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "support-crew") {
		t.Errorf("output missing crew slug: %q", out)
	}
}

func TestOnboardingProposalGet_NotFoundSurfacesError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/onboarding/proposals/ghost", clitest.ErrorResponse(404, "Proposal not found"))
	covSetupCli10(t, s.URL())
	onboardingProposalGetCmd.SetContext(context.Background())

	if err := onboardingProposalGetCmd.RunE(onboardingProposalGetCmd, []string{"ghost"}); err == nil {
		t.Fatal("expected an error for a 404")
	}
}

func TestOnboardingProposalApply_SendsNoBody(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/onboarding/proposals/prop_123/apply", clitest.JSONResponse(201, map[string]any{
		"proposal_id": "prop_123", "status": "APPLIED", "already_applied": false,
		"crew": map[string]any{
			"crew_id": "crew_1", "crew_name": "Support Crew", "crew_slug": "support-crew",
			"agent_count": 1, "agent_ids": []string{"agent_1"},
		},
	}))
	covSetupCli10(t, s.URL())
	onboardingProposalApplyCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return onboardingProposalApplyCmd.RunE(onboardingProposalApplyCmd, []string{"prop_123"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "crew_1") || !strings.Contains(out, "Support Crew") {
		t.Errorf("output missing crew fields: %q", out)
	}

	calls := s.CallsFor("POST", "/api/v1/onboarding/proposals/prop_123/apply")
	if len(calls) != 1 {
		t.Fatalf("apply calls = %d, want 1", len(calls))
	}
	// The CLI must send no body at all — apply's only input is the id in
	// the path (internal/api/onboarding_proposal.go's Apply doc comment).
	if len(strings.TrimSpace(string(calls[0].Body))) != 0 && string(calls[0].Body) != "null" {
		t.Errorf("apply request body = %q, want empty/null (apply takes no content)", calls[0].Body)
	}
}

func TestOnboardingProposalApply_AlreadyApplied(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/onboarding/proposals/prop_123/apply", clitest.JSONResponse(200, map[string]any{
		"proposal_id": "prop_123", "status": "APPLIED", "already_applied": true,
		"crew": map[string]any{
			"crew_id": "crew_1", "crew_name": "Support Crew", "crew_slug": "support-crew",
			"agent_count": 1, "agent_ids": []string{"agent_1"},
		},
	}))
	covSetupCli10(t, s.URL())
	onboardingProposalApplyCmd.SetContext(context.Background())

	// PrintSuccess writes the headline to stderr; capture that instead of
	// stdout (which carries the AutoDetail table).
	errOut, err := captureStderrCov(t, func() error {
		return onboardingProposalApplyCmd.RunE(onboardingProposalApplyCmd, []string{"prop_123"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(errOut, "Already applied") {
		t.Errorf("expected an 'already applied' headline, got %q", errOut)
	}
}

// Acceptance tests for `crewship onboarding setup-agent start`
// (internal/api/onboarding_setup_agent.go's StartSetupAgent). Follows the
// same clitest stub-server pattern as the proposal tests above.

func TestOnboardingSetupAgentStart_Success(t *testing.T) {
	// Prevents a regression where the CLI stops parsing the success shape
	// (agent_id/session_id) or stops printing both ids for the user to copy
	// into the chat client.
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/onboarding/setup-agent/start", clitest.JSONResponse(200, map[string]any{
		"agent_id": "agent_setup_1", "session_id": "chat_setup_1",
	}))
	covSetupCli10(t, s.URL())
	onboardingSetupAgentStartCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return onboardingSetupAgentStartCmd.RunE(onboardingSetupAgentStartCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "agent_setup_1") || !strings.Contains(out, "chat_setup_1") {
		t.Errorf("output missing agent/session ids: %q", out)
	}

	calls := s.CallsFor("POST", "/api/v1/onboarding/setup-agent/start")
	if len(calls) != 1 {
		t.Fatalf("start calls = %d, want 1", len(calls))
	}
}

func TestOnboardingSetupAgentStart_CredentialRequiredIsLegibleAndExitsValidation(t *testing.T) {
	// Prevents a regression where the CLI hands the user a bare "API error
	// (428): ..." with no named cause/fix (this task's requirement #2), and
	// where the 428 collapses into the same exit code as an unclassified
	// failure instead of the deliberately chosen cli.ExitValidation
	// (requirement #3) — a caller scripting around this needs to tell "go
	// add a credential" apart from "something else broke".
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/onboarding/setup-agent/start", clitest.JSONResponse(428, map[string]any{
		"error":  "Add a model token before starting the setup agent — it runs in a container and cannot answer without one.",
		"reason": "credential_required",
	}))
	covSetupCli10(t, s.URL())
	onboardingSetupAgentStartCmd.SetContext(context.Background())

	err := onboardingSetupAgentStartCmd.RunE(onboardingSetupAgentStartCmd, nil)
	if err == nil {
		t.Fatal("expected an error for a 428 credential_required response")
	}
	if !strings.Contains(err.Error(), "model credential") {
		t.Errorf("error should name the cause (missing model credential), got %q", err)
	}
	if !strings.Contains(err.Error(), "crewship credential create") {
		t.Errorf("error should name the fix (crewship credential create), got %q", err)
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitValidation {
		t.Errorf("exit code = %d, want cli.ExitValidation (%d)", code, cli.ExitValidation)
	}
}

func TestOnboardingSetupAgentStart_TransportErrorIsConnectionExit(t *testing.T) {
	// Prevents a regression where a network failure before any response is
	// read gets misreported as the credential-required case (or as a plain
	// ExitGeneric) instead of the transport-failure exit code — a caller
	// retrying on ExitConnection but not on ExitValidation needs these two
	// kept apart.
	covSetupCli10(t, "http://127.0.0.1:1") // unroutable port: connection refused, no real traffic
	onboardingSetupAgentStartCmd.SetContext(context.Background())

	err := onboardingSetupAgentStartCmd.RunE(onboardingSetupAgentStartCmd, nil)
	if err == nil {
		t.Fatal("expected a transport error, got nil")
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitConnection {
		t.Errorf("exit code = %d, want cli.ExitConnection (%d)", code, cli.ExitConnection)
	}
}
