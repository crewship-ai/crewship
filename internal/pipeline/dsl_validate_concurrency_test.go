package pipeline

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// dsl_validate_concurrency.go — concurrency_key empty-render gate (#832).
//
// A routine that declares `concurrency_key: "{{ inputs.account_id }}"` is
// asking the platform to serialise runs per tenant. If that key can render to
// the empty string — no literal text survives AND every reference may render
// empty (optional/defaultless input, or a non-input ref) — a run that omits
// the inputs dies at RUN time with ErrConcurrencyKeyEmpty (the documented
// "concurrency_key rendered to empty value" 500). Validate rejects exactly
// those keys pre-save. It is a PARITY check: a surviving literal prefix
// ("tenant-{{ inputs.account_id }}") or a single anchoring ref (one required /
// defaulted input) means the key is never empty, so it validates — matching
// what the runtime actually does.
// ---------------------------------------------------------------------------

func concurrencyProbeDSL() *DSL {
	return &DSL{
		Name:   "tenant-serialised",
		Inputs: []InputSpec{{Name: "account_id", Type: "string", Required: true}},
		Steps: []Step{
			{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "do the work"},
		},
		ConcurrencyKey: "{{ inputs.account_id }}",
	}
}

func TestValidate_Concurrency_RequiredInputOK(t *testing.T) {
	if err := Validate(concurrencyProbeDSL(), nil, nil); err != nil {
		t.Fatalf("concurrency_key bound to a required input should validate, got: %v", err)
	}
}

func TestValidate_Concurrency_NonEmptyDefaultOK(t *testing.T) {
	dsl := concurrencyProbeDSL()
	dsl.Inputs = []InputSpec{{Name: "account_id", Type: "string", Required: false, Default: "global"}}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("concurrency_key bound to an input with a non-empty default should validate, got: %v", err)
	}
}

func TestValidate_Concurrency_OptionalDefaultlessInputRejected(t *testing.T) {
	dsl := concurrencyProbeDSL()
	dsl.Inputs = []InputSpec{{Name: "account_id", Type: "string", Required: false}}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("concurrency_key referencing an optional, defaultless input must be rejected pre-save")
	}
	if !strings.Contains(err.Error(), "concurrency_key") || !strings.Contains(err.Error(), "account_id") {
		t.Errorf("error should name concurrency_key and the offending input, got: %v", err)
	}
}

func TestValidate_Concurrency_EmptyStringDefaultRejected(t *testing.T) {
	dsl := concurrencyProbeDSL()
	// A default of "" still renders empty → same silent-bypass as no default.
	dsl.Inputs = []InputSpec{{Name: "account_id", Type: "string", Required: false, Default: ""}}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("concurrency_key bound to an input whose default is the empty string must be rejected")
	}
	if !strings.Contains(err.Error(), "concurrency_key") {
		t.Errorf("error should name concurrency_key, got: %v", err)
	}
}

func TestValidate_Concurrency_UnknownInputRejected(t *testing.T) {
	dsl := concurrencyProbeDSL()
	dsl.ConcurrencyKey = "{{ inputs.ghost }}"
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("concurrency_key referencing an undeclared input must be rejected")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the unknown input, got: %v", err)
	}
}

func TestValidate_Concurrency_LiteralKeyOK(t *testing.T) {
	dsl := concurrencyProbeDSL()
	// A constant key is always non-empty — no per-run binding needed.
	dsl.ConcurrencyKey = "global-serialise"
	dsl.Inputs = nil
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("literal (non-template) concurrency_key should validate, got: %v", err)
	}
}

func TestValidate_Concurrency_EmptyKeyOK(t *testing.T) {
	dsl := concurrencyProbeDSL()
	dsl.ConcurrencyKey = ""
	dsl.Inputs = nil
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("absent concurrency_key means no gate — should validate, got: %v", err)
	}
}

func TestValidate_Concurrency_MixedLiteralWithOptionalInputOK(t *testing.T) {
	dsl := concurrencyProbeDSL()
	// "tenant-{{ inputs.account_id }}" always renders at least "tenant-", so it
	// can never trip ErrConcurrencyKeyEmpty even when account_id is omitted.
	// This is the documented "literal prefix" fix (#3) — Validate must accept
	// it, matching runtime behaviour. (Whether the gate is per-tenant is the
	// author's design choice, not a validity error.)
	dsl.ConcurrencyKey = "tenant-{{ inputs.account_id }}"
	dsl.Inputs = []InputSpec{{Name: "account_id", Type: "string", Required: false}}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("mixed literal+ref concurrency_key never renders empty — must validate, got: %v", err)
	}
}

func TestValidate_Concurrency_MultiRefAnchoredByOneRequiredOK(t *testing.T) {
	dsl := concurrencyProbeDSL()
	// Pure-template, no literal, but the required `tenant` ref always renders
	// non-empty and anchors the whole key — so it can never be empty.
	dsl.ConcurrencyKey = "{{ inputs.tenant }}{{ inputs.account_id }}"
	dsl.Inputs = []InputSpec{
		{Name: "tenant", Type: "string", Required: true},
		{Name: "account_id", Type: "string", Required: false},
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("a pure-template key with one anchoring (required) ref must validate, got: %v", err)
	}
}

func TestValidate_Concurrency_MultiRefAllOptionalRejected(t *testing.T) {
	dsl := concurrencyProbeDSL()
	// Pure-template, no literal, every ref optional+defaultless → a caller
	// omitting both renders the whole key empty → run-time failure.
	dsl.ConcurrencyKey = "{{ inputs.tenant }}{{ inputs.account_id }}"
	dsl.Inputs = []InputSpec{
		{Name: "tenant", Type: "string", Required: false},
		{Name: "account_id", Type: "string", Required: false},
	}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Fatal("a pure-template key with no anchoring ref can render empty and must be rejected")
	}
}
