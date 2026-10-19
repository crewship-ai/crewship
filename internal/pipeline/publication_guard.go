package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/access"
)

// ErrPublicationGenericRun refuses to interpret publication authority as a
// trusted generic recipe. Only the bounded manual consumer may execute it.
var ErrPublicationGenericRun = errors.New("publication requires bounded manual execution")

// ErrGenericRecipeChanged rejects a body loaded before a different selection.
var ErrGenericRecipeChanged = errors.New("generic recipe changed before execution")

type publicationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CheckGenericRun checks the exact stored version and the caller's loaded body.
// It grants no execution authority and never treats a read error as legacy.
func (s *Store) CheckGenericRun(ctx context.Context, p *Pipeline, version *int) error {
	if s == nil || s.db == nil || p == nil {
		return errors.New("generic recipe storage unavailable")
	}
	return checkGenericPublication(ctx, s.db, p.ID, p.WorkspaceID, version, p.DefinitionJSON, p.DefinitionHash)
}

func checkGenericPublication(ctx context.Context, q publicationQuery, id, workspace string, version *int, expectedRaw, expectedHash string) error {
	var head int
	var raw, hash string
	err := q.QueryRowContext(ctx, `SELECT head_version,definition_json,definition_hash FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, id, workspace).Scan(&head, &raw, &hash)
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
		err = q.QueryRowContext(ctx, `SELECT definition_json,definition_hash,authority_fingerprint,typeof(authority_fingerprint),publication_authority_json IS NULL FROM pipeline_versions WHERE pipeline_id=? AND version=?`, id, selected).Scan(&raw, &hash, &fingerprint, &fingerprintType, &noManifest)
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
	// HEAD0 keeps its existing legacy contract. Numeric pins never become HEAD0.
	if (expectedRaw != "" && expectedRaw != raw) || (expectedHash != "" && expectedHash != hash) {
		return ErrGenericRecipeChanged
	}
	return nil
}

func checkGenericTrigger(ctx context.Context, q publicationQuery, workspace, id string, version *int) error {
	err := checkGenericPublication(ctx, q, id, workspace, version, "", "")
	if errors.Is(err, ErrPublicationGenericRun) || errors.Is(err, ErrGenericRecipeChanged) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrPinnedVersionNotFound) {
		return fmt.Errorf("%w: %w", ErrInvalidTrigger, err)
	}
	return err
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
