package seeddata

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// A COMPLETE interpolation, not the bare substring: a prompt that merely says
// the word "secrets." in prose would otherwise let a fabricated declaration
// through the check below. Tolerates {{secrets.x}} and {{ secrets.x }} alike.
var secretRef = regexp.MustCompile(`\{\{[[:space:]]*secrets\.[^[:space:]}]+[[:space:]]*\}\}`)

// The seeded dataset must be able to satisfy its own declarations.
//
// It could not. 17 routines and 6 eval scenarios declared
//
//	credentials_required: [{"type": "anthropic", "scope": "any"}]
//
// and every run of them 422'd on a freshly seeded instance with
// `routine requires credential of type "anthropic" not present in the vault`.
// That single mistake was the root cause of four of the five failing tests in
// the stage e2e harness — including the HITL approval gate, whose first step is
// an agent_run that 422s before the `wait` step is ever reached, so it read for
// months as a missing approval-gate feature.
//
// Three things were wrong, and each of these tests pins one:
//
//  1. `anthropic` is a PROVIDER, not a credential type. The vault's `type`
//     column holds API_KEY / AI_CLI_TOKEN / SECRET / …, and the gate matches
//     `UPPER(type) = UPPER(?)`, so the declaration could never be satisfied.
//     docs/manifest/routine.md's own example puts the provider in `scope`:
//     `{ type: API_KEY, scope: anthropic }`.
//
//  2. The declaration was fabricated. credentials_required exists to make an
//     unresolvable `{{ secrets.<type> }}` a hard failure (see dsl.go). NONE of
//     the seeded routines reference `secrets.` anywhere — the agent's LLM token
//     is supplied by the agent runtime, not by the routine's secret resolver.
//
//  3. It is not even knowable at authoring time: ResolveAnthropicCredential
//     seeds type AI_CLI_TOKEN for an `sk-ant-oat` OAuth token and API_KEY
//     otherwise. A routine cannot hard-code the type of a credential whose type
//     depends on the operator's key.
//
// Nothing validates the type at save time (RequiredCredentialTypes only
// lowercases the string), so a bad declaration persists happily and only fails
// at dispatch. These tests are that missing gate.

// seedCredentialTypes is what the seeder can actually create — the only types
// a declaration could be satisfied by on a freshly seeded instance.
var seedCredentialTypes = map[string]bool{
	"API_KEY":        true,
	"AI_CLI_TOKEN":   true,
	"CLI_TOKEN":      true,
	"SECRET":         true,
	"OAUTH2":         true,
	"ENDPOINT_URL":   true,
	"GENERIC_SECRET": true,
}

// credsRequired pulls the declared entries out of a seeded definition.
func credsRequired(t *testing.T, def map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := def["credentials_required"]
	if !ok || raw == nil {
		return nil
	}
	// The seed builds these as []map[string]interface{} literals; round-trip
	// through JSON so the test does not depend on that exact Go type.
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal credentials_required: %v", err)
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal credentials_required: %v", err)
	}
	return out
}

func allSeededRoutines() []RoutineDef {
	return append(append([]RoutineDef(nil), Routines...), EvalScenarios...)
}

// A declaration only means something if the routine actually resolves a secret.
func TestSeededRoutines_DeclareOnlyCredentialsTheyConsume(t *testing.T) {
	for _, r := range allSeededRoutines() {
		declared := credsRequired(t, r.Definition)
		if len(declared) == 0 {
			continue
		}
		body, err := json.Marshal(r.Definition)
		if err != nil {
			t.Fatalf("%s: marshal definition: %v", r.Slug, err)
		}
		if !secretRef.Match(body) {
			t.Errorf("routine %q declares credentials_required %v but never references {{ secrets.* }} — "+
				"the declaration is fabricated and will 422 every run", r.Slug, declared)
		}
	}
}

// A declared type must be a type the vault can hold, not a provider name.
func TestSeededRoutines_CredentialTypesAreRealTypes(t *testing.T) {
	for _, r := range allSeededRoutines() {
		for _, cr := range credsRequired(t, r.Definition) {
			typ, _ := cr["type"].(string)
			typ = strings.TrimSpace(typ)
			if typ == "" {
				t.Errorf("routine %q declares a credential with an empty type", r.Slug)
				continue
			}
			if !seedCredentialTypes[strings.ToUpper(typ)] {
				t.Errorf("routine %q requires credential type %q, which the seeder never creates — "+
					"provider names belong in `scope` (see docs/manifest/routine.md: "+
					"{ type: API_KEY, scope: anthropic })", r.Slug, typ)
			}
		}
	}
}
