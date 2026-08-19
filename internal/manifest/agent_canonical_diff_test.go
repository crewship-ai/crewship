package manifest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/askforms"
)

// `crewship apply` has to converge: a manifest applied twice reports no change
// the second time, or drift-detection CI never settles and every run PATCHes
// an agent that nobody edited.
//
// Two agent columns are canonicalised BY THE SERVER on write —
// suggested_prompts through normalizeSuggestedPrompts (trim each line, drop
// blanks, join with "\n", no trailing newline) and ask_forms through
// askforms.Normalize (fixed key order, two-space indent, defaults spelled
// out). Comparing what the manifest literally says against what the server
// stored therefore compares two different spellings of the same value:
//
//   - a YAML block scalar — the form schema.go documents for
//     suggested_prompts — unmarshals WITH a trailing newline, so the raw
//     comparison is true on every run;
//   - hand-written ask_forms JSON will not match Normalize's key order and
//     indent byte for byte, so only export→apply round-trips ever converged,
//     which is exactly why the round-trip tests passed while `apply` did not.
//
// These pin the diff on the canonical form of both sides.

// blockScalarPromptsYAML is the documented shape, verbatim from
// schema.go's doc comment: `|`, not `|-`. The chomping indicator is the whole
// bug — `|` keeps the trailing newline.
const blockScalarPromptsYAML = `
apiVersion: crewship/v1
kind: Crew
metadata: { name: Ops, slug: ops }
spec:
  agents:
    - slug: amy
      name: Amy
      agent_role: LEAD
      cli_adapter: CLAUDE_CODE
      tool_profile: CODING
      timeout_seconds: 1800
      memory_enabled: true
      prompt: hi amy
      suggested_prompts: |
        What shipped this week?
        Who is blocked?
      ask_forms: >-
        [{"id":"receipt","label":"Add a receipt","template":"Supplier: {{supplier}}","attachment":"required","fields":[{"name":"supplier","label":"Supplier","type":"text","required":true}]}]
`

// The block scalar really does carry the trailing newline the diff trips on.
// Asserted rather than assumed: if a YAML library ever changed this, the tests
// below would still pass while proving nothing.
func TestBlockScalarSuggestedPromptsKeepsTrailingNewline(t *testing.T) {
	b, err := Load([]byte(blockScalarPromptsYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := b.Documents[0].Spec.Agents[0].SuggestedPrompts
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("suggested_prompts = %q — the documented block scalar no longer carries a "+
			"trailing newline, so this whole file is testing a case that cannot happen", got)
	}
}

func TestAgentBodyDiffers_ComparesCanonicalValues(t *testing.T) {
	strp := func(s string) *string { return &s }

	// What the server stores for the ask form the manifests below declare.
	canonicalForms, err := askforms.Normalize(oneAskForm)
	if err != nil {
		t.Fatalf("Normalize(oneAskForm): %v", err)
	}
	if canonicalForms == oneAskForm {
		t.Fatal("the fixture form is already canonical — it cannot exercise the ask_forms case")
	}
	// Same form, keys in a different order: the shape a person types.
	reordered := `[{"label":"Add a receipt","id":"receipt","attachment":"required","fields":[{"required":true,"type":"text","name":"supplier","label":"Supplier"}],"template":"Supplier: {{supplier}}"}]`

	cases := []struct {
		name     string
		declared Agent
		existing AgentResponse
		want     bool
	}{{
		name:     "block-scalar prompts against the stored canonical value",
		declared: Agent{SuggestedPrompts: twoPrompts + "\n"},
		existing: AgentResponse{SuggestedPrompts: strp(twoPrompts)},
		want:     false,
	}, {
		name:     "prompts with trailing spaces and a blank line between them",
		declared: Agent{SuggestedPrompts: "What shipped this week?  \n\n  Who is blocked?\n"},
		existing: AgentResponse{SuggestedPrompts: strp(twoPrompts)},
		want:     false,
	}, {
		name:     "a stored value that predates the normaliser",
		declared: Agent{SuggestedPrompts: twoPrompts},
		existing: AgentResponse{SuggestedPrompts: strp(twoPrompts + "\n")},
		want:     false,
	}, {
		name:     "a prompt that genuinely changed is still drift",
		declared: Agent{SuggestedPrompts: "What shipped this week?\nWho is stuck?\n"},
		existing: AgentResponse{SuggestedPrompts: strp(twoPrompts)},
		want:     true,
	}, {
		name:     "hand-written compact ask_forms against the stored canonical JSON",
		declared: Agent{AskForms: oneAskForm},
		existing: AgentResponse{AskForms: strp(canonicalForms)},
		want:     false,
	}, {
		name:     "the same form with the keys in another order",
		declared: Agent{AskForms: reordered},
		existing: AgentResponse{AskForms: strp(canonicalForms)},
		want:     false,
	}, {
		name:     "an ask form that genuinely changed is still drift",
		declared: Agent{AskForms: strings.Replace(oneAskForm, "Add a receipt", "Add an invoice", 1)},
		existing: AgentResponse{AskForms: strp(canonicalForms)},
		want:     true,
	}, {
		// Unparseable JSON has no canonical form. It must not silently
		// compare equal to whatever is stored — `crewship validate` refuses
		// it, and the diff falls back to the literal comparison.
		name:     "ask_forms that cannot be parsed falls back to a literal compare",
		declared: Agent{AskForms: `[{"id":`},
		existing: AgentResponse{AskForms: strp(canonicalForms)},
		want:     true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every other field matched, so the verdict is only ever about
			// the two canonicalised columns.
			base := func(a Agent) *Agent {
				a.Name, a.AgentRole, a.CLIAdapter = "Amy", "LEAD", "CLAUDE_CODE"
				a.ToolProfile, a.TimeoutSeconds, a.MemoryEnabled = "CODING", 1800, true
				return &a
			}
			ex := tc.existing
			ex.Name, ex.AgentRole, ex.CLIAdapter = "Amy", "LEAD", "CLAUDE_CODE"
			ex.ToolProfile, ex.TimeoutSeconds, ex.MemoryEnabled = "CODING", 1800, true

			if got := agentBodyDiffers(&ex, base(tc.declared)); got != tc.want {
				t.Errorf("agentBodyDiffers = %v, want %v", got, tc.want)
			}
		})
	}
}

// The end-to-end shape of the bug: apply the documented manifest against a
// server that already holds what a previous apply wrote, and the plan must be
// unchanged. Before the fix this reported `update` on every invocation, for
// both columns.
func TestBuildPlan_DocumentedManifestConvergesOnSecondApply(t *testing.T) {
	canonicalForms, err := askforms.Normalize(oneAskForm)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	stored := func(v string) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}

	b, err := Load([]byte(blockScalarPromptsYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	stub := newCovStub()
	stub.on("GET", "/api/v1/crews", 200, `[{"id":"c1","slug":"ops","name":"Ops"}]`)
	stub.on("GET", "/api/v1/credentials", 200, `[]`)
	// The agent as the server holds it after the first apply: both columns
	// in the canonical form the write path produced.
	stub.on("GET", "/api/v1/agents?crew_id=c1", 200, `[
		{"id":"a1","slug":"amy","name":"Amy","agent_role":"LEAD","cli_adapter":"CLAUDE_CODE",
		 "tool_profile":"CODING","timeout_seconds":1800,"memory_enabled":true,
		 "system_prompt":"hi amy","suggested_prompts":`+stored(twoPrompts)+`,
		 "ask_forms":`+stored(canonicalForms)+`}
	]`)
	stub.on("GET", "/api/v1/crews/c1/integrations", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/skills", 200, `[]`)
	stub.on("GET", "/api/v1/agents/a1/credentials", 200, `[]`)
	stub.on("GET", "/api/v1/workspaces/ws_cov/skills", 200, `[]`)

	plan, err := BuildPlan(context.Background(), NewClient(stub), b, Options{Mode: ApplyUpsert})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if got := findAgentItem(t, plan).Action; got != ActionUnchanged {
		t.Errorf("re-applying the documented manifest reports %v, want unchanged — "+
			"`apply` would PATCH on every run and drift CI would never settle", got)
	}
}
