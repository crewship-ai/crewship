package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/work"
)

type assignmentWorkInput = work.AssignmentInput

func acceptAssignmentTx(ctx context.Context, tx *sql.Tx, id string) (work.Receipt, error) {
	return work.AcceptAssignmentTx(ctx, tx, id)
}

func assignmentWorkID(ctx context.Context, db *sql.DB, id string) (string, error) {
	var workID string
	err := db.QueryRowContext(ctx, `SELECT id FROM work_items WHERE source=? AND domain_kind=? AND domain_id=? AND replay_of IS NULL`, work.SourceAssignment, work.DomainAssignment, id).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return workID, err
}
