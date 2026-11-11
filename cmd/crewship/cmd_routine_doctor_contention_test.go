package main

import (
	"bytes"
	"encoding/json"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestDoctorForeachAgentContentionIsAdvisory(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		parallelism int
		want        int
	}{
		{"fixed shared agent", "worker", 3, 1},
		{"sequential", "worker", 1, 0},
		{"per item target", "{{ inputs.item.agent }}", 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"steps": []any{map[string]any{"type": "foreach", "foreach": map[string]any{"parallelism": tc.parallelism, "steps": []any{map[string]any{"type": "agent_run", "agent_slug": tc.agent}}}}}})
			var def map[string]any
			_ = json.Unmarshal(raw, &def)
			got := checkForeachAgentContention(def)
			if len(got) != tc.want {
				t.Fatalf("warnings %+v", got)
			}
			covSaveState(t)
			var buf bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&buf)
			if err := finishDoctorReport(cmd, doctorReport{Slug: "test", Checks: got}); err != nil {
				t.Fatalf("warning blocked doctor: %v", err)
			}
			for _, c := range got {
				if c.Level != doctorWarn || !strings.Contains(c.Message, "steps[0].foreach.steps[0].agent_slug") {
					t.Fatalf("warning %+v", c)
				}
			}
		})
	}
}
