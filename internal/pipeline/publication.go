package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ErrPublicationHasTriggers distinguishes unsupported v1 authoring from a
// stale draft revision. The caller must explicitly remove the trigger first.
var ErrPublicationHasTriggers = errors.New("publication requires removing existing triggers")

// ErrPublicationHasCallers refuses publishing a routine that saved routines
// already reach through call_pipeline; nested calls cannot run bounded authority.
var ErrPublicationHasCallers = errors.New("publication requires removing call_pipeline steps that target this routine")

// PublishDraftIn consumes an authorized draft; its caller owns the writer, live
// authorization, audit and commit. The stored identity cannot authorize execution.
func (s *Store) PublishDraftIn(ctx context.Context, tx *sql.Tx, publisher string, draft Draft, canonical []byte, fingerprint string) (int, error) {
	if tx == nil {
		return 0, errors.New("publication writer transaction required")
	}
	authority, err := access.DecodePublicationAuthority(canonical, fingerprint)
	if err != nil || authority.WorkspaceID != draft.WorkspaceID || authority.PipelineID != draft.BasePipelineID || authority.PublisherID != publisher {
		return 0, access.ErrDenied
	}
	var content struct {
		Name        string          `json:"name"`
		Description *string         `json:"description"`
		Definition  json.RawMessage `json:"definition"`
	}
	if err := json.Unmarshal(draft.Document, &content); err != nil || content.Name == "" || !json.Valid(content.Definition) {
		return 0, errors.New("invalid stored publication draft")
	}
	var head, revision int
	var origin sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT head_version,publication_revision,chat_origin_id FROM pipelines WHERE id=? AND workspace_id=? AND slug=? AND deleted_at IS NULL`, draft.BasePipelineID, draft.WorkspaceID, draft.Slug).Scan(&head, &revision, &origin)
	if err != nil {
		return 0, err
	}
	if revision != draft.BaseRevision || origin.String != draft.ChatOriginID {
		return 0, ErrDraftConflict
	}
	// Existing triggers must not silently acquire a new executable authority.
	// They can be removed explicitly before publishing this v1 subset.
	var triggers bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_schedules WHERE deleted_at IS NULL AND (target_pipeline_id=? OR wake_pipeline_id=?)) OR EXISTS(SELECT 1 FROM pipeline_webhooks WHERE target_pipeline_id=? AND deleted_at IS NULL)`, draft.BasePipelineID, draft.BasePipelineID, draft.BasePipelineID).Scan(&triggers)
	if err != nil {
		return 0, err
	}
	if triggers {
		return 0, ErrPublicationHasTriggers
	}
	// Saving a caller already refuses a published target; this is the reverse
	// order. Publication is rare, so one bounded scan of this workspace's live
	// recipes (any nesting depth) is acceptable inside the writer.
	var callers bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipelines p, json_tree(CASE WHEN json_valid(p.definition_json) THEN p.definition_json ELSE '{}' END) t
 WHERE p.workspace_id=? AND p.id!=? AND p.deleted_at IS NULL
 AND t.type='object' AND json_extract(t.value,'$.type')='call_pipeline' AND json_extract(t.value,'$.pipeline_slug')=?)`, draft.WorkspaceID, draft.BasePipelineID, draft.Slug).Scan(&callers)
	if err != nil {
		return 0, err
	}
	if callers {
		return 0, ErrPublicationHasCallers
	}
	raw, hash := string(content.Definition), definitionHash(string(content.Definition))
	var version int
	var storedRaw, storedManifest string
	err = tx.QueryRowContext(ctx, `SELECT version,definition_json,publication_authority_json FROM pipeline_versions WHERE pipeline_id=? AND definition_hash=? AND authority_fingerprint=?`, draft.BasePipelineID, hash, fingerprint).Scan(&version, &storedRaw, &storedManifest)
	if err == nil {
		if storedRaw != raw || storedManifest != string(canonical) {
			return 0, errors.New("stored publication identity mismatch")
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM pipeline_versions WHERE pipeline_id=?`, draft.BasePipelineID).Scan(&version); err != nil {
			return 0, err
		}
		var parent any
		if head > 0 {
			parent = head
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,parent_version,change_summary,created_at,chat_origin_id,authority_fingerprint,publication_authority_json) VALUES(?,?,?,?,?,'user',?,?,'Published shared draft',?,?,?,?)`, generateVersionID(), draft.BasePipelineID, version, raw, hash, publisher, parent, tsformat.Format(time.Now()), origin, fingerprint, string(canonical))
		if err != nil {
			return 0, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return 0, errors.New("publication version was not stored")
		}
	} else {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM pipeline_drafts WHERE id=? AND workspace_id=? AND revision=? AND base_pipeline_id=? AND base_revision=? AND document_json=?`, draft.ID, draft.WorkspaceID, draft.Revision, draft.BasePipelineID, draft.BaseRevision, string(draft.Document))
	if err != nil {
		return 0, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, ErrDraftConflict
	}
	// The existing publication trigger increments changed recipes. An exact
	// content/authority dedup still consumes a draft and advances its base once.
	result, err = tx.ExecContext(ctx, `UPDATE pipelines SET publication_revision=CASE WHEN name IS ? AND description IS ? AND definition_json IS ? AND head_version IS ? THEN publication_revision+1 ELSE publication_revision END,name=?,description=?,definition_json=?,definition_hash=?,head_version=?,publication_authority_fingerprint=?,publication_withdrawn_at=NULL,updated_at=? WHERE id=? AND workspace_id=? AND deleted_at IS NULL AND publication_revision=?`, content.Name, content.Description, raw, version, content.Name, content.Description, raw, hash, version, fingerprint, tsformat.Format(time.Now()), draft.BasePipelineID, draft.WorkspaceID, draft.BaseRevision)
	if err != nil {
		return 0, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return 0, err
	} else if n != 1 {
		return 0, ErrDraftConflict
	}
	return version, nil
}
