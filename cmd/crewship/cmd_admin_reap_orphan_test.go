package main

// CLI side of the #1390 detector-coverage work. The behaviour worth pinning is
// the wording: "no orphans" has to read differently depending on whether the
// detector could actually look, and it must NOT overclaim against a server too
// old to say.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const reapPath = "/api/v1/admin/reap-orphan-containers"

func runReap(t *testing.T) string {
	t.Helper()
	covResetFlags(t, adminReapOrphanCmd)
	return covCaptureAll(t, func() {
		if err := adminReapOrphanCmd.RunE(adminReapOrphanCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
}

func TestAdminReapOrphan_Coverage(t *testing.T) {
	t.Run("inert detector says so instead of reporting a clean sweep", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(reapPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"orphans":[],"count":0,"applied":false,
				"inspected":3,"identified":0,"detector_inert":true}`), "application/json"
		})
		out := runReap(t)
		if !strings.Contains(out, "DETECTOR INERT") {
			t.Errorf("must not read as a clean sweep:\n%s", out)
		}
		if !strings.Contains(out, "3") {
			t.Errorf("should name how many were inspected:\n%s", out)
		}
		// The operator's next action has to be obvious.
		if !strings.Contains(out, "sidecar") {
			t.Errorf("should point at the stale sidecar:\n%s", out)
		}
	})

	t.Run("healthy sweep reports its coverage", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(reapPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"orphans":[],"count":0,"applied":false,
				"inspected":2,"identified":2,"detector_inert":false}`), "application/json"
		})
		out := runReap(t)
		if !strings.Contains(out, "No orphaned crew containers found") {
			t.Errorf("want the clean-sweep line:\n%s", out)
		}
		if !strings.Contains(out, "2 of 2") {
			t.Errorf("coverage should be explicit:\n%s", out)
		}
	})

	t.Run("partial coverage is called out", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(reapPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"orphans":[],"count":0,"applied":false,
				"inspected":3,"identified":1,"detector_inert":false}`), "application/json"
		})
		out := runReap(t)
		if !strings.Contains(out, "1 of 3") {
			t.Errorf("coverage missing:\n%s", out)
		}
		if !strings.Contains(out, "could not be classified") {
			t.Errorf("the 2 unclassified containers must be surfaced:\n%s", out)
		}
	})

	t.Run("no running containers is not an inert detector", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(reapPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"orphans":[],"count":0,"applied":false,
				"inspected":0,"identified":0,"detector_inert":false}`), "application/json"
		})
		out := runReap(t)
		if !strings.Contains(out, "No running crew containers to inspect") {
			t.Errorf("empty fleet should say so plainly:\n%s", out)
		}
		if strings.Contains(out, "DETECTOR INERT") {
			t.Errorf("an empty fleet is not a broken detector:\n%s", out)
		}
	})

	t.Run("older server omitting the fields does not get a false claim", func(t *testing.T) {
		// Forward-compat: a plain int would decode the missing field to 0 and
		// have the CLI assert "no running crew containers" — a statement the
		// server never made. Caught against the live dev3 build.
		stub := covStub(t)
		stub.OnPost(reapPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"orphans":[],"count":0,"applied":false}`), "application/json"
		})
		out := runReap(t)
		if strings.Contains(out, "No running crew containers to inspect") {
			t.Errorf("must not invent a fact the server didn't report:\n%s", out)
		}
		if !strings.Contains(out, "does not report detector coverage") {
			t.Errorf("should say the server is too old to tell:\n%s", out)
		}
	})

	t.Run("orphans are still listed", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(reapPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"count":1,"applied":false,"inspected":2,"identified":2,
				"orphans":[{"crew_id":"c-qua","slug":"quality","container_id":"ctr-qua"}]}`), "application/json"
		})
		out := runReap(t)
		if !strings.Contains(out, "quality") || !strings.Contains(out, "stale token") {
			t.Errorf("orphan listing regressed:\n%s", out)
		}
	})

	t.Run("api error propagates", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost(reapPath, clitest.ErrorResponse(403, "Forbidden"))
		covResetFlags(t, adminReapOrphanCmd)
		err := adminReapOrphanCmd.RunE(adminReapOrphanCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("got %v", err)
		}
	})
}
