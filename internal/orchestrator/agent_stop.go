package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
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
				// A live invocation can register after the initial stop snapshot
				// and publish state before List returns. It is a new admission,
				// not a recovered runtime: the recovery path cannot close its
				// creation gate and must not persist a false cancellation.
				if _, live := o.agentRuns.Load(state.ID); live {
					continue
				}
				recovered = append(recovered, state)
			}
		}
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
	for range recovered {
		if err := <-recoveredResults; err != nil {
			errs = append(errs, err)
		}
	}
	if len(runs) == 0 && len(recovered) == 0 && len(errs) == 0 {
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
	raw, err := o.state.Get(ctx, "agent_runs", state.ID)
	if err != nil {
		return fmt.Errorf("read stopped run %s: %w", state.ID, err)
	}
	var current RunState
	if err := json.Unmarshal(raw, &current); err != nil {
		return fmt.Errorf("decode stopped run %s: %w", state.ID, err)
	}
	// A normal completion may have won while the stop probe was in flight.
	// Preserve its outcome and leave its completion owner responsible for it.
	if current.Status != "running" {
		return nil
	}
	state = current
	if _, owned := o.agentRuns.Load(state.ID); !owned {
		state.StopJournalPending = true
	}
	state.Status, state.LastActivity = "cancelled", time.Now()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := o.state.Set(ctx, "agent_runs", state.ID, data); err != nil {
		return fmt.Errorf("persist cancellation for run %s: %w", state.ID, err)
	}
	return nil
}
