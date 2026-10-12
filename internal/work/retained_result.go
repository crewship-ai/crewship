package work

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RecoverRetainedResult settles only the current attempt parked by lease
// recovery. The caller must first validate the protected host capture bound to
// runID. Live workers and operator resolutions retain their own authority.
// Failed execution may have external effects, so it keeps reconciliation and
// its capacity fence; a real successful exit can complete the work.
func (s *Store) RecoverRetainedResult(ctx context.Context, runID string, result RunResult, successful bool) (bool, error) {
	if runID == "" || result.ExitCode == nil || successful && *result.ExitCode != 0 {
		return false, fmt.Errorf("%w: retained result requires an actual exit", ErrNotBound)
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > 512*1024 {
		return false, errors.New("work: invalid retained result")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var workID, phase, locator, reason, status string
	var generation int64
	var ended sql.NullString
	var staged sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT work_id,generation,runtime_phase,runtime_locator,ended_at,end_reason,run_status,run_result_json FROM work_attempts WHERE run_id=?`, runID).
		Scan(&workID, &generation, &phase, &locator, &ended, &reason, &status, &staged)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	it, err := getItemTx(ctx, tx, workID)
	if err != nil {
		return true, err
	}
	if it.Generation != generation {
		return true, ErrStaleGeneration
	}
	if status != "" {
		// A previously confirmed outcome is authoritative, including an
		// operator resolution. Never replace it with a later capture.
		return true, tx.Commit()
	}
	// Kept as a literal here to avoid a work -> dispatch import cycle.
	parked := strings.HasPrefix(reason, LeaseExpiredReasonPrefix+" in runtime phase ") || reason == "protected host capture awaiting retained result"
	if it.State != StateNeedsReconciliation || !ended.Valid || !parked || phase == runtimePhasePlanned || locator != "agent-run:"+runID {
		return true, fmt.Errorf("%w: attempt was not parked by lease recovery", ErrNotBound)
	}
	if staged.Valid {
		var prior RunResult
		decoder := json.NewDecoder(strings.NewReader(staged.String))
		decoder.UseNumber()
		if err := decoder.Decode(&prior); err != nil {
			return true, err
		}
		// An incomplete observation may be replaced by the actual capture.
		// Conflicting previously captured terminal facts require reconciliation.
		priorData, err := json.Marshal(prior)
		if err != nil {
			return true, err
		}
		if prior.ExitCode != nil && !bytes.Equal(priorData, data) {
			return true, errors.New("work: staged terminal result conflicts with protected capture")
		}
	}
	now := s.now().UTC()
	status = "FAILED"
	if successful {
		status = "COMPLETED"
		if err := s.setStateTx(ctx, tx, it, StateSucceeded, runID, generation, "protected retained capture confirmed successful execution", now); err != nil {
			return true, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE work_attempts SET run_result_json=?,run_status=?,exit_evidence=?,end_reason=?,ended_at=? WHERE run_id=? AND work_id=? AND generation=? AND run_status=''`,
		string(data), status, "protected host capture", "protected retained capture confirmed execution exit", tsformat.Format(now), runID, workID, generation)
	if err != nil {
		return true, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return true, fmt.Errorf("%w: retained settlement changed binding", ErrNotBound)
	}
	return true, tx.Commit()
}
