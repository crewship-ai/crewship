package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var errKeeperRunScope = errors.New("keeper calling run scope unavailable")

func keeperStoredRun(ctx context.Context, tx *sql.Tx, requestID string) (runID, requestType string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(requesting_run_id,''),COALESCE(request_type,'access') FROM keeper_requests WHERE id=?`, requestID).Scan(&runID, &requestType)
	return
}

// This mirrors the journal-run authority used by provider admission. Validate
// only a new request/execution; a later human decision still belongs to the
// original run even if that run has since ended.
func validateKeeperRunTx(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workspace, agent, crew, run string) error {
	if run == "" {
		return nil
	}
	var starts, ended int
	var recordedAgent, recordedCrew string
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(agent_id),''),COALESCE(MAX(crew_id),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type='run.started'`, workspace, run).Scan(&starts, &recordedAgent, &recordedCrew); err != nil {
		return fmt.Errorf("keeper run attribution unavailable: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')`, workspace, run).Scan(&ended); err != nil {
		return fmt.Errorf("keeper run attribution unavailable: %w", err)
	}
	if starts != 1 || recordedAgent != agent || recordedCrew != crew || ended != 0 {
		return errKeeperRunScope
	}
	return nil
}
