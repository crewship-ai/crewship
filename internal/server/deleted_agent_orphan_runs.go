package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// deletedAgentOrphanMinAge keeps the reaper away from a run whose journal
// start was just written and whose runtime record may not exist yet.
const deletedAgentOrphanMinAge = 10 * time.Minute

// deletedAgentOrphanBatch bounds one pass; the next tick continues.
const deletedAgentOrphanBatch = 100

// reapDeletedAgentOrphanRuns ends the journal history of runs whose agent was
// deleted and which no runtime can still be executing (#5511). Before this,
// only a server restart finalized them, so `now`, `run list` and run insights
// showed them RUNNING for as long as the server stayed up.
//
// A trace is ended only when ALL of these hold:
//   - its agent is soft-deleted or gone, so no new execution can be admitted;
//   - its run.started is older than deletedAgentOrphanMinAge;
//   - it has no terminal run.* entry and no durable work attempt (work keeps
//     its own fenced outcome authority);
//   - the agent has no non-terminal runtime record, no record whose history
//     is still pending (stop outbox or retained output). Every orchestrator
//     launch persists that record before its exec starts, and deleted-agent
//     runs keep it "running" until StopDeletedAgentRuns confirms the runtime
//     absent, so a live process always blocks this pass. Undecodable records
//     of the agent block it too;
//   - this process owns no invocation of the agent (checked under the same
//     lock that admits invocations).
//
// It writes only history: it never releases capacity, admission or
// credentials, which stay with the owners that confirm runtime absence.
func (s *Server) reapDeletedAgentOrphanRuns(ctx context.Context) (int, error) {
	if s.db == nil || s.journalWriter == nil || s.state == nil {
		return 0, nil
	}
	cutoff := time.Now().Add(-deletedAgentOrphanMinAge).UTC().Format("2006-01-02 15:04:05")
	rows, err := s.db.QueryContext(ctx, `
		SELECT je1.trace_id, MAX(je1.agent_id), je1.workspace_id
		FROM journal_entries je1
		LEFT JOIN agents a ON a.id = je1.agent_id
		WHERE je1.entry_type = 'run.started'
		  AND je1.trace_id IS NOT NULL AND je1.trace_id <> ''
		  AND COALESCE(je1.agent_id, '') <> ''
		  AND (a.id IS NULL OR a.deleted_at IS NOT NULL)
		  AND julianday(je1.ts) < julianday(?)
		  AND NOT EXISTS (SELECT 1 FROM work_attempts wa WHERE wa.run_id = je1.trace_id)
		  AND NOT EXISTS (
		    SELECT 1 FROM journal_entries je2
		    WHERE je2.workspace_id = je1.workspace_id
		      AND je2.trace_id = je1.trace_id
		      AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')
		      AND (je2.entry_type <> 'run.recovered_stop' OR je2.agent_id = je1.agent_id)
		  )
		  AND je1.trace_id > ?
		GROUP BY je1.workspace_id, je1.trace_id
		ORDER BY je1.trace_id
		LIMIT ?`, cutoff, s.deletedOrphanCursor, deletedAgentOrphanBatch)
	if err != nil {
		return 0, fmt.Errorf("scan deleted-agent runs: %w", err)
	}
	type orphan struct{ id, agentID, workspaceID string }
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.agentID, &o.workspaceID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		orphans = append(orphans, o)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// A short page wraps; a full one resumes after its last trace.
	if len(orphans) < deletedAgentOrphanBatch {
		s.deletedOrphanCursor = ""
	} else {
		s.deletedOrphanCursor = orphans[len(orphans)-1].id
	}

	reaped := 0
	var failures []error
	for _, o := range orphans {
		if err := ctx.Err(); err != nil {
			return reaped, errors.Join(append(failures, err)...)
		}
		emit := func(locallyActive bool) error {
			if locallyActive {
				return nil
			}
			blocked, err := s.agentHasOpenRuntimeRecord(ctx, o.agentID)
			if err != nil || blocked {
				return err
			}
			_, err = s.journalWriter.EmitSync(ctx, journal.Entry{
				ID: "agent-deleted-orphan:" + o.id, WorkspaceID: o.workspaceID, AgentID: o.agentID,
				TraceID: o.id, Type: journal.EntryRunCancelled, Severity: journal.SeverityNotice,
				ActorType: journal.ActorSystem,
				Summary:   "run cancelled — its agent was deleted and no runtime remains",
				Payload:   map[string]any{"reason": "agent_deleted", "metadata": map[string]any{"stop_origin": "agent_deleted"}},
			})
			if err == nil {
				reaped++
			}
			return err
		}
		var err error
		if s.orchestrator != nil {
			err = s.orchestrator.ReconcileAgentActivity(o.agentID, emit)
		} else {
			err = emit(false)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("end deleted-agent run %s: %w", o.id, err))
		}
	}
	return reaped, errors.Join(failures...)
}

// agentHasOpenRuntimeRecord reports whether any runtime record of agentID is
// not terminal, or cannot be read. Either means a process may exist.
func (s *Server) agentHasOpenRuntimeRecord(ctx context.Context, agentID string) (bool, error) {
	states, err := s.state.List(ctx, "agent_runs")
	if err != nil {
		return true, err
	}
	for _, raw := range states {
		var run orchestrator.RunState
		if err := json.Unmarshal(raw, &run); err != nil {
			owner := orchestrator.RuntimeRecordAgent(raw)
			if owner == "" || owner == agentID {
				return true, nil
			}
			continue
		}
		if run.AgentID != agentID {
			continue
		}
		// Retained host output reaches a terminal status before its journal
		// projection (flushRetainedOutput): its real outcome is still due.
		if run.Output != nil && run.Output.Version == 2 && !run.Output.Collected && !run.Output.Acknowledged && run.Output.Unprojected == "" {
			return true, nil
		}
		switch run.Status {
		case "completed", "error", "failed", "cancelled", "stopped":
			// A confirmed stop whose history is still pending projects its
			// own terminal; leave the trace to it.
			if run.StopJournalPending {
				return true, nil
			}
		default:
			return true, nil
		}
	}
	return false, nil
}
