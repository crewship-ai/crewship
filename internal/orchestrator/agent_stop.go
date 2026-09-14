package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

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
	req.ExecGate = func(ctx context.Context) error {
		c.mu.Lock()
		stopped := c.stopped
		c.mu.Unlock()
		if stopped {
			return context.Canceled
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

// StopAgent stops the invocations owned by this orchestrator at the time of
// the request. New submissions remain a separate admission decision. It waits
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
	var errs []error
	if o.state != nil {
		states, err := o.state.List(ctx, "agent_runs")
		if err != nil {
			return fmt.Errorf("inspect runtime ownership: %w", err)
		}
		for _, raw := range states {
			var state RunState
			if json.Unmarshal(raw, &state) != nil {
				return fmt.Errorf("unreadable runtime ownership")
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
				errs = append(errs, fmt.Errorf("run %s has no live process owner; stop cannot be confirmed", state.ID))
			}
		}
	}

	for _, c := range runs {
		c.mu.Lock()
		c.stopped = true
		creating := c.creating
		if !creating {
			c.cancel()
		}
		c.mu.Unlock()
		if err := o.stopAgentInvocation(ctx, c, creating); err != nil {
			errs = append(errs, err)
		}
	}
	if len(runs) == 0 && len(errs) == 0 {
		return fmt.Errorf("agent has no runtime owned by this process; stop not confirmed")
	}
	return errors.Join(errs...)
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
