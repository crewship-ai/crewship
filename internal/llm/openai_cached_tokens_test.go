package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/lookout"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

// OpenAI's usage.prompt_tokens is INCLUSIVE of
// usage.prompt_tokens_details.cached_tokens — the cached read is counted
// twice if InputToks is taken verbatim, once at the full input rate and
// once at the cached rate. internal/sidecar/usage.go:122 already subtracts
// on the proxy billing path, so before this fix the two writers of the same
// cost_ledger disagreed: the sidecar billed fresh input, the in-process
// codec billed fresh + cached.
//
// These tests pin the convention on the codec side. The subtraction belongs
// here and NOT in paymaster.Estimate: Estimate is also fed by the sidecar
// (already corrected) and by Anthropic (whose input_tokens genuinely
// excludes cache reads), so a subtracting Estimate would under-bill both.
// TestAnthropicInputToksKeepsWireValue below is the guard against a later
// "simplification" that moves it.

// openaiUsageFixture serves one non-streaming chat completion carrying the
// given usage block. Numbers, not a canned body, so the table can drive it.
func openaiUsageFixture(t *testing.T, promptToks, completionToks, cachedToks int) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":         promptToks,
				"completion_tokens":     completionToks,
				"prompt_tokens_details": map[string]int{"cached_tokens": cachedToks},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return NewOpenAIWithBaseURL("test-key", srv.URL+"/v1/chat/completions")
}

// openaiSSEFixture serves an SSE stream whose terminal chunk(s) carry the
// usage block. usageChunks says how many usage-bearing chunks to emit: a
// backend that reports usage more than once must not have the cached count
// subtracted more than once.
func openaiSSEFixture(t *testing.T, promptToks, completionToks, cachedToks, usageChunks int) *OpenAI {
	t.Helper()
	var sse strings.Builder
	sse.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	for i := 0; i < usageChunks; i++ {
		finish := ""
		if i == usageChunks-1 {
			finish = `"finish_reason":"stop"`
		} else {
			finish = `"finish_reason":null`
		}
		fmt.Fprintf(&sse,
			"data: {\"choices\":[{\"delta\":{},%s}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":%d}}}\n\n",
			finish, promptToks, completionToks, cachedToks)
	}
	sse.WriteString("data: [DONE]\n")

	body := sse.String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewOpenAIWithBaseURL("test-key", srv.URL+"/v1/chat/completions")
}

func discardStream(StreamEvent) error { return nil }

// TestOpenAIInputToksExcludesCachedTokens is the core assertion, run over
// both wire paths. The 1500/50/1200 row is the fixture already used by
// TestOpenAICachedTokens in provider_test.go — an 80%-cached prompt, which
// is the ordinary shape once OpenAI's auto-cache warms up.
func TestOpenAIInputToksExcludesCachedTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		prompt     int
		completion int
		cached     int
		wantInput  int
	}{
		{
			name: "80 percent cached prompt", prompt: 1500, completion: 50, cached: 1200,
			wantInput: 300,
		},
		{
			name: "no cache hit leaves the wire value alone", prompt: 900, completion: 12, cached: 0,
			wantInput: 900,
		},
		{
			name: "fully cached prompt bills no fresh input", prompt: 2048, completion: 8, cached: 2048,
			wantInput: 0,
		},
		{
			// A compat backend (vllm / ollama-openai presets) reporting
			// cached > prompt must not write a NEGATIVE input_tokens onto
			// the ledger: Estimate's clamp protects the dollar figure, not
			// the stored count, and a negative count inverts the cache-hit
			// ratio gate at paymaster/ledger.go:141.
			name: "cached over prompt clamps at zero", prompt: 100, completion: 4, cached: 400,
			wantInput: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := Request{
				Model:    "gpt-5.5",
				Messages: []Message{{Role: RoleUser, Content: "Hi"}},
			}

			t.Run("complete", func(t *testing.T) {
				p := openaiUsageFixture(t, tt.prompt, tt.completion, tt.cached)
				resp, err := p.Complete(context.Background(), req)
				if err != nil {
					t.Fatalf("Complete: %v", err)
				}
				assertUsage(t, resp, tt.wantInput, tt.completion, tt.cached)
			})

			t.Run("stream", func(t *testing.T) {
				p := openaiSSEFixture(t, tt.prompt, tt.completion, tt.cached, 1)
				resp, err := p.Stream(context.Background(), req, discardStream)
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				assertUsage(t, resp, tt.wantInput, tt.completion, tt.cached)
			})
		})
	}
}

// TestOpenAIStreamRepeatedUsageChunkSubtractsOnce pins the implementation
// shape of the streaming path: each usage-bearing chunk must be reduced
// from its OWN prompt/cached pair, never from the already-stored
// final.InputToks. Subtracting in place would leave -900 here.
func TestOpenAIStreamRepeatedUsageChunkSubtractsOnce(t *testing.T) {
	t.Parallel()
	p := openaiSSEFixture(t, 1500, 50, 1200, 2)
	resp, err := p.Stream(context.Background(), Request{
		Model:    "gpt-5.5",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	}, discardStream)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	assertUsage(t, resp, 300, 50, 1200)
}

// TestAnthropicInputToksKeepsWireValue is the guard, not a new feature:
// Anthropic reports input_tokens and cache_read_input_tokens as disjoint
// counts, so the OpenAI subtraction must never be generalised into
// paymaster.Estimate or copied into anthropic.go. Green before and after.
func TestAnthropicInputToksKeepsWireValue(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage": map[string]int{
				"input_tokens":                100,
				"output_tokens":               20,
				"cache_read_input_tokens":     500,
				"cache_creation_input_tokens": 0,
			},
		})
	}))
	defer srv.Close()

	p := newTestAnthropic("test-key", srv)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-haiku-4-5",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	assertUsage(t, resp, 100, 20, 500)
}

// TestMiddlewareOpenAICachedTokensBilledOnce is the money, end to end: the
// real codec behind the real middleware chain, writing a real ledger row.
//
// On an 80%-cached 1500-token gpt-5.5 prompt the over-bill was exactly
// cachedIn * InputPerM:
//
//	before: 1500*4.00/M + 50*24.00/M + 1200*0.40/M = $0.00768
//	after:   300*4.00/M + 50*24.00/M + 1200*0.40/M = $0.00288
//
// 2.67x over, on every OpenAI-family call in the fleet (the openai,
// deepseek, ollama-openai and vllm presets all share this codec).
func TestMiddlewareOpenAICachedTokensBilledOnce(t *testing.T) {
	db := openLLMTestDB(t)
	em := &fakeLLMEmitter{}
	p := openaiUsageFixture(t, 1500, 50, 1200)

	mw := Middleware(p, em, db)
	ctx := lookout.WithScope(context.Background(), lookout.Scope{WorkspaceID: "ws-cached"})
	if _, err := mw.Complete(ctx, Request{
		Model:    "gpt-5.5",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var in, out, cached int64
	var costUSD float64
	err := db.QueryRowContext(context.Background(), `
		SELECT input_tokens, output_tokens, cached_input_tokens, cost_usd
		FROM cost_ledger WHERE workspace_id = 'ws-cached'`).
		Scan(&in, &out, &cached, &costUSD)
	if err != nil {
		t.Fatalf("ledger query: %v", err)
	}
	if in != 300 {
		t.Errorf("cost_ledger.input_tokens = %d, want 300 (fresh input, cached subtracted)", in)
	}
	if out != 50 {
		t.Errorf("cost_ledger.output_tokens = %d, want 50", out)
	}
	if cached != 1200 {
		t.Errorf("cost_ledger.cached_input_tokens = %d, want 1200", cached)
	}
	want := paymaster.Estimate("openai", "gpt-5.5", 300, 50, 1200, 0)
	if diff := costUSD - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost_ledger.cost_usd = %.8f, want %.8f (over-bill is cachedIn*InputPerM)", costUSD, want)
	}
}

// assertUsage checks the three counters a cached-token bug can move.
func assertUsage(t *testing.T, resp *Response, wantIn, wantOut, wantCached int) {
	t.Helper()
	if resp.InputToks != wantIn {
		t.Errorf("InputToks = %d, want %d", resp.InputToks, wantIn)
	}
	if resp.OutputToks != wantOut {
		t.Errorf("OutputToks = %d, want %d", resp.OutputToks, wantOut)
	}
	if resp.CachedInputToks != wantCached {
		t.Errorf("CachedInputToks = %d, want %d", resp.CachedInputToks, wantCached)
	}
}
