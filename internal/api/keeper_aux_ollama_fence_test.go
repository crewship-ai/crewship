package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
)

// The aux slots dial the runtime-settable judge endpoint, so a redirect from it
// must not be followed, on Complete or on the stream.
func TestAuxOllamaDoesNotFollowEndpointRedirects(t *testing.T) {
	var bounced atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { bounced.Add(1) }))
	t.Cleanup(target.Close)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/latest/meta-data", http.StatusFound)
	}))
	t.Cleanup(endpoint.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider, err := buildAuxWithCredential(context.Background(), llm.AuxModel{Provider: "ollama", Model: "qa"}, endpoint.URL, "", nil, logger)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req := llm.Request{Messages: []llm.Message{{Role: "user", Content: "ping"}}}
	if _, err := provider.Complete(context.Background(), req); err == nil {
		t.Error("a redirect must not read as a completion")
	}
	if got := bounced.Load(); got != 0 {
		t.Fatalf("the redirect was followed %d time(s)", got)
	}
}

// The keeper's own local model server stays reachable: only the hard tier is blocked.
func TestAuxOllamaStillReachesALoopbackEndpoint(t *testing.T) {
	var reached atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"pong"},"done":true}`))
	}))
	t.Cleanup(endpoint.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider, err := buildAuxWithCredential(context.Background(), llm.AuxModel{Provider: "ollama", Model: "qa"}, endpoint.URL, "", nil, logger)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := provider.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Content: "ping"}}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if reached.Load() == 0 {
		t.Fatal("the loopback endpoint was not reached")
	}
}
