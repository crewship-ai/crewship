package llm

// hooks_middleware_test.go proves pre_llm_call / post_llm_call — two of the
// ten hook events found alongside pre_tool_call (#2132) to be declared in
// hooks.AllEvents, accepted by the CLI/API, and reached by zero
// hooks.Dispatch call sites — now actually fire around every Complete/
// Stream call, via the hooksCaller layer Middleware wraps outermost.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/lookout"
)

// TestMiddleware_DispatchesPreAndPostLLMCallHooks registers a webhook on
// each event (blocking only for the pre-event), drives one Complete call through the full
// middleware stack, and asserts both fired exactly once, in order.
func TestMiddleware_DispatchesPreAndPostLLMCallHooks(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	db := openLLMTestDB(t)
	ctx := context.Background()

	type hookHit struct {
		path string
		body map[string]any
	}
	seenCh := make(chan hookHit, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode hook body: %v", err)
		}
		seenCh <- hookHit{path: r.URL.Path, body: body}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	for _, ev := range []hooks.Event{hooks.EventPreLLMCall, hooks.EventPostLLMCall} {
		if _, err := hooks.Register(ctx, db, hooks.Hook{
			WorkspaceID:   "ws-1",
			Event:         ev,
			HandlerKind:   hooks.HandlerKindHTTP,
			HandlerConfig: map[string]any{"url": ts.URL + "/" + string(ev)},
			Blocking:      ev == hooks.EventPreLLMCall,
			Enabled:       true,
		}, false); err != nil {
			t.Fatalf("register %s hook: %v", ev, err)
		}
	}

	stub := &stubProvider{
		name: "anthropic",
		resp: &Response{Content: "hello back", StopReason: StopEndTurn, InputToks: 12, OutputToks: 4},
	}
	mw := Middleware(stub, &fakeLLMEmitter{}, db)
	rctx := lookout.WithScope(ctx, lookout.Scope{WorkspaceID: "ws-1"})

	if _, err := mw.Complete(rctx, Request{
		Model:    "claude-haiku-4-5",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	seen := make([]hookHit, 0, 2)
	for len(seen) < 2 {
		select {
		case hit := <-seenCh:
			seen = append(seen, hit)
		case <-time.After(time.Second):
			t.Fatalf("hook hits = %v, want exactly [pre_llm_call, post_llm_call]", seen)
		}
	}
	if seen[0].path != "/pre_llm_call" || seen[1].path != "/post_llm_call" {
		t.Errorf("hook order = %v, want [/pre_llm_call /post_llm_call]", seen)
	}
	if cost, _ := seen[1].body["cost_usd"].(float64); cost <= 0 {
		t.Errorf("post_llm_call cost_usd = %v, want the paymaster estimate", seen[1].body["cost_usd"])
	}
}

// TestMiddleware_PreLLMCallHookBlocksTheCall proves a blocking
// pre_llm_call hook refuses the call before the provider is ever
// invoked — the whole point of dispatching it outermost in the chain.
func TestMiddleware_PreLLMCallHookBlocksTheCall(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")

	db := openLLMTestDB(t)
	ctx := context.Background()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503) // Block outcome per hooks' http handler contract
	}))
	defer ts.Close()

	if _, err := hooks.Register(ctx, db, hooks.Hook{
		WorkspaceID:   "ws-1",
		Event:         hooks.EventPreLLMCall,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	stub := &stubProvider{name: "anthropic", resp: &Response{Content: "should not happen"}}
	mw := Middleware(stub, &fakeLLMEmitter{}, db)
	rctx := lookout.WithScope(ctx, lookout.Scope{WorkspaceID: "ws-1"})

	_, err := mw.Complete(rctx, Request{
		Model:    "claude-haiku-4-5",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected the blocking pre_llm_call hook to refuse the call")
	}
	if stub.gotReq.Model != "" {
		t.Error("provider was called despite the pre_llm_call block")
	}
}
