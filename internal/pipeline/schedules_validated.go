package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// SaveValidated is the public schedule-write boundary. Recheck the effective
// preset in the same transaction as the write: an API preflight may have seen
// a recipe that was replaced before this transaction acquired its write lock.
// Raw Save remains available for fixture creation and internally composed writes.
func (s *ScheduleStore) SaveValidated(ctx context.Context, in SaveScheduleInput) (*Schedule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("SaveValidated: begin: %w", err)
	}
	defer tx.Rollback()
	var before, saved *Schedule
	if in.ID == "" {
		saved, err = createSchedule(ctx, tx, in)
	} else {
		before, err = getScheduleByID(ctx, tx, in.ID)
		if err == nil {
			saved, err = updateSchedule(ctx, tx, in)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("SaveValidated: write: %w", err)
	}
	if err = validateEnabledSchedule(ctx, tx, before, saved); err != nil {
		return nil, fmt.Errorf("SaveValidated: validate preset: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("SaveValidated: commit: %w", err)
	}
	return saved, nil
}

// ActivateValidated rolls back the activation as well as the enabled flag when
// the draft's preset no longer satisfies the recipe selected at commit time.
func (s *ScheduleStore) ActivateValidated(ctx context.Context, id string) (*Schedule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("ActivateValidated: begin: %w", err)
	}
	defer tx.Rollback()
	saved, err := activateSchedule(ctx, tx, id)
	if err != nil {
		return nil, fmt.Errorf("ActivateValidated: write: %w", err)
	}
	if err = validateEnabledSchedule(ctx, tx, nil, saved); err != nil {
		return nil, fmt.Errorf("ActivateValidated: validate preset: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("ActivateValidated: commit: %w", err)
	}
	return saved, nil
}

func validateEnabledSchedule(ctx context.Context, x sqlExecQuerier, before, after *Schedule) error {
	// Removing execution capacity must remain possible for a broken or newly
	// bounded recipe. Only an existing unchanged selection may take this path;
	// disabled creates and retargets still pass the authority checks below.
	if before != nil && !after.Enabled && before.WorkspaceID == after.WorkspaceID &&
		before.TargetPipelineID == after.TargetPipelineID &&
		reflect.DeepEqual(before.TargetPipelineVersion, after.TargetPipelineVersion) &&
		before.WakePipelineID == after.WakePipelineID {
		return nil
	}
	// Keep the authority check inside this writer transaction, including draft
	// creates. Publication checks existing triggers under the same write lock.
	if err := checkGenericTrigger(ctx, x, after.WorkspaceID, after.TargetPipelineID, after.TargetPipelineVersion); err != nil {
		return err
	}
	if after.WakePipelineID != "" {
		if err := checkGenericTrigger(ctx, x, after.WorkspaceID, after.WakePipelineID, nil); err != nil {
			return err
		}
	}
	if !after.Enabled {
		return nil
	}
	enabling := before == nil || !before.Enabled
	if enabling || before.TargetPipelineID != after.TargetPipelineID ||
		!reflect.DeepEqual(before.TargetPipelineVersion, after.TargetPipelineVersion) ||
		!sameScheduleInputs(before.InputsJSON, after.InputsJSON) {
		if err := validateStoredSchedulePreset(ctx, x, after.TargetPipelineID, after.TargetPipelineVersion, after.InputsJSON); err != nil {
			return err
		}
	}
	if after.WakePipelineID != "" && (enabling || before.WakePipelineID != after.WakePipelineID ||
		!sameScheduleInputs(before.WakeInputsJSON, after.WakeInputsJSON)) {
		if err := validateStoredSchedulePreset(ctx, x, after.WakePipelineID, nil, after.WakeInputsJSON); err != nil {
			return err
		}
	}
	return nil
}

func sameScheduleInputs(a, b string) bool {
	if a == b {
		return true
	}
	var av, bv map[string]any
	return json.Unmarshal([]byte(a), &av) == nil && json.Unmarshal([]byte(b), &bv) == nil && reflect.DeepEqual(av, bv)
}

func validateStoredSchedulePreset(ctx context.Context, x sqlExecQuerier, id string, version *int, raw string) error {
	var definition string
	var err error
	if version == nil {
		err = x.QueryRowContext(ctx, `SELECT definition_json FROM pipelines WHERE id=? AND deleted_at IS NULL`, id).Scan(&definition)
	} else {
		err = x.QueryRowContext(ctx, `SELECT v.definition_json FROM pipeline_versions v JOIN pipelines p ON p.id=v.pipeline_id WHERE v.pipeline_id=? AND v.version=? AND p.deleted_at IS NULL`, id, *version).Scan(&definition)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: schedule recipe or version does not exist", ErrInvalidTrigger)
	}
	if err != nil {
		return fmt.Errorf("load schedule recipe: %w", err)
	}
	dsl, err := Parse([]byte(definition))
	if err != nil {
		return fmt.Errorf("%w: invalid schedule recipe: %v", ErrInvalidTrigger, err)
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
		return fmt.Errorf("%w: unreadable schedule inputs", ErrInvalidTrigger)
	}
	if err := ValidateFormInputs(dsl, inputs); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTrigger, err)
	}
	return nil
}
