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

type publicationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// loadGenericSelected binds authority classification to the exact SQL snapshot
// consumed by Run or call_pipeline. HEAD0 retains its historical legacy behavior;
// positive pins select immutable archive content without changing live governance.
func (s *Store) loadGenericSelected(ctx context.Context, id, workspace string, pin *int) (*Pipeline, error) {
	if pin != nil && *pin < 1 {
		return nil, ErrPinnedVersionNotFound
	}
	var head int
	var versionID, raw, hash, fingerprint sql.NullString
	var noManifest, fingerprintText, physicalText bool
	var physical string
	p, err := scanPipeline(s.db.QueryRowContext(ctx, `SELECT `+pipelineColumns+`,head_version,
 selected_id,selected_raw,selected_hash,selected_fingerprint,selected_manifest IS NULL,typeof(selected_fingerprint)='text',publication_authority_fingerprint,typeof(publication_authority_fingerprint)='text'
 FROM pipelines LEFT JOIN (
 SELECT pipeline_id,version AS selected_version,id AS selected_id,
 definition_json AS selected_raw,definition_hash AS selected_hash,
 authority_fingerprint AS selected_fingerprint,publication_authority_json AS selected_manifest
 FROM pipeline_versions) v ON v.pipeline_id=pipelines.id AND v.selected_version=COALESCE(?,head_version)
 WHERE pipelines.id=? AND (?='' OR workspace_id=?) AND deleted_at IS NULL`, pin, id, workspace, workspace),
		&head, &versionID, &raw, &hash, &fingerprint, &noManifest, &fingerprintText, &physical, &physicalText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load generic recipe: %w", err)
	}
	if pin == nil && head < 0 {
		return nil, ErrNotFound
	}
	if err := classifyGenericSelection(pin, versionID, fingerprint, fingerprintText, noManifest, physical, physicalText); err != nil {
		if pin != nil && errors.Is(err, ErrPinnedVersionNotFound) {
			return nil, fmt.Errorf("pinned version %d: %w", *pin, err)
		}
		return nil, err
	}
	if pin != nil {
		p.DefinitionJSON = raw.String
		p.DefinitionHash = hash.String
	}

	return p, nil
}

func checkGenericPublication(ctx context.Context, q publicationQuery, id, workspace string, version *int) error {
	if version != nil && *version < 1 {
		return ErrPinnedVersionNotFound
	}
	var selected int
	var archive, fingerprint sql.NullString
	var noManifest, fingerprintText, physicalText bool
	var physical string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(?,p.head_version),v.id,v.authority_fingerprint,v.publication_authority_json IS NULL,typeof(v.authority_fingerprint)='text',p.publication_authority_fingerprint,typeof(p.publication_authority_fingerprint)='text'
 FROM pipelines p LEFT JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=COALESCE(?,p.head_version)
 WHERE p.id=? AND p.workspace_id=? AND p.deleted_at IS NULL`, version, version, id, workspace).Scan(&selected, &archive, &fingerprint, &noManifest, &fingerprintText, &physical, &physicalText)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load generic trigger recipe: %w", err)
	}
	if selected < 0 {
		return ErrNotFound
	}
	// A future trusted pin keeps its legacy write contract; execution still requires its archive.
	if version != nil && !archive.Valid {
		return checkGenericPublication(ctx, q, id, workspace, nil)
	}
	return classifyGenericSelection(version, archive, fingerprint, fingerprintText, noManifest, physical, physicalText)
}

// Physical HEAD retains legacy pending-body semantics; an archive pin selects
// its own authority. Known bounded archives also remain inert after old restores.
func classifyGenericSelection(pin *int, archive, fingerprint sql.NullString, fingerprintText, noManifest bool, physical string, physicalText bool) error {
	if pin != nil && !archive.Valid {
		return ErrPinnedVersionNotFound
	}
	if pin == nil && (!physicalText || physical != access.LegacyPublicationAuthorityFingerprint) {
		return ErrPublicationGenericRun
	}
	if archive.Valid && (!fingerprintText || !fingerprint.Valid || fingerprint.String != access.LegacyPublicationAuthorityFingerprint || !noManifest) {
		return ErrPublicationGenericRun
	}
	return nil
}

func checkGenericTrigger(ctx context.Context, q publicationQuery, workspace, id string, version *int) error {
	err := checkGenericPublication(ctx, q, id, workspace, version)
	if errors.Is(err, ErrPublicationGenericRun) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrPinnedVersionNotFound) {
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
