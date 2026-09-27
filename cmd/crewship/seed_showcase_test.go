package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// The default catalogue is a connected local demo, not a set of unrelated examples.
func TestSeedShowcase_BusinessProjects(t *testing.T) {
	if len(seeddata.Stories) != 4 {
		t.Fatalf("expected four initial business stories")
	}
	routines := map[string]seeddata.RoutineDef{}
	for _, r := range seeddata.Routines {
		routines[r.Slug] = r
	}
	agents := map[string]seeddata.AgentDef{}
	for _, a := range seeddata.Agents {
		agents[a.Slug] = a
	}
	pages := map[string]seeddata.PageDef{}
	for _, p := range seeddata.Pages {
		pages[p.Slug] = p
	}
	for _, s := range seeddata.Stories {
		t.Run(s.Slug, func(t *testing.T) {
			if agents[s.Agent].CrewSlug != s.Crew {
				t.Fatal("story agent belongs to another crew")
			}
			if pages["demo-"+s.Slug].Project == nil {
				t.Fatal("story needs a custom Page")
			}
			issueFound := false
			for _, i := range seeddata.Issues {
				if i.StorySlug == s.Slug {
					issueFound = true
					if i.TargetState != "TODO" || i.Assignee != s.Agent || i.RoutineSlug != "demo-"+s.Slug+"-check" {
						t.Fatal("Issue must start TODO and link to its check and agent")
					}
				}
			}
			if !issueFound {
				t.Fatal("missing prepared Issue")
			}
			for _, action := range []string{"check", "draft", "resolve"} {
				r, ok := routines["demo-"+s.Slug+"-"+action]
				if !ok {
					t.Fatalf("missing %s routine", action)
				}
				if r.CrewSlug != s.Crew || r.AuthorAgentSlug != s.Agent {
					t.Fatal("routine author mismatch")
				}
				raw, _ := json.Marshal(r.Definition)
				dsl, e := pipeline.Parse(raw)
				if e != nil {
					t.Fatal(e)
				}
				if dsl.MaxConcurrent != 1 || dsl.ConcurrencyKey != "demo-story-"+s.Slug {
					t.Fatal("actions must share one concurrency slot")
				}
				for _, step := range dsl.Steps {
					if action != "draft" && step.Type == pipeline.StepAgentRun {
						t.Fatal("basic story must work without AI")
					}
					if step.Type == pipeline.StepHTTP {
						t.Fatal("default demo must not poll public websites")
					}
				}
			}
		})
	}
	for _, r := range seeddata.Routines {
		if !strings.HasPrefix(r.Slug, "demo-") && r.Slug != "pages-operations-sample" {
			t.Errorf("obsolete default routine %s", r.Slug)
		}
	}
}
