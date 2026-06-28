package pipeline

import (
	"context"
	"database/sql"
	"errors"
)

// PipelineTagStore manages routine-DEFINITION tags (v123) for cross-crew
// discovery — distinct from run_tags (per-run labels).
type PipelineTagStore struct {
	db *sql.DB
}

// MaxPipelineTags caps discovery tags per routine so a caller can't
// bloat pipeline_tags with unbounded labels.
const MaxPipelineTags = 20

// ErrTooManyTags is returned by Add when the cap would be exceeded — a
// client error the handler maps to 400, not 500.
var ErrTooManyTags = errors.New("routine already has the maximum number of tags")

// NewPipelineTagStore wraps a DB handle.
func NewPipelineTagStore(db *sql.DB) *PipelineTagStore {
	return &PipelineTagStore{db: db}
}

// Add tags a routine. Tags are normalized + de-duped (PK ignores dups).
// Rejects the batch if it would push the routine past MaxPipelineTags.
func (s *PipelineTagStore) Add(ctx context.Context, workspaceID, pipelineID string, tags []string) error {
	var existing int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_tags WHERE pipeline_id = ?`, pipelineID).Scan(&existing); err != nil {
		return err
	}
	added := 0
	for _, raw := range tags {
		t := normalizeTag(raw)
		if t == "" {
			continue
		}
		if existing+added >= MaxPipelineTags {
			return ErrTooManyTags
		}
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO pipeline_tags (pipeline_id, workspace_id, tag) VALUES (?, ?, ?)
             ON CONFLICT(pipeline_id, tag) DO NOTHING`,
			pipelineID, workspaceID, t)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	return nil
}

// Remove untags a routine.
func (s *PipelineTagStore) Remove(ctx context.Context, pipelineID, tag string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM pipeline_tags WHERE pipeline_id = ? AND tag = ?`, pipelineID, normalizeTag(tag))
	return err
}

// TagsFor returns a routine's sorted tag set.
func (s *PipelineTagStore) TagsFor(ctx context.Context, pipelineID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag FROM pipeline_tags WHERE pipeline_id = ? ORDER BY tag`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PipelineIDsByTag returns the ids of routines carrying a tag — backs
// `routine list --tag` discovery.
func (s *PipelineTagStore) PipelineIDsByTag(ctx context.Context, workspaceID, tag string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pipeline_id FROM pipeline_tags WHERE workspace_id = ? AND tag = ?`, workspaceID, normalizeTag(tag))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}
