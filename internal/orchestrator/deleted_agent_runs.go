package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
)

// DeletedAgentLookup reports whether an agent may no longer run, and the slug
// to fall back on for runs persisted before RunState recorded one.
type DeletedAgentLookup func(ctx context.Context, agentID string) (deleted bool, slug string, err error)

// DeletedAgentRunsResult counts what one pass did. Pending runs are still
// alive or could not be confirmed gone; the next pass retries them.
type DeletedAgentRunsResult struct {
	Stopped int
	Pending int
	Errors  []error
}

// StopDeletedAgentRuns stops the live runs of deleted agents, and only those.
// It reads the persisted run state rather than this process's memory, so a run
// that outlived a server restart (tmux keeps it alive in the crew container)
// is found and stopped too. agentID limits the pass to one agent; empty means
// every deleted agent. A run counts as stopped only when its runtime is
// confirmed absent; anything else stays pending for the next pass.
func (o *Orchestrator) StopDeletedAgentRuns(ctx context.Context, agentID string, lookup DeletedAgentLookup) DeletedAgentRunsResult {
	var res DeletedAgentRunsResult
	if o.state == nil || lookup == nil {
		return res
	}
	raw, err := o.state.List(ctx, "agent_runs")
	if err != nil {
		res.Errors = append(res.Errors, fmt.Errorf("list run state: %w", err))
		return res
	}
	deleted := map[string]bool{}
	slugs := map[string]string{}
	for _, b := range raw {
		var st RunState
		if json.Unmarshal(b, &st) != nil || st.Status != "running" || st.AgentID == "" {
			continue
		}
		if agentID != "" && st.AgentID != agentID {
			continue
		}
		gone, known := deleted[st.AgentID]
		if !known {
			d, slug, err := lookup(ctx, st.AgentID)
			if err != nil {
				res.Pending++
				res.Errors = append(res.Errors, fmt.Errorf("agent %s: %w", st.AgentID, err))
				continue
			}
			deleted[st.AgentID], slugs[st.AgentID], gone = d, slug, d
		}
		if !gone {
			continue
		}
		// An in-process owner is cancelled first, so its own completion path
		// records the terminal state.
		o.cancelOwnedRun(st.ID)
		slug := st.AgentSlug
		if slug == "" {
			slug = slugs[st.AgentID]
		}
		stopped, err := o.StopRunAt(ctx, RunLocation{ContainerID: st.ContainerID, AgentSlug: slug, RunID: st.ID, Managed: st.ManagedLaunch != nil})
		if err != nil || !stopped {
			res.Pending++
			if err != nil {
				res.Errors = append(res.Errors, err)
			}
			continue
		}
		if err := o.persistStoppedRun(ctx, st); err != nil {
			res.Pending++
			res.Errors = append(res.Errors, err)
			continue
		}
		res.Stopped++
	}
	return res
}

// cancelOwnedRun cancels the RunAgent invocation this process owns for runID,
// if any, and closes its creation gate.
func (o *Orchestrator) cancelOwnedRun(runID string) {
	o.agentRuns.Range(func(_, v any) bool {
		c := v.(*agentRunControl)
		if c.location.RunID != runID {
			return true
		}
		c.mu.Lock()
		c.stopped = true
		creating := c.creating
		c.mu.Unlock()
		if !creating {
			c.cancel()
		}
		return false
	})
}
