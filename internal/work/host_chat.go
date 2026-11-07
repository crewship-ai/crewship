package work

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const DomainHostChatTurn = "host_chat_turn"

// HostChatRequest is supplied only after current human chat authority has been
// validated by the host. The existing chat run ID remains the attempt identity.
type HostChatRequest struct {
	RunID, WorkspaceID, AgentID, CrewID, ChatID, UserID, TriggerMessageID string
}

type HostChatAttempt struct {
	WorkID, RunID, WorkspaceID, AgentID, CrewID string
	Generation                                  int64
	State                                       State
	Phase                                       string
	Ended                                       bool
}

func (s *Store) HostChatAttempt(ctx context.Context, runID string) (HostChatAttempt, bool, error) {
	var b HostChatAttempt
	var itemGeneration int64
	var ended sql.NullString
	b.RunID = runID
	err := s.db.QueryRowContext(ctx, `SELECT w.id,w.workspace_id,w.agent_id,COALESCE(w.crew_id,''),w.state,w.generation,a.generation,a.runtime_phase,a.ended_at
 FROM work_attempts a JOIN work_items w ON w.id=a.work_id
 WHERE a.run_id=? AND w.source='chat' AND w.domain_kind=?`, runID, DomainHostChatTurn).Scan(&b.WorkID, &b.WorkspaceID, &b.AgentID, &b.CrewID, &b.State, &itemGeneration, &b.Generation, &b.Phase, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return b, false, nil
	}
	if err != nil {
		return b, false, err
	}
	b.Ended = ended.Valid
	if itemGeneration != b.Generation {
		return b, true, ErrStaleGeneration
	}
	return b, true, nil
}

// BeginHostChat uses the same claim capacity/fencing helpers as the dispatcher.
// It never manufactures a memory-only authorization row or expands capacity.
func (s *Store) BeginHostChat(ctx context.Context, r HostChatRequest) (*Claimed, bool, error) {
	if r.RunID == "" || r.WorkspaceID == "" || r.AgentID == "" || r.ChatID == "" || r.UserID == "" || r.TriggerMessageID == "" || strings.ContainsAny(r.RunID, "/\\:\x00") {
		return nil, false, fmt.Errorf("%w: host chat identity incomplete", ErrNotBound)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id string
	var generation int64
	var ended sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT work_id,generation,ended_at FROM work_attempts WHERE run_id=?`, r.RunID).Scan(&id, &generation, &ended)
	if err == nil {
		item, e := getItemTx(ctx, tx, id)
		if e != nil {
			return nil, false, e
		}
		if item.Source != SourceChat || item.DomainKind != DomainHostChatTurn || item.WorkspaceID != r.WorkspaceID || item.AgentID != r.AgentID || item.CrewID != r.CrewID || item.SessionID != r.ChatID || item.AuthorizedByUserID != r.UserID || item.SourceRef != r.TriggerMessageID {
			return nil, false, ErrNotBound
		}
		if ended.Valid || item.Generation != generation || !item.State.Live() {
			return nil, false, ErrStaleGeneration
		}
		if e = tx.Commit(); e != nil {
			return nil, false, e
		}
		return &Claimed{Item: item, RunID: r.RunID, Generation: generation}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	input, _ := json.Marshal(struct{ ChatID, TriggerMessageID string }{r.ChatID, r.TriggerMessageID})
	inputHash := sha256.Sum256(input)
	receipt, err := s.AcceptTx(ctx, tx, AcceptRequest{WorkspaceID: r.WorkspaceID, Source: SourceChat, SourceRef: r.TriggerMessageID, DomainKind: DomainHostChatTurn, DomainID: r.RunID, AgentID: r.AgentID, CrewID: r.CrewID, SessionID: r.ChatID, Class: ClassChat, AuthorizedByUserID: r.UserID, AuthorizedScope: "chat:send", InputJSON: string(input), InputSHA256: hex.EncodeToString(inputHash[:])})
	if err != nil {
		return nil, false, err
	}
	item, err := getItemTx(ctx, tx, receipt.WorkID)
	if err != nil {
		return nil, false, err
	}
	live, err := s.countLiveTx(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	admitted, err := s.admitsTx(ctx, tx, item, SerialAgentLimits(), live)
	if err != nil {
		return nil, false, err
	}
	if !admitted {
		return nil, false, ErrNoWork
	}
	claimed, err := s.startAttemptWithRunIDTx(ctx, tx, item, "host-chat:"+r.RunID, s.now().UTC(), r.RunID)
	if err != nil {
		return nil, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_attempts SET runtime_locator=?,runtime_phase='starting' WHERE run_id=? AND work_id=? AND generation=? AND ended_at IS NULL AND runtime_phase='planned'`, "agent-run:"+r.RunID, r.RunID, item.ID, claimed.Generation)
	if err != nil {
		return nil, false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, false, ErrNotBound
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return claimed, true, nil
}

func (s *Store) HostChatPhase(ctx context.Context, runID, phase string) error {
	b, owned, err := s.HostChatAttempt(ctx, runID)
	if err != nil {
		return err
	}
	if !owned || b.Ended || !b.State.Live() {
		return ErrNotBound
	}
	switch phase {
	case "request":
		// A second creation request is not an idempotent heartbeat: it could
		// launch two processes with the same capability and generation.
		if b.Phase != runtimePhaseStarting {
			return ErrNotBound
		}
		return s.MarkRuntimeRequested(ctx, b.WorkID, runID, b.Generation)
	case "started":
		if b.Phase == runtimePhaseRequested {
			return s.StartRunning(ctx, b.WorkID, runID, b.Generation, "agent-run:"+runID)
		}
		if b.Phase != runtimePhaseConfirmed {
			return ErrNotBound
		}
	case "heartbeat":
	default:
		return fmt.Errorf("unknown host chat lifecycle phase")
	}
	return s.Heartbeat(ctx, runID, b.Generation)
}

// HostChatResult contains only host-observed lifecycle proof. A model result or
// captured timestamp alone cannot close an attempt after execution was requested.
type HostChatResult struct {
	Result       RunResult
	Status       string
	ObservedExit *int
	ExecRefused  bool
}

func (s *Store) FinishHostChat(ctx context.Context, runID string, r HostChatResult) error {
	b, owned, err := s.HostChatAttempt(ctx, runID)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotBound
	}
	if b.Ended {
		projection, _, err := s.RunProjection(ctx, runID)
		if err == nil && projection.Ready {
			return nil
		}
		return ErrStaleGeneration
	}
	// A separately supplied display exit can never overrule observed process proof.
	r.Result.ExitCode = r.ObservedExit
	encoded, err := json.Marshal(r.Result)
	if err != nil || len(encoded) > 512*1024 {
		return ErrRunResultUnstored
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	item, err := getItemTx(ctx, tx, b.WorkID)
	if err != nil {
		return err
	}
	if err = verifyAttemptBindingTx(ctx, tx, b.WorkID, runID, b.Generation, item.Generation); err != nil {
		return err
	}
	var phase string
	if err = tx.QueryRowContext(ctx, `SELECT runtime_phase FROM work_attempts WHERE run_id=? AND work_id=? AND generation=?`, runID, b.WorkID, b.Generation).Scan(&phase); err != nil {
		return err
	}
	known := phase == runtimePhasePlanned || phase == runtimePhaseStarting || r.ObservedExit != nil || r.ExecRefused
	target := StateNeedsReconciliation
	if known {
		switch r.Status {
		case "COMPLETED":
			if r.ObservedExit == nil || *r.ObservedExit != 0 {
				return fmt.Errorf("completion needs an observed zero exit")
			}
			target = StateSucceeded
		case "FAILED", "TIMEOUT":
			target = StateFailed
		case "CANCELLED":
			target = StateCancelled
		default:
			return fmt.Errorf("host chat result is not terminal")
		}
	}
	if !CanTransition(item.State, target) {
		return ErrIllegalTransition
	}
	now := s.now().UTC()
	reason := "host chat process exit observed"
	evidence := "creation_not_requested"
	if r.ObservedExit != nil {
		evidence = fmt.Sprintf("provider_exit:%d", *r.ObservedExit)
	} else if r.ExecRefused {
		evidence = "creation_gate_refused"
		reason = "agent creation gate refused before process creation"
	} else {
		reason = "host turn returned before agent execution was requested"
	}
	status := r.Status
	if !known {
		reason = "protected host capture awaiting retained result"
		evidence = "execution_outcome_unknown"
		status = ""
	}
	if err = s.setStateTx(ctx, tx, item, target, runID, b.Generation, reason, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_attempts SET ended_at=?,end_reason=?,exit_evidence=?,run_status=?,run_result_json=COALESCE(run_result_json,?) WHERE run_id=? AND work_id=? AND generation=? AND ended_at IS NULL`, tsformat.Format(now), reason, evidence, status, string(encoded), runID, b.WorkID, b.Generation)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrNotBound
	}
	return tx.Commit()
}
