package pipeline

import (
	"context"
	"strings"
	"testing"
)

// Exercise the actual executor, including step dispatch, journaling, and the
// production CEL/expr runners. Input values must never become source text.
func TestCodeDataBoundary_RunDefinition(t *testing.T) {
	for _, tc := range []struct{ name, runtime, code, value string }{
		{"cel", "cel", `"{{ inputs.value }}" == "approved"`, `x" == "x" || true || "x`},
		{"expr", "expr", `{{ inputs.value }} > 5`, `" > 5" == "`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			agent := newMockRunner()
			executor := NewExecutor(store, resolver, agent, &captureEmitter{}).WithCodeRunner(NewMultiCodeRunner())
			dsl := &DSL{DSLVersion: "1.0", Name: "data-boundary", Agentless: true,
				Inputs: []InputSpec{{Name: "value", Type: "string"}},
				Steps:  []Step{{ID: "predicate", Type: StepCode, Code: &CodeStep{Runtime: tc.runtime, Code: tc.code}}}}
			benign := "approved"
			if tc.runtime == "expr" {
				benign = "6"
			}
			control, err := executor.RunDefinition(context.Background(), dsl, RunInput{
				WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
				Inputs: map[string]any{"value": benign},
			})
			if err != nil || control.Status != "COMPLETED" || control.Output != "true" {
				t.Fatalf("benign execution control failed: result=%+v err=%v", control, err)
			}
			res, err := executor.RunDefinition(context.Background(), dsl, RunInput{
				WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
				Inputs: map[string]any{"value": tc.value},
			})
			if tc.runtime == "cel" {
				if err != nil || res.Status != "COMPLETED" || res.Output != "false" {
					t.Fatalf("input changed the predicate: result=%+v err=%v; want completed false", res, err)
				}
			} else {
				message := ""
				if err != nil {
					message = err.Error()
				} else if res != nil {
					message = res.ErrorMessage
				}
				if !strings.Contains(message, "numeric") {
					t.Fatalf("expr must reject nonnumeric data: result=%+v err=%v", res, err)
				}
			}
			if len(agent.calls) != 0 {
				t.Fatal("code step invoked an agent")
			}
		})
	}
}
