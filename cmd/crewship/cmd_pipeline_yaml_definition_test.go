package main

// `routine get -f yaml` and `routine list -f yaml` carry the routine's DSL
// `definition`, which arrives from the server as an opaque JSON document.
//
// Held as a bare json.RawMessage that document IS a []byte, and yaml.v3 has
// no special case for it: it renders a byte slice as a sequence of integers,
// one per line, so the field the command exists to show came out as
//
//	definition:
//	    - 123
//	    - 34
//	    ...
//
// exit 0, on an invocation docs/cli/routine.mdx newly advertises. internal/cli
// already settled this shape once — cli.RawJSON is json.RawMessage plus a
// MarshalYAML that decodes the document so the encoder is handed its SHAPE —
// and its doc comment says in as many words that any other field holding an
// opaque document should take the type. This is that other field.

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func yamlDefinitionRowFixture() map[string]any {
	return map[string]any{
		"id": "p1", "slug": "email-fetch", "name": "Email Fetch",
		"description": "fetches", "dsl_version": "1",
		"author_crew_id": "crew_a", "author_agent_id": "ag_1", "authored_via": "cli",
		"invocation_count": 3,
		"created_at":       "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
		"definition": map[string]any{
			"name":  "Email Fetch",
			"steps": []any{map[string]any{"id": "s1", "agent": "researcher"}},
		},
	}
}

func TestPipelineGet_YAMLDefinitionIsADocumentNotBytes(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(pipelinesPathCov()+"/email-fetch", clitest.JSONResponse(200, yamlDefinitionRowFixture()))
	setFormatCov(t, "yaml")

	out, err := captureStdoutCov(t, func() error {
		return pipelineGetCmd.RunE(pipelineGetCmd, []string{"email-fetch"})
	})
	if err != nil {
		t.Fatalf("RunE under -f yaml: %v\nstdout:\n%s", err, out)
	}

	var doc map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &doc); uerr != nil {
		t.Fatalf("-f yaml stdout is not valid YAML: %v\ngot:\n%s", uerr, out)
	}

	def, ok := doc["definition"].(map[string]any)
	if !ok {
		t.Fatalf("definition rendered as %T, want a mapping — a json.RawMessage "+
			"encoded by yaml.v3 comes out as a sequence of byte integers.\ngot:\n%s",
			doc["definition"], out)
	}
	steps, ok := def["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("definition.steps = %#v, want the one step the server sent\ngot:\n%s", def["steps"], out)
	}

	// The visible symptom, asserted directly so a regression is named rather
	// than inferred from a type assertion failure.
	if strings.Contains(out, "    - 123") {
		t.Errorf("definition is being spelled out byte by byte:\n%s", out)
	}
}

func TestPipelineList_YAMLDefinitionIsADocumentNotBytes(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(pipelinesPathCov(), clitest.JSONResponse(200, []map[string]any{yamlDefinitionRowFixture()}))
	setFormatCov(t, "yaml")

	out, err := captureStdoutCov(t, func() error {
		return pipelineListCmd.RunE(pipelineListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE under -f yaml: %v\nstdout:\n%s", err, out)
	}

	var rows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &rows); uerr != nil {
		t.Fatalf("-f yaml stdout is not valid YAML: %v\ngot:\n%s", uerr, out)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1\ngot:\n%s", len(rows), out)
	}
	if _, ok := rows[0]["definition"].(map[string]any); !ok {
		t.Errorf("definition rendered as %T, want a mapping\ngot:\n%s", rows[0]["definition"], out)
	}
}

// `-f json` must be byte-for-byte the document the server sent — cli.RawJSON's
// MarshalJSON hands the bytes back, and an absent definition is `null`, not
// `""`. This is the half a naive "decode it into an any" fix would break.
func TestPipelineGet_JSONDefinitionStaysRaw(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(pipelinesPathCov()+"/email-fetch", clitest.JSONResponse(200, yamlDefinitionRowFixture()))
	setFormatCov(t, "json")

	out, err := captureStdoutCov(t, func() error {
		return pipelineGetCmd.RunE(pipelineGetCmd, []string{"email-fetch"})
	})
	if err != nil {
		t.Fatalf("RunE under -f json: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, `"steps"`) || !strings.Contains(out, `"researcher"`) {
		t.Errorf("-f json lost the definition document:\n%s", out)
	}
}

// The human renderer prints the definition as indented JSON under a
// "Definition:" heading. cli.RawJSON is still a []byte, so json.Indent keeps
// working — assert it, because the type change is the kind that compiles and
// silently prints a Go struct instead.
func TestPipelineGet_HumanDefinitionStillPrettyPrints(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(pipelinesPathCov()+"/email-fetch", clitest.JSONResponse(200, yamlDefinitionRowFixture()))
	setFormatCov(t, "")

	out, err := captureStdoutCov(t, func() error {
		return pipelineGetCmd.RunE(pipelineGetCmd, []string{"email-fetch"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Slug:", "email-fetch", "Definition:", `"steps"`} {
		if !strings.Contains(out, want) {
			t.Errorf("human stdout missing %q; got:\n%s", want, out)
		}
	}
}
