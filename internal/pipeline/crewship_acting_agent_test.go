package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// A routine whose `crewship` step is `issue.comment` cannot run unless the
// routine has an acting agent: the dispatcher injects agent_id from the
// routine's author agent (crewship_actions.go), and the internal comments
// route answers an empty one with `400: agent_id is required` because
// mission_comments' CHECK has no author kind for "nobody".
//
// Before this gate that was a RUN-time failure on a routine that had saved
// clean — the 03:00 400 the save-time gate exists to prevent, and the reason
// the unknown-verb and ungoverned-verb refusals already live at save.
func TestValidateCrewshipActingAgent_IssueCommentWithoutAnActingAgentIsRefused(t *testing.T) {
	dsl := &DSL{Steps: []Step{{
		ID:     "report",
		Type:   StepCrewship,
		Action: "issue.comment",
		Args:   map[string]any{"identifier": "ENG-1", "body": "the nightly check failed"},
	}}}

	err := ValidateCrewshipActingAgent(dsl, false)
	if err == nil {
		t.Fatal("issue.comment with no acting agent saved clean — it will 400 at run time instead")
	}
	if !errors.Is(err, ErrCrewshipNoActingAgent) {
		t.Errorf("error = %v, want ErrCrewshipNoActingAgent", err)
	}
	// The message has to name the fix, not just the symptom.
	for _, want := range []string{"report", "issue.comment", "--author-agent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// Supplying one is what makes the verb reachable.
func TestValidateCrewshipActingAgent_IssueCommentWithAnActingAgentPasses(t *testing.T) {
	dsl := &DSL{Steps: []Step{{
		ID: "report", Type: StepCrewship, Action: "issue.comment",
		Args: map[string]any{"identifier": "ENG-1", "body": "hi"},
	}}}
	if err := ValidateCrewshipActingAgent(dsl, true); err != nil {
		t.Fatalf("ValidateCrewshipActingAgent = %v, want nil", err)
	}
}

// A foreach is a wrapper: the loop reaches nothing, its BODY does. Not
// recursing would make wrapping the step in a foreach a way past the gate —
// the same hole validateStepEgress had.
func TestValidateCrewshipActingAgent_RecursesIntoForeachBodies(t *testing.T) {
	dsl := &DSL{Steps: []Step{{
		ID:   "fan",
		Type: StepForeach,
		Foreach: &ForeachStep{Steps: []Step{{
			ID: "inner", Type: StepCrewship, Action: "issue.comment",
			Args: map[string]any{"identifier": "ENG-1", "body": "hi"},
		}}},
	}}}
	err := ValidateCrewshipActingAgent(dsl, false)
	if err == nil {
		t.Fatal("issue.comment inside a foreach body escaped the acting-agent gate")
	}
	if !strings.Contains(err.Error(), "inner") {
		t.Errorf("error %q does not name the inner step", err.Error())
	}
}

// Only the verbs that structurally cannot act as "nobody" are gated. The
// others have a documented fallback — issue.update files under "system",
// assignment.create falls back to the chat's agent — so refusing them would
// be a new restriction wearing a bug fix's clothes.
func TestValidateCrewshipActingAgent_OnlyGatesVerbsThatRequireOne(t *testing.T) {
	for _, verb := range CrewshipVerbs() {
		args := map[string]any{
			"identifier": "ENG-1", "body": "hi", "title": "t",
			"target_identifier": "ENG-2", "relation_type": "blocks",
			"target_slug": "a", "task": "t", "chat_id": "c",
			"from_slug": "a", "reason": "r",
		}
		dsl := &DSL{Steps: []Step{{ID: "s", Type: StepCrewship, Action: verb, Args: args}}}
		err := ValidateCrewshipActingAgent(dsl, false)
		if verb == "issue.comment" {
			if err == nil {
				t.Errorf("verb %q: want a refusal with no acting agent", verb)
			}
			continue
		}
		if err != nil {
			t.Errorf("verb %q: refused without an acting agent (%v) — it has a documented no-agent fallback", verb, err)
		}
	}
}

// A DSL with no crewship steps at all must never be held up by this gate.
func TestValidateCrewshipActingAgent_IgnoresRoutinesWithNoCrewshipSteps(t *testing.T) {
	dsl := &DSL{Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "lead"}}}
	if err := ValidateCrewshipActingAgent(dsl, false); err != nil {
		t.Fatalf("ValidateCrewshipActingAgent = %v, want nil", err)
	}
}
