package episodic

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// The bug these pin: /healthz reported episodic recall as "vector"
// whenever an embedder had been CONSTRUCTED, which is true as soon as
// KEEPER_OLLAMA_URL is set. It never asked whether the embedder could
// actually embed anything. On the stage slot the Ollama host was up but
// had no nomic-embed-text model, so every call 404'd — 4032 consecutive
// index failures in one day, an empty vector index, and a health surface
// (plus `crewship doctor`) reporting vector recall the whole time.
//
// ObservedEmbedder is the seam that lets the health surface answer from
// what actually happened rather than from what was configured.

type observedStub struct {
	mu   sync.Mutex
	vec  []float32
	err  error
	dim  int
	name string
	// calls counts delegated Embed calls, so the tests can prove the
	// wrapper is a pass-through and not swallowing work.
	calls int
}

func (s *observedStub) Embed(_ context.Context, _ string) ([]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.vec, nil
}

func (s *observedStub) Dim() int      { return s.dim }
func (s *observedStub) Model() string { return s.name }

func (s *observedStub) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *observedStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestObservedEmbedder_UntriedIsNotDegraded(t *testing.T) {
	// Before anything has been embedded there is no evidence of failure,
	// and reporting degraded on no evidence would be its own false alarm
	// — a fresh boot with an empty journal never calls Embed at all.
	obs := NewObservedEmbedder(&observedStub{dim: 768, name: "nomic-embed-text"})

	if obs.Degraded() {
		t.Fatal("a wrapper that has embedded nothing must not report degraded")
	}
	if got := obs.LastError(); got != nil {
		t.Fatalf("LastError() = %v, want nil before any call", got)
	}
}

func TestObservedEmbedder_RecordsFailureAndRecovery(t *testing.T) {
	wantErr := errors.New(`ollama http 404: model "nomic-embed-text" not found`)
	stub := &observedStub{dim: 768, name: "nomic-embed-text", err: wantErr}
	obs := NewObservedEmbedder(stub)

	if _, err := obs.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed() error = nil, want the delegate's error surfaced unchanged")
	}
	if !obs.Degraded() {
		t.Fatal("after an observed embed failure the wrapper must report degraded")
	}
	if got := obs.LastError(); !errors.Is(got, wantErr) {
		t.Fatalf("LastError() = %v, want %v — the health surface has to say WHY", got, wantErr)
	}

	// Recovery matters as much as detection: on stage the fix was pulling
	// the missing model, with no restart. A latch that never clears would
	// have kept reporting degraded on a subsystem that was working again.
	stub.setErr(nil)
	if _, err := obs.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("Embed() after recovery: unexpected error %v", err)
	}
	if obs.Degraded() {
		t.Fatal("a successful embed must clear degraded, not latch it forever")
	}
	if got := obs.LastError(); got != nil {
		t.Fatalf("LastError() = %v, want nil after recovery", got)
	}
}

func TestObservedEmbedder_IsATransparentPassThrough(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}
	stub := &observedStub{vec: want, dim: 768, name: "nomic-embed-text"}
	obs := NewObservedEmbedder(stub)

	got, err := obs.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed() unexpected error: %v", err)
	}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Embed() = %v, want the delegate's vector %v", got, want)
	}
	if stub.callCount() != 1 {
		t.Fatalf("delegate called %d times, want exactly 1", stub.callCount())
	}
	if obs.Dim() != 768 {
		t.Errorf("Dim() = %d, want the delegate's 768", obs.Dim())
	}
	if obs.Model() != "nomic-embed-text" {
		t.Errorf("Model() = %q, want the delegate's name", obs.Model())
	}
}

func TestObservedEmbedder_NilDelegateStaysNil(t *testing.T) {
	// server.go only wraps when an embedder was actually built. Wrapping
	// nil would turn "no embedder configured" (sparse-only, a supported
	// state) into a non-nil interface that reads as configured — exactly
	// the class of lie this change exists to remove.
	if obs := NewObservedEmbedder(nil); obs != nil {
		t.Fatal("NewObservedEmbedder(nil) must return nil, not a wrapper around nothing")
	}
}

func TestObservedEmbedder_ConcurrentUseIsRaceFree(t *testing.T) {
	// The indexer sweeps on its own goroutine while recall embeds queries
	// on request goroutines, and /healthz reads the state from a third.
	// Run under -race; without synchronisation this is where it shows up.
	stub := &observedStub{vec: []float32{1}, dim: 1, name: "m"}
	obs := NewObservedEmbedder(stub)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if i%2 == 0 {
					stub.setErr(errors.New("boom"))
				} else {
					stub.setErr(nil)
				}
				_, _ = obs.Embed(context.Background(), "x")
				_ = obs.Degraded()
				_ = obs.LastError()
			}
		}(i)
	}
	wg.Wait()
}
