package manifest

import (
	"strings"
	"testing"
)

// TestValidateAllKinds_DetectsDuplicateAgentSlug pins the gap
// surfaced in bug-hunt iter 10: two Agent docs with the same
// metadata.slug in one apply bundle used to BOTH pass validation,
// then the second would silently overwrite the first at server
// apply (CREATE-OR-UPDATE keyed on slug). The new duplicate-slug
// pass in validateAllKinds catches this at validate time.
func TestValidateAllKinds_DetectsDuplicateAgentSlug(t *testing.T) {
	t.Parallel()
	doc := `
apiVersion: crewship/v1
kind: Agent
metadata: { name: A, slug: dup }
spec:
  crew_slug: x
  role: AGENT
  cli_adapter: CLAUDE_CODE
  llm_provider: ANTHROPIC
  llm_model: claude-haiku-4-5
  tool_profile: CODING
  prompt: a
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: B, slug: dup }
spec:
  crew_slug: x
  role: AGENT
  cli_adapter: CLAUDE_CODE
  llm_provider: ANTHROPIC
  llm_model: claude-haiku-4-5
  tool_profile: CODING
  prompt: b
`
	b, err := Load([]byte(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Agents) != 2 {
		t.Fatalf("expected 2 Agent docs in bundle, got %d", len(b.Agents))
	}
	wsCtx := buildKindWorkspaceContext(b)
	err = validateAllKinds(b, wsCtx)
	if err == nil {
		t.Fatal("expected duplicate-slug error, got nil — regression: two Agent docs with same slug should fail validation")
	}
	if !strings.Contains(err.Error(), `duplicate Agent slug "dup"`) {
		t.Errorf("error %q missing expected substring `duplicate Agent slug \"dup\"`", err.Error())
	}
}

// TestValidateAllKinds_DetectsDuplicatesAcrossKinds confirms the
// check runs for every SPEC-2 kind, not just Agent. Picks a few
// representative ones to keep the table compact.
func TestValidateAllKinds_DetectsDuplicatesAcrossKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind    string
		yaml    string
		wantSub string
	}{
		{
			kind:    "Skill",
			yaml:    skillTwiceYAML("dup-skill"),
			wantSub: `duplicate Skill slug "dup-skill"`,
		},
		{
			kind:    "Crew",
			yaml:    crewTwiceYAML("dup-crew"),
			wantSub: `duplicate Crew slug "dup-crew"`,
		},
		{
			kind:    "Integration",
			yaml:    integrationTwiceYAML("dup-integ"),
			wantSub: `duplicate Integration slug "dup-integ"`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			b, err := Load([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("Load %s: %v", tc.kind, err)
			}
			wsCtx := buildKindWorkspaceContext(b)
			err = validateAllKinds(b, wsCtx)
			if err == nil {
				t.Fatalf("%s: expected duplicate-slug error, got nil", tc.kind)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("%s: error %q missing expected substring %q", tc.kind, err.Error(), tc.wantSub)
			}
		})
	}
}

// TestValidateAllKinds_UniqueSlugsPass confirms the duplicate
// check doesn't fire on bundles where every slug is unique —
// guards against a regression that would make `crewship apply`
// fail on legitimate manifests.
func TestValidateAllKinds_UniqueSlugsPass(t *testing.T) {
	t.Parallel()
	doc := `
apiVersion: crewship/v1
kind: Agent
metadata: { name: A, slug: alice }
spec:
  crew_slug: x
  role: AGENT
  cli_adapter: CLAUDE_CODE
  llm_provider: ANTHROPIC
  llm_model: m
  tool_profile: CODING
  prompt: a
---
apiVersion: crewship/v1
kind: Agent
metadata: { name: B, slug: bob }
spec:
  crew_slug: x
  role: AGENT
  cli_adapter: CLAUDE_CODE
  llm_provider: ANTHROPIC
  llm_model: m
  tool_profile: CODING
  prompt: b
`
	b, err := Load([]byte(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wsCtx := buildKindWorkspaceContext(b)
	if err := validateAllKinds(b, wsCtx); err != nil {
		t.Errorf("expected nil error for unique slugs, got %v", err)
	}
}

func skillTwiceYAML(slug string) string {
	return `
apiVersion: crewship/v1
kind: Skill
metadata: { name: A, slug: ` + slug + ` }
spec: { description: x, inline: "body a" }
---
apiVersion: crewship/v1
kind: Skill
metadata: { name: B, slug: ` + slug + ` }
spec: { description: y, inline: "body b" }
`
}

func crewTwiceYAML(slug string) string {
	return `
apiVersion: crewship/v1
kind: Crew
metadata: { name: A, slug: ` + slug + ` }
spec: { description: a, icon: terminal, color: "#3B82F6", runtime_image: img }
---
apiVersion: crewship/v1
kind: Crew
metadata: { name: B, slug: ` + slug + ` }
spec: { description: b, icon: terminal, color: "#3B82F6", runtime_image: img }
`
}

func integrationTwiceYAML(slug string) string {
	return `
apiVersion: crewship/v1
kind: Integration
metadata: { name: A, slug: ` + slug + ` }
spec: { crew_slug: x, transport: stdio, command: foo }
---
apiVersion: crewship/v1
kind: Integration
metadata: { name: B, slug: ` + slug + ` }
spec: { crew_slug: x, transport: stdio, command: bar }
`
}
