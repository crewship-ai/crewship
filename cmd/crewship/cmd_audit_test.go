package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestAuditCmdStructure(t *testing.T) {
	t.Parallel()

	if auditCmd.Use != "audit" {
		t.Errorf("audit Use: got %q want %q", auditCmd.Use, "audit")
	}
	if !strings.Contains(strings.ToLower(auditCmd.Short), "audit") {
		t.Errorf("audit Short should mention audit; got %q", auditCmd.Short)
	}
	// Long help should document usage examples.
	if !strings.Contains(auditCmd.Long, "crewship audit") {
		t.Errorf("audit Long should show CLI examples; got %q", auditCmd.Long)
	}
}

func TestAuditCmdFlags(t *testing.T) {
	t.Parallel()

	action := auditCmd.Flags().Lookup("action")
	if action == nil {
		t.Fatal("audit missing --action flag")
	}
	if action.DefValue != "" {
		t.Errorf("--action default: got %q want empty", action.DefValue)
	}

	lines := auditCmd.Flags().Lookup("lines")
	if lines == nil {
		t.Fatal("audit missing --lines flag")
	}
	if lines.DefValue != "50" {
		t.Errorf("--lines default: got %q want %q", lines.DefValue, "50")
	}
	if lines.Value.Type() != "int" {
		t.Errorf("--lines type: got %q want %q", lines.Value.Type(), "int")
	}
}

func TestAuditRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{} // empty token

	err := auditCmd.RunE(auditCmd, nil)
	if err == nil {
		t.Fatal("expected 'not logged in' error; got nil")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("error: got %v", err)
	}
}

func TestAuditRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := auditCmd.RunE(auditCmd, nil)
	if err == nil {
		t.Fatal("expected workspace error; got nil")
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error: got %v", err)
	}
}

// TestAuditRunE_QueryConstruction verifies the URL the CLI builds: lines
// comes from --lines, action is appended when non-empty, and the client
// injects workspace_id when a CUID is configured.
func TestAuditRunE_QueryConstruction(t *testing.T) {
	saveCLIState(t)

	var capturedURL string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer mock.Close()

	// Use a CUID-shaped workspace so the client doesn't attempt slug resolution.
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    mock.URL,
	}
	if err := auditCmd.Flags().Set("lines", "25"); err != nil {
		t.Fatalf("set --lines: %v", err)
	}
	if err := auditCmd.Flags().Set("action", "create"); err != nil {
		t.Fatalf("set --action: %v", err)
	}
	t.Cleanup(func() {
		_ = auditCmd.Flags().Set("lines", "50")
		_ = auditCmd.Flags().Set("action", "")
	})

	if err := auditCmd.RunE(auditCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	// The client appends workspace_id as a query param, so the exact order of
	// params is not guaranteed — parse and assert each piece.
	if !strings.HasPrefix(capturedURL, "/api/v1/audit?") {
		t.Fatalf("path: got %q", capturedURL)
	}
	for _, want := range []string{"limit=25", "action=create", "workspace_id=cabcdefghijklmnopqrs"} {
		if !strings.Contains(capturedURL, want) {
			t.Errorf("query missing %q; got %q", want, capturedURL)
		}
	}
}

func TestAuditRunE_OmitsActionWhenEmpty(t *testing.T) {
	saveCLIState(t)

	var capturedURL string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer mock.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    mock.URL,
	}
	if err := auditCmd.Flags().Set("lines", "10"); err != nil {
		t.Fatalf("set --lines: %v", err)
	}
	if err := auditCmd.Flags().Set("action", ""); err != nil {
		t.Fatalf("reset --action: %v", err)
	}
	t.Cleanup(func() { _ = auditCmd.Flags().Set("lines", "50") })

	if err := auditCmd.RunE(auditCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if strings.Contains(capturedURL, "action=") {
		t.Errorf("action should be omitted when empty; got %q", capturedURL)
	}
	if !strings.Contains(capturedURL, "limit=10") {
		t.Errorf("limit not set: %q", capturedURL)
	}
}

// TestAuditRunE_ServerError bubbles up HTTP errors through cli.CheckError.
func TestAuditRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "boom"})
	}))
	defer mock.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    mock.URL,
	}
	_ = auditCmd.Flags().Set("lines", "5")
	_ = auditCmd.Flags().Set("action", "")
	t.Cleanup(func() { _ = auditCmd.Flags().Set("lines", "50") })

	err := auditCmd.RunE(auditCmd, nil)
	if err == nil {
		t.Fatal("expected server error to bubble up; got nil")
	}
}
