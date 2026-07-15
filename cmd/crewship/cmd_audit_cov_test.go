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
		"user":        "cuser0123456789012345",
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
		"user_id=cuser0123456789012345", "search=rotate", "date_from=", "date_to=2026-06-02",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q; got %q", want, q)
		}
	}
	// --action was left empty → must be omitted.
	if strings.Contains(q, "action=") && !strings.Contains(q, "_action=") {
		t.Errorf("action should be omitted; got %q", q)
	}

	// Table rendering: parsable timestamp reformatted, short entity ID
	// shown in full (not needlessly truncated), nils dashed.
	for _, want := range []string{"2026-06-01 10:20:30", "cred_abcdefghijklmnop", "ops@example.com", "not-a-timestamp", "agent.run"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got %q", want, out)
		}
	}
}

// TestAuditRunE_BackupPathEntityIDNotDestroyed is a regression test for
// #1199: backup.* audit actions record the full backup file path as the
// entity ID (see internal/api/backup.go WriteAuditLog calls). The old
// `entityID[:12]` truncation turned e.g.
// "/home/ubuntu/.crewship/backups/2026-07-15-full.tar.gz.age" into just
// "/home/ubuntu" — discarding the only useful part (the filename) with no
// truncation marker at all, so the value looked complete when it wasn't.
func TestAuditRunE_BackupPathEntityIDNotDestroyed(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	longPath := "/home/ubuntu/.crewship/backups/2026-07-15-1200-full.tar.gz.age"

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/audit", clitest.JSONResponse(http.StatusOK, map[string]any{
		"data": []map[string]any{
			{
				"id":          "a1",
				"action":      "backup.create",
				"entity_type": "BACKUP",
				"entity_id":   longPath,
				"user_email":  "ops@example.com",
				"created_at":  "2026-07-15T12:00:00Z",
			},
		},
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	out, err := captureStdout(t, func() error {
		return auditCmd.RunE(auditCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if strings.Contains(out, "/home/ubuntu") && !strings.Contains(out, "full.tar.gz.age") {
		t.Errorf("truncation destroyed the useful filename suffix; got %q", out)
	}
	if !strings.Contains(out, "full.tar.gz.age") {
		t.Errorf("expected the backup filename to survive truncation; got %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("truncated value must carry a visible ellipsis marker; got %q", out)
	}
}

// TestAuditRunE_UserFlagBadShapeErrors is a regression test for #1207:
// --user's own help text says it wants a user ID, but a wrong-shaped value
// (like an email pasted from `crewship whoami` output, or any other
// non-ID garbage) used to be forwarded straight through to the server and
// silently come back with zero rows — indistinguishable from "this user
// has no audit history". It must now fail loudly instead.
func TestAuditRunE_UserFlagBadShapeErrors(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	// No route registered for /api/v1/audit or /members — if the CLI
	// forwarded the bad value to the server at all, the test would fail
	// on the transport error instead of the validation error we want.
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := auditCmd.Flags().Set("user", "bob"); err != nil {
		t.Fatal(err)
	}
	err := auditCmd.RunE(auditCmd, nil)
	if err == nil {
		t.Fatal("expected an error for a non-ID, non-email --user value; got nil")
	}
	if !strings.Contains(err.Error(), "--user") {
		t.Errorf("error should name --user; got %v", err)
	}
	if len(stub.CallsFor("GET", "/api/v1/audit")) != 0 {
		t.Error("bad --user value must not reach the server at all")
	}
}

// TestAuditRunE_UserFlagResolvesEmail covers the other half of #1207: an
// email-shaped --user value is resolved against the workspace member
// roster (the same source `crewship workspace member list` reads) rather
// than rejected outright, since email is the only user identifier
// exposed anywhere else in the CLI (audit rows carry user_email, never
// user_id, and `crewship whoami` doesn't show the ID either).
func TestAuditRunE_UserFlagResolvesEmail(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/cabcdefghijklmnopqrs/members", clitest.JSONResponse(http.StatusOK, []map[string]any{
		{"id": "mem_1", "user_id": "cuser0123456789012345", "email": "ops@example.com", "full_name": "Ops", "role": "ADMIN"},
	}))
	stub.OnGet("/api/v1/audit", clitest.JSONResponse(http.StatusOK, map[string]any{"data": []map[string]any{}}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := auditCmd.Flags().Set("user", "OPS@Example.com"); err != nil {
		t.Fatal(err)
	}
	if err := auditCmd.RunE(auditCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("GET", "/api/v1/audit")
	if len(calls) != 1 {
		t.Fatalf("want 1 audit GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "user_id=cuser0123456789012345") {
		t.Errorf("email must resolve to the member's user_id; query=%q", calls[0].Query)
	}
}

// TestAuditRunE_UserFlagEmailNotFoundErrors: an email that doesn't match
// any workspace member must fail loudly too, not silently query an empty
// user_id (or the raw email) and come back with zero rows.
func TestAuditRunE_UserFlagEmailNotFoundErrors(t *testing.T) {
	saveCLIState(t)
	resetAuditFlags(t)

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/cabcdefghijklmnopqrs/members", clitest.JSONResponse(http.StatusOK, []map[string]any{
		{"id": "mem_1", "user_id": "cuser0123456789012345", "email": "ops@example.com"},
	}))
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: "cabcdefghijklmnopqrs", Server: stub.URL()}

	if err := auditCmd.Flags().Set("user", "ghost@example.com"); err != nil {
		t.Fatal(err)
	}
	err := auditCmd.RunE(auditCmd, nil)
	if err == nil {
		t.Fatal("expected an error for an email with no matching workspace member; got nil")
	}
	if len(stub.CallsFor("GET", "/api/v1/audit")) != 0 {
		t.Error("unresolved --user email must not reach the audit endpoint")
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
