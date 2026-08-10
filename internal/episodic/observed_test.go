package episodic

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
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

func TestObservedEmbedder_CallerCancellationIsNotDegradation(t *testing.T) {
	// The wrapper is also handed to hybrid search, which embeds queries on
	// REQUEST goroutines — so a client that disconnects mid-search cancels
	// the context and the embedder returns context.Canceled through no
	// fault of its own. Recording that would flip /healthz to
	// vector-degraded and make `crewship doctor` exit non-zero on a
	// perfectly healthy embedder: the same "health reports something that
	// isn't true" bug this type exists to remove, pointing the other way.
	stub := &observedStub{dim: 768, name: "nomic-embed-text"}
	obs := NewObservedEmbedder(stub)

	for _, tc := range []struct {
		name    string
		ctxErr  error
		mkCtx   func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "cancelled by the caller",
			mkCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantErr: context.Canceled,
		},
		{
			name: "caller deadline exceeded",
			mkCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				return ctx, cancel
			},
			wantErr: context.DeadlineExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.mkCtx()
			defer cancel()
			stub.setErr(tc.wantErr)

			if _, err := obs.Embed(ctx, "hello"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Embed() error = %v, want the delegate's %v surfaced unchanged", err, tc.wantErr)
			}
			if obs.Degraded() {
				t.Error("a caller-cancelled context must not mark the EMBEDDER degraded")
			}
			if got := obs.LastError(); got != nil {
				t.Errorf("LastError() = %v, want nil — the embedder did not fail", got)
			}
		})
	}

	// And the guard must not swallow a real failure that happens to occur
	// while a live context is in play.
	stub.setErr(errors.New(`ollama http 404: model "nomic-embed-text" not found`))
	if _, err := obs.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("Embed() error = nil, want the delegate's error")
	}
	if !obs.Degraded() {
		t.Error("a genuine embedder failure must still mark degraded")
	}
}

func TestObservedEmbedder_NilReceiverIsNotDegraded(t *testing.T) {
	// server.go only wraps a non-nil embedder, but the accessors guard
	// o == nil and nothing pinned it — a refactor could drop the guard and
	// stay green while introducing a nil-deref on the health path.
	var obs *ObservedEmbedder
	if obs.Degraded() {
		t.Error("a nil wrapper must not report degraded")
	}
	if err := obs.LastError(); err != nil {
		t.Errorf("LastError() = %v, want nil on a nil wrapper", err)
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
