package pipeline

import (
	"strings"
	"testing"
)

// Widening the template walk to notify and script fields (#1518) made
// previously-saveable routines unsaveable.
//
// A script step's args are arguments to ANOTHER PROGRAM, and that program
// routinely has template syntax of its own:
//
//	args: ["ps", "--format", "{{.Names}}"]
//
// Those fields were never walked before, so that routine saved fine. After
// the widening, checkTemplateRef's unknown-namespace branch rejects
// ".Names", and every caller of Validate starts failing: the save endpoints,
// `crewship routine validate`, and `crewship apply` — which then aborts on a
// routine the operator never touched, with no flag that bypasses template
// validation.
//
// The fix keeps the check that was worth adding — a KNOWN namespace used
// wrongly, which is how `{{ steps.fetch.status }}` shipped rendering empty —
// and declines to judge syntax belonging to someone else's tool in the two
// field groups that never judged it before.

func scriptArgsDSL(arg string) *DSL {
	return &DSL{
		DSLVersion: "1.0",
		Name:       "probe",
		Inputs:     []InputSpec{{Name: "target", Type: "string"}},
		Steps: []Step{
			// An earlier step, so a ref to it exercises the SHAPE check
			// rather than tripping the ordering check first.
			{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x.test"}},
			{ID: "list", Type: StepScript, Script: &ScriptStep{
				Path: "scripts/list.sh",
				Args: []string{"ps", "--format", arg},
			}},
		},
	}
}

func TestValidate_ForeignTemplateSyntaxInScriptArgsIsNotOurs(t *testing.T) {
	// The exact shape that regressed.
	for _, arg := range []string{"{{.Names}}", "{{ .Names }}", "{{range .Items}}", "{{printf \"%s\" .X}}"} {
		if err := Validate(scriptArgsDSL(arg), nil, nil); err != nil {
			t.Errorf("arg %q belongs to the invoked program, not to us: %v", arg, err)
		}
	}
}

func TestValidate_ForeignTemplateSyntaxInAScriptEnvValue(t *testing.T) {
	dsl := scriptArgsDSL("plain")
	dsl.Steps[1].Script.Env = map[string]string{"FORMAT": "{{.Names}}"}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Errorf("script env carries the same foreign syntax: %v", err)
	}
}

func TestValidate_KnownNamespaceStillCheckedInScriptFields(t *testing.T) {
	// The value the widening added must survive: a ref that names one of OUR
	// namespaces and gets it wrong is still a mistake we can see.
	for _, tc := range []struct{ arg, want string }{
		{"{{ steps.nope.output }}", "nope"},
		{"{{ inputs.trget }}", "trget"},
		{"{{ steps.fetch.status }}", "output"},
	} {
		err := Validate(scriptArgsDSL(tc.arg), nil, nil)
		if err == nil {
			t.Errorf("arg %q uses a Crewship namespace wrongly and must still be caught", tc.arg)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("arg %q: error should mention %q, got: %v", tc.arg, tc.want, err)
		}
	}
}

func TestValidate_NotifyFieldsTreatForeignSyntaxTheSameWay(t *testing.T) {
	// A notify body quoting another tool's template — a runbook message
	// telling someone which format string to pass — was equally saveable
	// before, and is equally not ours to judge.
	dsl := &DSL{
		DSLVersion: "1.0", Name: "probe",
		Steps: []Step{{ID: "tell", Type: StepNotify, Notify: &NotifyStep{
			To: "workspace", Title: "How to list", Body: "run: docker ps --format {{.Names}}",
		}}},
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Errorf("a notify body quoting foreign syntax must still save: %v", err)
	}
}

func TestValidate_NotifyStillCatchesOurOwnBadRefs(t *testing.T) {
	// steps.X.status is what this whole check exists for.
	dsl := &DSL{
		DSLVersion: "1.0", Name: "probe",
		Steps: []Step{
			{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x.test"}},
			{ID: "tell", Type: StepNotify, Notify: &NotifyStep{
				To: "workspace", Title: "Result", Body: "HTTP {{ steps.fetch.status }}",
			}},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Error("steps.fetch.status can never resolve and must still be rejected")
	}
}

func TestValidate_FieldsThatAlreadyRejectedForeignSyntaxStillDo(t *testing.T) {
	// The leniency is scoped to the two field groups the widening ADDED.
	// Anywhere the check already existed, behaviour is unchanged — an
	// unknown namespace in an http URL was an error before and stays one.
	dsl := &DSL{
		DSLVersion: "1.0", Name: "probe",
		Steps: []Step{{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{
			Method: "GET", URL: "https://x.test/{{.Names}}",
		}}},
	}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Error("an unknown namespace in an http url was rejected before this change and must stay rejected")
	}
}
