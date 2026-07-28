package pipeline

import (
	"strings"
	"testing"
)

// `{{ steps.<id>.<something> }}` was validated as far as the step id and no
// further: checkTemplateRef looked up the id, then explicitly declined to
// check the rest, on the reasoning that a JSON path into the output cannot be
// verified up front.
//
// True for the path, wrong for the segment before it. The renderer supports
// exactly two shapes — `output` and `output.<path>` — and returns not-found
// for anything else, so `steps.fetch.status` was never a path that might not
// resolve; it was a reference that could never resolve. It rendered as an
// empty string, and the notification built from it went out reading
// "→ HTTP" with nothing after it. The run succeeded, the delivery was logged
// as sent, and the only sign of the bug was in the message someone read.
//
// This is the demo routine's own bug, found by reading what actually arrived.
// An http step's output is the response body — there is no `.status` and no
// `.body` — which is precisely the kind of thing an author guesses wrong and
// a validator should say out loud.

func stepRefDSL(ref string) *DSL {
	return &DSL{
		DSLVersion: "1.0",
		Name:       "probe",
		Steps: []Step{
			{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://dns.google/resolve?name=example.com"}},
			{ID: "tell", Type: StepNotify, Notify: &NotifyStep{
				To:    "workspace",
				Title: "Result",
				Body:  "got {{ " + ref + " }}",
			}},
		},
	}
}

func TestValidate_StepRef_AcceptsTheTwoShapesThatResolve(t *testing.T) {
	for _, ref := range []string{
		"steps.fetch.output",
		"steps.fetch.output.Answer",
		"steps.fetch.output.Answer.0.data",
	} {
		if err := Validate(stepRefDSL(ref), nil, nil); err != nil {
			t.Errorf("%s must validate: %v", ref, err)
		}
	}
}

func TestValidate_StepRef_RejectsASegmentTheRendererCannotResolve(t *testing.T) {
	// The exact refs the demo routine shipped with.
	for _, ref := range []string{
		"steps.fetch.status",
		"steps.fetch.body",
		"steps.fetch.headers.content-type",
	} {
		err := Validate(stepRefDSL(ref), nil, nil)
		if err == nil {
			t.Errorf("%s renders empty and must be rejected at author time", ref)
			continue
		}
		if !strings.Contains(err.Error(), "output") {
			t.Errorf("the error must point at the shape that works, got: %v", err)
		}
	}
}

func TestValidate_TemplatesAreCheckedInNotifyAndScriptFields(t *testing.T) {
	// Both step kinds document their fields as template-substituted and
	// neither was in the walk, so a bad ref in either passed save silently.
	// One case each, using a step id that does not exist — the cheapest
	// mistake to make and the one that proves the field is looked at.
	cases := map[string]*DSL{
		"notify/to": {DSLVersion: "1.0", Name: "p", Steps: []Step{
			{ID: "tell", Type: StepNotify, Notify: &NotifyStep{To: "user:{{ steps.nope.output }}", Title: "x"}},
		}},
		"notify/title": {DSLVersion: "1.0", Name: "p", Steps: []Step{
			{ID: "tell", Type: StepNotify, Notify: &NotifyStep{To: "workspace", Title: "{{ steps.nope.output }}"}},
		}},
		"script/args": {DSLVersion: "1.0", Name: "p", Steps: []Step{
			{ID: "run", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py", Args: []string{"{{ steps.nope.output }}"}}},
		}},
		"script/env": {DSLVersion: "1.0", Name: "p", Steps: []Step{
			{ID: "run", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py", Env: map[string]string{"A": "{{ steps.nope.output }}"}}},
		}},
	}
	for name, dsl := range cases {
		t.Run(name, func(t *testing.T) {
			err := Validate(dsl, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "nope") {
				t.Errorf("a bad template ref in %s must be caught, got: %v", name, err)
			}
		})
	}
}

func TestValidate_StepRef_StillNamesTheStepOnAnUnknownID(t *testing.T) {
	// The existing check must keep firing first — an unknown step id is a
	// different mistake with a different fix, and the did-you-mean hint on
	// it is more useful than a shape complaint.
	err := Validate(stepRefDSL("steps.fetchh.output"), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "fetchh") {
		t.Errorf("want the unknown-step error naming fetchh, got: %v", err)
	}
}
