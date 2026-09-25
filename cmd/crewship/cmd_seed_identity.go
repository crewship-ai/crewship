package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memport"
)

func demoProjectAppearance(name string) map[string]string {
	for _, p := range seeddata.Projects {
		if p.Name == name {
			return map[string]string{"icon": p.Icon, "color": p.Color}
		}
	}
	return map[string]string{"icon": "activity", "color": "cyan"}
}

func seedAppearance(client *cli.Client, path string, body map[string]string) error {
	r, err := client.Patch(path, body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if err = cli.CheckError(r); err != nil {
		return fmt.Errorf("demo appearance %s: %w", path, err)
	}
	return nil
}

// Identity is metadata: changing it must not change routine execution or
// overwrite an operator's custom agent persona on a subsequent seed.
func seedDemoIdentity(ctx context.Context, client *cli.Client, agentIDs, crewIDs map[string]string) error {
	client = client.WithContext(ctx)
	ws := client.GetWorkspaceID()
	for _, c := range seeddata.ActiveCrews() {
		if id := crewIDs[c.Slug]; id != "" {
			if err := seedAppearance(client, "/api/v1/crews/"+id, map[string]string{"name": c.Name, "description": c.Description, "icon": c.Icon, "color": c.Color}); err != nil {
				return err
			}
		}
	}
	for _, story := range seeddata.Stories {
		style := demoProjectAppearance(story.Project)
		pageSlug := "demo-" + story.Slug
		if err := seedAppearance(client, fmt.Sprintf("/api/v1/pages/%s", pageSlug), style); err != nil {
			return err
		}
		for _, action := range []struct{ suffix, icon string }{{"check", "search"}, {"draft", "sparkles"}, {"resolve", "badge-check"}} {
			body := map[string]string{"icon": action.icon, "color": style["color"]}
			routineSlug := "demo-" + story.Slug + "-" + action.suffix
			if err := seedAppearance(client, fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/appearance", ws, routineSlug), body); err != nil {
				return err
			}
		}
	}
	for _, slug := range []string{"custom-operations", "demo-live"} {
		if err := seedAppearance(client, "/api/v1/pages/"+slug, demoProjectAppearance("Crewship Lab")); err != nil {
			return err
		}
	}
	for slug, icon := range map[string]string{"pages-operations-sample": "activity", "demo-live-start": "play", "demo-live-stop": "power"} {
		if err := seedAppearance(client, "/api/v1/workspaces/"+ws+"/pipelines/"+slug+"/appearance", map[string]string{"icon": icon, "color": "cyan"}); err != nil {
			return err
		}
	}
	for _, agent := range seeddata.Agents {
		id := agentIDs[agent.Slug]
		if id == "" {
			continue
		}
		if err := seedAppearance(client, "/api/v1/agents/"+id, map[string]string{"role_title": agent.RoleTitle, "system_prompt": seeddata.AgentPrompt(agent.PromptSlug)}); err != nil {
			return err
		}
		path := "/api/v1/agents/" + id + "/persona"
		var current PersonaResponse
		if err := verifyBusinessGet(client, path, &current); err != nil {
			return err
		}
		if current.Layer == "agent" && strings.TrimSpace(current.Content) != "" {
			continue
		}
		r, err := client.Put(path, map[string]string{"content": seeddata.AgentSoul(agent.Slug)})
		if err != nil {
			return err
		}
		status := r.StatusCode
		err = cli.CheckError(r)
		r.Body.Close()
		if err != nil && status == http.StatusInternalServerError {
			// Running containers own their memory tree. The existing import
			// transport writes as the container identity without chmod/chown.
			crewID, resolveErr := resolveCrewID(client, agent.CrewSlug)
			if resolveErr != nil {
				return resolveErr
			}
			refused, importErr := postImportBatch(client, crewID, agent.Slug, []memport.Doc{{RelPath: "PERSONA.md", Tier: memory.TierAgent, Scope: memport.ScopeAgent, Body: []byte(seeddata.AgentSoul(agent.Slug))}})
			if importErr != nil {
				return importErr
			}
			if refused != 0 {
				return fmt.Errorf("agent %s soul import refused", agent.Slug)
			}
			var installed PersonaResponse
			if err := verifyBusinessGet(client, path, &installed); err != nil {
				return err
			}
			if installed.Content != seeddata.AgentSoul(agent.Slug) {
				return fmt.Errorf("agent %s soul read-back mismatch", agent.Slug)
			}
			err = nil
		}
		if err != nil {
			return fmt.Errorf("agent %s soul: %w", agent.Slug, err)
		}
	}
	return nil
}
