package orchestrator

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func TestRecordRecoveredAbsencePreservesNewOwnership(t *testing.T) {
	for _, scenario := range []string{"absent", "local-owner", "replaced", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			state := newMemState()
			o := New(nil, state, slog.Default())
			expected := RunState{ID: "run", AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: "running", StartedAt: time.Now()}
			current := expected
			switch scenario {
			case "local-owner":
				_, finish := o.trackAgentRun(t.Context(), &AgentRunRequest{RunID: "run", AgentID: "a"})
				defer finish()
			case "replaced":
				current.StartedAt = current.StartedAt.Add(time.Second)
			case "completed":
				current.Status = "completed"
			}
			raw, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			if err = state.Set(t.Context(), "agent_runs", "run", raw); err != nil {
				t.Fatal(err)
			}
			changed, err := o.RecordRecoveredAbsence(t.Context(), "run", expected)
			if err != nil {
				t.Fatal(err)
			}
			if changed != (scenario == "absent") {
				t.Fatalf("changed=%v", changed)
			}
			actualRaw, err := state.Get(t.Context(), "agent_runs", "run")
			if err != nil {
				t.Fatal(err)
			}
			var actual RunState
			if err = json.Unmarshal(actualRaw, &actual); err != nil {
				t.Fatal(err)
			}
			if scenario == "absent" {
				if actual.Status != "cancelled" || !actual.StopJournalPending || actual.StopOrigin != "recovered_absence" {
					t.Fatalf("missing durable recovery outcome: %+v", actual)
				}
			} else if string(actualRaw) != string(raw) {
				t.Fatalf("newer or owned state overwritten: %s", actualRaw)
			}
		})
	}
}
