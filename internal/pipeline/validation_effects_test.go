package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type validationScriptProbe struct{ calls atomic.Int32 }

func (p *validationScriptProbe) RunScript(context.Context, ScriptRunRequest) (ScriptRunResult, error) {
	p.calls.Add(1)
	return ScriptRunResult{Stdout: "script result"}, nil
}

// Prove both sides of the boundary with the same executable recipe. A test
// that only passes an unwired HTTP/script step could succeed because execution
// was broken, rather than because validation correctly avoided effects.
func TestDefinitionValidationDoesNotExecuteWork(t *testing.T) {
	for _, parallelism := range []string{ParallelismOff, ParallelismExplicit, ParallelismAuto} {
		t.Run(parallelism, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Write([]byte("http result"))
			}))
			defer server.Close()
			agent := newMockRunner()
			script := &validationScriptProbe{}
			executor := NewExecutor(store, resolver, agent, &captureEmitter{}).WithScriptRunner(script)
			executor.allowPrivateHTTP = true
			dsl := &DSL{
				DSLVersion: "1.0", Name: "validation-effects", Parallelism: parallelism,
				Steps: []Step{
					{ID: "send", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: server.URL}},
					{ID: "script", Type: StepScript, Needs: []string{"send"}, Script: &ScriptStep{Path: "scripts/check.py"}},
					{ID: "agent", Type: StepAgentRun, Needs: []string{"script"}, AgentSlug: "agent_lead", Prompt: "prepare result"},
				},
			}
			input := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeDryRun}
			preview, err := executor.RunDefinition(context.Background(), dsl, input)
			if err != nil || preview.Status != "DRY_RUN_OK" || len(preview.WouldExecute) != 3 {
				t.Fatalf("preview: %#v, %v", preview, err)
			}
			if requests.Load() != 0 || script.calls.Load() != 0 || len(agent.calls) != 0 {
				t.Fatal("definition validation performed real work")
			}
			input.Mode = ModeRun
			live, err := executor.RunDefinition(context.Background(), dsl, input)
			if err != nil || live.Status != "COMPLETED" {
				t.Fatalf("live control: %#v, %v", live, err)
			}
			if requests.Load() != 1 || script.calls.Load() != 1 || len(agent.calls) != 1 {
				t.Fatalf("live control did not exercise every adapter: http=%d script=%d agent=%d", requests.Load(), script.calls.Load(), len(agent.calls))
			}
		})
	}
}
