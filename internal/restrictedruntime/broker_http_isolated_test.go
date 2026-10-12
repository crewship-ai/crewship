//go:build linux && restrictedruntime_live

package restrictedruntime

// These tests run only in an owned network-none container: the production
// broker binds a fixed loopback port. They must not bind that port on the host.
// Build the tagged test binary with coverage, mount it read-only, then run
// -test.run=TestIsolatedHTTPBroker with CREWSHIP_BROKER_ISOLATED_TEST=1.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func isolatedBrokerConfig() brokerFrame {
	return brokerFrame{Kind: "configure", Token: strings.Repeat("a", 64), Limits: map[string]int64{"echo": 16}}
}

func requireIsolatedBroker(t *testing.T) {
	t.Helper()
	if os.Getenv("CREWSHIP_BROKER_ISOLATED_TEST") != "1" {
		t.Fatal("requires owned network-none container; never bind the production broker port on the host")
	}
}

func startIsolatedBroker(t *testing.T, cfg brokerFrame) (*io.PipeWriter, *io.PipeReader) {
	t.Helper()
	requireIsolatedBroker(t)
	input, relay := io.Pipe()
	output, brokerOut := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	go func() {
		err := RunHTTPBroker(ctx, input, brokerOut)
		_ = brokerOut.CloseWithError(err)
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		_ = relay.Close()
		_ = output.Close()
		_ = brokerOut.Close()
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("broker did not stop")
		}
	})
	sent := make(chan error, 1)
	go func() { sent <- writeBrokerFrame(relay, cfg) }()
	var ready brokerFrame
	if err := readBrokerFrame(output, &ready); err != nil || ready.Kind != "ready" {
		t.Fatalf("readiness = %+v, %v", ready, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	return relay, output
}

func TestIsolatedHTTPBrokerRejectsInvalidConfiguration(t *testing.T) {
	requireIsolatedBroker(t)
	for _, tc := range []struct {
		name string
		edit func(*brokerFrame)
	}{
		{"wrong kind", func(c *brokerFrame) { c.Kind = "request" }},
		{"short token", func(c *brokerFrame) { c.Token = "a" }},
		{"no operations", func(c *brokerFrame) { c.Limits = nil }},
		{"too many operations", func(c *brokerFrame) {
			c.Limits = map[string]int64{}
			for i := 0; i < 17; i++ {
				c.Limits[strings.Repeat("a", i+1)] = 1
			}
		}},
		{"invalid operation", func(c *brokerFrame) { c.Limits = map[string]int64{"../echo": 1} }},
		{"negative bound", func(c *brokerFrame) { c.Limits["echo"] = -1 }},
		{"oversized bound", func(c *brokerFrame) { c.Limits["echo"] = 1<<20 + 1 }},
		{"unknown stream", func(c *brokerFrame) { c.Streams = map[string]int64{"missing": 1} }},
		{"zero stream", func(c *brokerFrame) { c.Streams = map[string]int64{"echo": 0} }},
		{"large stream", func(c *brokerFrame) { c.Streams = map[string]int64{"echo": 1<<20 + 1} }},
		{"responses without grant", func(c *brokerFrame) { c.ResponsesOperation = "missing" }},
		{"responses without stream", func(c *brokerFrame) { c.ResponsesOperation = "echo" }},
		{"buffered mismatch", func(c *brokerFrame) { c.BufferedOperation = "echo" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := isolatedBrokerConfig()
			tc.edit(&cfg)
			var in bytes.Buffer
			if err := writeBrokerFrame(&in, cfg); err != nil {
				t.Fatal(err)
			}
			if err := RunHTTPBroker(t.Context(), &in, io.Discard); !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid configuration accepted: %v", err)
			}
		})
	}
	if err := RunHTTPBroker(t.Context(), strings.NewReader(""), io.Discard); !errors.Is(err, io.EOF) {
		t.Fatalf("missing frame = %v", err)
	}
	listener, err := net.Listen("tcp4", brokerAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var in bytes.Buffer
	if err := writeBrokerFrame(&in, isolatedBrokerConfig()); err != nil {
		t.Fatal(err)
	}
	if err := RunHTTPBroker(t.Context(), &in, io.Discard); err == nil {
		t.Fatal("occupied listener accepted")
	}
}

func TestIsolatedHTTPBrokerRejectsUnboundedOrUnauthenticatedRequests(t *testing.T) {
	cfg := isolatedBrokerConfig()
	startIsolatedBroker(t, cfg)
	client := &http.Client{Timeout: 3 * time.Second}
	for _, tc := range []struct {
		name, method, path, body, header, value string
		status                                  int
	}{
		{"method", "GET", "/v1/operations/echo", "", "", "", 403},
		{"query", "POST", "/v1/operations/echo?q=x", "", "", "", 403},
		{"empty query", "POST", "/v1/operations/echo?", "", "", "", 403},
		{"encoded path", "POST", "/v1/operations/%65cho", "", "", "", 403},
		{"upgrade", "POST", "/v1/operations/echo", "", "Upgrade", "websocket", 403},
		{"compressed body", "POST", "/v1/operations/echo", "", "Content-Encoding", "gzip", 403},
		{"token", "POST", "/v1/operations/echo", "", "Authorization", "Bearer wrong", 403},
		{"unknown operation", "POST", "/v1/operations/missing", "", "", "", 403},
		{"unconfigured responses", "POST", "/v1/responses", "", "", "", 403},
		{"unbounded body", "POST", "/v1/operations/echo", strings.Repeat("x", 17), "", "", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tc.method, "http://"+brokerAddress+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
			if tc.header != "" {
				req.Header.Set(tc.header, tc.value)
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
		})
	}
}

func TestIsolatedHTTPBrokerForwardsBoundedResponsesAndStreams(t *testing.T) {
	for _, mode := range []string{"response", "stream", "buffered"} {
		t.Run(mode, func(t *testing.T) {
			cfg := isolatedBrokerConfig()
			path := "/v1/operations/echo"
			if mode != "response" {
				cfg.Streams = map[string]int64{"echo": 64}
				cfg.ResponsesOperation = "echo"
				path = "/v1/responses"
			}
			if mode == "buffered" {
				cfg.BufferedOperation = "echo"
			}
			relay, output := startIsolatedBroker(t, cfg)
			frames := []brokerFrame{{Kind: "response", Status: 201, Body: []byte("accepted")}}
			if mode != "response" {
				frames = []brokerFrame{{Kind: "stream_start", Status: 200}, {Kind: "stream_chunk", Body: []byte("data: first\n\n")}, {Kind: "stream_chunk", Body: []byte("data: last\n\n")}, {Kind: "stream_end", Status: 200}}
			}
			echoed := make(chan brokerFrame, 1)
			relayDone := make(chan error, 1)
			go func() {
				var request brokerFrame
				if err := readBrokerFrame(output, &request); err != nil {
					relayDone <- err
					return
				}
				echoed <- request
				for _, f := range frames {
					if err := writeBrokerFrame(relay, f); err != nil {
						relayDone <- err
						return
					}
				}
				relayDone <- nil
			}()
			req, _ := http.NewRequestWithContext(t.Context(), "POST", "http://"+brokerAddress+path, strings.NewReader("payload"))
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
			res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "response" {
				if res.StatusCode != 201 || string(body) != "accepted" {
					t.Fatalf("response = %d %q", res.StatusCode, body)
				}
			} else if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" || string(body) != "data: first\n\ndata: last\n\n" {
				t.Fatalf("stream = %d %q", res.StatusCode, body)
			}
			if err := <-relayDone; err != nil {
				t.Fatal(err)
			}
			request := <-echoed
			if request.Kind != "request" || request.Operation != "echo" || request.Token != cfg.Token || string(request.Body) != "payload" {
				t.Fatalf("relay identity changed: %+v", request)
			}
		})
	}
}

func TestIsolatedHTTPBrokerAbortsInvalidRelayFrames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []brokerFrame
	}{
		{"unknown reply", []brokerFrame{{Kind: "unknown"}}},
		{"invalid status", []brokerFrame{{Kind: "response", Status: 199}}},
		{"large reply", []brokerFrame{{Kind: "response", Status: 200, Body: bytes.Repeat([]byte("x"), 1<<20+1)}}},
		{"chunk before start", []brokerFrame{{Kind: "stream_chunk", Body: []byte("x")}}},
		{"invalid start", []brokerFrame{{Kind: "stream_start", Status: 201}}},
		{"duplicate start", []brokerFrame{{Kind: "stream_start", Status: 200}, {Kind: "stream_start", Status: 200}}},
		{"empty chunk", []brokerFrame{{Kind: "stream_start", Status: 200}, {Kind: "stream_chunk"}}},
		{"oversized stream", []brokerFrame{{Kind: "stream_start", Status: 200}, {Kind: "stream_chunk", Body: bytes.Repeat([]byte("x"), 65)}}},
		{"response during stream", []brokerFrame{{Kind: "stream_start", Status: 200}, {Kind: "response", Status: 200}}},
		{"invalid end", []brokerFrame{{Kind: "stream_start", Status: 200}, {Kind: "stream_end", Status: 500}}},
		{"end without start", []brokerFrame{{Kind: "stream_end", Status: 200}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := isolatedBrokerConfig()
			cfg.Streams = map[string]int64{"echo": 64}
			relay, output := startIsolatedBroker(t, cfg)
			done := make(chan struct{})
			go func() {
				defer close(done)
				var request brokerFrame
				if readBrokerFrame(output, &request) != nil {
					return
				}
				for _, f := range tc.frames {
					if writeBrokerFrame(relay, f) != nil {
						return
					}
				}
			}()
			req, _ := http.NewRequestWithContext(t.Context(), "POST", "http://"+brokerAddress+"/v1/operations/echo", nil)
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
			res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			if err == nil {
				_, err = io.ReadAll(res.Body)
				_ = res.Body.Close()
			}
			if err == nil {
				t.Fatal("invalid relay completed a successful response")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("relay did not stop")
			}
		})
	}
}

func TestIsolatedHTTPBrokerSerializesRequestsAndRejectsConcurrentWork(t *testing.T) {
	cfg := isolatedBrokerConfig()
	relay, output := startIsolatedBroker(t, cfg)
	client := &http.Client{Timeout: 5 * time.Second}
	request := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+brokerAddress+"/v1/operations/echo", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		return client.Do(req)
	}
	firstDone := make(chan error, 1)
	go func() {
		res, err := request()
		if err == nil {
			_, err = io.ReadAll(res.Body)
			_ = res.Body.Close()
		}
		firstDone <- err
	}()
	var admitted brokerFrame
	if err := readBrokerFrame(output, &admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.Kind != "request" {
		t.Fatalf("first request not admitted: %+v", admitted)
	}
	second, err := request()
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("concurrent work admitted: %d", second.StatusCode)
	}
	if err := writeBrokerFrame(relay, brokerFrame{Kind: "response", Status: 200, Body: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first admitted request failed: %v", err)
	}
}

func TestIsolatedHTTPBrokerRelayDisconnectAbortsPendingRequest(t *testing.T) {
	cfg := isolatedBrokerConfig()
	relay, output := startIsolatedBroker(t, cfg)
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", "http://"+brokerAddress+"/v1/operations/echo", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err == nil {
			_, err = io.ReadAll(res.Body)
			_ = res.Body.Close()
		}
		done <- err
	}()
	var admitted brokerFrame
	if err := readBrokerFrame(output, &admitted); err != nil {
		t.Fatal(err)
	}
	if err := relay.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("relay disconnect presented as a successful response")
	}
}
