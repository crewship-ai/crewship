package chatbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// hostChatCancelPollInterval matches the dispatcher's CancelPollInterval
// default: a work-item cancel reaches the owning host within this bound.
const hostChatCancelPollInterval = 2 * time.Second

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
	hostChatCancelRequested(context.Context, string) (bool, error)
}

func (r *IPCResolver) takeHostChatWork(runID string) bool {
	_, ok := r.hostChatWork.LoadAndDelete(runID)
	return ok
}
func (r *IPCResolver) hostChatPhase(ctx context.Context, runID, phase string) error {
	ctx = context.WithValue(ctx, hostChatProofKey{}, &hostChatProof{Phase: phase})
	return r.UpdateRun(ctx, runID, "RUNNING", nil, nil, nil)
}

// hostChatCancelRequested asks the API whether a cancel was recorded for this
// host-owned attempt (POST /work-items/{id}/cancel). The host is the only
// owner of these attempts, so nothing else can deliver that request (#4799).
func (r *IPCResolver) hostChatCancelRequested(ctx context.Context, runID string) (bool, error) {
	body, err := json.Marshal(map[string]any{"status": "RUNNING", "host_chat_proof": &hostChatProof{Phase: "poll"}})
	if err != nil {
		return false, err
	}
	reqURL := fmt.Sprintf("%s/api/v1/internal/runs/%s", r.baseURL, url.PathEscape(runID))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, reqURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return false, fmt.Errorf("host chat cancel poll: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("host chat cancel poll: server returned %d", resp.StatusCode)
	}
	var reply struct {
		CancelRequested bool `json:"cancel_requested"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8*1024)).Decode(&reply); err != nil {
		return false, fmt.Errorf("host chat cancel poll: %w", err)
	}
	return reply.CancelRequested, nil
}

type hostChatTracker struct {
	stop         chan struct{}
	done         chan struct{}
	once         sync.Once
	mu           sync.Mutex
	observedExit *int
	cancelRun    context.CancelCauseFunc

	// fence orders stop delivery against completion: once the run has
	// returned, a poll answer still in flight may no longer cancel it.
	fence     sync.Mutex
	finished  bool
	delivered bool
}

// deliverStop cancels the run unless it has already returned.
func (t *hostChatTracker) deliverStop() bool {
	t.fence.Lock()
	defer t.fence.Unlock()
	if t.finished {
		return false
	}
	t.delivered = true
	t.cancelRun(ws.ErrExplicitStopRequested)
	return true
}

// StopDelivered reports whether a work-item cancel was delivered to the run
// before it returned. Only then may settlement treat the run as stopped.
func (t *hostChatTracker) StopDelivered() bool {
	if t == nil {
		return false
	}
	t.fence.Lock()
	defer t.fence.Unlock()
	return t.delivered
}

// bindHostChatWork returns the context the run must execute under. A cancel
// recorded through the work-item endpoint is delivered by cancelling it with
// the same explicit-stop cause a chat Stop uses, so the orchestrator stops the
// process and the turn settles from its observed exit like any other stop.
func (b *Bridge) bindHostChatWork(ctx context.Context, req *orchestrator.AgentRunRequest, runID string) (context.Context, *hostChatTracker) {
	lifecycle, ok := b.resolver.(hostChatLifecycle)
	if !ok || !lifecycle.takeHostChatWork(runID) {
		return ctx, nil
	}
	runCtx, cancelRun := context.WithCancelCause(ctx)
	tracker := &hostChatTracker{stop: make(chan struct{}), done: make(chan struct{}), cancelRun: cancelRun}
	stopIfCancelRequested := func() {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		requested, err := lifecycle.hostChatCancelRequested(callCtx, runID)
		cancel()
		if err != nil {
			b.logger.Warn("host chat cancel poll failed; retaining durable ownership", "run_id", runID, "error", err)
			return
		}
		if requested && tracker.deliverStop() {
			b.logger.Info("work-item cancel requested; stopping host chat execution", "run_id", runID)
		}
	}
	started := make(chan struct{}, 1)
	priorGate := req.ExecGate
	req.ExecGate = func(gateCtx context.Context) error {
		if priorGate != nil {
			if err := priorGate(gateCtx); err != nil {
				return err
			}
		}
		err := lifecycle.hostChatPhase(gateCtx, runID, "request")
		if err != nil {
			// The API refuses creation once a cancel is recorded; settle that
			// refusal as the requested stop rather than as a failure.
			stopIfCancelRequested()
		}
		return err
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
		cancelPoll := time.NewTicker(hostChatCancelPollInterval)
		defer cancelPoll.Stop()
		for {
			phase := "heartbeat"
			select {
			case <-tracker.stop:
				return
			case <-cancelPoll.C:
				if runCtx.Err() == nil {
					stopIfCancelRequested()
				}
				continue
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
	return runCtx, tracker
}

// Stop fences stop delivery and ends the lifecycle loop. Call it as soon as
// the run returns: a poll answer arriving afterwards cannot cancel the run.
func (t *hostChatTracker) Stop() {
	if t != nil {
		t.fence.Lock()
		t.finished = true
		t.fence.Unlock()
		t.once.Do(func() { close(t.stop); <-t.done })
	}
}

// Close stops the loop and releases the run context once the turn is settled.
func (t *hostChatTracker) Close() {
	if t != nil {
		t.Stop()
		t.cancelRun(nil)
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
