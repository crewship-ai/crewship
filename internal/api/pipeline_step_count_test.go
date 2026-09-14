package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestRoutineListStepCountDistinguishesEmptyAndUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name, definition string
		want             int
		known            bool
	}{
		{"empty", `{"name":"empty","steps":[]}`, 0, true},
		{"named step", `{"name":"sample","steps":[{"id":"internal","name":"Prepare the result","type":"transform","transform":{"expression":"."}}]}`, 1, true},
		{"unreadable", `{`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toPipelineResponse(&pipeline.Pipeline{DefinitionJSON: tc.definition}, false)
			if (got.StepCount != nil) != tc.known {
				t.Fatalf("known count=%v, want %v", got.StepCount, tc.known)
			}
			if got.StepCount != nil && *got.StepCount != tc.want {
				t.Fatalf("count=%d, want %d", *got.StepCount, tc.want)
			}
			if got.Definition != nil {
				t.Fatal("list must not include the entire recipe")
			}
		})
	}
}
