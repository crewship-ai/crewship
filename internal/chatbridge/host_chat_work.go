package chatbridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type hostChatAdmissionKey struct{}

// WithHostChatWorkAdmission marks a host-validated ordinary human turn. It is
// never populated from WebSocket/model metadata or a container credential.
func WithHostChatWorkAdmission(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostChatAdmissionKey{}, true)
}

type hostChatProofKey struct{}
type hostChatProof struct {
	Phase        string `json:"phase"`
	ObservedExit *int   `json:"observed_exit,omitempty"`
	ExecRefused  bool   `json:"exec_refused,omitempty"`
}

type hostChatLifecycle interface {
	takeHostChatWork(string) bool
	hostChatPhase(context.Context, string, string) error
}

func (r *IPCResolver) takeHostChatWork(runID string) bool {
	_, ok := r.hostChatWork.LoadAndDelete(runID)
	return ok
}
func (r *IPCResolver) hostChatPhase(ctx context.Context, runID, phase string) error {
	ctx = context.WithValue(ctx, hostChatProofKey{}, &hostChatProof{Phase: phase})
	return r.UpdateRun(ctx, runID, "RUNNING", nil, nil, nil)
}

type hostChatTracker struct {
	stop         chan struct{}
	done         chan struct{}
	once         sync.Once
	mu           sync.Mutex
	observedExit *int
}

func (b *Bridge) bindHostChatWork(ctx context.Context, req *orchestrator.AgentRunRequest, runID string) *hostChatTracker {
	lifecycle, ok := b.resolver.(hostChatLifecycle)
	if !ok || !lifecycle.takeHostChatWork(runID) {
		return nil
	}
	tracker := &hostChatTracker{stop: make(chan struct{}), done: make(chan struct{})}
	started := make(chan struct{}, 1)
	priorGate := req.ExecGate
	req.ExecGate = func(gateCtx context.Context) error {
		if priorGate != nil {
			if err := priorGate(gateCtx); err != nil {
				return err
			}
		}
		return lifecycle.hostChatPhase(gateCtx, runID, "request")
	}
	priorStarted := req.OnExecStarted
	req.OnExecStarted = func(execID string) {
		select {
		case started <- struct{}{}:
		default:
		}
		if priorStarted != nil {
			priorStarted(execID)
		}
	}
	priorTerminal := req.OnExecTerminal
	req.OnExecTerminal = func(code int) {
		tracker.mu.Lock()
		copy := code
		tracker.observedExit = &copy
		tracker.mu.Unlock()
		if priorTerminal != nil {
			priorTerminal(code)
		}
	}
	// The run owns this goroutine and joins it before settlement. Nothing is
	// detached from the generation's lifetime or allowed to create another run.
	go func() {
		defer close(tracker.done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			phase := "heartbeat"
			select {
			case <-tracker.stop:
				return
			case <-started:
				phase = "started"
			case <-ticker.C:
			}
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := lifecycle.hostChatPhase(callCtx, runID, phase)
			cancel()
			if err != nil {
				b.logger.Warn("host chat lifecycle write refused; retaining durable ownership", "run_id", runID, "phase", phase, "error", err)
			}
		}
	}()
	return tracker
}
func (t *hostChatTracker) Stop() {
	if t != nil {
		t.once.Do(func() { close(t.stop); <-t.done })
	}
}
func (t *hostChatTracker) completionContext(ctx context.Context, runErr error) context.Context {
	if t == nil {
		return ctx
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	proof := &hostChatProof{Phase: "finish", ExecRefused: errors.Is(runErr, orchestrator.ErrExecRefused)}
	if t.observedExit != nil {
		copy := *t.observedExit
		proof.ObservedExit = &copy
	}
	return context.WithValue(ctx, hostChatProofKey{}, proof)
}
