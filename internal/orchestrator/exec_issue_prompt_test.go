package orchestrator

import (
	"strings"
	"testing"
)

// The agent's half of the issue track.
//
// Same failure mode the Keeper block was added for: crewshipd can expose a verb
// and the agent will still not use it, because nothing in its context says the
// verb exists. Before this block the preamble described the filesystem, the
// Keeper, port exposure and skill authoring, and said NOTHING about issues —
// so the only reachable issue action was the one a human explicitly told the
// agent to curl. An endpoint no agent knows about is an endpoint that does not
// ship.
//
// These assertions are about the contract, not the wording: the paths, the
// verbs, the field an agent cannot guess, and the two rules it must not try to
// route around.

func TestPreamble_TellsAgentsTheIssueVerbsExist(t *testing.T) {
	p := crewshipSystemPreamble

	// The endpoints, by path. A description without a path is not actionable.
	for _, want := range []string{
		"/issues?",            // search / list
		"/issue/<IDENTIFIER>", // read one
		"/comment",            // comment
		"/link",               // relate / decompose
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble never mentions %q, so an agent cannot reach that verb", want)
		}
	}

	// PATCH is the only non-obvious method here; an agent that guesses POST
	// gets a 404 from the sidecar switch and concludes the verb is missing.
	if !strings.Contains(p, "PATCH http://localhost:9119/issue/") {
		t.Error("the preamble does not give the method for the issue update")
	}

	// The fields an agent cannot infer from the path.
	for _, want := range []string{"target_identifier", "relation_type", "sub_issue_of"} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble does not name the required field/value %q", want)
		}
	}

	// The decomposition workflow is the reason the link verb exists; naming the
	// verb without the workflow leaves the model to invent one.
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "child") {
		t.Error("the preamble does not explain that sub_issue_of creates a child issue")
	}
}

// Two rules the agent must not spend turns probing: it cannot author as someone
// else, and it cannot reach another crew's issues. Both are enforced server-side
// — the point of stating them is that an agent which does not know them reads
// the resulting 403 as a bug and retries.
func TestPreamble_StatesTheIssueAuthorisationRules(t *testing.T) {
	lower := strings.ToLower(crewshipSystemPreamble)

	if !strings.Contains(lower, "agent_id in the body is ignored") {
		t.Error("the preamble does not say that a body-supplied issue author is ignored")
	}
	// The rule is about MUTATION, not reach: an agent may read and relate more
	// than it may change, and a preamble that says "your own crew's issues"
	// flatly would have the agent refuse work it is allowed to do.
	if !strings.Contains(lower, "only change your own") {
		t.Error("the preamble does not scope issue CHANGES to the agent's own crew")
	}
	// …and that the link TARGET is the documented exception. Stating the rule
	// without the exception makes the agent refuse a legitimate cross-crew
	// "we are blocked on their work" link it is actually allowed to create.
	if !strings.Contains(lower, "link target") {
		t.Error("the preamble does not carve out the cross-crew link target")
	}
}

// The issue read path fences its free text, and the [UNTRUSTED CONTENT] block
// at the top of the preamble is the other half of that contract. Say so at the
// point of use too: an agent that reads a fenced description ten thousand tokens
// after the general rule should not have to remember it.
func TestPreamble_MarksIssueTextAsUntrusted(t *testing.T) {
	p := crewshipSystemPreamble

	idx := strings.Index(p, "ISSUE TRACKER")
	if idx < 0 {
		t.Fatal("no ISSUE TRACKER block in the preamble")
	}
	block := p[idx:]
	if end := strings.Index(block, "\nEXPOSE PORT"); end >= 0 {
		block = block[:end]
	}
	if !strings.Contains(block, "<untrusted") {
		t.Error("the issue block does not tell the agent that issue text arrives fenced")
	}
}
