package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #1423 item 3: RunDeterministicStep lets a caller (the /step_run API
// handler) execute one http/transform/script step in isolation by reusing
// the exact same unexported runner functions (runHTTPStep / runTransformStep
// / runScriptStep) the real DAG dispatch loop (dispatchStep) uses — not a
// second, reimplemented execution path.

func TestRunDeterministicStep_Transform(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)

	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{Input: `{"a":1,"b":2}`, Expression: "@json"},
	}
	out, cost, _, err := exec.RunDeterministicStep(context.Background(), step, RenderContext{}, RunInput{}, "")
	if err != nil {
		t.Fatalf("RunDeterministicStep: %v", err)
	}
	if out != `{"a":1,"b":2}` {
		t.Errorf("output: got %q", out)
	}
	if cost != 0 {
		t.Errorf("transform steps burn no tokens; cost = %v, want 0", cost)
	}
}

func TestRunDeterministicStep_HTTP(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	exec.SetAllowPrivateHTTPForTesting(true) // httptest.NewServer binds to 127.0.0.1

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "hello" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	step := Step{
		ID: "h", Type: StepHTTP,
		HTTP: &HTTPStep{Method: "GET", URL: srv.URL + "?q={{ inputs.q }}"},
	}
	rctx := RenderContext{Inputs: map[string]any{"q": "hello"}}
	out, _, _, err := exec.RunDeterministicStep(context.Background(), step, rctx, RunInput{}, "")
	if err != nil {
		t.Fatalf("RunDeterministicStep: %v", err)
	}
	if out != "pong" {
		t.Errorf("output: got %q, want pong", out)
	}
}

func TestRunDeterministicStep_RejectsAgentRun(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)

	step := Step{ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "hi"}
	_, _, _, err := exec.RunDeterministicStep(context.Background(), step, RenderContext{}, RunInput{}, "")
	if err == nil {
		t.Fatal("expected an error for a non-deterministic step type")
	}
}

func TestRunDeterministicStep_RejectsWait(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)

	step := Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "datetime", Until: "2099-01-01T00:00:00Z"}}
	_, _, _, err := exec.RunDeterministicStep(context.Background(), step, RenderContext{}, RunInput{}, "")
	if err == nil {
		t.Fatal("expected an error for a non-deterministic step type")
	}
}
