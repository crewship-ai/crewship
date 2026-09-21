package sidecar

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A recorder observes Write immediately even when a real HTTP client cannot.
// Keep upstream open until the client receives the first small SSE event.
func TestZAICodingPlanFlushesBeforeUpstreamEOF(t *testing.T) {
	t.Run("observed", func(t *testing.T) { testZAIFlush(t, true) })
	t.Run("unobserved", func(t *testing.T) { testZAIFlush(t, false) })
}

func testZAIFlush(t *testing.T, observed bool) {
	t.Helper()
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "plan", Provider: "ZAI_CODING_PLAN", Token: "test-key"}})
	p := NewProxy(ProxyConfig{CredStore: cs, Logger: covLogger(), FreeMode: true, OnLLMCall: func(LLMUsage, QuotaInfo, string, string) {}})
	if !observed {
		p.onLLMCall = nil
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
	})
	srv := httptest.NewServer(p)
	defer func() { _ = writer.Close(); _ = reader.Close(); srv.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() { _, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n") }()
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/llm/zai-coding-plan/chat/completions", strings.NewReader(`{"model":"glm-5.3","stream":true}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("first event was buffered until upstream EOF: %v", err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unsafe stream headers: %v", resp.Header)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "hello") {
		t.Fatalf("first event: %q, %v", line, err)
	}
	_ = writer.Close()
}

func TestZAICodingPlanRevokedAndOtherProductFailClosed(t *testing.T) {
	cs := NewCredStore()
	p := NewProxy(ProxyConfig{CredStore: cs, Logger: covLogger(), FreeMode: true})
	calls := 0
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonUpstreamResponse(200, "application/json", `{}`, nil), nil
	})
	for _, creds := range [][]Credential{
		{{ID: "metered", Provider: "ZAI", Token: "metered-key"}},
		{{ID: "plan", Provider: "ZAI_CODING_PLAN", Token: "plan-key"}},
		{},
	} {
		cs.Load(creds)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://localhost/llm/zai-coding-plan/chat/completions", strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:1234"
		p.ServeHTTP(w, r)
		want := 503
		if len(creds) > 0 && creds[0].Provider == "ZAI_CODING_PLAN" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("status %d, want %d", w.Code, want)
		}
	}
	if calls != 1 {
		t.Fatalf("forwarded %d requests, want only granted request", calls)
	}
}

func BenchmarkZAICodingPlanProxy(b *testing.B) {
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "plan", Provider: "ZAI_CODING_PLAN", Token: "test-key"}})
	p := NewProxy(ProxyConfig{CredStore: cs, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), FreeMode: true, OnLLMCall: func(LLMUsage, QuotaInfo, string, string) {}})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return jsonUpstreamResponse(200, "text/event-stream", "data: {\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", nil), nil
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest("POST", "http://localhost/llm/zai-coding-plan/chat/completions", strings.NewReader(`{"model":"glm-5.3","stream":true}`))
		r.RemoteAddr = "127.0.0.1:1234"
		p.ServeHTTP(httptest.NewRecorder(), r)
	}
}

func TestZAICodingPlanAccountScopeAndPriority(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a", Provider: "ZAI_CODING_PLAN", Token: "a", Priority: 0, AgentIDs: []string{"alice"}},
		{ID: "b", Provider: "ZAI_CODING_PLAN", Token: "b", Priority: 1, AgentIDs: []string{"alice", "bob"}},
	})
	for _, tc := range []struct{ agent, want string }{{"alice", "a"}, {"bob", "b"}, {"mallory", ""}} {
		t.Run(tc.agent, func(t *testing.T) {
			for i := 0; i < 10; i++ {
				c := cs.Select("ZAI_CODING_PLAN", tc.agent)
				got := ""
				if c != nil {
					got = c.ID
				}
				if got != tc.want {
					t.Fatalf("%s selects %s want %s", tc.agent, got, tc.want)
				}
			}
		})
	}
}

func TestZAICodingPlanCancellationReachesUpstream(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "p", Provider: "ZAI_CODING_PLAN", Token: "test"}})
	p := NewProxy(ProxyConfig{CredStore: cs, Logger: covLogger(), FreeMode: true})
	entered := make(chan struct{})
	done := make(chan struct{})
	p.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest("POST", "http://localhost/llm/zai-coding-plan/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:1234"
	go func() { p.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("upstream not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach upstream")
	}
}

// Measures observation allocations, excluding construction of the payload and
// client buffering. This is a local parser/copy benchmark, not vendor latency.
func BenchmarkZAIStreamObservation(b *testing.B) {
	for _, size := range []int{1024, 1024 * 1024, 12 * 1024 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			body := strings.Repeat("data: {}\n\n", size/10)
			p := NewProxy(ProxyConfig{OnLLMCall: func(LLMUsage, QuotaInfo, string, string) {}})
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp := jsonUpstreamResponse(200, "text/event-stream", body, nil)
				p.copyAndObserveLLM(auditDiscardWriter{}, resp, "openai", "zai-coding-plan", "", "")
			}
		})
	}
}

type auditDiscardWriter struct{}

func (auditDiscardWriter) Header() http.Header         { return http.Header{} }
func (auditDiscardWriter) WriteHeader(int)             {}
func (auditDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (auditDiscardWriter) Flush()                      {}

func TestZAICodingPlanUsageCRLFMultiline(t *testing.T) {
	body := "event: completion\r\ndata: {\"model\":\"glm-5.3\",\r\ndata: \"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\r\n\r\ndata: [DONE]\r\n\r\n"
	got := parseLLMUsageSSE("openai", body)
	if got.Model != "glm-5.3" || got.InputTokens != 9 || got.OutputTokens != 2 {
		t.Fatalf("multiline CRLF usage: %+v", got)
	}
}

func TestZAIStreamHTMLRemainsData(t *testing.T) {
	w := httptest.NewRecorder()
	writer := sseFlushWriter{w: w, controller: http.NewResponseController(w)}
	payload := []byte("data: <script>alert(1)</script>\n\n")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	resp := w.Result()
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || w.Body.String() != string(payload) {
		t.Fatal("SSE must retain data bytes with an inert MIME type")
	}
}

func TestZAICodingPlanBillingIsSubscription(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/event-stream"} {
		for _, provider := range []string{"zai-coding-plan", "zai"} {
			t.Run(contentType+"/"+provider, func(t *testing.T) {
				body := `{"model":"glm-5.3","usage":{"prompt_tokens":10,"completion_tokens":2}}`
				if contentType == "text/event-stream" {
					body = "data: " + body + "\n\n"
				}
				calls := 0
				p := NewProxy(ProxyConfig{Logger: covLogger(), BillingMode: "metered", OnLLMCall: func(u LLMUsage, q QuotaInfo, mode, plan string) {
					calls++
					if u.InputTokens != 10 || u.OutputTokens != 2 {
						t.Fatalf("usage lost: %+v", u)
					}
					if provider == "zai-coding-plan" {
						if mode != "flat_rate" || plan != "GLM Coding Plan" {
							t.Errorf("subscription reported as %q / %q", mode, plan)
						}
					} else if mode != "metered" || plan != "" {
						t.Errorf("metered ZAI changed: %q / %q", mode, plan)
					}
				}})
				resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
				p.copyAndObserveLLM(httptest.NewRecorder(), resp, "openai", provider, "agent", "credential")
				if calls != 1 {
					t.Fatalf("observer called %d times", calls)
				}
			})
		}
	}
}
