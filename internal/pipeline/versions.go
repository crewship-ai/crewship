package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// PipelineVersion is one immutable snapshot in a pipeline's edit
// history. v78 stored only the latest definition_json; v79 adds
// pipeline_versions so every save creates a new row keyed
// (pipeline_id, version) and pipelines.head_version points at the
// "current" one.
//
// Version is monotonic per pipeline_id (1, 2, 3, ...). Content-hash
// dedup at save time means re-saving the exact same DSL is a no-op
// — UNIQUE (pipeline_id, definition_hash) catches it. The author
// fields capture provenance so the diff UI can render
// "v3 by agent xyz on 2026-05-07" rows.
type PipelineVersion struct {
	ID             string
	PipelineID     string
	Version        int
	DefinitionJSON string
	DefinitionHash string
	AuthorType     string // agent | user | system | imported
	AuthorID       string
	ParentVersion  *int
	ChangeSummary  string
	CreatedAt      time.Time
}

// SaveVersion persists a new immutable version for the given
// pipeline. Called by Store.Save in a transaction so the head
// pointer + new row land atomically.
//
// The Store.Save method retains the existing in-place behaviour
// (pipelines.definition_json + .head_version updated) for
// backwards-compatible read paths; SaveVersion just adds the
// historical row alongside.
//
// Returns the persisted version. If a version with the same
// definition_hash already exists for this pipeline, returns the
// existing row (idempotent) and does NOT bump head_version —
// re-saving identical content is a no-op.
func (s *Store) SaveVersion(ctx context.Context, in SaveVersionInput) (*PipelineVersion, error) {
	if in.PipelineID == "" {
		return nil, errors.New("SaveVersion: pipeline_id required")
	}
	if in.DefinitionJSON == "" {
		return nil, errors.New("SaveVersion: definition_json required")
	}
	if in.AuthorType == "" {
		in.AuthorType = "agent"
	}

	hash := definitionHash(in.DefinitionJSON)

	// Check for existing version with same hash — idempotent save.
	var existingVersionID string
	var existingVersionNum int
	err := s.db.QueryRowContext(ctx, `
SELECT id, version FROM pipeline_versions
WHERE pipeline_id = ? AND definition_hash = ?
LIMIT 1`, in.PipelineID, hash).Scan(&existingVersionID, &existingVersionNum)
	if err == nil {
		// Already exists — return it without bumping head.
		return s.getVersionByID(ctx, existingVersionID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("SaveVersion: hash lookup: %w", err)
	}

	// New version: query current head, increment, insert.
	var currentHead int
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM pipeline_versions WHERE pipeline_id = ?`,
		in.PipelineID,
	).Scan(&currentHead)
	if err != nil {
		return nil, fmt.Errorf("SaveVersion: max version: %w", err)
	}

	newVersion := currentHead + 1
	id := generateVersionID()
	now := time.Now().UTC()

	parentVal := sql.NullInt64{}
	if in.ParentVersion != nil {
		parentVal = sql.NullInt64{Int64: int64(*in.ParentVersion), Valid: true}
	} else if currentHead > 0 {
		// Default: parent is the current head
		parentVal = sql.NullInt64{Int64: int64(currentHead), Valid: true}
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO pipeline_versions (
    id, pipeline_id, version, definition_json, definition_hash,
    author_type, author_id, parent_version, change_summary, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.PipelineID, newVersion, in.DefinitionJSON, hash,
		in.AuthorType, in.AuthorID, parentVal, nullStr(in.ChangeSummary),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		// UNIQUE (pipeline_id, version) violation should be impossible
		// thanks to the MAX query, but a concurrent save could race.
		// Treat as conflict — caller can retry.
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("SaveVersion: race on version %d (retry): %w", newVersion, err)
		}
		return nil, fmt.Errorf("SaveVersion: insert: %w", err)
	}

	// Update head pointer on pipelines table.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE pipelines SET head_version = ?, updated_at = ? WHERE id = ?`,
		newVersion, now.Format(time.RFC3339Nano), in.PipelineID,
	); err != nil {
		return nil, fmt.Errorf("SaveVersion: update head: %w", err)
	}

	return &PipelineVersion{
		ID:             id,
		PipelineID:     in.PipelineID,
		Version:        newVersion,
		DefinitionJSON: in.DefinitionJSON,
		DefinitionHash: hash,
		AuthorType:     in.AuthorType,
		AuthorID:       in.AuthorID,
		ParentVersion:  derefIntPtr(parentVal),
		ChangeSummary:  in.ChangeSummary,
		CreatedAt:      now,
	}, nil
}

// SaveVersionInput is the payload for Store.SaveVersion. Mirrors
// SaveInput's Author block but without the test-run gate (versions
// piggyback on Save's gate).
type SaveVersionInput struct {
	PipelineID     string
	DefinitionJSON string
	AuthorType     string // agent | user | system | imported
	AuthorID       string
	ParentVersion  *int
	ChangeSummary  string
}

// ListVersions returns the pipeline's full version history,
// newest first. Pagination via Limit (default 100, max 500).
func (s *Store) ListVersions(ctx context.Context, pipelineID string, limit int) ([]*PipelineVersion, error) {
	if pipelineID == "" {
		return nil, errors.New("ListVersions: pipeline_id required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, pipeline_id, version, definition_json, definition_hash,
       author_type, author_id, parent_version, COALESCE(change_summary, ''),
       created_at
FROM pipeline_versions
WHERE pipeline_id = ?
ORDER BY version DESC
LIMIT ?`, pipelineID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListVersions: %w", err)
	}
	defer rows.Close()
	var out []*PipelineVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion returns one specific version by (pipeline_id, version
// number). Used by /pipelines/{slug}/versions/{n} endpoints and by
// the executor when a run pins to a specific version.
func (s *Store) GetVersion(ctx context.Context, pipelineID string, version int) (*PipelineVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, pipeline_id, version, definition_json, definition_hash,
       author_type, author_id, parent_version, COALESCE(change_summary, ''),
       created_at
FROM pipeline_versions
WHERE pipeline_id = ? AND version = ?`, pipelineID, version)
	if err != nil {
		return nil, fmt.Errorf("GetVersion: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	return scanVersion(rows)
}

// Rollback updates pipelines.head_version to a previous version
// AND copies that version's definition_json back into
// pipelines.definition_json so the regular Get/List paths see the
// rolled-back DSL.
//
// Rollback does NOT delete intervening versions — they stay in the
// history. The next save creates a NEW version (e.g. 5) on top of
// the rolled-back head, so the timeline reads:
//
//	v1 → v2 → v3 → v4 → ROLLBACK to v2 → v5 (parent=v2)
//
// This preserves auditability: "we tried v3 + v4, neither worked,
// kept rolling on top of v2".
func (s *Store) Rollback(ctx context.Context, pipelineID string, targetVersion int) (*Pipeline, error) {
	target, err := s.GetVersion(ctx, pipelineID, targetVersion)
	if err != nil {
		return nil, fmt.Errorf("Rollback: load target: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
UPDATE pipelines
SET head_version = ?, definition_json = ?, definition_hash = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`,
		target.Version, target.DefinitionJSON, target.DefinitionHash, now, pipelineID,
	)
	if err != nil {
		return nil, fmt.Errorf("Rollback: update pipeline: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetByID(ctx, pipelineID)
}

// getVersionByID is the internal lookup by pipeline_versions.id —
// used by SaveVersion's idempotent return path.
func (s *Store) getVersionByID(ctx context.Context, id string) (*PipelineVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, pipeline_id, version, definition_json, definition_hash,
       author_type, author_id, parent_version, COALESCE(change_summary, ''),
       created_at
FROM pipeline_versions
WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	return scanVersion(rows)
}

func scanVersion(rs rowScanner) (*PipelineVersion, error) {
	var (
		v            PipelineVersion
		parent       sql.NullInt64
		createdAtStr string
		summary      string
	)
	err := rs.Scan(
		&v.ID, &v.PipelineID, &v.Version, &v.DefinitionJSON, &v.DefinitionHash,
		&v.AuthorType, &v.AuthorID, &parent, &summary, &createdAtStr,
	)
	if err != nil {
		return nil, err
	}
	if parent.Valid {
		x := int(parent.Int64)
		v.ParentVersion = &x
	}
	v.ChangeSummary = summary
	v.CreatedAt = parseTimeOrZero(createdAtStr)
	return &v, nil
}

// derefIntPtr converts a sql.NullInt64 to *int for the response shape.
func derefIntPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	x := int(n.Int64)
	return &x
}

// generateVersionID mints a "plnv_" CUID for pipeline_versions.id.
// Same shape as generatePipelineID but with a different prefix so
// log lines + journal entries can pattern-match by entity kind.
func generateVersionID() string {
	ts := time.Now().UnixMilli()
	c := versionIDCounter.Add(1)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	var b strings.Builder
	b.WriteString("plnv_c")
	b.WriteString(strconv.FormatInt(ts, 36))
	b.WriteByte(hexdigits[(tail>>12)&0xf])
	b.WriteByte(hexdigits[(tail>>8)&0xf])
	b.WriteByte(hexdigits[(tail>>4)&0xf])
	b.WriteByte(hexdigits[tail&0xf])
	// 8 hex chars from random
	rb := make([]byte, 4)
	if _, err := cryptoReadFull(rb); err != nil {
		for i := range rb {
			rb[i] = byte(c >> (i * 8))
		}
	}
	for _, x := range rb {
		b.WriteByte(hexdigits[x>>4])
		b.WriteByte(hexdigits[x&0xf])
	}
	return b.String()
}

var versionIDCounter atomic.Uint64

// cryptoReadFull is a tiny shim around crypto/rand.Read so the test
// path can swap it. Production uses the real RNG; this signature
// makes future fault-injection trivial.
func cryptoReadFull(b []byte) (int, error) {
	return rand.Read(b)
}
