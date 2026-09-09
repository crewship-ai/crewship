package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// FixtureStepInput contains explicit local test data, never a production cache.
type FixtureStepInput struct {
	Definition    json.RawMessage   `json:"definition"`
	StepID        string            `json:"step_id"`
	Inputs        map[string]any    `json:"inputs,omitempty"`
	StepOutputs   map[string]string `json:"step_outputs,omitempty"`
	FixtureOutput *string           `json:"fixture_output,omitempty"`
	// Runtime context contains explicit samples; no environment or vault is read.
	Env      map[string]string `json:"env,omitempty"`
	Metadata map[string]any    `json:"metadata,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

type FixtureStepResult struct {
	ExecutionMode      string   `json:"execution_mode"`
	StepID             string   `json:"step_id"`
	StepType           StepType `json:"step_type"`
	DefinitionHash     string   `json:"definition_hash"`
	FixtureHash        string   `json:"fixture_hash"`
	OutputSource       string   `json:"output_source"`
	Output             string   `json:"output"`
	Valid              bool     `json:"valid"`
	ValidationDeclared bool     `json:"validation_declared"`
	ValidationReason   string   `json:"validation_reason,omitempty"`
	Limitations        []string `json:"limitations"`
}

// TestStepWithFixtures reuses the production transform and structural output
// validator. It never dispatches a step and has no store, runner, credentials,
// HTTP client, emitter, clock, or container wiring. Adding another executable
// type here requires proving that its implementation is effect-free.
func TestStepWithFixtures(in FixtureStepInput) (*FixtureStepResult, error) {
	raw, err := ToCanonicalJSON(in.Definition)
	if err != nil {
		return nil, err
	}
	dsl, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if err = Validate(dsl, nil, nil); err != nil {
		return nil, err
	}
	var selected *Step
	seen := map[string]bool{}
	for i := range dsl.Steps {
		if seen[dsl.Steps[i].ID] {
			return nil, fmt.Errorf("duplicate step ID %q", dsl.Steps[i].ID)
		}
		seen[dsl.Steps[i].ID] = true
		if dsl.Steps[i].ID == in.StepID {
			selected = &dsl.Steps[i]
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("step %q is not a top-level step in this recipe", in.StepID)
	}
	if err = ValidateFormInputs(dsl, in.Inputs); err != nil {
		return nil, err
	}
	inputs := mergeInputs(in.Inputs, dsl)
	result := &FixtureStepResult{ExecutionMode: "fixtures", StepID: selected.ID, StepType: selected.Type,
		DefinitionHash: fmt.Sprintf("%x", sha256.Sum256(raw)),
		Limitations:    []string{"Single step only: dependencies, conditions, retries and outcome graders are not executed.", "This does not validate real service behavior, credentials, model quality, or publication readiness."}}
	switch selected.Type {
	case StepTransform:
		if in.FixtureOutput != nil {
			return nil, fmt.Errorf("transform steps compute their output; omit fixture_output")
		}
		render := RenderContext{Inputs: inputs, StepOutputs: in.StepOutputs, Env: in.Env, Metadata: in.Metadata, Secrets: in.Secrets}
		// Unlike live rendering, a fixture test must not silently substitute
		// missing evidence with an empty string. Existence is separate from a
		// legitimate empty/null output, including projected JSON properties.
		for _, match := range templateRE.FindAllStringSubmatch(selected.Transform.Input, -1) {
			if !referenceValueExists(strings.TrimSpace(match[1]), render) {
				return nil, fmt.Errorf("missing fixture value for %s", match[1])
			}
		}
		result.Output, _, _, err = (&Executor{}).runTransformStep(*selected, render)
		if err != nil {
			return nil, err
		}
		result.OutputSource = "transform"
	case StepAgentRun, StepHTTP, StepScript:
		if in.FixtureOutput == nil {
			return nil, fmt.Errorf("step %s (%s) requires fixture_output; live execution is blocked", selected.ID, selected.Type)
		}
		result.Output = *in.FixtureOutput
		result.OutputSource = "fixture"
	default:
		return nil, fmt.Errorf("fixture testing does not support %s steps; no work was executed", selected.Type)
	}
	result.Valid, result.ValidationReason = validateFixtureOutput(result.Output, selected.Validation)
	if v := selected.Validation; v != nil {
		result.ValidationDeclared = len(v.Schema) > 0 || len(v.MustContain) > 0 || len(v.MustNotContain) > 0 || v.MinLength != nil || v.MaxLength != nil
	}
	evidence, err := json.Marshal(struct {
		DefinitionHash string            `json:"definition_hash"`
		StepID         string            `json:"step_id"`
		Inputs         map[string]any    `json:"inputs"`
		StepOutputs    map[string]string `json:"step_outputs"`
		FixtureOutput  *string           `json:"fixture_output"`
		Env            map[string]string `json:"env"`
		Metadata       map[string]any    `json:"metadata"`
		Secrets        map[string]string `json:"secrets"`
	}{result.DefinitionHash, in.StepID, inputs, in.StepOutputs, in.FixtureOutput, in.Env, in.Metadata, in.Secrets})
	if err != nil {
		return nil, err
	}
	result.FixtureHash = fmt.Sprintf("%x", sha256.Sum256(evidence))
	return result, nil
}

// Schema references are also effects: the regular schema compiler can load a
// file or a registered URL scheme. Fixtures always use a private offline
// compiler, never that cache or a mutable global loader.
func validateFixtureOutput(output string, validation *Validation) (bool, string) {
	if validation == nil {
		return true, ""
	}
	structural := *validation
	structural.Schema = nil
	if ok, reason := ValidateStepOutput(output, &structural); !ok {
		return ok, reason
	}
	if len(validation.Schema) == 0 {
		return true, ""
	}
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, fmt.Errorf("external schema references are blocked in fixture tests")
	}
	if err := compiler.AddResource("inline://fixture.json", strings.NewReader(string(validation.Schema))); err != nil {
		return false, "schema invalid: " + truncate(err.Error(), 200)
	}
	schema, err := compiler.Compile("inline://fixture.json")
	if err != nil {
		return false, "schema invalid: " + truncate(err.Error(), 200)
	}
	var value any
	if err = DecodeAgentJSON(output, &value); err != nil {
		return false, "output not valid JSON: " + truncate(err.Error(), 200)
	}
	if err = schema.Validate(value); err != nil {
		return false, "schema validation: " + truncate(err.Error(), 200)
	}
	return true, ""
}
