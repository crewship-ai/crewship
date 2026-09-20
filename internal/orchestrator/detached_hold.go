package orchestrator

import (
	"context"
	"errors"
	"fmt"
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
	startedAt   time.Time
}

// detachedWatchCap bounds how long a hold may live. The exec's own timeout is
// expected to settle it long before this; the cap exists so a runtime on an
// unreachable container cannot pin a run slot and an agent forever. On the
// cap the slot is released and the hold dropped with an ERROR — a second run
// becoming possible again is then an operator-visible decision, not a silent
// one.
const detachedWatchCap = 24 * time.Hour

// registerDetachedHold records the hold and starts its watcher. The caller
// has already transferred the run-slot release into h; from here on the
// watcher owns both. It returns an error (and does not take ownership) if the
// agent already carries a hold — the admission check makes that unreachable
// in practice, and this is the fence for the day it is not.
func (o *Orchestrator) registerDetachedHold(h *detachedHold, poll time.Duration) error {
	if poll <= 0 {
		poll = defaultDetachedPollInterval
	}
	o.detachedMu.Lock()
	if _, exists := o.detached[h.req.AgentID]; exists {
		o.detachedMu.Unlock()
		return fmt.Errorf("agent %s already carries a detached hold", h.req.AgentID)
	}
	o.detached[h.req.AgentID] = h
	o.detachedMu.Unlock()
	go o.watchDetachedHold(h, poll)
	return nil
}

// detachedHoldRunID reports the run a hold names for agentID, if any. The
// admission path turns this into ErrAgentDetachedBusy.
func (o *Orchestrator) detachedHoldRunID(agentID string) (string, bool) {
	o.detachedMu.Lock()
	defer o.detachedMu.Unlock()
	h, ok := o.detached[agentID]
	if !ok {
		return "", false
	}
	return h.runID, true
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
	capTimer := time.NewTimer(detachedWatchCap)
	defer capTimer.Stop()
	ticks := 0
	for {
		select {
		case <-capTimer.C:
			o.logger.Error("detached exec outlived the hold's watch cap; releasing the slot and lifting the agent hold — an operator must verify the process is gone",
				"agent_id", h.req.AgentID, "run_id", h.runID, "exec_id", h.execID,
				"held_for", time.Since(h.startedAt).Round(time.Second))
			o.closeDetachedHold(h, false)
			return
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
					o.restopDetachedHold(h)
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
func (o *Orchestrator) restopDetachedHold(h *detachedHold) {
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
	case !stopped:
		o.logger.Warn("detached hold: runtime ignored the re-stop signal", "agent_id", h.req.AgentID, "run_id", h.runID)
	}
}

// closeDetachedHold performs the terminal bookkeeping, exactly once per hold:
// the exec.command end journal entry (confirmed, with how the watch ended),
// the agent's return to online, the unregister, and the run-slot release.
func (o *Orchestrator) closeDetachedHold(h *detachedHold, confirmedGone bool) {
	o.detachedMu.Lock()
	if cur, ok := o.detached[h.req.AgentID]; !ok || cur != h {
		o.detachedMu.Unlock()
		// Already closed by another path (a re-stop that confirmed between
		// ticks, the cap firing twice); nothing to do twice.
		return
	}
	delete(o.detached, h.req.AgentID)
	o.detachedMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), preflightExecTimeout)
	defer cancel()
	payload := map[string]any{"detached": true, "confirmed_gone": confirmedGone}
	if confirmedGone {
		o.emitExecEnd(ctx, h.req, h.execID, journalCmdView{argv: []string{"<detached exec>"}},
			"info",
			fmt.Sprintf("%s: detached exec confirmed gone (%s)", h.agentSlug, time.Since(h.startedAt).Round(time.Second)),
			h.startedAt, payload)
	} else {
		o.emitExecEnd(ctx, h.req, h.execID, journalCmdView{argv: []string{"<detached exec>"}},
			"error",
			fmt.Sprintf("%s: detached exec NOT confirmed gone; hold expired by cap", h.agentSlug),
			h.startedAt, payload)
	}
	if confirmedGone {
		// Only a runtime nobody can find anymore earns the agent its
		// `online` back. The cap path leaves presence alone on purpose:
		// the process may be alive, and "available" would be the same lie
		// this file exists to stop (#2626 review).
		o.markAgentOnline(ctx, h.req, map[string]any{"reason": "detached_exec_confirmed_gone"})
	}
	h.release()
}
