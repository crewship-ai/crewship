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
	"github.com/crewship-ai/crewship/internal/runoutput"
	"github.com/crewship-ai/crewship/internal/work"
)

// flushRetainedOutput projects only capabilities recorded by this controller.
// It includes terminal rows: execution can finish before the journal catches up.
// The source acknowledgement follows synchronous projection, never precedes it.
func (s *Server) flushRetainedOutput(ctx context.Context) error {
	if s.state == nil || s.orchestrator == nil || s.journalWriter == nil || s.db == nil {
		return nil
	}
	if !s.retainedOutputMu.TryLock() {
		return nil
	}
	defer s.retainedOutputMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	states, err := s.state.List(ctx, "agent_runs")
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(states))
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for key, raw := range states {
		var run orchestrator.RunState
		if json.Unmarshal(raw, &run) != nil || run.ID != key || run.Output == nil || run.Output.Version != 2 || run.Output.Collected {
			continue
		}
		if run.Output.Acknowledged && (run.Status == "running" || !run.LastActivity.Before(cutoff)) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	start := sort.Search(len(keys), func(i int) bool { return keys[i] > s.retainedOutputCursor })
	var failures []error
	for offset := 0; offset < len(keys) && offset < 100 && ctx.Err() == nil; offset++ {
		key := keys[(start+offset)%len(keys)]
		s.retainedOutputCursor = key
		var run orchestrator.RunState
		if json.Unmarshal(states[key], &run) != nil || run.ID != key || run.Output == nil || run.Output.Version != 2 {
			continue
		}
		if run.Output.Collected {
			continue
		}
		if run.Status != "running" && run.LastActivity.Before(time.Now().Add(-7*24*time.Hour)) {
			collected, err := s.orchestrator.CollectRetainedRun(ctx, run, time.Now().Add(-7*24*time.Hour))
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if collected {
				continue
			}
		}
		if err := s.projectRetainedOutput(ctx, run); err != nil {
			failures = append(failures, fmt.Errorf("retained run %s: %w", key, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) projectRetainedOutput(ctx context.Context, run orchestrator.RunState) error {
	if run.WorkspaceID == "" || run.AgentID == "" || run.StartedAt.IsZero() {
		return errors.New("retained run lacks original ownership")
	}
	var workspace string
	if err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM agents WHERE id=?`, run.AgentID).Scan(&workspace); err != nil || workspace != run.WorkspaceID {
		return errors.New("retained run ownership disagrees with agent")
	}
	var conflict bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM journal_entries WHERE trace_id=? AND (workspace_id<>? OR COALESCE(agent_id,'')<>?) AND entry_type IN ('run.started','run.completed','run.failed','run.cancelled','run.timeout'))`, run.ID, workspace, run.AgentID).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return errors.New("retained run has conflicting journal ownership")
	}
	if projection, owned, err := work.NewStore(s.db).RunProjection(ctx, run.ID); err != nil {
		return err
	} else if owned && (projection.WorkspaceID != workspace || projection.AgentID != run.AgentID || (projection.CrewID != "" && projection.CrewID != run.CrewID)) {
		return errors.New("retained work scope differs")
	}
	snapshot, err := s.orchestrator.ReplayRetainedRun(ctx, run, func(line runoutput.Line) error {
		id := fmt.Sprintf("retained:%s:%d:%d", run.ID, line.Sequence, line.Index)
		_, _, err := s.journalWriter.EmitReplaySync(ctx, journal.Entry{
			ID: id, WorkspaceID: workspace, CrewID: run.CrewID, AgentID: run.AgentID, MissionID: run.MissionID,
			TS: run.StartedAt, Type: journal.EntryExecOutputChunk, Severity: journal.SeverityInfo,
			ActorType: journal.ActorSidecar, ActorID: run.AgentID, Summary: "retained execution output",
			TraceID: run.ID, SpanID: id,
			Payload: map[string]any{"output": string(line.Data), "stream": line.Stream, "sequence": line.Sequence, "index": line.Index, "retained": true},
			Refs:    map[string]any{"chat_id": run.ChatID},
		})
		return err
	}, func(snapshot runoutput.Snapshot) error { return s.completeRetainedOutput(ctx, run, snapshot) })
	if err == nil && snapshot.Complete {
		return s.orchestrator.MarkRetainedOutputProjected(ctx, run)
	}
	return err
}

func (s *Server) completeRetainedOutput(ctx context.Context, run orchestrator.RunState, snapshot runoutput.Snapshot) error {
	if err := snapshot.Validate(); err != nil || !snapshot.Complete {
		return errors.New("retained completion lacks terminal evidence")
	}
	if s.orchestrator.RunLocallyOwned(run.ID) {
		return orchestrator.ErrHostCaptureUnconfirmed
	}
	code := snapshot.Result.ExitCode
	analysis, err := s.orchestrator.AnalyzeRetainedRun(ctx, run)
	if err != nil {
		return err
	}
	success := snapshot.Result.Reason == "exited" && code == 0 && !analysis.Failed
	result := work.RunResult{ExitCode: &code, Metadata: analysis.Metadata}
	status := "FAILED"
	if success {
		status = "COMPLETED"
	} else {
		message := fmt.Sprintf("retained execution ended: %s (exit %d)", snapshot.Result.Reason, code)
		if analysis.ErrorMessage != "" {
			message = analysis.ErrorMessage
		}
		result.ErrorMessage = &message
	}
	store := work.NewStore(s.db)
	p, owned, err := store.RunProjection(ctx, run.ID)
	if err != nil {
		return err
	}
	if owned && (p.WorkspaceID != run.WorkspaceID || p.AgentID != run.AgentID || (p.CrewID != "" && p.CrewID != run.CrewID)) {
		return errors.New("retained work scope differs")
	}
	if analysis.Unknown && (!owned || !p.Ready || p.Status == "COMPLETED") {
		return errors.New("retained CLI output lacks a terminal envelope")
	}
	if owned && !p.Ready {
		if _, err := store.RecoverRetainedResult(ctx, run.ID, result, success); err != nil {
			return err
		}
		p, _, err = store.RunProjection(ctx, run.ID)
		if err != nil {
			return err
		}
		if !p.Ready {
			return errors.New("retained work outcome awaits its settlement owner")
		}
	}
	if owned {
		if p.WorkspaceID != run.WorkspaceID || p.AgentID != run.AgentID || (p.CrewID != "" && p.CrewID != run.CrewID) {
			return errors.New("retained work scope differs")
		}
		status, result = p.Status, p.Result
	}
	kind := journal.EntryRunFailed
	if status == "COMPLETED" {
		kind = journal.EntryRunCompleted
	}
	if status == "CANCELLED" {
		kind = journal.EntryRunCancelled
	}
	if status == "TIMEOUT" {
		kind = journal.EntryRunTimeout
	}
	terminalID := "run-terminal:" + run.ID
	if owned {
		terminalID = "work-terminal:" + run.ID
	}
	if prior, exists, err := s.journalWriter.ReplayTerminal(ctx, terminalID, run.WorkspaceID, run.AgentID, run.ID, result.ExitCode); err != nil {
		return err
	} else if exists {
		if prior != kind {
			return errors.New("retained result conflicts with durable terminal receipt")
		}
		if owned {
			if err := store.MarkRunProjected(ctx, run.ID, status); err != nil {
				return err
			}
		}
		return s.orchestrator.AcknowledgeRetainedRunResult(ctx, run, snapshot, status)
	}
	var existing, existingPayload string
	err = s.db.QueryRowContext(ctx, `SELECT entry_type,payload FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout') LIMIT 1`, run.WorkspaceID, run.ID).Scan(&existing, &existingPayload)
	if err == nil {
		var prior struct {
			ExitCode *int `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(existingPayload), &prior); err != nil {
			return err
		}
		if (prior.ExitCode == nil) != (result.ExitCode == nil) || (prior.ExitCode != nil && *prior.ExitCode != *result.ExitCode) {
			return errors.New("retained result conflicts with terminal journal exit")
		}
		if existing != string(kind) {
			return errors.New("retained result conflicts with terminal journal")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		started := run.StartedAt
		var ts string
		if lookupErr := s.db.QueryRowContext(ctx, `SELECT ts FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=? AND entry_type='run.started' LIMIT 1`, run.WorkspaceID, run.AgentID, run.ID).Scan(&ts); lookupErr == nil {
			started, err = time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return err
			}
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		entry := journal.RunTerminalReplay(run.ID, run.WorkspaceID, run.AgentID, started, kind, owned, result.ExitCode, result.ErrorMessage, result.Metadata)
		if _, _, err = s.journalWriter.EmitReplaySync(ctx, entry); err != nil {
			return err
		}
	} else {
		return err
	}
	if owned {
		if err := store.MarkRunProjected(ctx, run.ID, status); err != nil {
			return err
		}
	}
	return s.orchestrator.AcknowledgeRetainedRunResult(ctx, run, snapshot, status)
}
