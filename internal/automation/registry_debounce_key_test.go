package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// What the debounce key must cover
//
// The coalescing identity has to include every entity the rendered inputs vary
// by, or a burst of events about DIFFERENT things collapses into one run that
// acts on whichever arrived last — and reports coalesced_events: N as though
// that were the right answer.
//
// mission_id alone covers this only for the mission.* / issue.* types. An
// automation on run.failed, assignment.failed, guardrail.input_blocked,
// agent.mentioned or budget.warning gets entries with NO mission id, and those
// entries are not "about the workspace": they carry a run, an agent and a crew,
// and the inputs an author writes against them reference exactly those.
// ---------------------------------------------------------------------------

// The motivating case, stated as an author would build it: "when a run fails,
// open an incident naming the run". Two unrelated runs fail inside one
// debounce window. That is two incidents, and it must not be one incident
// about the second run with the first silently discarded.
func TestNoMissionEventsDoNotCoalesceAcrossDifferentRuns(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{Now: func() time.Time { return now }})

	r := rule("a1", "ws_1", "run.failed")
	r.Action.Inputs = map[string]any{"failed_run": "{{ event.run_id }}"}
	reg.Load([]Resolved{r})

	failed := func(runID string) journal.Entry {
		return journal.Entry{
			WorkspaceID: "ws_1",
			Type:        "run.failed",
			TraceID:     runID, // journal.EventContext exposes trace_id as run_id
			CrewID:      "crew_1",
			Severity:    journal.SeverityError,
			ActorType:   journal.ActorSystem,
			Summary:     "run failed",
		}
	}

	reg.Observer([]journal.Entry{failed("run_alpha")})
	reg.Observer([]journal.Entry{failed("run_beta")})
	reg.Flush(context.Background())

	if got := enq.n(); got != 2 {
		t.Fatalf("enqueues = %d, want 2 — two unrelated failed runs collapsed into one "+
			"deferred run because the debounce key ignores every identity except mission_id, "+
			"so one of the two incidents was silently dropped", got)
	}

	seen := map[string]bool{}
	for i := 0; i < enq.n(); i++ {
		var inputs map[string]any
		if err := json.Unmarshal([]byte(enq.at(i).InputsJSON), &inputs); err != nil {
			t.Fatalf("inputs_json #%d: %v", i, err)
		}
		seen[inputs["failed_run"].(string)] = true
	}
	if !seen["run_alpha"] || !seen["run_beta"] {
		t.Fatalf("parked runs cover %v, want both run_alpha and run_beta", seen)
	}
}

// Same shape one rung down the specificity ladder: entries with neither a
// mission nor a run, but distinct agents. "When an agent is blocked by a
// guardrail, review that agent" must not review only the last one.
func TestNoMissionEventsDoNotCoalesceAcrossDifferentAgents(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{Now: func() time.Time { return now }})

	r := rule("a1", "ws_1", "guardrail.input_blocked")
	r.Action.Inputs = map[string]any{"agent": "{{ event.agent_id }}"}
	reg.Load([]Resolved{r})

	blocked := func(agentID string) journal.Entry {
		return journal.Entry{
			WorkspaceID: "ws_1",
			Type:        "guardrail.input_blocked",
			AgentID:     agentID,
			CrewID:      "crew_1",
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			Summary:     "blocked",
		}
	}

	reg.Observer([]journal.Entry{blocked("ag_1"), blocked("ag_2")})
	reg.Flush(context.Background())

	if got := enq.n(); got != 2 {
		t.Fatalf("enqueues = %d, want 2 — two agents' guardrail blocks coalesced into one "+
			"run that names only the last agent", got)
	}
}

// The coalescing that DOES have to keep working: repeated events about the
// SAME entity are one run. Asserted here so the key never widens into "one run
// per event", which would turn the debounce off for exactly the storms it
// exists to absorb.
func TestSameEntityStillCoalescesWithoutAMission(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{Now: func() time.Time { return now }})
	reg.Load([]Resolved{rule("a1", "ws_1", "run.failed")})

	for i := 0; i < 50; i++ {
		reg.Observer([]journal.Entry{{
			WorkspaceID: "ws_1",
			Type:        "run.failed",
			TraceID:     "run_alpha",
			CrewID:      "crew_1",
			Severity:    journal.SeverityError,
			ActorType:   journal.ActorSystem,
			Summary:     "run failed",
		}})
	}
	reg.Flush(context.Background())

	if got := enq.n(); got != 1 {
		t.Fatalf("enqueues = %d, want 1 — fifty events about one run must be one deferred run", got)
	}
}

// An entry with no identity at all still collapses to the automation's own
// key: that is the genuinely workspace-scoped case the original comment
// described, and it keeps its behaviour.
func TestEntriesWithNoIdentityCollapseToTheAutomation(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	enq := &recordingEnqueuer{}
	reg := NewRegistry(nil, enq, Options{Now: func() time.Time { return now }})
	reg.Load([]Resolved{rule("a1", "ws_1", "workspace.notice")})

	for i := 0; i < 10; i++ {
		reg.Observer([]journal.Entry{{
			WorkspaceID: "ws_1",
			Type:        "workspace.notice",
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorSystem,
			Summary:     "notice",
		}})
	}
	reg.Flush(context.Background())

	if got := enq.n(); got != 1 {
		t.Fatalf("enqueues = %d, want 1", got)
	}
}
