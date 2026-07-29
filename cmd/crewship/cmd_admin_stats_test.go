package main

// CLI parity for GET /api/v1/admin/stats. The point of the command is the
// comparison: a count on its own is not something anyone acts on.

import (
	"net/http"
	"strings"
	"testing"
)

func runAdminStats(t *testing.T) string {
	t.Helper()
	covResetFlags(t, adminStatsCmd)
	return covCaptureAll(t, func() {
		if err := adminStatsCmd.RunE(adminStatsCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
}

func TestAdminStats(t *testing.T) {
	t.Run("reads counts against the licensed ceilings", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/admin/stats", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"workspaces":1,"users":2,"crews":3,"agents":8,"running":0}`), "application/json"
		})
		stub.OnGet("/api/v1/system/license", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"edition":"community","max_crews":15,"max_agents_per_crew":10,"max_members":5}`), "application/json"
		})
		out := runAdminStats(t)
		for _, want := range []string{"3 of 15", "2 of 5", "community"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	// An unreadable licence costs the ceilings, not the counts: an operator
	// asking how many crews exist should still get an answer.
	t.Run("still reports the counts when the licence cannot be read", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/admin/stats", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"workspaces":1,"users":2,"crews":3,"agents":8,"running":1}`), "application/json"
		})
		stub.OnGet("/api/v1/system/license", func(*http.Request, []byte) (int, []byte, string) {
			return 500, []byte(`{"error":"nope"}`), "application/json"
		})
		out := runAdminStats(t)
		if !strings.Contains(out, "3") || !strings.Contains(out, "Running") {
			t.Errorf("counts lost with an unreadable licence:\n%s", out)
		}
	})
}
