package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Reasoning models are the second way a correctly configured Keeper judge could
// deny everything while looking healthy.
//
// Ollama returns a reasoning model's chain of thought in message.thinking,
// SEPARATELY from message.content. A model that spends its whole token budget
// thinking answers with content:"" and done_reason:"length" — an HTTP 200 with
// no error. The provider reported that as a successful, empty, StopEndTurn
// completion, so the fail-closed judge parsed nothing and DENIED, with nothing
// in the logs to explain it. Verified against a live Ollama 0.32.5 with
// qwen3:4b, whose /api/show capabilities include "thinking".
//
// The provider cannot fix the model choice, but it must stop hiding the reason.

func thinkingServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOllama_ThinkingBudgetExhausted is the exact shape a live qwen3:4b returns
// when reasoning eats the budget: empty content, populated thinking, and
// done_reason "length".
func TestOllama_ThinkingBudgetExhausted(t *testing.T) {
	srv := thinkingServer(t, `{"message":{"role":"assistant","content":"","thinking":"We are given a specific instruction..."},"done":true,"done_reason":"length","prompt_eval_count":22,"eval_count":200}`)

	resp, err := NewOllama(srv.URL, "qwen3:4b").Complete(context.Background(), probeRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "" {
		t.Fatalf("content = %q, want empty (the model never answered)", resp.Content)
	}
	if resp.Thinking == "" {
		t.Fatal("thinking was dropped — the caller cannot tell 'said nothing' from 'reasoned and ran out of budget'")
	}
	if resp.StopReason != StopMaxToks {
		t.Fatalf("stop reason = %q, want %q so a truncated verdict is distinguishable from a considered one",
			resp.StopReason, StopMaxToks)
	}
}

// TestOllama_ThinkingAlongsideAnswer covers the healthy reasoning case: thinking
// is captured but content still carries the verdict, and the turn ended normally.
func TestOllama_ThinkingAlongsideAnswer(t *testing.T) {
	srv := thinkingServer(t, `{"message":{"role":"assistant","content":"{\"decision\":\"ALLOW\",\"risk\":1}","thinking":"L1 credential, stated intent is proportional."},"done":true,"done_reason":"stop","prompt_eval_count":30,"eval_count":18}`)

	resp, err := NewOllama(srv.URL, "qwen3:4b").Complete(context.Background(), probeRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("content was dropped when thinking was present")
	}
	if resp.Thinking == "" {
		t.Fatal("thinking was dropped")
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, StopEndTurn)
	}
}

// TestOllama_NonThinkingModelUnaffected pins that the ordinary path did not
// change shape: no thinking field, a normal stop, content intact.
func TestOllama_NonThinkingModelUnaffected(t *testing.T) {
	srv := thinkingServer(t, `{"message":{"role":"assistant","content":"{\"decision\":\"DENY\",\"risk\":8}"},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":9}`)

	resp, err := NewOllama(srv.URL, "mistral:7b").Complete(context.Background(), probeRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Thinking != "" {
		t.Fatalf("thinking = %q, want empty", resp.Thinking)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, StopEndTurn)
	}
	if resp.Content == "" {
		t.Fatal("content lost")
	}
}

// TestOllama_ToolCallsStillWinOverLengthStop guards the precedence: a response
// that both carries tool calls and hit the length cap must still report tool
// use, because that is what the caller has to act on.
func TestOllama_ToolCallsStillWinOverLengthStop(t *testing.T) {
	srv := thinkingServer(t, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"lookup","arguments":{"q":"x"}}}]},"done":true,"done_reason":"length"}`)

	resp, err := NewOllama(srv.URL, "m").Complete(context.Background(), probeRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, StopToolUse)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
}

// TestOllama_StreamCarriesThinkingAndBudgetStop covers the streaming path, which
// accumulated only content: a reasoning model's chain of thought was dropped and
// a budget-truncated stream still reported end_turn. Raised by review on #1528 —
// the non-streaming fix left the two paths disagreeing about the same response.
func TestOllama_StreamCarriesThinkingAndBudgetStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		// Thinking arrives as deltas, then a done chunk cut off by the budget.
		for _, line := range []string{
			`{"message":{"role":"assistant","thinking":"We are given "},"done":false}`,
			`{"message":{"role":"assistant","thinking":"a specific instruction..."},"done":false}`,
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"length","prompt_eval_count":22,"eval_count":200}`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer srv.Close()

	var streamed []string
	resp, err := NewOllama(srv.URL, "qwen3:4b").Stream(context.Background(), probeRequest(),
		func(e StreamEvent) error {
			if e.Type == "text" {
				streamed = append(streamed, e.Content)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Thinking != "We are given a specific instruction..." {
		t.Fatalf("thinking = %q, want the accumulated deltas", resp.Thinking)
	}
	if resp.StopReason != StopMaxToks {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, StopMaxToks)
	}
	// Reasoning must not reach chat output as text.
	if len(streamed) != 0 {
		t.Fatalf("thinking leaked into text events: %q", streamed)
	}
}

// TestOllama_StreamNormalTurnUnaffected pins that the ordinary streaming path
// still reports end_turn and streams its content.
func TestOllama_StreamNormalTurnUnaffected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, line := range []string{
			`{"message":{"role":"assistant","content":"{\"decision\":"},"done":false}`,
			`{"message":{"role":"assistant","content":"\"ALLOW\"}"},"done":true,"done_reason":"stop","eval_count":9}`,
		} {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}))
	defer srv.Close()

	resp, err := NewOllama(srv.URL, "mistral:7b").Stream(context.Background(), probeRequest(),
		func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Content != `{"decision":"ALLOW"}` {
		t.Fatalf("content = %q, want the joined deltas", resp.Content)
	}
	if resp.StopReason != StopEndTurn {
		t.Fatalf("stop reason = %q, want %q", resp.StopReason, StopEndTurn)
	}
	if resp.Thinking != "" {
		t.Fatalf("thinking = %q, want empty", resp.Thinking)
	}
}
