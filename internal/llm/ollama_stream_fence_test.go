package llm

import (
	"net/http"
	"testing"
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
