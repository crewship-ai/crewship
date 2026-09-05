package main

// Workspace id→slug reference tables, shared by every command that renders a
// row holding a foreign key.
//
// The problem this solves is not cosmetic. A CUID in a human column
// (`cmtov6bxv00fa7a9515de` under AUTHOR CREW, `agent/cmtov6c2f010216ea53ca`
// under Top spenders) is not just ugly — it is unusable: nothing else in the
// CLI takes an id there, so the operator's only next move is to grep
// `crew list` by hand. Most list commands already resolve names; the ones
// that did not had each grown their own inline map, or none at all.
//
// One endpoint answers all of it. GET /api/v1/journal/lookup is the
// denormalised id→{name,slug} table the dashboard already consumes, capped
// server-side, so a command pays one extra request for the whole workspace
// rather than one per row.
//
// Every lookup here is BEST-EFFORT by design: a failure returns empty maps
// and the caller falls back to the id it already has. A `cost` report that
// refuses to print because a name lookup 404ed would be a worse command than
// one that prints cuids.

import (
	"github.com/crewship-ai/crewship/internal/cli"
)

// workspaceSlugs holds the id→slug tables for one workspace. A missing key
// means "not in the lookup" — an id from a deleted row, or one past the
// server's cap — and callers render the raw id for those.
type workspaceSlugs struct {
	Agents map[string]string
	Crews  map[string]string
}

// agent returns the agent's slug, or id when it cannot be resolved. The
// fallback is the id rather than a placeholder: an unresolvable id is still
// the truth, and "—" would destroy the only handle the caller has.
func (l workspaceSlugs) agent(id string) string {
	if slug, ok := l.Agents[id]; ok && slug != "" {
		return slug
	}
	return id
}

// crew returns the crew's slug, or id when it cannot be resolved.
func (l workspaceSlugs) crew(id string) string {
	if slug, ok := l.Crews[id]; ok && slug != "" {
		return slug
	}
	return id
}

// fetchWorkspaceSlugs loads both tables in one request. Best-effort: any
// failure yields empty (non-nil) maps, so the caller's rendering path never
// has to distinguish "lookup failed" from "id not found" — both fall back to
// the raw id.
func fetchWorkspaceSlugs(client *cli.Client) workspaceSlugs {
	out := workspaceSlugs{
		Agents: map[string]string{},
		Crews:  map[string]string{},
	}
	resp, err := client.Get("/api/v1/journal/lookup")
	if err != nil {
		return out
	}
	if err := cli.CheckError(resp); err != nil {
		return out
	}
	var body struct {
		Agents []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"agents"`
		Crews []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"crews"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return out
	}
	for _, a := range body.Agents {
		if a.ID != "" && a.Slug != "" {
			out.Agents[a.ID] = a.Slug
		}
	}
	for _, c := range body.Crews {
		switch {
		case c.ID == "":
		case c.Slug != "":
			out.Crews[c.ID] = c.Slug
		default:
			// A crew with no slug still has a name, and a name beats a cuid.
			out.Crews[c.ID] = c.Name
		}
	}
	return out
}
