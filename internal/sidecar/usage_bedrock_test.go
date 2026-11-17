package sidecar

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func bedrockTestFrame(payload string) []byte {
	frame := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

const bedrockTestUsage = `{"usage":{"inputTokens":40,"outputTokens":22,"cacheReadInputTokens":10,"cacheWriteInputTokens":5}}`

func TestBedrockUsageFrameBoundaries(t *testing.T) {
	frame := bedrockTestFrame(bedrockTestUsage)
	for _, size := range []int{1, 2, 7, 12, 64, 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var observer bedrockUsageObserver
			body := append(bedrockTestFrame(`{"contentBlockDelta":{"delta":{"text":"hello"}}}`), frame...)
			for len(body) > 0 {
				n := min(size, len(body))
				written, err := observer.Write(body[:n])
				if written != n || err != nil {
					t.Fatal(written, err)
				}
				body = body[n:]
			}
			got, ok := observer.finish()
			if !ok || got.InputTokens != 40 || got.OutputTokens != 22 || got.CachedInputTokens != 10 || got.CacheCreationTokens != 5 {
				t.Fatalf("%+v complete=%t", got, ok)
			}
		})
	}
}

func TestBedrockUsageRejectsDamagedAndOversizedFrames(t *testing.T) {
	prelude := bedrockTestFrame(bedrockTestUsage)
	prelude[0] ^= 1
	crc := bedrockTestFrame(bedrockTestUsage)
	crc[len(crc)-1] ^= 1
	for name, body := range map[string][]byte{"prelude": prelude, "crc": crc, "truncated": crc[:len(crc)-5], "oversize": bedrockTestFrame(strings.Repeat("x", bedrockUsageFrameLimit+1))} {
		t.Run(name, func(t *testing.T) {
			var observer bedrockUsageObserver
			_, _ = observer.Write(body)
			_, _ = observer.Write(bedrockTestFrame(bedrockTestUsage))
			if _, ok := observer.finish(); ok {
				t.Fatal("damaged observation counted as complete")
			}
			if cap(observer.frame) > bedrockUsageFrameLimit {
				t.Fatal("unbounded observation")
			}
		})
	}
}

func TestBedrockProxyForwardsBytesAndAccountsLateUsage(t *testing.T) {
	var got LLMUsage
	var calls int
	proxy := NewProxy(ProxyConfig{OnLLMCall: func(u LLMUsage, _ QuotaInfo, _, _ string) { got = u; calls++ }})
	text := bedrockTestFrame(`{"contentBlockDelta":{"delta":{"text":"hello"}}}`)
	var body bytes.Buffer
	for i := 0; i < 10000; i++ {
		body.Write(text)
	}
	body.Write(bedrockTestFrame(bedrockTestUsage))
	forwarded := httptest.NewRecorder()
	resp := jsonUpstreamResponse(200, "application/vnd.amazon.eventstream", "", nil)
	resp.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	proxy.copyAndObserveLLM(forwarded, resp, "bedrock", "amazon-bedrock", "agent", "account")
	if !bytes.Equal(forwarded.Body.Bytes(), body.Bytes()) {
		t.Fatal("binary response modified")
	}
	if calls != 1 || got.InputTokens != 40 || got.OutputTokens != 22 || got.CredentialID != "account" || got.Provider != "amazon-bedrock" {
		t.Fatalf("calls=%d usage=%+v", calls, got)
	}
}

func BenchmarkBedrockUsageObserver(b *testing.B) {
	frame := bedrockTestFrame(bedrockTestUsage)
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	var observer bedrockUsageObserver
	for b.Loop() {
		_, _ = observer.Write(frame)
	}
}

func TestBedrockFlushesBeforeUpstreamEOF(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			p := NewProxy(ProxyConfig{})
			if observed {
				p.onLLMCall = func(LLMUsage, QuotaInfo, string, string) {}
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				resp := jsonUpstreamResponse(200, "application/vnd.amazon.eventstream", "", nil)
				resp.Body = reader
				p.copyAndObserveLLM(w, resp, "bedrock", "amazon-bedrock", "agent", "account")
			}))
			defer func() { writer.Close(); reader.Close(); srv.Close() }()
			frame := bedrockTestFrame(bedrockTestUsage)
			go func() { _, _ = writer.Write(frame) }()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("stream waited for EOF: %v", err)
			}
			defer resp.Body.Close()
			got := make([]byte, len(frame))
			if _, err := io.ReadFull(resp.Body, got); err != nil || !bytes.Equal(got, frame) {
				t.Fatalf("first frame: %v", err)
			}
		})
	}
}

func TestBedrockIncompleteUsageIsStillAccounted(t *testing.T) {
	valid := bedrockTestFrame(bedrockTestUsage)
	broken := append([]byte(nil), valid...)
	broken[len(broken)-1] ^= 1
	for name, body := range map[string][]byte{
		"empty-usage":         bedrockTestFrame(`{"usage":{}}`),
		"partial-usage":       bedrockTestFrame(`{"usage":{"inputTokens":40}}`),
		"negative-usage":      bedrockTestFrame(`{"usage":{"inputTokens":-10,"outputTokens":-1}}`),
		"missing":             bedrockTestFrame(`{"contentBlockDelta":{"delta":{"text":"hello"}}}`),
		"damaged":             broken,
		"truncated":           valid[:len(valid)-1],
		"partial-after-usage": append(append([]byte(nil), valid...), broken...),
	} {
		t.Run(name, func(t *testing.T) {
			var got LLMUsage
			calls := 0
			p := NewProxy(ProxyConfig{OnLLMCall: func(u LLMUsage, _ QuotaInfo, mode, _ string) {
				got = u
				calls++
				if mode != "metered" {
					t.Errorf("billing mode=%q", mode)
				}
			}})
			resp := jsonUpstreamResponse(200, "application/vnd.amazon.eventstream", "", nil)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			w := httptest.NewRecorder()
			p.copyAndObserveLLM(w, resp, "bedrock", "amazon-bedrock", "agent", "account", "eu.anthropic.claude-opus-5-5")
			if calls != 1 || !got.UsageIncomplete || got.AgentID != "agent" || got.CredentialID != "account" || got.Model != "eu.anthropic.claude-opus-5-5" {
				t.Fatalf("lost attribution/incomplete usage: calls=%d %+v", calls, got)
			}
			if !bytes.Equal(w.Body.Bytes(), body) {
				t.Fatal("forwarded bytes changed")
			}
			wantInput := int64(0)
			if name == "partial-after-usage" || name == "partial-usage" {
				wantInput = 40
			}
			if got.InputTokens != wantInput {
				t.Fatalf("input=%d want %d", got.InputTokens, wantInput)
			}
		})
	}
}

func TestBedrockJSONUsageCompleteness(t *testing.T) {
	for _, body := range []string{`{"usage":{}}`, `{"usage":{"inputTokens":40}}`, `{"usage":{"outputTokens":1}}`, `{"usage":{"inputTokens":-1,"outputTokens":0}}`} {
		if got := parseLLMUsage("bedrock", body); !got.UsageIncomplete {
			t.Fatalf("partial usage precise: %s %+v", body, got)
		}
	}
	if got := parseLLMUsage("bedrock", `{"usage":{"inputTokens":0,"outputTokens":0}}`); got.UsageIncomplete {
		t.Fatal("explicit zero counters are complete")
	}
}
