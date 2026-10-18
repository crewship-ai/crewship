package main

import (
	"strings"
	"testing"
)

func TestAcceptance_AgentGetReportsConfiguredModel(t *testing.T) {
	config := startAgentReadinessServer(t)
	for _, format := range []string{"json", "yaml", "table"} {
		t.Run(format, func(t *testing.T) {
			out, err := runAgentReadinessCLI(t, config, "agent", "get", "casey", "--format", format)
			if err != nil {
				t.Fatalf("real CLI: %v %s", err, out)
			}
			for _, want := range []string{"ANTHROPIC", "claude-sonnet-4-5"} {
				if !strings.Contains(out, want) {
					t.Errorf("%s omits %s: %s", format, want, out)
				}
			}
			if format != "table" {
				for _, key := range []string{"llm_provider", "llm_model"} {
					if !strings.Contains(out, key) {
						t.Errorf("missing %s", key)
					}
				}
			}
		})
	}
}
