package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

const classifyFailureDef = `{"name":"invoices","steps":[
	{"id":"extract","type":"transform","transform":{"input":"{}","expression":"."}},
	{"id":"verify","name":"Check the extraction","type":"agent_run","agent_slug":"worker","prompt":"x",
	 "outcomes":{"grader_agent_slug":"checker","max_iterations":3,"criteria":[{"name":"total_equals_lines","rule":"totals match"}]}},
	{"id":"decide","type":"transform","transform":{"input":"{}","expression":"."}},
	{"id":"post","name":"Post to the ledger","type":"script","script":{"path":"scripts/ledger-post.go"},"timeout_seconds":90},
	{"id":"notify","type":"transform","transform":{"input":"{}","expression":"."}}
]}`

// ClassifyFailure turns the engine's own error strings into a kind the UI
// can phrase, plus the kept / not-done step lists an operator needs to decide
// what to do next. One case per kind, plus unknown, plus the step-list rules.
func TestClassifyFailure(t *testing.T) {
	dsl, err := Parse([]byte(classifyFailureDef))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	outputs := map[string]string{"extract": "{}"}
	executed := []string{"extract", "verify"}

	tests := []struct {
		name        string
		msg         string
		step        string
		wantKind    string
		wantStep    string
		wantName    string
		wantSummary string
		wantKept    []string
		wantNotDone []string
	}{
		{
			name:        "checker rejected after tiers",
			msg:         "step failed after exhausting tiers: outcomes failed: total_equals_lines",
			step:        "verify",
			wantKind:    FailureCheckerRejected,
			wantStep:    "verify",
			wantName:    "Check the extraction",
			wantSummary: "The checker rejected the result after exhausting the allowed model tiers.",
			wantKept:    []string{"extract"},
			wantNotDone: []string{"decide", "post", "notify"},
		},
		{
			name:        "checker rejected on abort",
			msg:         "outcomes failed: outcomes failed: missing signature",
			step:        "verify",
			wantKind:    FailureCheckerRejected,
			wantSummary: "The checker rejected the result.",
		},
		{
			name:        "validation failed on abort",
			msg:         `validation failed: output must contain "total"`,
			step:        "verify",
			wantKind:    FailureValidationFailed,
			wantSummary: `The output failed a structural check.`,
		},
		{
			name:        "validation failed after tiers",
			msg:         "step failed after exhausting tiers: output shorter than min_length 10",
			step:        "verify",
			wantKind:    FailureValidationFailed,
			wantSummary: "The output failed a structural check after exhausting the allowed model tiers.",
		},
		{
			name:        "transform input not JSON",
			msg:         `transform step "decide" input is not JSON and expression ".total" requires JSON`,
			step:        "decide",
			wantKind:    FailureTransformInput,
			wantStep:    "decide",
			wantName:    "decide",
			wantSummary: `Step "decide" received input that is not JSON; the expression ".total" needs JSON.`,
			wantKept:    []string{"extract"},
			wantNotDone: []string{"post", "notify"},
		},
		{
			name:        "transform input not JSON with a quoted expression",
			msg:         `transform step "decide" input is not JSON and expression "error(\"required phrase is missing\")" requires JSON`,
			step:        "decide",
			wantKind:    FailureTransformInput,
			wantStep:    "decide",
			wantName:    "decide",
			wantSummary: `Step "decide" received input that is not JSON; the expression "error(\"required phrase is missing\")" needs JSON.`,
			wantKept:    []string{"extract"},
			wantNotDone: []string{"post", "notify"},
		},
		{
			name:        "step timeout with declared limit",
			msg:         `script step "post": context deadline exceeded (stderr: )`,
			step:        "post",
			wantKind:    FailureTimeout,
			wantSummary: `Step "Post to the ledger" did not finish within its 90-second limit.`,
			wantNotDone: []string{"notify"},
		},
		{
			name:        "approval wait timed out",
			msg:         `wait step "decide" (approval) timed out`,
			step:        "decide",
			wantKind:    FailureTimeout,
			wantSummary: `Nobody answered the approval at step "decide" before its deadline.`,
		},
		{
			name:        "run cancelled at step",
			msg:         "run cancelled at step verify",
			step:        "verify",
			wantKind:    FailureCancelled,
			wantSummary: `The run was stopped at step "Check the extraction".`,
		},
		{
			name:        "context canceled inside a step error",
			msg:         `script step "post": context canceled (stderr: )`,
			step:        "post",
			wantKind:    FailureCancelled,
			wantSummary: `The run was stopped at step "Post to the ledger".`,
		},
		{
			name:        "missing credential from the vault gate",
			msg:         `routine requires credential of type "openai_api_key" not present in the vault for crew "finance"`,
			step:        "",
			wantKind:    FailureMissingCredential,
			wantSummary: `A required credential of type "openai_api_key" is missing from the vault.`,
			wantNotDone: []string{"decide", "post", "notify"},
		},
		{
			name:        "missing credential at resolution time",
			msg:         `http step "post": no active credential of type "erp_token" in workspace vault`,
			step:        "post",
			wantKind:    FailureMissingCredential,
			wantSummary: `A required credential of type "erp_token" is missing from the vault.`,
		},
		{
			name:        "missing integration",
			msg:         `routine requires integration "slack" not connected for crew "finance"`,
			step:        "notify",
			wantKind:    FailureMissingIntegration,
			wantSummary: `The integration "slack" is not connected for the author crew.`,
		},
		{
			name:        "http status",
			msg:         `http step "post" got HTTP 502 (success codes: [200 201])`,
			step:        "post",
			wantKind:    FailureHTTPStatus,
			wantSummary: `The HTTP call in step "Post to the ledger" returned status 502.`,
		},
		{
			name:        "script exit code",
			msg:         `script step "post" exit code 3 (stderr: ledger refused)`,
			step:        "post",
			wantKind:    FailureScriptExit,
			wantSummary: `The script scripts/ledger-post.go in step "Post to the ledger" exited with code 3.`,
		},
		{
			name:        "code step exit code has no path",
			msg:         `code step "decide" exit code 1 (stderr: boom)`,
			step:        "decide",
			wantKind:    FailureScriptExit,
			wantSummary: `The script in step "decide" exited with code 1.`,
		},
		{
			name:        "cost cap exceeded after a step",
			msg:         `cost cap exceeded: $1.2500 > $1.0000 after step "verify"`,
			step:        "verify",
			wantKind:    FailureCostCap,
			wantSummary: "The run reached its cost cap: $1.2500 spent against a cap of $1.0000.",
		},
		{
			name:        "cost cap stops a retry and wins over the wrapped reason",
			msg:         `cost cap would be exceeded: $0.9000 spent on step "verify", next retry (~$0.3000) would breach cap $1.0000: outcomes failed: x`,
			step:        "verify",
			wantKind:    FailureCostCap,
			wantSummary: "The run stopped before a retry that would have exceeded the cost cap of $1.0000.",
		},
		{
			name:        "wrapped foreach failure classifies by the inner error",
			msg:         `foreach step "post": item 2 failed: body step "one" failed: http step "one" got HTTP 404 (success codes: [200])`,
			step:        "post",
			wantKind:    FailureHTTPStatus,
			wantSummary: `The HTTP call in step "Post to the ledger" returned status 404.`,
		},
		{
			name:        "unknown uses a generic summary",
			msg:         "tier resolver: workspace not found",
			step:        "verify",
			wantKind:    FailureUnknown,
			wantSummary: "The run did not finish.",
		},
		{
			name:        "unknown with an empty message",
			msg:         "",
			step:        "",
			wantKind:    FailureUnknown,
			wantSummary: "The run did not finish.",
			wantNotDone: []string{"decide", "post", "notify"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyFailure(tc.msg, tc.step, dsl, outputs, executed)
			if got == nil {
				t.Fatal("nil failure")
			}
			if got.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", got.Summary, tc.wantSummary)
			}
			if tc.wantStep != "" && got.StepID != tc.wantStep {
				t.Errorf("step_id = %q, want %q", got.StepID, tc.wantStep)
			}
			if tc.wantName != "" && got.StepName != tc.wantName {
				t.Errorf("step_name = %q, want %q", got.StepName, tc.wantName)
			}
			if tc.wantKept != nil && !reflect.DeepEqual(got.KeptStepIDs, tc.wantKept) {
				t.Errorf("kept = %v, want %v", got.KeptStepIDs, tc.wantKept)
			}
			if tc.wantNotDone != nil && !reflect.DeepEqual(got.NotDoneStepIDs, tc.wantNotDone) {
				t.Errorf("not_done = %v, want %v", got.NotDoneStepIDs, tc.wantNotDone)
			}
			if got.KeptStepIDs == nil || got.NotDoneStepIDs == nil {
				t.Errorf("step lists must be empty arrays, never null: %#v", got)
			}
		})
	}
}

// Without a definition the classifier still names the kind and the step id;
// the step lists are empty rather than null and the name falls back to the id.
func TestClassifyFailure_NoDefinition(t *testing.T) {
	got := ClassifyFailure(`http step "x" got HTTP 500 (success codes: [200])`, "x", nil, nil, nil)
	if got.Kind != FailureHTTPStatus || got.StepID != "x" || got.StepName != "x" {
		t.Fatalf("got %#v", got)
	}
	if len(got.KeptStepIDs) != 0 || len(got.NotDoneStepIDs) != 0 || got.KeptStepIDs == nil || got.NotDoneStepIDs == nil {
		t.Fatalf("step lists: %#v", got)
	}
	if !strings.Contains(got.Summary, `"x"`) {
		t.Fatalf("summary should name the step: %q", got.Summary)
	}
}

// A step that failed inside a foreach body or a hook is not a top-level step;
// every top-level step without an execution record is then "not done".
func TestClassifyFailure_HookStepLeavesEverythingNotDone(t *testing.T) {
	dsl, err := Parse([]byte(classifyFailureDef))
	if err != nil {
		t.Fatal(err)
	}
	got := ClassifyFailure("before_all hook failed: script step \"setup\" exit code 2 (stderr: )", "setup", dsl, nil, nil)
	if got.Kind != FailureScriptExit {
		t.Fatalf("kind = %q", got.Kind)
	}
	want := []string{"extract", "verify", "decide", "post", "notify"}
	if !reflect.DeepEqual(got.NotDoneStepIDs, want) {
		t.Fatalf("not_done = %v, want %v", got.NotDoneStepIDs, want)
	}
}

// MaxIterations is a ceiling, not recorded evidence of how many tiers ran.
// A recipe can allow ten tiers while the worker has only one available model.
func TestClassifyFailure_DoesNotTreatIterationLimitAsAttemptCount(t *testing.T) {
	dsl, err := Parse([]byte(strings.Replace(classifyFailureDef, `"max_iterations":3`, `"max_iterations":10`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	got := ClassifyFailure("step failed after exhausting tiers: outcomes failed: total_equals_lines", "verify", dsl, nil, nil)
	want := "The checker rejected the result after exhausting the allowed model tiers."
	if got.Summary != want {
		t.Fatalf("summary = %q, want %q", got.Summary, want)
	}
}

// Arbitrary diagnostics must not become a portable summary, even when they
// resemble an engine message. Secrets are not always recognizable by regex.
func TestClassifyFailureDoesNotProjectDiagnostics(t *testing.T) {
	for _, prefix := range []string{"", "outcomes failed: ", "validation failed: ", "exhausting tiers: "} {
		for _, secret := range []string{"sk-proj-exampleSecret1234567890", "Bearer opaqueToken123456789", "private-customer-credential"} {
			got := ClassifyFailure(prefix+secret, "verify", nil, nil, nil)
			if strings.Contains(got.Summary, secret) {
				t.Errorf("diagnostic propagated: %q", got.Summary)
			}
		}
	}
}
