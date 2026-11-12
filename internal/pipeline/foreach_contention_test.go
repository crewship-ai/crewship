package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestForeachAgentContentionWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
		path       string
	}{
		{"same agent", `{"steps":[{"id":"batch","type":"foreach","foreach":{"parallelism":3,"steps":[{"id":"classify","type":"agent_run","agent_slug":"worker"}]}}]}`, 1, "steps[0].foreach.steps[0].agent_slug"},
		{"sequential", `{"steps":[{"type":"foreach","foreach":{"parallelism":1,"steps":[{"type":"agent_run","agent_slug":"worker"}]}}]}`, 0, ""},
		{"default parallel", `{"steps":[{"type":"foreach","foreach":{"steps":[{"type":"agent_run","agent_slug":"worker"}]}}]}`, 1, "steps[0].foreach.steps[0].agent_slug"},
		{"different item agents", `{"steps":[{"type":"foreach","foreach":{"parallelism":3,"steps":[{"type":"agent_run","agent_slug":"{{ inputs.item.agent }}"}]}}]}`, 0, ""},
		{"distinct top level agents", `{"steps":[{"type":"agent_run","agent_slug":"a"},{"type":"agent_run","agent_slug":"b"}]}`, 0, ""},
		{"non agent", `{"steps":[{"type":"foreach","foreach":{"parallelism":3,"steps":[{"type":"script","agent_slug":"worker"}]}}]}`, 0, ""},
		{"nested sequential still repeated by outer", `{"steps":[{"type":"foreach","foreach":{"parallelism":2,"steps":[{"type":"foreach","foreach":{"parallelism":1,"steps":[{"type":"agent_run","agent_slug":"worker"}]}}]}}]}`, 1, "steps[0].foreach.steps[0].foreach.steps[0].agent_slug"},
		{"sequential parent parallel child", `{"steps":[{"type":"foreach","foreach":{"parallelism":1,"steps":[{"type":"foreach","foreach":{"parallelism":2,"steps":[{"type":"agent_run","agent_slug":"worker"}]}}]}}]}`, 1, "steps[0].foreach.steps[0].foreach.steps[0].agent_slug"},
		{"fixed grader", `{"steps":[{"type":"foreach","foreach":{"parallelism":2,"steps":[{"type":"agent_run","agent_slug":"{{ inputs.item.agent }}","outcomes":{"grader_agent_slug":"reviewer"}}]}}]}`, 1, "steps[0].foreach.steps[0].outcomes.grader_agent_slug"},
		{"two static agents each repeats", `{"steps":[{"type":"foreach","foreach":{"parallelism":3,"steps":[{"type":"agent_run","agent_slug":"a"},{"type":"agent_run","agent_slug":"b"}]}}]}`, 2, "steps[0].foreach.steps[0].agent_slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var def map[string]any
			if err := json.Unmarshal([]byte(tc.body), &def); err != nil {
				t.Fatal(err)
			}
			got := ForeachAgentContentionWarnings(def)
			if len(got) != tc.want {
				t.Fatalf("got %+v, want %d warnings", got, tc.want)
			}
			if len(got) > 0 && (got[0].Path != tc.path || !strings.Contains(got[0].Message, "agent")) {
				t.Fatalf("warning %+v", got[0])
			}
		})
	}
}
