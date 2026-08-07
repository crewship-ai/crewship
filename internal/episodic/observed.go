package episodic

import (
	"context"
	"sync"
)

// ObservedEmbedder wraps an Embedder and remembers whether the last call
// actually worked, so health surfaces can answer from evidence instead of
// from configuration.
//
// WHY. /healthz reported episodic recall as "vector" whenever an embedder
// had been CONSTRUCTED — which server.go does as soon as Keeper's Ollama
// URL is set, without ever calling it. On the stage slot on 2026-08-07 the
// Ollama host was up but had no nomic-embed-text model, so every embed
// returned `ollama http 404: model "nomic-embed-text" not found`. That is
// 4032 consecutive index failures in a single day, a vector index that
// stayed empty, and HybridRecall quietly serving BM25 results only — while
// /healthz and `crewship doctor` both reported healthy vector recall and
// no test noticed. "Configured" and "working" had been the same word.
//
// Deliberately NOT a latch. The stage fix was pulling the missing model,
// with no restart; a wrapper that never cleared would then have reported a
// broken subsystem that was working again. Degraded tracks the LAST
// outcome, so recovery is reported as promptly as failure.
type ObservedEmbedder struct {
	Embedder

	mu      sync.RWMutex
	lastErr error
}

// NewObservedEmbedder wraps e. A nil delegate returns a nil wrapper:
// "no embedder configured" is a supported state (sparse-only recall), and
// handing back a non-nil interface holding nothing would make the absence
// read as presence — the same lie in a new place.
func NewObservedEmbedder(e Embedder) *ObservedEmbedder {
	if e == nil {
		return nil
	}
	return &ObservedEmbedder{Embedder: e}
}

// Embed delegates and records the outcome. The delegate's error is
// returned unchanged; callers keep whatever handling they already had.
func (o *ObservedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, err := o.Embedder.Embed(ctx, text)
	o.mu.Lock()
	o.lastErr = err
	o.mu.Unlock()
	return vec, err
}

// Degraded reports whether the most recent embed attempt failed. An
// embedder that has not been called yet is NOT degraded: a fresh boot with
// an empty journal never calls Embed at all, and reporting a fault on no
// evidence is its own false alarm.
func (o *ObservedEmbedder) Degraded() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.lastErr != nil
}

// LastError returns the error from the most recent failed embed, or nil.
// Health surfaces include it because "degraded" without a reason sends the
// operator looking in the wrong place — a missing model, an unreachable
// host and a timeout all present identically otherwise.
func (o *ObservedEmbedder) LastError() error {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.lastErr
}
