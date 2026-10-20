package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/access"
)

// ErrPublicationGenericRun refuses to interpret publication authority as a
// trusted generic recipe. Only the bounded manual consumer may execute it.
var ErrPublicationGenericRun = errors.New("publication requires bounded manual execution")

type publicationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CheckGenericRun classifies the selected stored version for the generic runner.
// It grants no execution authority and never treats a read error as legacy.
func (s *Store) CheckGenericRun(ctx context.Context, p *Pipeline, version *int) error {
	if s == nil || s.db == nil || p == nil {
		return errors.New("generic recipe storage unavailable")
	}
	return checkGenericPublication(ctx, s.db, p.ID, p.WorkspaceID, version)
}

func checkGenericPublication(ctx context.Context, q publicationQuery, id, workspace string, version *int) error {
	var head int
	err := q.QueryRowContext(ctx, `SELECT head_version FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, id, workspace).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load generic recipe: %w", err)
	}
	selected := head
	if version != nil {
		selected = *version
	}
	if selected < 0 || (version != nil && selected == 0) {
		return ErrNotFound
	}
	if selected > 0 {
		var fingerprint, fingerprintType string
		var noManifest bool
		err = q.QueryRowContext(ctx, `SELECT authority_fingerprint,typeof(authority_fingerprint),publication_authority_json IS NULL FROM pipeline_versions WHERE pipeline_id=? AND version=?`, id, selected).Scan(&fingerprint, &fingerprintType, &noManifest)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPinnedVersionNotFound
		}
		if err != nil {
			return fmt.Errorf("load generic recipe version: %w", err)
		}
		if fingerprintType != "text" || fingerprint != access.LegacyPublicationAuthorityFingerprint || !noManifest {
			return ErrPublicationGenericRun
		}
	}
	// HEAD0 retains its existing legacy contract.
	return nil
}

func checkGenericTrigger(ctx context.Context, q publicationQuery, workspace, id string, version *int) error {
	err := checkGenericPublication(ctx, q, id, workspace, version)
	if errors.Is(err, ErrPublicationGenericRun) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrPinnedVersionNotFound) {
		return fmt.Errorf("%w: %w", ErrInvalidTrigger, err)
	}
	return err
}

// Authoring rejects known published nested targets before storing a caller.
// Missing targets retain the existing forward-reference contract; resolution
// and the same publication classification still run at nested execution.
func checkPublicationCalls(ctx context.Context, q publicationQuery, workspace, raw string) error {
	var definition DSL
	if json.Unmarshal([]byte(raw), &definition) != nil {
		return nil // Existing validation owns syntax; this is not another parser gate.
	}
	var check func([]Step) error
	check = func(steps []Step) error {
		for _, step := range steps {
			if step.Type == StepCallPipeline {
				var target string
				err := q.QueryRowContext(ctx, `SELECT id FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, workspace, step.PipelineSlug).Scan(&target)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil {
					if err := checkGenericTrigger(ctx, q, workspace, target, nil); err != nil {
						return fmt.Errorf("call_pipeline %q: %w", step.PipelineSlug, err)
					}
				}
			}
			if step.Foreach != nil {
				if err := check(step.Foreach.Steps); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(definition.Steps)
}

// CheckGenericScheduleIn validates the stored target, numeric pin and wake in
// the caller's writer transaction before an inline upsert or approval commits.
func CheckGenericScheduleIn(ctx context.Context, q publicationQuery, workspace, scheduleID string) error {
	var target string
	var wake sql.NullString
	var pin sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT target_pipeline_id,target_pipeline_version,wake_pipeline_id FROM pipeline_schedules WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, scheduleID, workspace).Scan(&target, &pin, &wake)
	if errors.Is(err, sql.ErrNoRows) {
		// A deleted legacy approval has no schedule to enable. Keep that
		// existing no-op; a missing selected recipe is refused below.
		return nil
	}
	if err != nil {
		return err
	}
	var version *int
	if pin.Valid {
		selected := int(pin.Int64)
		version = &selected
	}
	if err := checkGenericTrigger(ctx, q, workspace, target, version); err != nil {
		return err
	}
	if wake.Valid && wake.String != "" {
		return checkGenericTrigger(ctx, q, workspace, wake.String, nil)
	}
	return nil
}
