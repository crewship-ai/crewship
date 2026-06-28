package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Run observability surface (trigger.dev-informed): tags, error
// fingerprinting + failure grouping for bulk replay. is_replay/metadata
// live on the run record itself (runs.go).

// MaxRunTags caps tags per run, matching trigger.dev's limit. Excess
// tags are dropped (not an error) so a noisy caller can't bloat the
// join table.
const MaxRunTags = 10

// boolToEnvStr renders a bool as the render-context string form. The
// step `if:` falsey set includes "false", so this drives `{{ env.is_replay }}`.
func boolToEnvStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// normalizeTag lowercases + trims a tag and bounds its length. Empty
// after normalization → dropped by SetTags.
func normalizeTag(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if len(t) > 128 {
		t = t[:128]
	}
	return t
}

// SetTags replaces the tag set for a run (idempotent). Tags are
// normalized + de-duplicated + capped at MaxRunTags.
func (s *RunStore) SetTags(ctx context.Context, workspaceID, runID string, tags []string) error {
	seen := map[string]struct{}{}
	var clean []string
	for _, t := range tags {
		n := normalizeTag(t)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		clean = append(clean, n)
		if len(clean) >= MaxRunTags {
			break
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort on the success path (commit precedes)
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_tags WHERE run_id = ?`, runID); err != nil {
		return err
	}
	for _, t := range clean {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO run_tags (run_id, workspace_id, tag) VALUES (?, ?, ?)`,
			runID, workspaceID, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TagsFor returns the sorted tag set for a run.
func (s *RunStore) TagsFor(ctx context.Context, runID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tag FROM run_tags WHERE run_id = ? ORDER BY tag`, runID)
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

// ListByTag returns a pipeline's runs carrying a given tag, newest
// first. Backs `routine records --tag` (and batch retrieval via the
// synthetic batch:<id> tag). Reuses runSelectColumns so the rows scan
// identically to the other list paths.
func (s *RunStore) ListByTag(ctx context.Context, pipelineID, tag string, limit int) ([]*RunRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, runSelectColumns+`
WHERE id IN (SELECT run_id FROM run_tags WHERE tag = ?) AND pipeline_id = ?
ORDER BY started_at DESC
LIMIT ?`, normalizeTag(tag), pipelineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailureGroup is one fingerprint bucket of failed runs.
type FailureGroup struct {
	Fingerprint  string   `json:"fingerprint"`
	Count        int      `json:"count"`
	PipelineSlug string   `json:"pipeline_slug"`
	FailedAtStep string   `json:"failed_at_step"`
	SampleError  string   `json:"sample_error"`
	RunIDs       []string `json:"run_ids"`
}

// FailureGroups buckets a workspace's failed runs by error_fingerprint,
// newest first. Backs the errors view + bulk replay: the caller picks a
// group and replays its RunIDs after shipping a fix.
func (s *RunStore) FailureGroups(ctx context.Context, workspaceID string, limit int) ([]FailureGroup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(error_fingerprint,''), COALESCE(pipeline_slug,''),
       COALESCE(failed_at_step,''), COALESCE(error_message,''), id
FROM pipeline_runs
WHERE workspace_id = ? AND status = 'failed' AND error_fingerprint IS NOT NULL
ORDER BY started_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byFP := map[string]*FailureGroup{}
	var order []string
	for rows.Next() {
		var fp, slug, step, msg, id string
		if err := rows.Scan(&fp, &slug, &step, &msg, &id); err != nil {
			return nil, err
		}
		g, ok := byFP[fp]
		if !ok {
			g = &FailureGroup{Fingerprint: fp, PipelineSlug: slug, FailedAtStep: step, SampleError: msg}
			byFP[fp] = g
			order = append(order, fp)
		}
		g.Count++
		g.RunIDs = append(g.RunIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]FailureGroup, 0, len(order))
	for _, fp := range order {
		out = append(out, *byFP[fp])
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// fingerprintVolatile strips run-specific noise (ids, hex blobs, numbers,
// timestamps) so two runs failing the same way hash identically.
var fingerprintVolatile = regexp.MustCompile(`(?i)(run_[a-z0-9]+|prn_[a-z0-9]+|0x[0-9a-f]+|\b[0-9a-f]{8,}\b|\d{4}-\d{2}-\d{2}t[\d:.+z-]+|\d+)`)

// ErrorFingerprint is a stable hash of a failure: the failing step plus
// the error message with volatile tokens normalized out. Two runs that
// fail at the same step for the same reason share a fingerprint, so the
// errors view can group + bulk-replay them.
func ErrorFingerprint(failedStep, errMsg string) string {
	norm := fingerprintVolatile.ReplaceAllString(strings.ToLower(errMsg), "·")
	norm = strings.Join(strings.Fields(norm), " ")
	sum := sha256.Sum256([]byte(failedStep + "\x00" + norm))
	return hex.EncodeToString(sum[:])[:16]
}
