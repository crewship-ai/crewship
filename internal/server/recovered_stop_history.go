package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// flushRecoveredStops drains the durable stop outbox. Runtime absence is
// already confirmed; this retries only history projection, never execution.
// Work-owned attempts retain their own fenced outcome authority.
func (s *Server) flushRecoveredStops(ctx context.Context) error {
	if s.state == nil || s.db == nil || s.journalWriter == nil {
		return nil
	}
	// Explicit stops and periodic recovery can overlap. One drain owns the
	// cursor; another caller can leave its durable marker for the next tick.
	if !s.recoveredStopsMu.TryLock() {
		return nil
	}
	defer s.recoveredStopsMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	states, err := s.state.List(ctx, "agent_runs")
	if err != nil {
		return err
	}
	// Resume after the last visited key, wrapping at the end. Unresolved IPC
	// or work-owned stops keep their markers but cannot consume the first
	// batch forever. Provider List returns a map, so its order is not a cursor.
	keys := make([]string, 0, len(states))
	for key := range states {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	start := sort.Search(len(keys), func(i int) bool { return keys[i] > s.recoveredStopsCursor })
	var failures []error
	processed := 0
	for offset := 0; offset < len(keys) && processed < 100; offset++ {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		key := keys[(start+offset)%len(keys)]
		s.recoveredStopsCursor = key
		raw := states[key]
		var run orchestrator.RunState
		if err := json.Unmarshal(raw, &run); err != nil {
			failures = append(failures, err)
			continue
		}
		if !run.StopJournalPending || run.Status != "cancelled" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := s.projectRecoveredStop(ctx, run); err != nil {
			failures = append(failures, fmt.Errorf("project confirmed stop %s: %w", run.ID, err))
		}
		processed++
	}
	return errors.Join(failures...)
}

func (s *Server) projectRecoveredStop(ctx context.Context, run orchestrator.RunState) error {
	var workspace string
	var terminal, workOwned, recoveredTerminal bool
	err := s.db.QueryRowContext(ctx, `SELECT je.workspace_id,
 EXISTS (SELECT 1 FROM journal_entries t WHERE t.workspace_id=je.workspace_id AND t.trace_id=je.trace_id
   AND t.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')),
 EXISTS (SELECT 1 FROM work_attempts a WHERE a.run_id=je.trace_id),
 EXISTS (SELECT 1 FROM journal_entries t WHERE t.id='recovered-stop:' || je.trace_id AND t.workspace_id=je.workspace_id)
 FROM journal_entries je WHERE je.trace_id=? AND je.agent_id=? AND je.entry_type='run.started' LIMIT 1`, run.ID, run.AgentID).
		Scan(&workspace, &terminal, &workOwned, &recoveredTerminal)
	if errors.Is(err, sql.ErrNoRows) {
		// Some manual IPC runs have no journal trace; a delayed run.started
		// may also not have committed yet. Neither authorizes inventing one.
		return nil
	}
	if err != nil {
		return err
	}
	origin, reason, summary, idleStatus := "agent_stop", "confirmed_runtime_stop", "run cancelled after confirmed runtime stop", "STOPPED"
	if run.StopOrigin == "recovered_absence" {
		origin, reason, summary, idleStatus = "recovered_absence", "recovered_runtime_absent", "recovered run ended without a verified outcome", "IDLE"
	}
	if !terminal {
		if workOwned {
			return nil
		}
		if _, err := s.journalWriter.EmitSync(ctx, journal.Entry{
			ID: "recovered-stop:" + run.ID, WorkspaceID: workspace, AgentID: run.AgentID,
			TraceID: run.ID, Type: journal.EntryRunCancelled, Severity: journal.SeverityNotice,
			ActorType: journal.ActorSystem, Summary: summary,
			Payload: map[string]any{"reason": reason, "metadata": map[string]any{"stop_origin": origin}},
		}); err != nil {
			return err
		}
		recoveredTerminal = true
	}
	if recoveredTerminal {
		if _, err := s.db.ExecContext(ctx, `UPDATE agents SET status=CASE WHEN EXISTS (
 SELECT 1 FROM journal_entries started WHERE started.workspace_id=? AND started.agent_id=? AND started.entry_type='run.started'
 AND NOT EXISTS (SELECT 1 FROM journal_entries done WHERE done.workspace_id=started.workspace_id AND done.trace_id=started.trace_id
   AND done.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout'))
 ) THEN 'RUNNING' ELSE ? END, updated_at=? WHERE id=? AND workspace_id=? AND deleted_at IS NULL`,
			workspace, run.AgentID, idleStatus, time.Now().UTC().Format(time.RFC3339), run.AgentID, workspace); err != nil {
			return err
		}
	}
	// Read again so acknowledging the outbox does not restore an older copy
	// of unrelated state fields. Clearing only after a durable terminal makes
	// failed writes and a crash after journal commit safe to retry.
	raw, err := s.state.Get(ctx, "agent_runs", run.ID)
	if err != nil {
		return err
	}
	var current orchestrator.RunState
	if err := json.Unmarshal(raw, &current); err != nil {
		return err
	}
	current.StopJournalPending = false
	raw, err = json.Marshal(current)
	if err != nil {
		return err
	}
	return s.state.Set(ctx, "agent_runs", run.ID, raw)
}
