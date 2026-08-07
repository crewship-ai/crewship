package automation

import (
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestRenderInputs(t *testing.T) {
	e := journal.Entry{
		MissionID: "m_1",
		AgentID:   "a_1",
		CrewID:    "c_1",
		TraceID:   "run_1",
		Payload:   map[string]any{"from": "TODO", "to": "DONE"},
	}
	in := map[string]any{
		"issue":   "{{ event.mission_id }}",
		"agent":   "{{ event.agent_id }}",
		"crew":    "{{ event.crew_id }}",
		"run":     "{{ event.run_id }}",
		"summary": "{{ event.payload.from }} → {{ event.payload.to }}",
		"nested":  map[string]any{"deep": "{{ event.mission_id }}"},
		"list":    []any{"{{ event.mission_id }}", "literal"},
		"number":  7,
		"unknown": "{{ event.payload.absent }}",
	}
	want := map[string]any{
		"issue":   "m_1",
		"agent":   "a_1",
		"crew":    "c_1",
		"run":     "run_1",
		"summary": "TODO → DONE",
		"nested":  map[string]any{"deep": "m_1"},
		"list":    []any{"m_1", "literal"},
		"number":  7,
		"unknown": "",
	}
	if got := RenderInputs(in, e); !reflect.DeepEqual(got, want) {
		t.Errorf("RenderInputs =\n%#v\nwant\n%#v", got, want)
	}
}

// run_id comes from the entry's trace_id, because a journal entry belonging to
// an agent run carries trace_id == run.id (journal.prepareEntry). Reading it
// from anywhere else would render empty for exactly the entries an automation
// most wants to chain from.
func TestEventContextRunIDIsTheTraceID(t *testing.T) {
	got := EventContext(journal.Entry{TraceID: "run_42"})
	if got["run_id"] != "run_42" {
		t.Errorf("run_id = %v, want run_42", got["run_id"])
	}
}

func TestRenderInputsEmpty(t *testing.T) {
	got := RenderInputs(nil, journal.Entry{})
	if len(got) != 0 {
		t.Errorf("RenderInputs(nil) = %#v, want an empty map", got)
	}
}
