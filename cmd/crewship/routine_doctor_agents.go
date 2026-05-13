package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// checkAgentSlugs walks the DSL steps and verifies each agent_slug
// + outcomes.grader_agent_slug exists in the author crew. The
// pipeline parser already does this at save time, but agents can
// be deleted/renamed AFTER save — this catches that drift.
func checkAgentSlugs(client doctorHTTPGetter, crewID string, def map[string]interface{}) []doctorCheck {
	steps, ok := def["steps"].([]interface{})
	if !ok || len(steps) == 0 {
		return []doctorCheck{{Name: "agent_slugs", Level: doctorWarn, Message: "no steps in DSL definition"}}
	}
	if crewID == "" {
		return []doctorCheck{{
			Name:    "agent_slugs",
			Level:   doctorWarn,
			Message: "author_crew_id is empty — skipping agent slug resolution",
		}}
	}

	available := fetchAgentSlugsForCrew(client, crewID)
	if available == nil {
		return []doctorCheck{{
			Name:    "agent_slugs",
			Level:   doctorWarn,
			Message: "could not fetch crew agents — skipping resolution check",
		}}
	}

	missing := map[string]string{} // slug → step ID where referenced
	for _, raw := range steps {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		stepID, _ := step["id"].(string)
		if slug, _ := step["agent_slug"].(string); slug != "" {
			if _, found := available[slug]; !found {
				missing[slug] = stepID
			}
		}
		if outcomes, ok := step["outcomes"].(map[string]interface{}); ok {
			if grader, _ := outcomes["grader_agent_slug"].(string); grader != "" {
				if _, found := available[grader]; !found {
					missing[grader] = stepID + "/outcomes"
				}
			}
		}
	}
	if len(missing) == 0 {
		return []doctorCheck{{
			Name:    "agent_slugs",
			Level:   doctorOK,
			Message: fmt.Sprintf("all agent_slug + grader references resolve in crew (%d agents available)", len(available)),
		}}
	}
	out := make([]doctorCheck, 0, len(missing))
	availList := make([]string, 0, len(available))
	for slug := range available {
		availList = append(availList, slug)
	}
	availList = truncateList(availList, 8)
	for slug, stepID := range missing {
		out = append(out, doctorCheck{
			Name:    "agent_slug:" + slug,
			Level:   doctorFail,
			Message: fmt.Sprintf("step %q references agent_slug %q not in author crew", stepID, slug),
			Hint:    "available slugs in crew: " + strings.Join(availList, ", "),
		})
	}
	return out
}

func fetchAgentSlugsForCrew(client doctorHTTPGetter, crewID string) map[string]struct{} {
	// Workspace ID is auto-injected as ?workspace_id by the client;
	// we just supply the crew filter. The list endpoint scopes by
	// workspace + filter, returning only agents in this crew.
	// crewID is escaped as a query value (not a path segment) so
	// it survives any reserved character intact.
	resp, err := client.Get(fmt.Sprintf("/api/v1/agents?crew_id=%s", url.QueryEscape(crewID)))
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Fall back to the workspace listing — slightly broader
		// but still useful for the suggestion hint.
		return nil
	}
	var rows []struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil
	}
	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.Slug != "" {
			out[r.Slug] = struct{}{}
		}
	}
	return out
}
