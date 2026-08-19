package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestPageGrants_RevokeWithoutASubjectIs400 pins the 4xx that
// requiredQueryParametersInSpec depends on.
//
// The OpenAPI generator infers `?subject` is REQUIRED on this route, and the
// golden list in cmd/gen-openapi is a deliberate review record: its own comment
// says every entry "was read against its handler and answers 4xx when the
// parameter is absent" and names the test that pins it. This route joined the
// list with no such test, so the claim rested on a reading rather than on
// anything that would notice if the handler changed.
//
// It matters more here than the generic case. A revoke that silently succeeded
// with no subject would answer 200 while withdrawing nothing, and the operator
// would believe an agent's access was gone.
func TestPageGrants_RevokeWithoutASubjectIs400(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-watcher", "watcher", "crew-lookout")
	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"agent","subject":"watcher","level":"read"}`)

	for _, tc := range []struct{ name, query string }{
		{"no subject at all", "?subject_type=agent"},
		{"an empty subject", "?subject_type=agent&subject="},
		{"whitespace only", "?subject_type=agent&subject=%20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := pagesGrantCall(t, h, "DELETE",
				"/api/v1/pages/fleet-201/grants"+tc.query, wsID, ownerID, "OWNER", "fleet-201", "")
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 — a revoke naming nobody must refuse, not report success: %s",
					rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "subject is required") {
				t.Errorf("body does not say what is missing: %s", rr.Body.String())
			}
		})
	}

	// And the grant is still there: a refused revoke must not half-run.
	if got := len(pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")); got != 1 {
		t.Errorf("grants after three refused revokes = %d, want the original 1", got)
	}
}
