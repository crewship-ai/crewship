package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// suggestSimilarRoutineSlugs fetches the workspace's routine list
// and returns a short "did you mean..." hint when the user-supplied
// `target` doesn't match any. Used by routine run / dry-run / bench /
// eval compare / eval scenarios on a 404 to convert "pipeline not
// found" into "pipeline not found — did you mean X, Y, Z?"
//
// Intentionally CLIENT-side (not in the API handlers): keeps the
// suggestion logic + ranking heuristic out of the public API
// contract, lets us tune the UX freely, and avoids fan-out cost
// on every server 404. The extra HTTP round-trip happens only on
// the user's failed call, which is already the slow path.
//
// Returns "" when:
//   - the listing fetch fails (best-effort; never crash)
//   - no candidates exist at any edit-distance threshold
//
// Caller composes with their own error message; this returns just
// the hint payload.
func suggestSimilarRoutineSlugs(client interface {
	Get(string) (*http.Response, error)
}, ws, target string) string {
	if target == "" || ws == "" {
		return ""
	}
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines", ws))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var rows []struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return ""
	}

	if len(rows) == 0 {
		return "no routines registered yet — try `crewship seed` to populate the workspace, or `crewship routine save` to author one"
	}

	candidates := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Slug != "" {
			candidates = append(candidates, r.Slug)
		}
	}

	// First try edit-distance match (the canonical "typo" path).
	if suggestions := nearestSlugs(target, candidates, 3); len(suggestions) > 0 {
		return "did you mean: " + strings.Join(suggestions, ", ") + "?"
	}

	// No close matches: fall back to substring containment so a
	// user typing `extract` against a workspace with 5 eval-extract-*
	// routines still gets a useful hint. Different rank class from
	// edit-distance, hence two passes rather than blending into one
	// score.
	tLow := strings.ToLower(target)
	substr := make([]string, 0, 3)
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c), tLow) {
			substr = append(substr, c)
			if len(substr) == 3 {
				break
			}
		}
	}
	if len(substr) > 0 {
		return "no exact match — routines containing %q: " + strings.Join(substr, ", ")
	}
	return ""
}
