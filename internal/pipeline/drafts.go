package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

var ErrDraftConflict = errors.New("routine draft changed or its published recipe changed; reload and review before publishing")

// ScheduleDraftConflict identifies the preset that prevents publication, so
// clients can open that schedule without parsing an error message.
type ScheduleDraftConflict struct {
	ScheduleID string `json:"schedule_id"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
}

func (e *ScheduleDraftConflict) Error() string {
	return fmt.Sprintf("publication blocked by schedule %s: %s", e.Name, e.Reason)
}
func (e *ScheduleDraftConflict) Unwrap() error { return ErrDraftConflict }

type Draft struct {
	ID             string          `json:"id"`
	WorkspaceID    string          `json:"workspace_id"`
	Slug           string          `json:"slug"`
	Revision       int             `json:"revision"`
	BasePipelineID string          `json:"base_pipeline_id"`
	BaseRevision   int             `json:"base_revision"`
	Document       json.RawMessage `json:"document"`
	UpdatedBy      string          `json:"updated_by"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

// DraftPublication is internal to the validated publication path. The document
// must be exactly the one the handler validated, not a newer edit of the draft.
type DraftPublication struct {
	ApproveRisk bool
	ID          string
	Revision    int
	Document    string
}

func scanDraft(row interface{ Scan(...any) error }) (*Draft, error) {
	var d Draft
	var document string
	err := row.Scan(&d.ID, &d.WorkspaceID, &d.Slug, &d.Revision, &d.BasePipelineID, &d.BaseRevision, &document, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt)
	d.Document = json.RawMessage(document)
	return &d, err
}

const draftColumns = `id, workspace_id, slug, revision, base_pipeline_id, base_revision, document_json, updated_by, created_at, updated_at`

// A missing draft returns a revision-zero baseline from a single published-row
// read. Clients edit this document, then create the draft with that exact base.
func (s *Store) GetDraft(ctx context.Context, ws, slug string) (*Draft, error) {
	d, err := scanDraft(s.db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM pipeline_drafts WHERE workspace_id=? AND slug=?`, ws, slug))
	if !errors.Is(err, sql.ErrNoRows) {
		return d, err
	}
	d = &Draft{WorkspaceID: ws, Slug: slug}
	var definition, name, description, crew, agent string
	err = s.db.QueryRowContext(ctx, `SELECT id, publication_revision, definition_json, name, COALESCE(description,''), COALESCE(author_crew_id,''), COALESCE(author_agent_id,'') FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, ws, slug).Scan(&d.BasePipelineID, &d.BaseRevision, &definition, &name, &description, &crew, &agent)
	if errors.Is(err, sql.ErrNoRows) {
		d.Document = json.RawMessage(`{}`)
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	d.Document, err = json.Marshal(map[string]any{"slug": slug, "name": name, "description": description, "definition": json.RawMessage(definition), "author_crew_id": crew, "author_agent_id": agent})
	return d, err
}

func (s *Store) SaveDraft(ctx context.Context, d Draft) (*Draft, error) {
	if d.WorkspaceID == "" || d.Slug == "" || d.UpdatedBy == "" || !json.Valid(d.Document) || d.Revision < 0 {
		return nil, errors.New("invalid draft")
	}
	now := tsformat.Format(s.now().UTC())
	var saved *Draft
	var err error
	if d.ID == "" && d.Revision == 0 {
		d.ID = generatePipelineID()
		saved, err = scanDraft(s.db.QueryRowContext(ctx, `INSERT INTO pipeline_drafts (`+draftColumns+`) SELECT ?,?,?,1,?,?,?,?,?,? WHERE COALESCE((SELECT id FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL),'')=? AND COALESCE((SELECT publication_revision FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL),0)=? ON CONFLICT(workspace_id,slug) DO NOTHING RETURNING `+draftColumns, d.ID, d.WorkspaceID, d.Slug, d.BasePipelineID, d.BaseRevision, string(d.Document), d.UpdatedBy, now, now, d.WorkspaceID, d.Slug, d.BasePipelineID, d.WorkspaceID, d.Slug, d.BaseRevision))
	} else {
		// Only unpublished drafts can move to an unused slug. Preserve the ID,
		// revision CAS and base metadata so renaming cannot orphan an old draft
		// or turn an edit of a published recipe into a new recipe.
		saved, err = scanDraft(s.db.QueryRowContext(ctx, `UPDATE pipeline_drafts
          SET slug=?,document_json=?,revision=revision+1,updated_by=?,updated_at=?
          WHERE id=? AND workspace_id=? AND revision=?
          AND (slug=? OR (base_pipeline_id='' AND NOT EXISTS (
            SELECT 1 FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL)))
          AND NOT EXISTS (SELECT 1 FROM pipeline_drafts other WHERE other.workspace_id=? AND other.slug=? AND other.id<>?)
          RETURNING `+draftColumns, d.Slug, string(d.Document), d.UpdatedBy, now, d.ID, d.WorkspaceID, d.Revision, d.Slug, d.WorkspaceID, d.Slug, d.WorkspaceID, d.Slug, d.ID))
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDraftConflict
	}
	// RETURNING reads the exact written revision and immutable base metadata,
	// never a subsequent edit or caller-supplied replacement of that base.
	return saved, err
}

func (s *Store) DeleteDraft(ctx context.Context, ws, slug, id string, revision int) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pipeline_drafts WHERE workspace_id=? AND slug=? AND id=? AND revision=?`, ws, slug, id, revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDraftConflict
	}
	return nil
}

// Deleting the draft first takes SQLite's write lock. Every following check,
// recipe/version/trigger write and this deletion either commits together or
// rolls back together. A second editor/publisher therefore cannot race validation.
func (s *Store) consumeDraftTx(ctx context.Context, tx *sql.Tx, in SaveInput) error {
	if in.Publication == nil {
		return nil
	}
	p := in.Publication
	d, err := scanDraft(tx.QueryRowContext(ctx, `DELETE FROM pipeline_drafts WHERE id=? AND workspace_id=? AND slug=? AND revision=? AND document_json=? RETURNING `+draftColumns, p.ID, in.WorkspaceID, in.Slug, p.Revision, p.Document))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDraftConflict
	}
	if err != nil {
		return err
	}
	var id string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT id,publication_revision FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, in.WorkspaceID, in.Slug).Scan(&id, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if id != d.BasePipelineID || revision != d.BaseRevision {
		return ErrDraftConflict
	}
	if id == "" {
		return nil
	}
	dsl, err := Parse([]byte(in.DefinitionJSON))
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,inputs_json FROM pipeline_schedules WHERE target_pipeline_id=? AND target_pipeline_version IS NULL AND enabled=1 AND deleted_at IS NULL ORDER BY id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var schedule, name, raw string
		if err := rows.Scan(&schedule, &name, &raw); err != nil {
			return err
		}
		var supplied map[string]any
		if err := json.Unmarshal([]byte(raw), &supplied); err != nil {
			return &ScheduleDraftConflict{schedule, name, "Stored inputs are unreadable"}
		}
		values := mergeInputs(supplied, dsl)
		for _, spec := range dsl.Inputs {
			value, exists := values[spec.Name]
			if !exists || value == nil {
				if spec.Required {
					return &ScheduleDraftConflict{schedule, name, fmt.Sprintf("Input %s is required by the draft", spec.Name)}
				}
				continue
			}
			// Legacy inputs have no form contract; match execution's validation.
			if !hasInputForm(spec) {
				continue
			}
			if err := validateFormValue(spec, value); err != nil {
				return &ScheduleDraftConflict{schedule, name, err.Error()}
			}
		}
	}
	return rows.Err()
}
