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

// StateEntry is one durable key with its write timestamp — the operator-facing
// projection. The executor only ever needs key→value (see Load), but a human
// debugging a stuck watermark needs to know WHEN it was last written, which is
// usually the tell (a cursor frozen three days ago vs one updated 5 min ago).
type StateEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
}

// StateBucket is one (pipeline, schedule) partition with its entries. The
// empty ScheduleID is the shared manual/webhook bucket.
type StateBucket struct {
	ScheduleID string       `json:"schedule_id"`
	Entries    []StateEntry `json:"entries"`
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

// Buckets returns every (schedule_id → entries) partition a routine has
// written, ordered by schedule then key so output is stable across calls.
//
// Operators reach for this when a routine "stopped seeing new items": the
// watermark lives in a bucket keyed by the SCHEDULE that wrote it, and which
// schedule that was is exactly the thing they don't know. Listing all buckets
// at once is the difference between finding the stuck cursor and guessing.
func (s *RoutineStateStore) Buckets(ctx context.Context, pipelineID string) ([]StateBucket, error) {
	out := []StateBucket{}
	if s == nil || s.db == nil || pipelineID == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT schedule_id, key, value, updated_at
FROM pipeline_routine_state
WHERE pipeline_id = ?
ORDER BY schedule_id, key`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Rows arrive grouped by schedule_id (ORDER BY), so a running pointer to
	// the current bucket is enough — no map + second sort pass.
	var cur *StateBucket
	for rows.Next() {
		var schedID string
		var e StateEntry
		if err := rows.Scan(&schedID, &e.Key, &e.Value, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if cur == nil || cur.ScheduleID != schedID {
			out = append(out, StateBucket{ScheduleID: schedID, Entries: []StateEntry{}})
			cur = &out[len(out)-1]
		}
		cur.Entries = append(cur.Entries, e)
	}
	return out, rows.Err()
}

// Delete removes one key from the (pipeline, schedule) bucket. Reports whether
// a row actually existed so a caller can 404 on a typo'd key instead of
// silently reporting success for a key that was never there.
func (s *RoutineStateStore) Delete(ctx context.Context, pipelineID, scheduleID, key string) (bool, error) {
	if s == nil || s.db == nil || pipelineID == "" || key == "" {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pipeline_routine_state WHERE pipeline_id = ? AND schedule_id = ? AND key = ?`,
		pipelineID, scheduleID, key)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Clear drops every key in one (pipeline, schedule) bucket and returns how many
// rows went. Deliberately bucket-scoped rather than pipeline-wide: wiping every
// schedule's cursor at once is almost never what an operator means, and the
// blast radius (each schedule reprocesses from scratch) is not recoverable.
func (s *RoutineStateStore) Clear(ctx context.Context, pipelineID, scheduleID string) (int64, error) {
	if s == nil || s.db == nil || pipelineID == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM pipeline_routine_state WHERE pipeline_id = ? AND schedule_id = ?`,
		pipelineID, scheduleID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
