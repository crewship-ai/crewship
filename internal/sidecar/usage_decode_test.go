package sidecar

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type usageDecodeFixture struct{ codec, body string }

func usageDecodeFixtures() []usageDecodeFixture {
	var fixtures []usageDecodeFixture
	for _, codec := range []string{"anthropic", "openai", "google", "", "OPENAI", "unknown"} {
		for _, body := range []string{
			``, `null`, `[]`, `42`, `true`, `{`, `{"response":{}} trailing`,
			`{"model":"r","usage":{"input_tokens":12,"prompt_tokens":12,"output_tokens":5},"modelVersion":"g","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}`,
			`{"mo\u0064el":"escaped","us\u0061ge":{"input_tokens":12,"prompt_tokens":12},"model\u0056ersion":"escaped-g","usage\u004detadata":{"promptTokenCount":12}}`,
			`{"MODEL":"upper","USAGE":{"INPUT_TOKENS":8,"PROMPT_TOKENS":8},"MODELVERSION":"upper-g","USAGEMETADATA":{"PROMPTTOKENCOUNT":8}}`,
			`{"model":7,"model":"later","response":{"model":"nested","usage":{"input_tokens":12}}}`,
			`{"model":"r","usage":false,"response":{"model":"nested","usage":{"input_tokens":12}}}`,
			`{"usage":{"input_tokens":9223372036854775808},"message":{"model":"nested","usage":{"input_tokens":12}}}`,
			`{"usage":{"input_tokens":-9223372036854775808,"prompt_tokens":-9223372036854775808,"prompt_tokens_details":{"cached_tokens":9223372036854775807}},"model":"extreme"}`,
			`{"usage":{"prompt_tokens":0,"input_tokens":25,"output_tokens":4,"input_tokens_details":{"cached_tokens":30}}}`,
			`{"usage":{"prompt_tokens":-1,"input_tokens":25,"output_tokens":4}}`,
			`{"usage":{"prompt_tokens":5},"usage":{"completion_tokens":9}}`,
			`{"response":{"model":"first","usage":{"input_tokens":9}},"response":null,"message":{"model":"last","usage":{"output_tokens":2}}}`,
			`{"response":{"model":"first"},"ReSpOnSe":{"model":"last","usage":{"input_tokens":8}}}`,
			`{"response":{"model":7,"usage":{"input_tokens":8}},"message":{"model":"valid","usage":{"input_tokens":9}}}`,
			`{"response":{"model":"nested","usage":{"input_tokens":12}},"usage":{"input_tokens":"bad"}}`,
			`{"usage":{"input_tokens":1.5,"prompt_tokens":true},"response":{"usage":{"input_tokens":8}}}`,
			`{"response":{"model":"valid","usage":{"input_tokens":12}},"model":"\q"}`,
			`{"response":{"model":"partial","usage":{"input_tokens":12}}`,
			`{"model":"a","usage":{"input_tokens":5},"message":{"model":"last"}}`,
			`{"modelVersion":7,"usageMetadata":false,"response":{"modelVersion":"nested","usageMetadata":{"promptTokenCount":12}}}`,
			`{"usageMetadata":{"promptTokenCount":9223372036854775808},"message":{"modelVersion":"nested","usageMetadata":{"candidatesTokenCount":8}}}`,
			`{"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":15},"modelVersion":"g"}`,
			`{"model":"` + string([]byte{0xff}) + `","usage":{"input_tokens":2}}`,
		} {
			fixtures = append(fixtures, usageDecodeFixture{codec, body})
		}
	}
	return fixtures
}

func TestUsageDecodeFrozenParity(t *testing.T) {
	for i, fixture := range usageDecodeFixtures() {
		t.Run(fmt.Sprintf("%s/%d", fixture.codec, i), func(t *testing.T) {
			if got, want := parseLLMUsage(fixture.codec, fixture.body), g08FrozenParse(fixture.codec, fixture.body); got != want {
				t.Fatalf("nonstream: got %+v want %+v", got, want)
			}
			for _, stream := range []string{
				"data: " + fixture.body + "\n\n",
				"event: synthetic\r\nid: 1\r\n: comment\r\ndata:" + fixture.body + "\r\n\r\ndata: [DONE]",
				"data: " + fixture.body + "\ndata: \n\ndata: {\"model\":\"last\",\"modelVersion\":\"last\"}",
			} {
				if got, want := parseLLMUsageSSE(fixture.codec, stream), g08FrozenParseSSE(fixture.codec, stream); got != want {
					t.Fatalf("SSE: got %+v want %+v", got, want)
				}
			}
		})
	}
}

func TestUsageDecodeExplicitFallbackAndMerge(t *testing.T) {
	for _, body := range []string{
		`{"model":7,"response":{"model":"nested","usage":{"input_tokens":12}}}`,
		`{"usage":"bad","response":{"model":"nested","usage":{"input_tokens":12}}}`,
		`{"model":7,"model":"later","response":{"model":"nested","usage":{"input_tokens":12}}}`,
		`{"usage":{"input_tokens":9223372036854775808},"response":{"model":"nested","usage":{"input_tokens":12}}}`,
	} {
		if got := parseLLMUsageSSE("openai", "data: "+body); got != (LLMUsage{Model: "nested", InputTokens: 12}) {
			t.Fatalf("invalid root hid valid nested usage: %+v", got)
		}
	}
	body := `{"model":"r","usage":{"prompt_tokens":10},"response":{"model":"s","usage":{"output_tokens":20}},"message":{"model":"t","usage":{"prompt_tokens_details":{"cached_tokens":3}}}}`
	if got := parseLLMUsageSSE("openai", "data: "+body); got != (LLMUsage{Model: "t", InputTokens: 10, OutputTokens: 20, CachedInputTokens: 3}) {
		t.Fatalf("root/response/message max and model order: %+v", got)
	}
}

func checkUsageObserverParity(t testing.TB, codec, body string, limit int, chunks []int) {
	t.Helper()
	got := &sseUsageObserver{codec: codec, limit: limit}
	want := &g08FrozenObserver{codec: codec, limit: limit}
	offset := 0
	for _, size := range chunks {
		if size > len(body)-offset {
			size = len(body) - offset
		}
		part := []byte(body[offset : offset+size])
		n, err := got.Write(part)
		nw, ew := want.Write(part)
		if err != nil || ew != nil || n != size || nw != size {
			t.Fatalf("Writer contract: %d/%d %v/%v", n, nw, err, ew)
		}
		if len(got.buf) > limit || cap(got.buf) > limit {
			t.Fatalf("observer retained beyond allowance: len=%d cap=%d limit=%d", len(got.buf), cap(got.buf), limit)
		}
		offset += size
	}
	if offset != len(body) {
		t.Fatalf("fixture chunks omitted bytes: %d/%d", offset, len(body))
	}
	g, w := got.finish(), want.finish()
	if g != w || got.skipped != want.skipped || got.usage != want.usage {
		t.Fatalf("observer: got %+v/%+v/%d want %+v/%+v/%d", g, got.usage, got.skipped, w, want.usage, want.skipped)
	}
}

func TestUsageObserverFrozenEveryByteCut(t *testing.T) {
	for _, codec := range []string{"anthropic", "openai", "google", "unknown"} {
		for _, body := range []string{
			"event: x\r\ndata:{\"mo\\u0064el\":\"loď\",\"usage\":\r\ndata:{\"input_tokens\":12,\"prompt_tokens\":12}}\r\n\r\ndata: [DONE]",
			"data: {\"modelVersion\":\"g\",\"usageMetadata\":{\"promptTokenCount\":10}}\n\ndata: {\"modelVersion\":\"last\"}",
			"data: {\"response\":{\"model\":\"valid\",\"usage\":{\"input_tokens\":8}},\"model\":7}\n\ndata: {\"model\":\"last\"}\n\n",
			"data: {\"usage\":{\"input_tokens\":8}}\n\n" + strings.Repeat("x", 300) + "\r\n\r\ndata: {\"model\":\"after\",\"usage\":{\"input_tokens\":2}}\n\n",
		} {
			for _, limit := range []int{0, 1, 64, 256, 1024} {
				for cut := 0; cut <= len(body); cut++ {
					checkUsageObserverParity(t, codec, body, limit, []int{cut, len(body) - cut})
				}
				for _, size := range []int{1, 2, 7, 64, 4096} {
					var chunks []int
					for offset := 0; offset < len(body); {
						n := min(size, len(body)-offset)
						chunks = append(chunks, n)
						offset += n
					}
					checkUsageObserverParity(t, codec, body, limit, chunks)
				}
			}
		}
	}
}

func TestUsageObserverExactEventAllowance(t *testing.T) {
	body := "data: {\"model\":\"m\",\"usage\":{\"prompt_tokens\":8}}\n\n"
	for _, limit := range []int{len(body) - 1, len(body), len(body) + 1} {
		s := &sseUsageObserver{codec: "openai", limit: limit}
		_, _ = s.Write([]byte(body))
		got := s.finish()
		if limit < len(body) {
			if s.skipped != 1 || got.InputTokens != 0 {
				t.Fatalf("oversize reported complete usage: %+v/%d", got, s.skipped)
			}
		} else if s.skipped != 0 || got.InputTokens != 8 {
			t.Fatalf("exact allowance lost usage: %+v/%d", got, s.skipped)
		}
	}
}

func TestUsageObserverProxyAttribution(t *testing.T) {
	for _, fixture := range []struct{ codec, body string }{
		{"openai", `{"model":"m","usage":{"prompt_tokens":12,"completion_tokens":4}}`},
		{"anthropic", `{"model":"m","usage":{"input_tokens":12,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}`},
		{"google", `{"modelVersion":"m","usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4,"cachedContentTokenCount":2}}`},
	} {
		for _, provider := range []string{"openrouter", "zai-coding-plan"} {
			body := "data: " + fixture.body + "\n\ndata: [DONE]\n\n"
			want := g08FrozenParseSSE(fixture.codec, body)
			want.Provider = provider
			want.AgentID = "local-agent"
			want.CredentialID = "local-credential"
			calls := 0
			p := NewProxy(ProxyConfig{BillingMode: "metered", SubscriptionPlan: "synthetic", OnLLMCall: func(u LLMUsage, q QuotaInfo, mode, plan string) {
				calls++
				if u != want || q != (QuotaInfo{}) {
					t.Fatalf("callback changed: %+v/%+v want %+v", u, q, want)
				}
				if provider == "zai-coding-plan" {
					if mode != "flat_rate" || plan != "GLM Coding Plan" {
						t.Fatal("coding-plan billing changed")
					}
				} else if mode != "metered" || plan != "synthetic" {
					t.Fatal("billing context changed")
				}
			}})
			resp := jsonUpstreamResponse(http.StatusOK, "text/event-stream", body, nil)
			dst := httptest.NewRecorder()
			p.copyAndObserveLLM(dst, resp, fixture.codec, provider, "local-agent", "local-credential")
			if calls != 1 || dst.Body.String() != body {
				t.Fatalf("callback/delivery: calls=%d bytes=%d", calls, dst.Body.Len())
			}
		}
	}
}

func FuzzUsageDecodeParity(f *testing.F) {
	for _, fixture := range usageDecodeFixtures() {
		index := uint8(3)
		switch fixture.codec {
		case "anthropic":
			index = 0
		case "openai":
			index = 1
		case "google":
			index = 2
		}
		f.Add(index, fixture.body)
		f.Add(index, "data: "+fixture.body+"\n\n")
	}
	f.Fuzz(func(t *testing.T, which uint8, body string) {
		if len(body) > 8192 {
			body = body[:8192]
		}
		codec := []string{"anthropic", "openai", "google", "unknown"}[which%4]
		if got, want := parseLLMUsage(codec, body), g08FrozenParse(codec, body); got != want {
			t.Fatalf("nonstream %+v != %+v", got, want)
		}
		if got, want := parseLLMUsageSSE(codec, body), g08FrozenParseSSE(codec, body); got != want {
			t.Fatalf("SSE %+v != %+v", got, want)
		}
		for _, size := range []int{1, 7, 64} {
			var chunks []int
			for offset := 0; offset < len(body); {
				n := min(size, len(body)-offset)
				chunks = append(chunks, n)
				offset += n
			}
			checkUsageObserverParity(t, codec, body, 256, chunks)
		}
	})
}

func TestUsageObserverPartialStreamDoesNotBillViaQuota(t *testing.T) {
	calls := 0
	p := NewProxy(ProxyConfig{OnLLMCall: func(LLMUsage, QuotaInfo, string, string) { calls++ }})
	start := "data: {\"model\":\"first\",\"usage\":{\"prompt_tokens\":99}}\n\n"
	end := "\n\ndata: {\"model\":\"last\",\"usage\":{\"prompt_tokens\":2}}\n\n"
	resp := jsonUpstreamResponse(http.StatusTooManyRequests, "text/event-stream", "", map[string]string{"x-ratelimit-limit-tokens": "100", "x-ratelimit-remaining-tokens": "0"})
	resp.Body = io.NopCloser(io.MultiReader(strings.NewReader(start), strings.NewReader(strings.Repeat("x", maxRequestBodyBytes+1)), strings.NewReader(end)))
	p.copyAndObserveLLM(auditDiscardWriter{}, resp, "openai", "openrouter", "local-agent", "local-credential")
	if calls != 0 {
		t.Fatalf("partial usage became a billing callback via quota: %d", calls)
	}
}
