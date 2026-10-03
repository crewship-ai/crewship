package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ErrAgentStopped identifies a SIGTERM exit following an explicit agent stop.
// It does not turn an unrelated failure or a successful late completion into
// cancellation, and is never used as proof that a process has stopped.
var ErrAgentStopped = errors.New("agent stopped by user")

func (o *Orchestrator) agentStopRequested(runID string) bool {
	v, ok := o.agentRuns.Load(runID)
	if !ok {
		return false
	}
	c := v.(*agentRunControl)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// agentRunControl is process-local ownership of a RunAgent invocation. Stop
// closes its creation gate before signalling; disconnecting a stream alone is
// never reported as a stopped process.
type agentRunControl struct {
	mu                sync.Mutex
	agentID           string
	location          RunLocation
	cancel            context.CancelFunc
	stopped, creating bool
	done              chan struct{}
}

func (o *Orchestrator) trackAgentRun(ctx context.Context, req *AgentRunRequest) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	c := &agentRunControl{agentID: req.AgentID, location: RunLocation{ContainerID: req.ContainerID, AgentSlug: req.AgentSlug, RunID: req.RunID}, cancel: cancel, done: make(chan struct{})}
	prior := req.ExecGate
	live := o.agentLiveness()
	agentID := req.AgentID
	req.ExecGate = func(ctx context.Context) error {
		c.mu.Lock()
		stopped := c.stopped
		c.mu.Unlock()
		if stopped {
			return context.Canceled
		}
		if live != nil && agentID != "" {
			if err := live(ctx, agentID); err != nil {
				return err
			}
		}
		// A caller's durable gate can block on storage. Never hold the stop
		// mutex across it: a stop must be able to cancel preparation.
		if prior != nil {
			if err := prior(ctx); err != nil {
				return err
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.stopped {
			return context.Canceled
		}
		c.creating = true
		return nil
	}

	o.agentRuns.Store(req.RunID, c)
	return ctx, func() { cancel(); close(c.done); o.agentRuns.Delete(req.RunID) }
}

// StopAgent stops current invocations and durable running records recovered
// after a server restart. New submissions remain a separate admission decision. It waits
// for both runtime absence and RunAgent settlement; an unresponsive provider
// or creation that outlives the deadline is an error, not STOPPED.
func (o *Orchestrator) StopAgent(ctx context.Context, agentID string) error {
	var runs []*agentRunControl
	o.agentRuns.Range(func(_, v any) bool {
		c := v.(*agentRunControl)
		if c.agentID == agentID {
			runs = append(runs, c)
		}
		return true
	})
	// Close every gate before cancelling preparation (which can wake callers).
	creating := make([]bool, len(runs))
	for i, c := range runs {
		c.mu.Lock()
		c.stopped = true
		creating[i] = c.creating
		c.mu.Unlock()
	}
	for i, c := range runs {
		if !creating[i] {
			c.cancel()
		}
	}
	// Ownership inspection may block or fail. Known invocations still receive
	// their stop independently, and one slow provider cannot starve its siblings.
	stopResults := make(chan error, len(runs))
	for i, c := range runs {
		go func() { stopResults <- o.stopAgentInvocation(ctx, c, creating[i]) }()
	}
	var errs []error
	var recovered []RunState
	var lateOwners []*agentRunControl
	if o.state != nil {
		states, err := o.state.List(ctx, "agent_runs")
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect runtime ownership: %w", err))
		}
		for _, raw := range states {
			var state RunState
			if err := json.Unmarshal(raw, &state); err != nil {
				errs = append(errs, fmt.Errorf("decode runtime ownership: %w", err))
				continue
			}
			if state.AgentID != agentID || state.Status != "running" {
				continue
			}
			owned := false
			for _, c := range runs {
				if c.location.RunID == state.ID {
					owned = true
					break
				}
			}
			if !owned {
				// Tracking precedes the durable running write. A control can
				// therefore appear after our initial snapshot, but before List.
				if v, ok := o.agentRuns.Load(state.ID); ok {
					c := v.(*agentRunControl)
					if c.agentID != agentID {
						errs = append(errs, fmt.Errorf("run ownership changed for %s", state.ID))
						continue
					}
					lateOwners = append(lateOwners, c)
				} else {
					recovered = append(recovered, state)
				}
			}
		}
	}
	lateResults := make(chan error, len(lateOwners))
	for _, c := range lateOwners {
		c.mu.Lock()
		c.stopped = true
		creating := c.creating
		c.mu.Unlock()
		if !creating {
			c.cancel()
		}
		go func() { lateResults <- o.stopAgentInvocation(ctx, c, creating) }()
	}
	recoveredResults := make(chan error, len(recovered))
	for _, state := range recovered {
		go func() { recoveredResults <- o.stopRecoveredAgentRun(ctx, state) }()
	}

	for range runs {
		if err := <-stopResults; err != nil {
			errs = append(errs, err)
		}
	}
	for range lateOwners {
		if err := <-lateResults; err != nil {
			errs = append(errs, err)
		}
	}
	for range recovered {
		if err := <-recoveredResults; err != nil {
			errs = append(errs, err)
		}
	}
	if len(runs) == 0 && len(lateOwners) == 0 && len(recovered) == 0 && len(errs) == 0 {
		return fmt.Errorf("agent has no runtime owned by this process; stop not confirmed")
	}
	return errors.Join(errs...)
}

// A server restart loses the invocation but not its durable runtime identity.
// Signal only that exact location, and persist cancellation only after the
// provider confirms absence. Missing legacy identities remain unconfirmed.
func (o *Orchestrator) stopRecoveredAgentRun(ctx context.Context, state RunState) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	location := RunLocation{ContainerID: state.ContainerID, AgentSlug: state.AgentSlug, RunID: state.ID}
	for {
		stopped, err := o.StopRunAt(ctx, location)
		if err != nil {
			return err
		}
		if stopped {
			return o.persistStoppedRun(ctx, state)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stop recovered run %s not confirmed: %w", state.ID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (o *Orchestrator) stopAgentInvocation(ctx context.Context, c *agentRunControl, creating bool) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		if !creating {
			select {
			case <-c.done:
				return nil
			default:
			}
		} else {
			stopped, err := o.StopRunAt(ctx, c.location)
			last = err
			if err == nil && stopped {
				select {
				case <-c.done:
					return nil
				default:
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stop run %s not confirmed: %w", c.location.RunID, errors.Join(ctx.Err(), last))
		case <-tick.C:
		}
	}
}

// persistStoppedRun acknowledges cancellation only when its durable state write
// succeeds. Both explicit stops and deleted-agent cleanup retain retry evidence
// if storage is unavailable after the provider has confirmed runtime absence.
func (o *Orchestrator) persistStoppedRun(ctx context.Context, state RunState) error {
	return o.persistStoppedRunWithOrigin(ctx, state, "agent_stop")
}

func (o *Orchestrator) persistStoppedRunWithOrigin(ctx context.Context, state RunState, origin string) error {
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return fmt.Errorf("state provider cannot atomically persist stopped run")
	}
	return atomic.Update(ctx, "agent_runs", state.ID, func(raw []byte) ([]byte, error) {
		var current RunState
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, fmt.Errorf("decode stopped run %s: %w", state.ID, err)
		}
		// A concurrent terminal writer retains its outcome. Only the still
		// running record can transition to confirmed cancellation.
		if current.Status != "running" {
			return raw, nil
		}
		if _, owned := o.agentRuns.Load(state.ID); !owned {
			current.StopJournalPending = true
			current.StopOrigin = origin
		}
		current.Status, current.LastActivity = "cancelled", time.Now()
		return json.Marshal(current)
	})
}

// ReconcileRecoveredRun clears a stale running identity only when inspecting
// its exact container proves that the original runtime cannot still exist.
// Unreachable, creating and running containers remain protected.
func (o *Orchestrator) ReconcileRecoveredRun(ctx context.Context, run RunState) (bool, error) {
	if run.Status != "running" || run.ContainerID == "" || o.container == nil {
		return false, nil
	}
	absent, err := o.containerRuntimeAbsent(ctx, run.ContainerID)
	if err != nil || !absent {
		return false, err
	}
	return true, o.persistStoppedRunWithOrigin(ctx, run, "recovered_absence")
}

// RuntimeRecordAgent recovers only the ownership field when another field is
// malformed. Invalid JSON or missing ownership cannot be safely scoped.
func RuntimeRecordAgent(raw []byte) string {
	var owner struct {
		AgentID string `json:"agent_id"`
	}
	if json.Unmarshal(raw, &owner) != nil {
		return ""
	}
	return owner.AgentID
}
