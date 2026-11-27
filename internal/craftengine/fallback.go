package craftengine

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// RuntimeStatus says which runtime really served the engine commands, so a
// quiet fallback to the in-process engine is visible to an administrator.
type RuntimeStatus struct {
	// Runtime is what served the last run: RuntimeProcess or RuntimeWASM.
	Runtime string
	// Degraded is true when the worker process was wanted but the in-process
	// engine runs instead (it is slower and cannot be killed mid-run).
	Degraded bool
	// Reason is a short, generic cause (no paths or engine output).
	Reason string
	// FallbackRuns counts runs the in-process engine served instead of a worker.
	FallbackRuns int64
}

// StatusReporter is implemented by engines that can say what really runs.
type StatusReporter interface{ Status() RuntimeStatus }

// Fallback runs on primary and, only when primary says the engine cannot run
// at all here (ErrUnavailable: no spawnable binary, no writable temp directory),
// on secondary. A run that started and failed (ErrFailed) is never retried. The
// first fallback is logged once through warn, and Status keeps the facts.
type Fallback struct {
	primary, secondary Engine
	warn               func(reason string)

	mu       sync.Mutex
	last     string
	reason   string
	degraded bool
	runs     atomic.Int64
	warned   sync.Once
}

// NewFallback combines two engines of the same application and module digest.
func NewFallback(primary, secondary Engine, warn func(reason string)) (*Fallback, error) {
	if secondary == nil || primary != nil && (primary.Application() != secondary.Application() || primary.SHA256() != secondary.SHA256()) {
		return nil, ErrUnavailable
	}
	return &Fallback{primary: primary, secondary: secondary, warn: warn}, nil
}

// NewDegraded is the in-process engine standing in for a worker that could not
// be used at all (reason says why, generically); it warns on first use.
func NewDegraded(secondary Engine, reason string, warn func(reason string)) (*Fallback, error) {
	f, err := NewFallback(nil, secondary, warn)
	if err != nil {
		return nil, err
	}
	f.degraded, f.reason = true, reason
	return f, nil
}

func (f *Fallback) primaryIfAvailable() Engine {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.degraded {
		return nil
	}
	return f.primary
}

func (f *Fallback) Application() string { return f.secondary.Application() }
func (f *Fallback) SHA256() string      { return f.secondary.SHA256() }
func (f *Fallback) Root(dir string) string {
	return f.secondary.Root(dir)
}

// Runtime is the runtime that served the last run, or the intended one before any run.
func (f *Fallback) Runtime() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last != "" {
		return f.last
	}
	if f.primary != nil && !f.degraded {
		return f.primary.Runtime()
	}
	return f.secondary.Runtime()
}

// Status reports what really ran.
func (f *Fallback) Status() RuntimeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	runtime := f.last
	if runtime == "" {
		runtime = f.secondary.Runtime()
		if f.primary != nil && !f.degraded {
			runtime = f.primary.Runtime()
		}
	}
	return RuntimeStatus{Runtime: runtime, Degraded: f.degraded, Reason: f.reason, FallbackRuns: f.runs.Load()}
}

func (f *Fallback) note(runtime string, degraded bool, reason string) {
	f.mu.Lock()
	f.last = runtime
	if degraded {
		f.degraded, f.reason = true, reason
	}
	f.mu.Unlock()
}

func (f *Fallback) Run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	if primary := f.primaryIfAvailable(); primary != nil {
		err := primary.Run(ctx, dir, args, stdout, stderr)
		if err == nil || !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrFailed) {
			f.note(primary.Runtime(), false, "")
			return err
		}
		f.note(f.secondary.Runtime(), true, "the worker process could not start")
	}
	f.runs.Add(1)
	f.warned.Do(func() {
		if f.warn != nil {
			f.mu.Lock()
			reason := f.reason
			f.mu.Unlock()
			f.warn(reason)
		}
	})
	f.note(f.secondary.Runtime(), true, f.reasonOrDefault())
	return f.secondary.Run(ctx, dir, args, stdout, stderr)
}

func (f *Fallback) reasonOrDefault() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reason == "" {
		return "the worker process could not start"
	}
	return f.reason
}
