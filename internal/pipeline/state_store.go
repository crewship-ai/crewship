package pipeline

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RoutineStateStore is the persistence layer for cross-run routine state
// (#1420): durable key/value pairs scoped per (pipeline_id, schedule_id),
// backing the {{ routine.state.* }} read namespace and the `state_write` step
// binding. It lives alongside pipeline.Store — the executor loads a run's
// bucket at start and writes back as steps declare state_write.
//
// Isolation is the (pipeline_id, schedule_id) key: two schedules of the same
// routine keep independent watermarks; runs with no schedule (manual/webhook)
// share the empty-string bucket per pipeline.
type RoutineStateStore struct {
	db *sql.DB
}

// NewRoutineStateStore returns a store backed by the given DB handle. The
// handle must be migrated to v155+.
func NewRoutineStateStore(db *sql.DB) *RoutineStateStore {
	return &RoutineStateStore{db: db}
}

// Load returns every key→value in the (pipeline, schedule) bucket. An empty
// map (never nil) is returned when the bucket has no rows yet, so the first
// run of a routine reads an empty namespace rather than an error.
func (s *RoutineStateStore) Load(ctx context.Context, pipelineID, scheduleID string) (map[string]string, error) {
	out := map[string]string{}
	if s == nil || s.db == nil || pipelineID == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM pipeline_routine_state WHERE pipeline_id = ? AND schedule_id = ?`,
		pipelineID, scheduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Write upserts one key in the (pipeline, schedule) bucket. Concurrent writers
// (DAG steps in the same run, or overlapping runs) resolve last-write-wins on
// the primary key — the whole point of a watermark is a single latest value.
func (s *RoutineStateStore) Write(ctx context.Context, pipelineID, scheduleID, key, value string) error {
	if s == nil || s.db == nil || pipelineID == "" || key == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_routine_state (pipeline_id, schedule_id, key, value, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (pipeline_id, schedule_id, key)
DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		pipelineID, scheduleID, key, value, tsformat.Format(time.Now()))
	return err
}
