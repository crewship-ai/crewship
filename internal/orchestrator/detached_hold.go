package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrAgentDetachedBusy is the admission refusal for an agent whose previous
// run returned ErrDetachedStillRunning with a stop that could not be
// confirmed (#2626). A process nobody has confirmed dead still occupies the
// agent's exclusivity and the host's capacity, so starting a second run for
// the same agent would be exactly the two-runtimes-for-one-agent shape the
// dispatcher's supersede rule exists to prevent. The refusal lifts itself
// when the hold's watcher confirms the runtime is gone.
var ErrAgentDetachedBusy = errors.New("orchestrator: agent busy: a previous run's detached exec is still alive")

// detachedHold is one agent's live-but-unsupervised exec: RunAgent has
// returned ErrDetachedStillRunning, the bounded stop inside that call could
// not confirm the process was gone, and somebody has to keep the truth until
// it is. What the hold owns:
//
//   - the departed run's run-slot release, so host capacity reflects a
//     process that may still be burning it;
//   - the agent's admission, via the map it lives in;
//   - the terminal bookkeeping RunAgent could not do — the exec.command end
//     journal entry and the agent's return to `online` — which are emitted
//     only once the runtime is CONFIRMED gone, never before (#2626 review:
//     a running agent must not be marked available).
type detachedHold struct {
	runID       string
	containerID string
	agentSlug   string
	execID      string
	req         AgentRunRequest
	release     func()
	finalize    func()
	once        sync.Once
	alertAfter  time.Duration
	done        chan struct{}
	startedAt   time.Time
}

// detachedWatchCap is an alert deadline, never permission to reopen admission.
// After the alert, polling backs off while the safety fence stays in place.
const detachedWatchCap = 24 * time.Hour

// Holds are keyed by run: runs admitted concurrently before a hold appeared
// must each retain their capacity and cleanup ownership when they detach.
func (o *Orchestrator) registerDetachedHold(h *detachedHold, poll time.Duration) error {
	if poll <= 0 {
		poll = defaultDetachedPollInterval
	}
	o.detachedMu.Lock()
	if _, exists := o.detached[h.runID]; exists {
		o.detachedMu.Unlock()
		return fmt.Errorf("run %s already carries a detached hold", h.runID)
	}
	h.done = make(chan struct{})
	o.detached[h.runID] = h
	o.detachedMu.Unlock()
	go o.watchDetachedHold(h, poll)
	return nil
}

// detachedHoldRunID reports the run a hold names for agentID, if any. The
// admission path turns this into ErrAgentDetachedBusy.
func (o *Orchestrator) detachedHoldRunID(agentID string) (string, bool) {
	o.detachedMu.Lock()
	defer o.detachedMu.Unlock()
	for _, h := range o.detached {
		if h.req.AgentID == agentID {
			return h.runID, true
		}
	}
	return "", false
}

// watchDetachedHold polls the runtime until the provider confirms it is gone,
// then does the terminal bookkeeping in the right order: journal end, agent
// online, unregister, slot release — all AFTER confirmation, none before.
//
// Probe errors keep the hold and retry: "cannot answer" is not "gone", and
// dropping the hold on an unreachable container is the inference this whole
// mechanism exists to refuse. Every tenth tick re-attempts a bounded stop,
// because a wedged exec that ignored one signal may honour the next.
func (o *Orchestrator) watchDetachedHold(h *detachedHold, poll time.Duration) {
	t := time.NewTicker(poll)
	defer t.Stop()
	alertAfter := h.alertAfter
	if alertAfter <= 0 {
		alertAfter = detachedWatchCap
	}
	capTimer := time.NewTimer(alertAfter)
	defer capTimer.Stop()
	ticks := 0
	for {
		select {
		case <-h.done:
			return
		case <-capTimer.C:
			o.logger.Error("detached runtime still unconfirmed; retaining admission and capacity, backing off probes", "agent_id", h.req.AgentID, "run_id", h.runID)
			if poll < time.Minute {
				t.Reset(time.Minute)
			}
			capTimer.Reset(detachedWatchCap)
		case <-t.C:
			ticks++
			ctx, cancel := context.WithTimeout(context.Background(), preflightExecTimeout)
			alive, err := o.RunIsAliveAt(ctx, RunLocation{
				ContainerID: h.containerID, AgentSlug: h.agentSlug, RunID: h.runID,
			})
			cancel()
			switch {
			case err == nil && !alive:
				o.closeDetachedHold(h, true)
				return
			case err == nil && alive:
				if ticks%10 == 0 {
					if o.restopDetachedHold(h) {
						return
					}
				}
			default:
				// Unanswerable, not gone. Throttled: an unreachable
				// container fails every tick and the log must not become
				// the outage.
				if ticks%10 == 1 {
					o.logger.Warn("detached hold: could not probe the runtime; holding the agent and slot",
						"agent_id", h.req.AgentID, "run_id", h.runID, "error", err)
				}
			}
		}
	}
}

// restopDetachedHold re-sends the stop signal to a hold's runtime, bounded.
func (o *Orchestrator) restopDetachedHold(h *detachedHold) bool {
	ctx, cancel := context.WithTimeout(context.Background(), preflightExecTimeout)
	defer cancel()
	stopped, err := o.StopRunAt(ctx, RunLocation{
		ContainerID: h.containerID, AgentSlug: h.agentSlug, RunID: h.runID,
	})
	switch {
	case err != nil:
		o.logger.Warn("detached hold: re-stop attempt failed", "agent_id", h.req.AgentID, "run_id", h.runID, "error", err)
	case stopped:
		// Confirmed gone by the stop itself; the next tick's Alive probe
		// would see it too, but the bookkeeping does not need to wait.
		o.closeDetachedHold(h, true)
		return true
	case !stopped:
		o.logger.Warn("detached hold: runtime ignored the re-stop signal", "agent_id", h.req.AgentID, "run_id", h.runID)
	}
	return false
}

// closeDetachedHold performs the terminal bookkeeping, exactly once per hold:
// the exec.command end journal entry (confirmed, with how the watch ended),
// the agent's return to online, the unregister, and the run-slot release.
func (o *Orchestrator) closeDetachedHold(h *detachedHold, confirmedGone bool) {
	// Uncertainty must never become a terminal event or free capacity.
	if !confirmedGone {
		return
	}
	h.once.Do(func() {
		// Keep the admission fence through cleanup and terminal bookkeeping.
		if h.finalize != nil {
			h.finalize()
		}
		ctx, cancel := context.WithTimeout(context.Background(), preflightExecTimeout)
		defer cancel()
		o.emitExecEnd(ctx, h.req, h.execID, journalCmdView{argv: []string{"<detached exec>"}}, "info",
			fmt.Sprintf("%s: detached exec confirmed gone (%s)", h.agentSlug, time.Since(h.startedAt).Round(time.Second)),
			h.startedAt, map[string]any{"detached": true, "confirmed_gone": true})
		o.updateRunStatus(ctx, h.runID, "error") // no trustworthy exit result survived detachment
		o.detachedMu.Lock()
		other := false
		for _, hold := range o.detached {
			if hold != h && hold.req.AgentID == h.req.AgentID {
				other = true
				break
			}
		}
		if other {
			// Retire this completed hold in the same critical section as
			// checking its peers. Otherwise two simultaneous completions can
			// both see the other and neither restores the agent's presence.
			// The remaining hold still fences admission until its own cleanup
			// and terminal bookkeeping finish.
			delete(o.detached, h.runID)
		}
		o.detachedMu.Unlock()
		if !other {
			// Keep the last hold registered through the presence update, but
			// do not hold detachedMu across external tracker I/O.
			o.markAgentOnline(ctx, h.req, map[string]any{"reason": "detached_exec_confirmed_gone"})
			o.detachedMu.Lock()
			delete(o.detached, h.runID)
			o.detachedMu.Unlock()
		}
		h.release()
		if h.done != nil {
			close(h.done)
		}
	})
}
