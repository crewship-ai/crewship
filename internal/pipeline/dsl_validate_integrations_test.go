package pipeline

import (
	"reflect"
	"testing"
)

func TestParse_IntegrationsRequired_RoundTripNormalized(t *testing.T) {
	raw := `{"name":"x","integrations_required":["GitHub"," Slack ","github"],"steps":[{"id":"a","type":"agent_run","agent_slug":"lead","prompt":"hi"}]}`
	dsl, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Parse normalizes in place (lowercase + trim) but keeps duplicates.
	want := []string{"github", "slack", "github"}
	if !reflect.DeepEqual(dsl.IntegrationsRequired, want) {
		t.Fatalf("IntegrationsRequired = %#v, want %#v", dsl.IntegrationsRequired, want)
	}
	// NormalizedIntegrationsRequired dedupes + drops empties.
	gotNorm := dsl.NormalizedIntegrationsRequired()
	if !reflect.DeepEqual(gotNorm, []string{"github", "slack"}) {
		t.Fatalf("NormalizedIntegrationsRequired = %#v, want [github slack]", gotNorm)
	}
}

func TestValidate_IntegrationsRequired_AcceptsValidList(t *testing.T) {
	dsl := &DSL{
		Name:                 "x",
		IntegrationsRequired: []string{"github", "slack"},
		Steps:                []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "lead", Prompt: "hi"}},
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("Validate rejected a valid integrations list: %v", err)
	}
}

func TestValidate_IntegrationsRequired_RejectsEmptyEntry(t *testing.T) {
	dsl := &DSL{
		Name:                 "x",
		IntegrationsRequired: []string{"github", "  "},
		Steps:                []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "lead", Prompt: "hi"}},
	}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Fatal("Validate accepted an empty/whitespace integration entry, want error")
	}
}

func TestParse_IntegrationsRequired_NonStringRejected(t *testing.T) {
	// Non-string entries can't unmarshal into []string — Parse must error.
	raw := `{"name":"x","integrations_required":[123],"steps":[]}`
	if _, err := Parse([]byte(raw)); err == nil {
		t.Fatal("Parse accepted a non-string integration entry, want error")
	}
}

func TestValidate_IntegrationsRequired_RejectsTooMany(t *testing.T) {
	many := make([]string, maxIntegrationsRequired+1)
	for i := range many {
		many[i] = "app"
	}
	dsl := &DSL{
		Name:                 "x",
		IntegrationsRequired: many,
		Steps:                []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "lead", Prompt: "hi"}},
	}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Fatal("Validate accepted an over-cap integrations list, want error")
	}
}

func TestNormalizedIntegrationsRequired_NilWhenEmpty(t *testing.T) {
	var d *DSL
	if got := d.NormalizedIntegrationsRequired(); got != nil {
		t.Fatalf("nil DSL → %#v, want nil", got)
	}
	d = &DSL{IntegrationsRequired: []string{"  ", ""}}
	if got := d.NormalizedIntegrationsRequired(); got != nil {
		t.Fatalf("all-empty → %#v, want nil", got)
	}
}
