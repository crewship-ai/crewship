package main

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

// looksLikeCUID returns true if s looks like a CUID (c[a-z0-9]{20,}+).
// CUIDs are the canonical primary-key format across the API; if a caller
// already has one, slug-resolution can short-circuit.
func looksLikeCUID(s string) bool {
	if len(s) < 20 || s[0] != 'c' {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// resolveAgentID maps a slug or CUID to the agent's CUID. On lookup miss it
// returns an error enriched with near-match suggestions (Levenshtein) so
// "crewship run vitkor" hints at "viktor" rather than dumping the user back
// to a flat "not found".
func resolveAgentID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		return slugOrID, nil
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
		return "", fmt.Errorf("agent not found: %s (no agents in this workspace)", slugOrID)
	}
	suggestions := nearestSlugs(slugOrID, available, 3)
	if len(suggestions) > 0 {
		return "", fmt.Errorf("agent not found: %s. Did you mean: %s?",
			slugOrID, strings.Join(suggestions, ", "))
	}
	return "", fmt.Errorf("agent not found: %s. Available: %s",
		slugOrID, strings.Join(truncateList(available, 8), ", "))
}

// resolveCrewID maps a slug or CUID to the crew's CUID.
func resolveCrewID(client *cli.Client, slugOrID string) (string, error) {
	if looksLikeCUID(slugOrID) {
		return slugOrID, nil
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
	return "", fmt.Errorf("crew not found: %s", slugOrID)
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
	return "", fmt.Errorf("integration %q not found", nameOrID)
}
