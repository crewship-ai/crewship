package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The `crewship` step kind: a routine acting on Crewship's own nouns.
//
// Everything a routine could already do reached OUT — an http step to a
// third party, an agent that talks to a model. Nothing let a routine write
// back into the product it runs inside, so "issue changes status → routine
// runs → routine comments on the issue" needed an http step pointed at our
// own API with a hand-managed token. This kind is that loop closed properly.
//
// Transport is loopback HTTP to the daemon's own internal API rather than an
// in-process call, and that is deliberate. Every guard those handlers carry —
// workspace binding, crew binding, capability checks, rate limits — is
// written INTO them. An in-process path would be a second door that has to
// re-implement all of it, which is the "two files each claiming the other
// enforces it" failure this repo has already had once (#1791). The cost is a
// localhost round-trip and that the caller must supply its own workspace_id.

// CrewshipActions is the seam the executor dispatches a `crewship` step
// through. Declared here, satisfied in internal/api (which owns the loopback
// client, the internal token and the policy resolver), wired in
// cmd/crewship/cmd_start.go — the same shape as the nine other seams in
// executor_factory.go, and required because internal/pipeline MUST NOT import
// internal/api.
type CrewshipActions interface {
	// Do performs one verb. It returns the JSON body the internal route
	// answered with, which becomes the step's output.
	//
	// Implementations run the verb's policy.Action through the autonomy gate
	// BEFORE the call. A routine-fired action carries no caller user id, so
	// it lands on the autonomous arm and is bounded by the crew's autonomy
	// level — the same bound an agent doing this by hand would face.
	Do(ctx context.Context, req CrewshipRequest) (string, error)
}

// CrewshipRequest is one dispatched verb. Provenance (RunID, ChainDepth) is
// carried explicitly rather than dug out of a context: the internal routes
// record author_run_id, which is what lets an automation reacting to the
// resulting event resolve the originating run and inherit its chain budget
// instead of starting a fresh one.
type CrewshipRequest struct {
	Verb        string
	Args        map[string]any
	WorkspaceID string
	// CrewID is the routine's AUTHOR crew — the principal whose autonomy
	// level bounds the action, and the crew the created row is attributed to.
	CrewID  string
	AgentID string
	RunID   string
	// ChainDepth is the composed depth of the RUN making this call. Anything
	// this verb creates belongs to the same chain, so a downstream automation
	// resolves depth+1 rather than 0. See GuardChainDepth.
	ChainDepth int
}

// crewshipVerb is one entry in the registry: what the verb is, where it goes,
// and which policy.Action decides whether it may happen.
type crewshipVerb struct {
	// Method + Path are the internal route. Path may contain one {arg}
	// placeholder, filled from Args by that name.
	Method string
	Path   string
	// PolicyAction is the internal/policy Action string this verb is gated
	// on. EMPTY means no Action exists for this capability yet — see
	// ErrCrewshipVerbUngoverned and the note on crewshipVerbs.
	PolicyAction string
	// RequiredArgs must be present and non-empty after rendering.
	RequiredArgs []string
	// Summary is the one-line description surfaced in editor completions and
	// docs.
	Summary string
}

// crewshipVerbs is the v1 registry. It is the ONLY list: validation, dispatch
// and the JSON Schema all read it, so a verb cannot be saveable but
// undispatchable, or dispatchable but ungated.
//
// ALL SIX ARE NOW GOVERNED. Five of them shipped refused, because
// internal/policy described none of these capabilities and picking an Action
// for them was a governance decision — what may a `guided` crew do unattended?
// — that a step kind does not get to make on the way past. That decision now
// exists: internal/policy declares issue_write, assignment_create and
// escalation_create with a decided cell for every autonomy level, and the
// reasoning for each cell lives there rather than here.
//
// The refusal path is deliberately still live. A verb added here with no
// PolicyAction is refused at SAVE with a message naming what is missing, so the
// next capability someone reaches for cannot ship ungoverned by being forgotten
// — the mechanism is not scaffolding that gets removed once the first five are
// through.
//
// PolicyAction is a plain string because internal/pipeline must not import
// internal/policy. That makes a typo here a silent forever-refusal rather than
// a compile error, so internal/api asserts every value names a declared Action
// (TestCrewshipVerbs_EveryPolicyActionIsDeclared).
//
// RequiredArgs is what the internal route requires MINUS what the dispatcher
// injects (workspace_id, crew_id, agent_id, actor_agent_id, author_agent_id,
// author_run_id — see crewshipInjected). Getting this list right is what turns
// a 400 at 03:00 into an error the author reads while saving.
var crewshipVerbs = map[string]crewshipVerb{
	"issue.create": {
		Method:       "POST",
		Path:         "/api/v1/internal/issues",
		PolicyAction: "mission_create",
		RequiredArgs: []string{"title"},
		Summary:      "Create an issue in the routine's author crew",
	},
	"issue.update": {
		Method:       "PATCH",
		Path:         "/api/v1/internal/issues/{identifier}",
		PolicyAction: "issue_write",
		RequiredArgs: []string{"identifier"},
		Summary:      "Change an issue's status, priority, assignee, labels or due date",
	},
	"issue.comment": {
		Method:       "POST",
		Path:         "/api/v1/internal/issues/{identifier}/comments",
		PolicyAction: "issue_write",
		RequiredArgs: []string{"identifier", "body"},
		Summary:      "Comment on an issue",
	},
	"issue.link": {
		Method: "POST",
		Path:   "/api/v1/internal/issues/{identifier}/relations",
		// relation_type is required by the route (blocks | blocked_by |
		// relates_to | duplicate_of | sub_issue_of) and has no default, so
		// omitting it is a 400. Required here instead.
		PolicyAction: "issue_write",
		RequiredArgs: []string{"identifier", "target_identifier", "relation_type"},
		Summary:      "Relate two issues (blocks, relates_to, sub_issue_of, …)",
	},
	"assignment.create": {
		Method:       "POST",
		Path:         "/api/v1/internal/assignments",
		PolicyAction: "assignment_create",
		RequiredArgs: []string{"target_slug", "task", "chat_id"},
		Summary:      "Delegate a task to an agent in the author crew",
	},
	"escalation.create": {
		Method: "POST",
		Path:   "/api/v1/internal/escalations",
		// from_slug is the ESCALATING agent, looked up inside the injected
		// crew_id — so it can only ever name a member of the routine's own
		// crew, which is why it stays an author-supplied arg.
		PolicyAction: "escalation_create",
		RequiredArgs: []string{"from_slug", "reason", "chat_id"},
		Summary:      "Raise an escalation to a human",
	},
}

// ErrCrewshipVerbUnknown / ErrCrewshipVerbUngoverned are the two save-time
// refusals. Separate sentinels because they need separate fixes: a typo is
// the author's to correct, an ungoverned verb is ours.
var (
	ErrCrewshipVerbUnknown    = errors.New("unknown crewship action")
	ErrCrewshipVerbUngoverned = errors.New("crewship action has no policy action")
)

// CrewshipVerbs returns every verb name in the registry, sorted. Used by the
// error messages and by the schema-parity test.
func CrewshipVerbs() []string {
	out := make([]string, 0, len(crewshipVerbs))
	for v := range crewshipVerbs {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// EnabledCrewshipVerbs returns the verbs that carry a policy action, i.e. the
// ones a routine can actually save today.
func EnabledCrewshipVerbs() []string {
	var out []string
	for name, v := range crewshipVerbs {
		if v.PolicyAction != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CrewshipVerbPolicyAction returns the policy action string a verb is gated
// on. Empty means the verb is not dispatchable (and is refused at save).
// Exported so internal/api can map it onto a policy.Action without importing
// the registry's internals or keeping a second copy of the mapping.
func CrewshipVerbPolicyAction(verb string) string {
	return crewshipVerbs[verb].PolicyAction
}

// CrewshipVerbRoute returns the internal route (method, path template) a verb
// dispatches to. ok is false for an unknown verb.
func CrewshipVerbRoute(verb string) (method, path string, ok bool) {
	v, found := crewshipVerbs[verb]
	if !found {
		return "", "", false
	}
	return v.Method, v.Path, true
}

// ValidateCrewshipAction is the SAVE-TIME gate on the verb: it must exist,
// and it must have a policy action behind it. Run-time is too late — a
// routine that saves cleanly and then refuses every night at 03:00 is worse
// than one that never saved, because by then someone has built on it.
func ValidateCrewshipAction(stepID, action string) error {
	if strings.TrimSpace(action) == "" {
		return fmt.Errorf("pipeline: step %q (crewship) missing action (one of: %s)",
			stepID, strings.Join(EnabledCrewshipVerbs(), ", "))
	}
	v, ok := crewshipVerbs[action]
	if !ok {
		return fmt.Errorf("pipeline: step %q (crewship) %w %q (available: %s)%s",
			stepID, ErrCrewshipVerbUnknown, action,
			strings.Join(EnabledCrewshipVerbs(), ", "),
			didYouMean(action, CrewshipVerbs()))
	}
	if v.PolicyAction == "" {
		return fmt.Errorf("pipeline: step %q (crewship) action %q is %w — "+
			"every crewship action must be bounded by a crew's autonomy level, and "+
			"internal/policy declares none for this capability yet. "+
			"Available today: %s",
			stepID, action, ErrCrewshipVerbUngoverned, strings.Join(EnabledCrewshipVerbs(), ", "))
	}
	return nil
}

// ValidateCrewshipArgs checks the verb's required arguments are present as
// authored. Values are templates, so "present" is all that can be judged
// here; emptiness after rendering is caught at dispatch.
func ValidateCrewshipArgs(stepID, action string, args map[string]any) error {
	v, ok := crewshipVerbs[action]
	if !ok {
		return nil // the unknown-verb error already fired
	}
	for _, want := range v.RequiredArgs {
		val, present := args[want]
		if !present {
			return fmt.Errorf("pipeline: step %q (crewship %s) missing required arg %q",
				stepID, action, want)
		}
		if s, isStr := val.(string); isStr && strings.TrimSpace(s) == "" {
			return fmt.Errorf("pipeline: step %q (crewship %s) arg %q is empty",
				stepID, action, want)
		}
	}
	return nil
}
