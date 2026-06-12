package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// resetAuditFlags restores every audit flag to its default at cleanup.
func resetAuditFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range []string{"action", "entity-type", "entity-id", "user", "since", "until", "search"} {
			_ = auditCmd.Flags().Set(name, "")
		}
		_ = auditCmd.Flags().Set("page", "0")
		_ = auditCmd.Flags().Set("lines", "50")
	})
}

func TestAuditRunE_AllFiltersAndRowRendering(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/audit", clitest.JSONResponse(http.StatusOK, map[string]any{
		"data": []map[string]any{
			{
				"id":          "a1",
				"action":      "credential.rotate",
				"entity_type": "CREDENTIAL",
				"entity_id":   "cred_abcdefghijklmnop", // > 12 chars → truncated
				"user_email":  "ops@example.com",
				"created_at":  "2026-06-01T10:20:30.123Z",
			},
			{
				"id":          "a2",
				"action":      "agent.run",
				"entity_type": "AGENT",
				"entity_id":   nil, // nil pointers render as "-"
				"user_email":  nil,
				"created_at":  "not-a-timestamp", // unparsable → raw passthrough
			},
		},
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	for flag, val := range map[string]string{
		"lines":       "5",
		"entity-type": "CREDENTIAL",
		"entity-id":   "cred_1",
		"user":        "u_42",
		"since":       "24h",
		"until":       "2026-06-02T00:00:00Z",
		"search":      "rotate",
		"page":        "2",
	} {
		if err := auditCmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}

	out, err := captureStdout(t, func() error {
		return auditCmd.RunE(auditCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("GET", "/api/v1/audit")
	if len(calls) != 1 {
		t.Fatalf("want 1 GET, got %d", len(calls))
	}
	q := calls[0].Query
	for _, want := range []string{
		"limit=5", "page=2", "entity_type=CREDENTIAL", "entity_id=cred_1",
		"user_id=u_42", "search=rotate", "date_from=", "date_to=2026-06-02",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q; got %q", want, q)
		}
	}
	// --action was left empty → must be omitted.
	if strings.Contains(q, "action=") && !strings.Contains(q, "_action=") {
		t.Errorf("action should be omitted; got %q", q)
	}

	// Table rendering: parsable timestamp reformatted, long entity ID
	// truncated to 12 chars, nils dashed.
	for _, want := range []string{"2026-06-01 10:20:30", "cred_abcdefg", "ops@example.com", "not-a-timestamp", "agent.run"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got %q", want, out)
		}
	}
	if strings.Contains(out, "cred_abcdefghijklmnop") {
		t.Errorf("entity ID should be truncated to 12 chars; got %q", out)
	}
}

func TestAuditRunE_BadSince(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: "http://127.0.0.1:0"}

	if err := auditCmd.Flags().Set("since", "banana"); err != nil {
		t.Fatal(err)
	}
	err := auditCmd.RunE(auditCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bad --since") {
		t.Errorf("got %v; want bad --since error", err)
	}
}

func TestAuditRunE_BadUntil(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: "http://127.0.0.1:0"}

	if err := auditCmd.Flags().Set("until", "next tuesday"); err != nil {
		t.Fatal(err)
	}
	err := auditCmd.RunE(auditCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bad --until") {
		t.Errorf("got %v; want bad --until error", err)
	}
}

func TestAuditRunE_TransportError(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	stub := clitest.NewStubServer()
	deadURL := stub.URL()
	stub.Close()
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: deadURL}

	if err := auditCmd.RunE(auditCmd, nil); err == nil {
		t.Error("want transport error; got nil")
	}
}

func TestAuditRunE_BadJSON(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/audit", clitest.TextResponse(http.StatusOK, "not json"))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := auditCmd.RunE(auditCmd, nil); err == nil {
		t.Error("want decode error; got nil")
	}
}
