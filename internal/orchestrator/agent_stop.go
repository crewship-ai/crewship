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

// AgentStopOutcome is the success half of StopAgentOutcome (#2879).
type AgentStopOutcome string

const (
	// AgentStopStopped: at least one invocation existed and its absence is
	// now confirmed.
	AgentStopStopped AgentStopOutcome = "stopped"
	// AgentStopAlreadyStopped: nothing was running, positively established
	// (see StopAgentOutcome); the stop is idempotent.
	AgentStopAlreadyStopped AgentStopOutcome = "already_stopped"
)

// The two refusal classes of StopAgentOutcome, so callers map refusals to
// stable codes without matching messages. A refusal wraps ErrStopNotConfirmed
// whenever any failure may hide a live process; only a refusal made purely of
// unreadable runtime evidence is ErrRuntimeUnavailable alone. Check
// ErrStopNotConfirmed first.
var (
	// ErrStopNotConfirmed: a process of this agent exists or may exist (a
	// live run this process does not own, a legacy record without a runtime
	// location, an unrecognised non-terminal record) and its absence could
	// not be confirmed.
	ErrStopNotConfirmed = errors.New("stop not confirmed")
	// ErrRuntimeUnavailable: the evidence needed to decide could not be read
	// at all — no state store, an unreadable run list, or a container runtime
	// that does not answer inspection.
	ErrRuntimeUnavailable = errors.New("runtime unavailable")
)

// Stable refusal codes for the stop routes, one per refusal class. The
// daemon's POST /agents/{id}/stop sends them and the public route forwards
// them unchanged, so CLI and web branch on these, never on sentences.
const (
	AgentStopCodeNotConfirmed       = "stop_not_confirmed"
	AgentStopCodeRuntimeUnavailable = "runtime_unavailable"
)

// terminalRunStatus lists the agent_runs statuses that name a finished run.
// Any other status, including one this build does not know, may be live.
func terminalRunStatus(status string) bool {
	switch status {
	case "completed", "error", "failed", "cancelled", "stopped":
		return true
	}
	return false
}

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
	c := &agentRunControl{agentID: req.AgentID, location: RunLocation{ContainerID: req.ContainerID, AgentSlug: req.AgentSlug, RunID: req.RunID, Managed: o.managedLaunchCrews[req.CrewID]}, cancel: cancel, done: make(chan struct{})}
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

	o.runRecoveryMu.Lock()
	o.agentRuns.Store(req.RunID, c)
	o.runRecoveryMu.Unlock()
	return ctx, func() {
		cancel()
		close(c.done)
		o.runRecoveryMu.Lock()
		o.agentRuns.Delete(req.RunID)
		o.runRecoveryMu.Unlock()
	}
}

// StopAgent is StopAgentOutcome without the outcome: nil means the agent is
// confirmed not running, either stopped now or already idle.
func (o *Orchestrator) StopAgent(ctx context.Context, agentID string) error {
	_, err := o.StopAgentOutcome(ctx, agentID)
	return err
}

// StopAgentOutcome stops current invocations and durable running records
// recovered after a server restart. New submissions remain a separate
// admission decision. It waits for both runtime absence and RunAgent
// settlement; an unresponsive provider or creation that outlives the deadline
// is a refusal, not STOPPED.
//
// AgentStopAlreadyStopped is returned only when all of these hold (#2879):
// this process owns no invocation of the agent (in-process ownership covers
// the window before the durable running write); the agent_runs store exists
// and lists without error; every record decodes or names another agent; and
// none of this agent's records has a non-terminal status. A running record
// is restart ownership: it is stopped at its persisted container/session
// location and yields AgentStopStopped once the runtime confirms absence.
// Every refusal wraps ErrStopNotConfirmed or ErrRuntimeUnavailable (see there).
func (o *Orchestrator) StopAgentOutcome(ctx context.Context, agentID string) (AgentStopOutcome, error) {
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
	if o.state == nil {
		// Without durable ownership a run surviving a restart is invisible.
		// Owned invocations are still stopped; idleness cannot be established.
		if len(runs) == 0 {
			errs = append(errs, fmt.Errorf("%w: no runtime ownership store", ErrRuntimeUnavailable))
		}
	} else {
		states, err := o.state.List(ctx, "agent_runs")
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: inspect runtime ownership: %w", ErrRuntimeUnavailable, err))
		}
		for _, raw := range states {
			var state RunState
			if err := json.Unmarshal(raw, &state); err != nil {
				// A corrupt record that still names another agent says
				// nothing about this one.
				if owner := RuntimeRecordAgent(raw); owner != "" && owner != agentID {
					continue
				}
				errs = append(errs, fmt.Errorf("%w: decode runtime ownership: %w", ErrStopNotConfirmed, err))
				continue
			}
			if state.AgentID != agentID || terminalRunStatus(state.Status) {
				continue
			}
			if state.Status != "running" {
				errs = append(errs, fmt.Errorf("%w: run %s has unrecognised status %q", ErrStopNotConfirmed, state.ID, state.Status))
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
	if len(recovered) > 0 && o.container == nil {
		errs = append(errs, fmt.Errorf("%w: no container provider to stop %d recovered run(s)", ErrRuntimeUnavailable, len(recovered)))
		recovered = nil
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
	if len(errs) > 0 {
		return "", classifyStopRefusal(errs)
	}
	if len(runs) == 0 && len(lateOwners) == 0 && len(recovered) == 0 {
		return AgentStopAlreadyStopped, nil
	}
	return AgentStopStopped, nil
}

// classifyStopRefusal reports ErrRuntimeUnavailable only when every failure
// was an unreadable runtime; any other failure means a process may be live.
func classifyStopRefusal(errs []error) error {
	joined := errors.Join(errs...)
	for _, err := range errs {
		if errors.Is(err, ErrRuntimeUnavailable) {
			continue
		}
		if errors.Is(err, ErrStopNotConfirmed) {
			return joined
		}
		return fmt.Errorf("%w: %w", ErrStopNotConfirmed, joined)
	}
	return joined
}

// A server restart loses the invocation but not its durable runtime identity.
// Signal only that exact location, and persist cancellation only after the
// provider confirms absence. Missing legacy identities remain unconfirmed.
func (o *Orchestrator) stopRecoveredAgentRun(ctx context.Context, state RunState) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	location := RunLocation{ContainerID: state.ContainerID, AgentSlug: state.AgentSlug, RunID: state.ID, Managed: state.ManagedLaunch != nil}
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
	o.runRecoveryMu.Lock()
	defer o.runRecoveryMu.Unlock()
	atomic, ok := o.state.(provider.AtomicStateProvider)
	if !ok {
		return fmt.Errorf("state provider cannot atomically persist stopped run")
	}
	return atomic.Update(ctx, "agent_runs", state.ID, func(raw []byte) ([]byte, error) {
		// A concurrent cleanup may already have removed this identity.
		// Preserve absence rather than recreating the row or reporting failure.
		if raw == nil {
			return nil, nil
		}
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
