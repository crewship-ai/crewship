package pipeline

import (
	"strings"
	"testing"
)

// TestParse_ResourcesRoundTrip — the declarative `resources` block parses into
// the typed RoutineResources struct.
func TestParse_ResourcesRoundTrip(t *testing.T) {
	raw := `{
		"dsl_version": "1.0",
		"name": "deploy",
		"resources": {
			"datastores": [
				{"type": "postgres", "name": "main", "note": "writes table runs"},
				{"type": "redis"}
			],
			"tools": [
				{"type": "ansible", "name": "deploy.yml"},
				{"type": "kubectl"}
			]
		},
		"steps": [{"id": "s1", "type": "agent_run", "agent_slug": "deployer", "prompt": "go"}]
	}`
	d, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d.Resources == nil {
		t.Fatal("Resources is nil after parse")
	}
	if len(d.Resources.Datastores) != 2 {
		t.Fatalf("Datastores len = %d, want 2", len(d.Resources.Datastores))
	}
	if d.Resources.Datastores[0].Type != "postgres" || d.Resources.Datastores[0].Note != "writes table runs" {
		t.Errorf("Datastores[0] = %+v", d.Resources.Datastores[0])
	}
	if len(d.Resources.Tools) != 2 || d.Resources.Tools[0].Name != "deploy.yml" {
		t.Errorf("Tools = %+v", d.Resources.Tools)
	}
}

// TestValidate_ResourcesAccepted — a valid resources block passes validation.
func TestValidate_ResourcesAccepted(t *testing.T) {
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "deploy",
		Resources: &RoutineResources{
			Datastores: []DatastoreRef{{Type: "redis", Name: "cache"}},
			Tools:      []ToolRef{{Type: "ansible", Name: "deploy.yml"}},
		},
		Steps: []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "deployer", Prompt: "go"}},
	}
	if err := Validate(d, map[string]struct{}{"deployer": {}}, nil); err != nil {
		t.Fatalf("valid resources rejected: %v", err)
	}
}

// TestValidate_ResourcesRejectsEmptyType — a datastore/tool with an empty type
// is malformed.
func TestValidate_ResourcesRejectsEmptyType(t *testing.T) {
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "deploy",
		Resources:  &RoutineResources{Datastores: []DatastoreRef{{Name: "no-type"}}},
		Steps:      []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "deployer", Prompt: "go"}},
	}
	err := Validate(d, map[string]struct{}{"deployer": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "datastore") {
		t.Fatalf("want datastore type error, got: %v", err)
	}

	d2 := &DSL{
		DSLVersion: "1.0",
		Name:       "deploy",
		Resources:  &RoutineResources{Tools: []ToolRef{{Name: "no-type"}}},
		Steps:      []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "deployer", Prompt: "go"}},
	}
	if err := Validate(d2, map[string]struct{}{"deployer": {}}, nil); err == nil || !strings.Contains(err.Error(), "tool") {
		t.Fatalf("want tool type error, got: %v", err)
	}
}

// TestValidate_ResourcesRejectsOverCap — more than the cap of datastores/tools.
func TestValidate_ResourcesRejectsOverCap(t *testing.T) {
	many := make([]DatastoreRef, maxRoutineResources+1)
	for i := range many {
		many[i] = DatastoreRef{Type: "redis"}
	}
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "deploy",
		Resources:  &RoutineResources{Datastores: many},
		Steps:      []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "deployer", Prompt: "go"}},
	}
	if err := Validate(d, map[string]struct{}{"deployer": {}}, nil); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("want over-cap error, got: %v", err)
	}
}

// TestValidate_ResourcesRejectsBadTypeSlug — type must be a short slug.
func TestValidate_ResourcesRejectsBadTypeSlug(t *testing.T) {
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "deploy",
		Resources:  &RoutineResources{Tools: []ToolRef{{Type: "has spaces and is way too long " + strings.Repeat("x", 64)}}},
		Steps:      []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "deployer", Prompt: "go"}},
	}
	if err := Validate(d, map[string]struct{}{"deployer": {}}, nil); err == nil {
		t.Fatal("want bad-slug error, got nil")
	}
}
