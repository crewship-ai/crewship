package resourcelifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type canceledRetentionProbe struct {
	*fakeRetentionRuntime
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (p *canceledRetentionProbe) ListContainers(context.Context) ([]RetentionContainer, error) {
	p.calls.Add(1)
	if p.cancel != nil {
		// Run's 1ms ticker has fired before cancellation, so both select cases
		// are ready when this failed pass returns. A ticker selection must still
		// recheck cancellation before starting any additional provider work.
		timer := time.NewTimer(2 * time.Millisecond)
		<-timer.C
		p.cancel()
	}
	return nil, errors.New("daemon unavailable")
}

func TestRetentionAlreadyCanceledDoesNotStartProviderWork(t *testing.T) {
	for _, run := range []bool{false, true} {
		name := "Tick"
		if run {
			name = "Run"
		}
		t.Run(name, func(t *testing.T) {
			retention, runtime, _ := retentionFixture(t)
			probe := &canceledRetentionProbe{fakeRetentionRuntime: runtime}
			retention.Runtime = probe
			retention.EvictCache = false
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if run {
				retention.Run(ctx)
			} else {
				retention.Tick(ctx)
			}
			if got := probe.calls.Load(); got != 0 {
				t.Fatalf("canceled %s started %d provider calls", name, got)
			}
		})
	}
}

func TestRetentionReadyTickCannotRestartCanceledPass(t *testing.T) {
	retention, runtime, _ := retentionFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	probe := &canceledRetentionProbe{fakeRetentionRuntime: runtime, cancel: cancel}
	retention.Runtime = probe
	retention.EvictCache = false
	retention.Interval = time.Millisecond
	retention.Run(ctx)
	if got := probe.calls.Load(); got != 1 {
		t.Fatalf("ready tick admitted another pass after cancellation: %d calls", got)
	}
}
