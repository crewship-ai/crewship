package sidecar

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestSSEUsageObserverAcrossChunkBoundaries(t *testing.T) {
	body := "data: {\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":40}}}\r\n\r\n" +
		"data: {\"usage\":\r\ndata: {\"output_tokens\":22}}\r\n\r\ndata: [DONE]"
	for _, size := range []int{1, 2, 7, 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := &sseUsageObserver{codec: "anthropic", limit: 1024}
			for p := body; len(p) > 0; {
				n := min(size, len(p))
				if got, err := s.Write([]byte(p[:n])); got != n || err != nil {
					t.Fatalf("Write = %d, %v", got, err)
				}
				p = p[n:]
			}
			got := s.finish()
			if got.InputTokens != 40 || got.OutputTokens != 22 || got.Model != "claude-test" || s.skipped != 0 {
				t.Fatalf("chunked usage = %+v, skipped=%d", got, s.skipped)
			}
		})
	}
}

func TestSSEUsageObserverLateUsageBeyondResponseCap(t *testing.T) {
	start := "data: {\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":40}}}\n\n"
	middle := strings.Repeat(": heartbeat\n\n", maxRequestBodyBytes/13+100)
	end := "data: {\"usage\":{\"output_tokens\":22}}\n\n"
	var calls int
	var got LLMUsage
	p := NewProxy(ProxyConfig{OnLLMCall: func(u LLMUsage, _ QuotaInfo, _, _ string) { calls++; got = u }})
	resp := jsonUpstreamResponse(200, "text/event-stream", "", nil)
	resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(start), strings.NewReader(middle), strings.NewReader(end)))
	p.copyAndObserveLLM(auditDiscardWriter{}, resp, "anthropic", "test-ledger", "agent", "credential")
	if calls != 1 || got.InputTokens != 40 || got.OutputTokens != 22 || got.Provider != "test-ledger" || got.CredentialID != "credential" {
		t.Fatalf("late usage lost or misattributed: calls=%d usage=%+v", calls, got)
	}
}

func TestSSEUsageObserverOversizedEventIsBoundedAndUnknown(t *testing.T) {
	s := &sseUsageObserver{codec: "openai", limit: 256}
	for i := 0; i < 100; i++ {
		_, _ = s.Write([]byte(strings.Repeat("x", 100)))
		if len(s.buf) > s.limit || cap(s.buf) > s.limit {
			t.Fatalf("retained payload exceeds limit: len=%d cap=%d", len(s.buf), cap(s.buf))
		}
	}
	_, _ = s.Write([]byte("\n\ndata: {\"model\":\"glm-5.3\",\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\n"))
	got := s.finish()
	if s.skipped != 1 || s.usage.InputTokens != 9 || got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Fatalf("oversized event must resync but not report partial totals: skipped=%d parsed=%+v reported=%+v", s.skipped, s.usage, got)
	}
}

func TestSSEUsageOversizedEventDoesNotBillZeroViaQuota(t *testing.T) {
	called := false
	p := NewProxy(ProxyConfig{OnLLMCall: func(LLMUsage, QuotaInfo, string, string) { called = true }})
	resp := jsonUpstreamResponse(429, "text/event-stream", strings.Repeat("x", maxRequestBodyBytes+1)+"\n\n", nil)
	p.copyAndObserveLLM(auditDiscardWriter{}, resp, "openai", "test-ledger", "agent", "credential")
	if called {
		t.Fatal("unobserved usage must not produce a zero-token ledger callback, even for 429")
	}
}
