package manifest

import (
	"strings"
	"testing"
)

func TestValidateBundleChecksSpec2PageSchema(t *testing.T) {
	t.Parallel()

	bundle, err := Load([]byte(`apiVersion: crewship/v1
kind: Page
metadata:
  name: Broken
  slug: broken
spec:
  panels: []
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// This assertion documents the historical split: Bundle.Validate owns
	// the legacy document model and therefore cannot be the only validation
	// call made by a manifest-facing tool.
	if err := bundle.Validate(); err != nil {
		t.Fatalf("legacy Bundle.Validate unexpectedly rejected Page: %v", err)
	}
	if err := ValidateBundle(bundle); err == nil || !strings.Contains(err.Error(), "at least one panel") {
		t.Fatalf("ValidateBundle error = %v, want Page panel schema failure", err)
	}
}

func TestValidateBundleAcceptsStructurallyValidStandalonePage(t *testing.T) {
	t.Parallel()

	bundle, err := Load([]byte(`apiVersion: crewship/v1
kind: Page
metadata:
  name: Operations
  slug: operations
spec:
  panels:
    - id: services
      schema: status.v1
      owner: crew/ops
      producer: script/watch.sh
      sla: 1m
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := ValidateBundle(bundle); err != nil {
		t.Fatalf("ValidateBundle: %v", err)
	}
}

func TestValidateBundleResolvesStandaloneCrewAndAgentAcrossRoutineAndPage(t *testing.T) {
	t.Parallel()

	bundle, err := Load([]byte(`apiVersion: crewship/v1
kind: Crew
metadata: {name: News, slug: news}
spec:
  runtime_image: example.invalid/runtime:1
---
apiVersion: crewship/v1
kind: Agent
metadata: {name: Writer, slug: writer}
spec:
  crew_slug: news
  agent_role: LEAD
  cli_adapter: CLAUDE_CODE
  llm: {provider: ANTHROPIC, model: claude-sonnet-5}
  prompt: Write accurate news.
---
apiVersion: crewship/v1
kind: Routine
metadata:
  name: Publish
  slug: publish
  labels: {crew: news}
spec:
  dsl_version: "1.0"
  steps:
    - id: write
      type: agent_run
      agent_slug: writer
      prompt: Write.
---
apiVersion: crewship/v1
kind: Page
metadata: {name: News, slug: news}
spec:
  panels:
    - id: digest
      schema: narrative.v1
      owner: crew/news
      producer: routine/publish
      sla: 1h
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := ValidateBundle(bundle); err != nil {
		t.Fatalf("standalone cross-kind bundle should validate: %v", err)
	}
}
