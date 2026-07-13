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
