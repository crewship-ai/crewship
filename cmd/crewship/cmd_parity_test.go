package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity for the recipes / connectors / feedback API surfaces —
// endpoints that existed server-side with no CLI caller (violating the
// "every endpoint gets a CLI command" rule). Each test drives the cobra
// RunE against a stub server, the same contract an agent will use.

// ─── recipe ──────────────────────────────────────────────────────────────

func TestRecipeList_TableAndJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/recipes", clitest.JSONResponse(200, []map[string]any{{
		"slug": "code-review-crew", "name": "Code Review Crew",
		"description": "Reviews PRs", "crew_slug": "code-review",
		"credentials": []map[string]any{{"env_var_name": "GITHUB_TOKEN"}},
		"mcp_servers": []map[string]any{{"name": "github"}},
	}}))
	covSetupCli10(t, s.URL())
	recipeListCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return recipeListCmd.RunE(recipeListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "code-review-crew") {
		t.Errorf("table output missing slug: %q", out)
	}

	flagFormat = "json"
	out, err = captureStdoutCovCli10(t, func() error {
		return recipeListCmd.RunE(recipeListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE json: %v", err)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
		t.Fatalf("--format json output does not parse: %v\n%s", jerr, out)
	}
	if len(rows) != 1 || rows[0]["slug"] != "code-review-crew" {
		t.Errorf("json rows = %+v", rows)
	}
}

func TestRecipeInstall_SendsCredentialValues(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/recipes/code-review-crew/install", clitest.JSONResponse(201, map[string]any{
		"crew_id": "ccrew123", "crew_slug": "code-review",
		"credentials_added":  []string{"GITHUB_TOKEN"},
		"credentials_reused": []string{},
		"mcp_servers_added":  []string{"github"},
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, recipeInstallCmd, "credential", "GITHUB_TOKEN=ghp_secret")
	recipeInstallCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return recipeInstallCmd.RunE(recipeInstallCmd, []string{"code-review-crew"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "code-review") {
		t.Errorf("install output missing crew slug: %q", out)
	}

	calls := s.CallsFor("POST", "/api/v1/recipes/code-review-crew/install")
	if len(calls) != 1 {
		t.Fatalf("install calls = %d, want 1", len(calls))
	}
	var body struct {
		CredentialValues map[string]string `json:"credential_values"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.CredentialValues["GITHUB_TOKEN"] != "ghp_secret" {
		t.Errorf("credential_values = %+v", body.CredentialValues)
	}
}

// ─── connector ───────────────────────────────────────────────────────────

func TestConnectorList_JSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/connectors", clitest.JSONResponse(200, []map[string]any{{
		"id": "slack", "name": "Slack", "category": "chat",
		"auth_mode": "token", "description": "Slack workspace",
	}}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"
	connectorListCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return connectorListCmd.RunE(connectorListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
		t.Fatalf("json output does not parse: %v\n%s", jerr, out)
	}
	if len(rows) != 1 || rows[0]["id"] != "slack" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestConnectorVerify_FailedProbeIsNonZero(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/connectors/slack/verify", clitest.JSONResponse(200, map[string]any{
		"ok": false, "message": "invalid token",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, connectorVerifyCmd, "field", "SLACK_TOKEN=bad")
	connectorVerifyCmd.SetContext(context.Background())

	_, err := captureStdoutCovCli10(t, func() error {
		return connectorVerifyCmd.RunE(connectorVerifyCmd, []string{"slack"})
	})
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("verify with ok=false should fail with the probe message, got %v", err)
	}
}

func TestConnectorInstall_SendsFieldsAndCrew(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/connectors/slack/install", clitest.JSONResponse(201, map[string]any{
		"integration_id": "cint123",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, connectorInstallCmd, "field", "SLACK_TOKEN=xoxb-1")
	setFlagCovCli10(t, connectorInstallCmd, "crew", "ccrew456aaaaaaaaaaaaaaaa")
	connectorInstallCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return connectorInstallCmd.RunE(connectorInstallCmd, []string{"slack"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "cint123") {
		t.Errorf("install output missing integration id: %q", out)
	}
	calls := s.CallsFor("POST", "/api/v1/connectors/slack/install")
	if len(calls) != 1 {
		t.Fatalf("install calls = %d, want 1", len(calls))
	}
	var body struct {
		CrewID string            `json:"crew_id"`
		Fields map[string]string `json:"fields"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.Fields["SLACK_TOKEN"] != "xoxb-1" || body.CrewID == "" {
		t.Errorf("body = %+v", body)
	}
}

// ─── feedback ────────────────────────────────────────────────────────────

func TestFeedbackCreateListDelete(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	// Real server shapes (verified live on dev2): Create returns only the
	// persisted id; List wraps rows in {"feedback": [...]}.
	s.OnPost("/api/v1/feedback", clitest.JSONResponse(201, map[string]any{"id": "cfb1"}))
	s.OnGet("/api/v1/feedback", clitest.JSONResponse(200, map[string]any{
		"feedback": []map[string]any{{
			"id": "cfb1", "message_id": "cmsg1", "signal": "thumbs_down", "created_at": "2026-07-03T00:00:00Z",
		}},
	}))
	s.OnDelete("/api/v1/feedback", clitest.EmptyResponse(204))
	covSetupCli10(t, s.URL())

	setFlagCovCli10(t, feedbackCreateCmd, "message", "cmsg1")
	setFlagCovCli10(t, feedbackCreateCmd, "signal", "thumbs_down")
	feedbackCreateCmd.SetContext(context.Background())
	if _, err := captureStdoutCovCli10(t, func() error {
		return feedbackCreateCmd.RunE(feedbackCreateCmd, nil)
	}); err != nil {
		t.Fatalf("create RunE: %v", err)
	}

	setFlagCovCli10(t, feedbackListCmd, "message", "cmsg1")
	feedbackListCmd.SetContext(context.Background())
	out, err := captureStdoutCovCli10(t, func() error {
		return feedbackListCmd.RunE(feedbackListCmd, nil)
	})
	if err != nil {
		t.Fatalf("list RunE: %v", err)
	}
	if !strings.Contains(out, "thumbs_down") {
		t.Errorf("list output missing signal: %q", out)
	}
	if got := s.CallsFor("GET", "/api/v1/feedback"); len(got) != 1 || !strings.Contains(got[0].Query, "message_id=cmsg1") {
		t.Errorf("list call query = %+v", got)
	}

	setFlagCovCli10(t, feedbackDeleteCmd, "message", "cmsg1")
	setFlagCovCli10(t, feedbackDeleteCmd, "signal", "thumbs_down")
	feedbackDeleteCmd.SetContext(context.Background())
	if _, err := captureStdoutCovCli10(t, func() error {
		return feedbackDeleteCmd.RunE(feedbackDeleteCmd, nil)
	}); err != nil {
		t.Fatalf("delete RunE: %v", err)
	}
	if got := s.CallsFor("DELETE", "/api/v1/feedback"); len(got) != 1 {
		t.Errorf("delete calls = %+v", got)
	}
}

// ─── kv helper ───────────────────────────────────────────────────────────

func TestParseKeyValuePairs(t *testing.T) {
	m, err := parseKeyValuePairs([]string{"A=1", "B=x=y"}, "--field")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["A"] != "1" || m["B"] != "x=y" {
		t.Errorf("m = %+v (value may itself contain '=')", m)
	}
	if _, err := parseKeyValuePairs([]string{"missing-equals"}, "--field"); err == nil {
		t.Error("expected error for pair without '='")
	}
	if _, err := parseKeyValuePairs([]string{"=v"}, "--field"); err == nil {
		t.Error("expected error for empty key")
	}
}
