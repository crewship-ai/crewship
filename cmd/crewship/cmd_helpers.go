package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

// looksLikeCUID returns true if s looks like a CUID — the canonical primary-
// key format across the API. The shape is "c" followed by at least 20
// lowercase-alphanumeric chars (total ≥21), matching cuid2's default length.
// If a caller already has one we can short-circuit slug resolution.
func looksLikeCUID(s string) bool {
	if len(s) < 21 || s[0] != 'c' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// cuidFetch verifies a CUID-shaped candidate against its own single-resource
// endpoint and, on a hit, returns the OPEN response so the caller can reuse it
// as the fetch instead of discarding the body and re-GETting the same URL
// (#1177). A slug can legitimately be 21+ lowercase-alphanumeric chars starting
// with "c" (e.g. "customersuccessemea42") and collide with looksLikeCUID's
// shape check; blindly forwarding that slug as if it were a real id used to die
// with a confusing 404 deep in whatever command consumed it (#1075). One direct
// GET settles both concerns:
//
//   - (resp, nil) — the id is real; resp has already passed cli.CheckError and
//     the caller OWNS the body (must close it). A get/export caller reuses this
//     as its fetch; an existence-only caller closes it (see cuidExists).
//   - (nil, nil)  — a clean miss: singlePath 404'd, so the ref only LOOKED like
//     a CUID. The caller should fall back to its slug/name scan.
//   - (nil, err)  — any other failure (network, 5xx, auth, ...), which the
//     caller should propagate unchanged rather than mask with a fallback.
func cuidFetch(client *cli.Client, singlePath string) (*http.Response, error) {
	resp, err := client.Get(singlePath)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, nil
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// cuidExists is the existence-only wrapper the resolve*ID helpers use when they
// need just the id, not the body: it verifies via cuidFetch and closes the
// response. Kept as a distinct boolean helper so the many resolve*ID callers
// that only need existence stay unchanged.
func cuidExists(client *cli.Client, singlePath string) (bool, error) {
	resp, err := cuidFetch(client, singlePath)
	if err != nil {
		return false, err
	}
	if resp == nil {
		return false, nil
	}
	resp.Body.Close()
	return true, nil
}

// getByRef fetches the single-resource GET response for a slug-or-CUID
// reference AND returns the resolved id, issuing exactly ONE request on the
// CUID fast path — the existence check IS the fetch, so get/export commands no
// longer verify-then-refetch the same URL (#1177). singlePrefix is the single-
// resource path prefix (e.g. "/api/v1/agents/"); resolveID handles the slug
// case via its LIST scan. Returning the id lets a command drive follow-up
// requests (e.g. /crews/{id}/assignments) without re-parsing it out of the
// body — reliable even if the response omits an "id" field. The returned
// response has already passed cli.CheckError; the caller owns and must close
// the body.
func getByRef(client *cli.Client, singlePrefix, ref string, resolveID func(*cli.Client, string) (string, error)) (*http.Response, string, error) {
	if looksLikeCUID(ref) {
		resp, err := cuidFetch(client, singlePrefix+ref)
		if err != nil {
			return nil, "", err
		}
		if resp != nil {
			return resp, ref, nil // one request: verify == fetch, ref IS the id
		}
		// 404: ref only looked like a CUID — fall through to the slug scan.
	}
	id, err := resolveID(client, ref)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Get(singlePrefix + id)
	if err != nil {
		return nil, "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, "", err
	}
	return resp, id, nil
}

// resolveAgentID maps a slug or CUID to the agent's CUID. On lookup miss it
// returns an error enriched with near-match suggestions (Levenshtein) so
// "crewship run vitkor" hints at "viktor" rather than dumping the user back
// to a flat "not found".
func resolveAgentID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		ok, err := cuidExists(client, "/api/v1/agents/"+slugOrID)
		if err != nil {
			return "", fmt.Errorf("resolve agent: %w", err)
		}
		if ok {
			return slugOrID, nil
		}
		// Miss: slugOrID only looks like a CUID (e.g. a slug such as
		// "customersuccessemea42") — fall through to the slug scan below
		// instead of forwarding a doomed id.
	}

	resp, err := client.Get("/api/v1/agents")
	if err != nil {
		return "", fmt.Errorf("resolve agent: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var agents []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &agents); err != nil {
		return "", err
	}

	available := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.Slug == slugOrID {
			return a.ID, nil
		}
		if a.Slug != "" {
			available = append(available, a.Slug)
		}
	}
	if len(available) == 0 {
		return "", cli.NotFoundf("agent not found: %s (no agents in this workspace)", slugOrID)
	}
	suggestions := nearestSlugs(slugOrID, available, 3)
	if len(suggestions) > 0 {
		return "", cli.NotFoundf("agent not found: %s. Did you mean: %s?",
			slugOrID, strings.Join(suggestions, ", "))
	}
	return "", cli.NotFoundf("agent not found: %s. Available: %s",
		slugOrID, strings.Join(truncateList(available, 8), ", "))
}

// resolveCrewID maps a slug or CUID to the crew's CUID.
func resolveCrewID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		ok, err := cuidExists(client, "/api/v1/crews/"+slugOrID)
		if err != nil {
			return "", fmt.Errorf("resolve crew: %w", err)
		}
		if ok {
			return slugOrID, nil
		}
		// Miss: slugOrID only looks like a CUID — fall through to the
		// slug scan below instead of forwarding a doomed id (#1075).
	}

	resp, err := client.Get("/api/v1/crews")
	if err != nil {
		return "", fmt.Errorf("resolve crew: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}

	var crews []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &crews); err != nil {
		return "", err
	}

	for _, c := range crews {
		if c.Slug == slugOrID {
			return c.ID, nil
		}
	}
	return "", cli.NotFoundf("crew not found: %s", slugOrID)
}

// resolveIntegrationID maps a name or CUID to the integration's CUID.
func resolveIntegrationID(client *cli.Client, nameOrID string) (string, error) {
	resp, err := client.Get("/api/v1/integrations")
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ID == nameOrID || item.Name == nameOrID {
			return item.ID, nil
		}
	}
	return "", cli.NotFoundf("integration %q not found", nameOrID)
}
