package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestCredListCmd_AllFollowsCursor covers #1033: `credential list --all` walks
// every page by following next_cursor from the {credentials, next_cursor}
// envelope.
func TestCredListCmd_AllFollowsCursor(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credListCmd)

	page := func(name string, next *string) []byte {
		body, _ := json.Marshal(map[string]any{
			"credentials": []map[string]any{
				{"id": "id-" + name, "name": name, "type": "API_KEY", "provider": "GITHUB", "status": "ACTIVE"},
			},
			"next_cursor": next,
			"limit":       1,
		})
		return body
	}
	c1 := "cursor1"
	stub.OnGet("/api/v1/credentials", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.URL.Query().Get("cursor") == c1 {
			return 200, page("KEY_B", nil), "application/json"
		}
		return 200, page("KEY_A", &c1), "application/json"
	})

	if err := credListCmd.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := credListCmd.RunE(credListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "KEY_A") || !strings.Contains(out, "KEY_B") {
		t.Errorf("--all should show both pages, got: %q", out)
	}
	// Two GETs: page 1 (no cursor) then page 2 (cursor1).
	calls := stub.CallsFor("GET", "/api/v1/credentials")
	if len(calls) != 2 {
		t.Fatalf("expected 2 page fetches, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "paginate=true") {
		t.Errorf("first call should opt into pagination: %q", calls[0].Query)
	}
	if !strings.Contains(calls[1].Query, "cursor="+c1) {
		t.Errorf("second call should follow the cursor: %q", calls[1].Query)
	}
}

// TestCredListCmd_SearchTagFlags asserts --search/--tag reach the server query.
func TestCredListCmd_SearchTagFlags(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credListCmd)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, map[string]any{
		"credentials": []map[string]any{}, "next_cursor": nil, "limit": 50,
	}))
	_ = credListCmd.Flags().Set("search", "stripe")
	_ = credListCmd.Flags().Set("tag", "prod")
	if err := credListCmd.RunE(credListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	q := stub.CallsFor("GET", "/api/v1/credentials")[0].Query
	if !strings.Contains(q, "search=stripe") || !strings.Contains(q, "tag=prod") {
		t.Errorf("search/tag not forwarded: %q", q)
	}
}

// TestCredListCmd_DefaultLimitIsOneHundred covers the BLOCKER page-size
// regression: the CLI now always opts into pagination (paginate=true), whose
// server-side default is 50 — vs. the long-standing bare-endpoint default of
// 100. Without an explicit limit, the CLI must send limit=100 so scripts that
// never passed --limit don't silently start losing rows past 50.
func TestCredListCmd_DefaultLimitIsOneHundred(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credListCmd)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, map[string]any{
		"credentials": []map[string]any{}, "next_cursor": nil, "limit": 100,
	}))
	if err := credListCmd.RunE(credListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	q := stub.CallsFor("GET", "/api/v1/credentials")[0].Query
	if !strings.Contains(q, "limit=100") {
		t.Errorf("expected limit=100 when --limit is absent, got query: %q", q)
	}
}

// TestCredListCmd_ExplicitLimitOverridesDefault asserts a user-supplied
// --limit is forwarded verbatim (not overridden by the 100 default).
func TestCredListCmd_ExplicitLimitOverridesDefault(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credListCmd)
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, map[string]any{
		"credentials": []map[string]any{}, "next_cursor": nil, "limit": 20,
	}))
	_ = credListCmd.Flags().Set("limit", "20")
	if err := credListCmd.RunE(credListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	q := stub.CallsFor("GET", "/api/v1/credentials")[0].Query
	if !strings.Contains(q, "limit=20") {
		t.Errorf("expected limit=20, got query: %q", q)
	}
	if strings.Contains(q, "limit=100") {
		t.Errorf("explicit --limit must not be shadowed by the default: %q", q)
	}
}

// TestCredListCmd_RejectsNonPositiveLimit covers the MINOR finding: --limit
// <= 0 must be rejected with a clear error instead of silently dropped
// (which would have fallen back to whatever the server defaults to).
func TestCredListCmd_RejectsNonPositiveLimit(t *testing.T) {
	for _, v := range []string{"0", "-1", "-50"} {
		t.Run(v, func(t *testing.T) {
			covStub(t)
			covResetFlags(t, credListCmd)
			_ = credListCmd.Flags().Set("limit", v)
			err := credListCmd.RunE(credListCmd, nil)
			if err == nil {
				t.Fatalf("expected an error for --limit %s, got nil", v)
			}
			if !strings.Contains(err.Error(), "positive integer") {
				t.Errorf("error message should explain the constraint, got: %v", err)
			}
		})
	}
}

// TestCredListCmd_AllBailsOnNonAdvancingCursor covers the MAJOR
// termination-guard finding: a server bug that returns the same next_cursor
// twice must not spin --all forever.
func TestCredListCmd_AllBailsOnNonAdvancingCursor(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credListCmd)

	stuckCursor := "stuck-cursor"
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, map[string]any{
		"credentials": []map[string]any{
			{"id": "id-x", "name": "X", "type": "API_KEY", "provider": "GITHUB", "status": "ACTIVE"},
		},
		"next_cursor": stuckCursor,
		"limit":       1,
	}))

	if err := credListCmd.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	err := credListCmd.RunE(credListCmd, nil)
	if err == nil {
		t.Fatal("expected an error for a non-advancing cursor, got nil")
	}
	if !strings.Contains(err.Error(), "no progress") {
		t.Errorf("expected a no-progress error, got: %v", err)
	}
	// Must bail after the very next request once it detects the repeat —
	// not spin through hundreds of identical calls.
	calls := stub.CallsFor("GET", "/api/v1/credentials")
	if len(calls) > 3 {
		t.Errorf("expected to bail quickly on the repeated cursor, made %d calls", len(calls))
	}
}

// TestCredListCmd_AllHasHardPageCap covers the hard page-cap half of the
// termination guard: a server that always advances the cursor to a new value
// (so the no-progress check never fires) must still be bounded.
func TestCredListCmd_AllHasHardPageCap(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credListCmd)

	stub.OnGet("/api/v1/credentials", func(r *http.Request, _ []byte) (int, []byte, string) {
		cur := r.URL.Query().Get("cursor")
		next := cur + "x" // always a fresh, ever-advancing cursor — never nil
		body, _ := json.Marshal(map[string]any{
			"credentials": []map[string]any{
				{"id": "id-" + next, "name": next, "type": "API_KEY", "provider": "GITHUB", "status": "ACTIVE"},
			},
			"next_cursor": next,
			"limit":       1,
		})
		return 200, body, "application/json"
	})

	if err := credListCmd.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	err := credListCmd.RunE(credListCmd, nil)
	if err == nil {
		t.Fatal("expected the hard page cap to trip, got nil error")
	}
	if !strings.Contains(err.Error(), "stopped after") {
		t.Errorf("expected a page-cap error, got: %v", err)
	}
}
