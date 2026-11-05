package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A guarded provider must be guarded on every method it exposes.
//
// NewOllamaWithClient used to apply the caller's client to Complete and
// ListModels only; Stream kept the default transport, so a fenced provider had an
// unfenced dial on it. That held together because no caller streamed — a property
// of the callers, not of the object, and the next one to stream would have had no
// way to know it had lost the fence it asked for.
func TestNewOllamaWithClient_FencesTheStreamingPathToo(t *testing.T) {
	marker := &markerTransport{}
	o := NewOllamaWithClient("http://127.0.0.1:11434", "m", &http.Client{Transport: marker})

	if o.client.Transport != http.RoundTripper(marker) {
		t.Error("the supplied transport did not reach the non-streaming client")
	}
	if o.stream == nil || o.stream.Transport != http.RoundTripper(marker) {
		t.Error("the supplied transport did not reach the streaming client — Stream would dial unfenced")
	}
	// The caller's 300s-style request timeout must NOT ride along: it is right
	// for one Complete and wrong for a stream that stays open.
	if o.stream.Timeout != 0 {
		t.Errorf("streaming client inherited a %s request timeout", o.stream.Timeout)
	}
}

// Exercise the HTTP boundary: preserving a Transport alone does not preserve a
// client's redirect policy. Both constructor paths (nil and supplied Transport)
// must consult the caller's policy before contacting a second server.
func TestNewOllamaWithClient_PreservesRedirectPolicy(t *testing.T) {
	for _, transport := range []bool{false, true} {
		for _, method := range []string{"Complete", "Stream"} {
			for _, policy := range []string{"deny", "allow", "default"} {
				name := method + "/" + policy + "/default-transport"
				if transport {
					name = method + "/" + policy + "/supplied-transport"
				}
				t.Run(name, func(t *testing.T) {
					var targetCalls, policyCalls atomic.Int64
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						targetCalls.Add(1)
						_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
					}))
					defer target.Close()
					origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
					}))
					defer origin.Close()
					client := &http.Client{Timeout: 5 * time.Second}
					if transport {
						client.Transport = http.DefaultTransport.(*http.Transport).Clone()
					}
					defer client.CloseIdleConnections()
					denied := errors.New("caller refused redirect")
					if policy != "default" {
						client.CheckRedirect = func(*http.Request, []*http.Request) error {
							policyCalls.Add(1)
							if policy == "deny" {
								return denied
							}
							return nil
						}
					}
					provider := NewOllamaWithClient(origin.URL, "m", client)
					defer provider.stream.CloseIdleConnections()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					request := Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
					var err error
					if method == "Complete" {
						_, err = provider.Complete(ctx, request)
					} else {
						_, err = provider.Stream(ctx, request, func(StreamEvent) error { return nil })
					}
					if policy == "deny" {
						if !errors.Is(err, denied) {
							t.Errorf("error = %v, want supplied redirect refusal", err)
						}
						if got := targetCalls.Load(); got != 0 {
							t.Errorf("redirect target received %d requests, want 0", got)
						}
					} else {
						if err != nil {
							t.Errorf("allowed request: %v", err)
						}
						if got := targetCalls.Load(); got != 1 {
							t.Errorf("redirect target received %d requests, want 1", got)
						}
					}
					wantPolicyCalls := int64(1)
					if policy == "default" {
						wantPolicyCalls = 0
					}
					if got := policyCalls.Load(); got != wantPolicyCalls {
						t.Errorf("redirect policy called %d times, want %d", got, wantPolicyCalls)
					}
				})
			}
		}
	}
}

// A nil client keeps the shipped defaults, so the many callers that pass one
// behave exactly as before.
func TestNewOllamaWithClient_NilKeepsDefaults(t *testing.T) {
	o := NewOllamaWithClient("http://127.0.0.1:11434", "m", nil)
	if o.client == nil || o.stream == nil {
		t.Fatal("nil client left the provider without one")
	}
	if o.stream.Transport == nil {
		t.Error("the default streaming transport was dropped")
	}
}

type markerTransport struct{}

func (markerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrUseLastResponse
}
