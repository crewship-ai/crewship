//go:build linux

package restrictedruntime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func streamingPlan() Plan {
	p := connectedPlan()
	p.Profile = "brokered-http-v2"
	p.Network.Version = 2
	p.Network.Grants[0].ResponseMode = "sse"
	return p
}

type segmentedStream struct{ chunks []string }

func (r *segmentedStream) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if r.chunks[0] == "" {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestStreamGrantIsExplicitAndCannotWiden(t *testing.T) {
	p := streamingPlan()
	p.Network.Grants[0].TimeoutMillis = 300000
	if err := p.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Plan)
	}{
		{"legacy profile", func(p *Plan) { p.Profile = "brokered-http-v1"; p.Network.Version = 1 }},
		{"wrong version", func(p *Plan) { p.Network.Version = 1 }},
		{"unknown mode", func(p *Plan) { p.Network.Grants[0].ResponseMode = "websocket" }},
		{"unbounded time", func(p *Plan) { p.Network.Grants[0].TimeoutMillis = 300001 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := streamingPlan()
			tc.edit(&p)
			if err := p.validate(time.Now()); err == nil {
				t.Fatal("invalid stream grant accepted")
			}
		})
	}
	parent, child := streamingPlan(), streamingPlan()
	parent.Network.Grants[0].ResponseMode = ""
	if err := Narrow(parent, child); err == nil {
		t.Fatal("delegation added streaming")
	}
}

func TestStreamRedactsSplitSecretsAndBoundsOutput(t *testing.T) {
	for _, tc := range []struct {
		name, secret, want string
		chunks             []string
		limit              int64
		failed             bool
	}{
		{"split", "synthetic-secret", "data: [REDACTED]\n\n", []string{"data: synt", "hetic-", "secret\n\n"}, 1024, false},
		{"overlap", "aaaa", "[REDACTED][REDACTED_PARTIAL]", []string{"aa", "aa", "a"}, 1024, false},
		{"partial suffix", "synthetic-secret", "data: [REDACTED_PARTIAL]", []string{"data: synthe"}, 1024, false},
		{"raw limit", "", "", []string{"12345"}, 4, true},
		{"redaction expansion limit", "secret", "", []string{"secret"}, 6, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := streamingPlan()
			s, _ := brokerTestSession(t, p)
			g := p.Network.Grants[0]
			g.MaxResponse = tc.limit
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}, Body: io.NopCloser(&segmentedStream{chunks: tc.chunks}), ContentLength: -1}
			var output strings.Builder
			started := false
			end := s.brokerStream(t.Context(), resp, g, BoundSecret{Value: tc.secret, Expires: time.Now().Add(time.Minute)}, func(f brokerFrame) error {
				if f.Kind == "stream_start" {
					started = true
				} else if f.Kind != "stream_chunk" || !started {
					t.Fatalf("bad stream order: %+v", f)
				}
				output.Write(f.Body)
				return nil
			})
			if !started || (end.Kind == "stream_error") != tc.failed || output.String() != tc.want {
				t.Fatalf("stream result kind=%s output=%q want=%q", end.Kind, output.String(), tc.want)
			}
			if tc.secret != "" && strings.Contains(output.String(), tc.secret) {
				t.Fatal("literal secret released")
			}
		})
	}
}

func TestActualTLSStreamRevocationBeforeNextChunk(t *testing.T) {
	p := streamingPlan()
	p.Network.Grants[0].URL = "https://example.com/stream"
	s, a := brokerTestSession(t, p)
	first := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: authorized\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-first:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: REVOKED_CANARY\n\n")
	}))
	defer server.Close()
	var output strings.Builder
	revoked := false
	end := s.brokerExchange(context.Background(), "token", brokerFrame{Kind: "request", Token: "token", Operation: "echo"}, *syntheticBrokerTransport(t, server), func(f brokerFrame) error {
		if f.Kind == "stream_chunk" {
			output.Write(f.Body)
			if !revoked && strings.Contains(output.String(), "authorized") {
				revoked = true
				a.revoke("h")
				close(first)
			}
		}
		return nil
	})
	if !strings.Contains(output.String(), "authorized") || strings.Contains(output.String(), "REVOKED_CANARY") || end.Kind != "stream_error" {
		t.Fatalf("revoked stream delivered data: kind=%s output=%q", end.Kind, output.String())
	}
}
