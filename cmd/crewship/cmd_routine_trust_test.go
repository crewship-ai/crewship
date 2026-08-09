package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func trustPathCov(slug string) string {
	return fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/trust", covWorkspaceID, slug)
}

// TestRoutineTrustGrantRunE covers the guards the CLI owns itself —
// everything else on this command is HTTP plumbing the API tests hold.
func TestRoutineTrustGrantRunE(t *testing.T) {
	t.Run("refuses a grant with no step", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFlagCov(t, routineTrustGrantCmd, "step", "")

		err := routineTrustGrantCmd.RunE(routineTrustGrantCmd, []string{"triage"})
		if err == nil {
			t.Fatal("granting with no --step succeeded; that is routine-wide trust, not gate trust")
		}
		if !strings.Contains(err.Error(), "--step is required") {
			t.Errorf("error = %v, want it to name --step", err)
		}
		// And it must not have reached the server.
		if calls := stub.CallsFor("POST", trustPathCov("triage")); len(calls) != 0 {
			t.Errorf("sent %d requests despite failing validation", len(calls))
		}
	})

	t.Run("refuses a non-RFC3339 expiry", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFlagCov(t, routineTrustGrantCmd, "step", "review")
		setFlagCov(t, routineTrustGrantCmd, "expires", "next tuesday")

		err := routineTrustGrantCmd.RunE(routineTrustGrantCmd, []string{"triage"})
		if err == nil {
			t.Fatal("accepted an unparseable --expires; the grant would have been treated as never expiring")
		}
		if !strings.Contains(err.Error(), "RFC3339") {
			t.Errorf("error = %v, want it to name the expected format", err)
		}
	})

	t.Run("posts the grant and points at the revoke command", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		setFlagCov(t, routineTrustGrantCmd, "step", "review")
		setFlagCov(t, routineTrustGrantCmd, "expires", "")
		setFlagCov(t, routineTrustGrantCmd, "reason", "approved 12x")
		setFlagCov(t, routineTrustGrantCmd, "max-uses", "20")
		stub.OnPost(trustPathCov("triage"), clitest.JSONResponse(201, map[string]any{
			"id":              "wtg_abc123",
			"slug":            "triage",
			"step_id":         "review",
			"definition_hash": "0123456789abcdef",
		}))

		out, err := captureStdoutCov(t, func() error {
			return routineTrustGrantCmd.RunE(routineTrustGrantCmd, []string{"triage"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		calls := stub.CallsFor("POST", trustPathCov("triage"))
		if len(calls) != 1 {
			t.Fatalf("expected one POST, got %d", len(calls))
		}
		body := string(calls[0].Body)
		for _, want := range []string{`"step_id":"review"`, `"max_uses":20`, `"reason":"approved 12x"`} {
			if !strings.Contains(body, want) {
				t.Errorf("request body missing %s; got %s", want, body)
			}
		}
		// The operator has just disarmed a gate; the way back must be on
		// screen, not in the docs.
		if !strings.Contains(out, "routine trust revoke triage wtg_abc123") {
			t.Errorf("output does not show how to undo the grant:\n%s", out)
		}
	})
}

// TestRoutineTrustListRunE pins the two things the table says that the
// raw API response does not: whether a grant is still in force, and
// whether it is pinned to a definition that has moved on.
func TestRoutineTrustListRunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(trustPathCov("triage"), clitest.JSONResponse(200, map[string]any{
		"slug":            "triage",
		"definition_hash": "currenthash0000",
		"grants": []map[string]any{
			{"id": "wtg_live", "step_id": "review", "definition_hash": "currenthash0000",
				"uses": 3, "max_uses": 20, "live": true, "reason": "approved 12x"},
			{"id": "wtg_stale", "step_id": "review", "definition_hash": "oldhash11111111",
				"uses": 9, "live": false, "reason": "granted before the rewrite"},
			{"id": "wtg_gone", "step_id": "publish", "definition_hash": "currenthash0000",
				"uses": 1, "live": false, "revoked_at": "2026-08-01T10:00:00Z", "reason": "policy review"},
		},
	}))

	out, err := captureStdoutCov(t, func() error {
		return routineTrustListCmd.RunE(routineTrustListCmd, []string{"triage"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"wtg_live", "3/20", "live", "(stale)", "revoked"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q; got:\n%s", want, out)
		}
	}
	// A grant that is merely used up must not read as a decision
	// somebody made to withdraw it.
	if strings.Count(out, "revoked") != 1 {
		t.Errorf("expected exactly one row labelled revoked; got:\n%s", out)
	}
}

func TestRoutineTrustListRunE_EmptyIsExplicit(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(trustPathCov("bare"), clitest.JSONResponse(200, map[string]any{
		"slug": "bare", "definition_hash": "h", "grants": []any{},
	}))

	out, err := captureStdoutCov(t, func() error {
		return routineTrustListCmd.RunE(routineTrustListCmd, []string{"bare"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "every gate still asks") {
		t.Errorf("empty list should state the safe status positively; got: %q", out)
	}
}

// A bounding flag that fails open defeats its own purpose: a negative
// --max-uses used to be dropped from the body, producing exactly the
// unlimited grant the operator was reaching for the flag to avoid.
func TestRoutineTrustGrantRunE_RejectsNegativeMaxUses(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFlagCov(t, routineTrustGrantCmd, "step", "review")
	setFlagCov(t, routineTrustGrantCmd, "expires", "")
	setFlagCov(t, routineTrustGrantCmd, "max-uses", "-1")

	err := routineTrustGrantCmd.RunE(routineTrustGrantCmd, []string{"triage"})
	if err == nil {
		t.Fatal("accepted --max-uses -1; the grant would have been created unlimited")
	}
	if calls := stub.CallsFor("POST", trustPathCov("triage")); len(calls) != 0 {
		t.Errorf("sent %d requests despite failing validation", len(calls))
	}
}
