package pipeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxRunMetadataBytes caps a run's metadata_json so repeated appends
// can't bloat the row. Mirrors trigger.dev's 256 KB ceiling.
const MaxRunMetadataBytes = 256 * 1024

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

// MetadataOps is a batch of run-metadata mutations (trigger.dev
// set/increment/append parity). Applied read-modify-write under the
// row's update; nil/empty maps are skipped.
type MetadataOps struct {
	Set       map[string]any `json:"set,omitempty"`
	Increment map[string]any `json:"increment,omitempty"`
	Append    map[string]any `json:"append,omitempty"`
}

// UpdateMetadata applies ops to a run's metadata_json and returns the
// merged object. Increment adds to a numeric key (creating it at 0);
// Append pushes onto an array key (creating it empty). Workspace-scoped.
func (s *RunStore) UpdateMetadata(ctx context.Context, workspaceID, runID string, ops MetadataOps) (map[string]any, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort; commit precedes on success

	var raw string
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(metadata_json,'{}') FROM pipeline_runs WHERE id = ? AND workspace_id = ?`,
		runID, workspaceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFoundInStore
	}
	if err != nil {
		return nil, err
	}
	md := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &md)

	for k, v := range ops.Set {
		md[k] = v
	}
	for k, v := range ops.Increment {
		md[k] = toFloat(md[k]) + toFloat(v)
	}
	for k, v := range ops.Append {
		arr, _ := md[k].([]any)
		md[k] = append(arr, v)
	}

	out, err := json.Marshal(md)
	if err != nil {
		return nil, err
	}
	// Cap the merged blob so repeated appends can't grow metadata_json
	// unboundedly (DB-row bloat + slow reads). Matches trigger.dev's
	// 256 KB ceiling; the mutation is rejected rather than truncated so
	// the caller sees the limit instead of silent data loss.
	if len(out) > MaxRunMetadataBytes {
		return nil, fmt.Errorf("run metadata would exceed %d bytes (%d) — prune before appending", MaxRunMetadataBytes, len(out))
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE pipeline_runs SET metadata_json = ?, updated_at = datetime('now','subsec') WHERE id = ? AND workspace_id = ?`,
		string(out), runID, workspaceID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return md, nil
}

// toFloat coerces a JSON-decoded numeric (float64 or json.Number) to
// float64 for the increment op; non-numerics count as 0.
func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
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

// RunTreeNode is one run in a parent/child run tree (call_pipeline /
// deferred / replay parentage via triggered_by_id).
type RunTreeNode struct {
	ID           string `json:"id"`
	ParentID     string `json:"parent_id,omitempty"`
	PipelineSlug string `json:"pipeline_slug"`
	Status       string `json:"status"`
	TriggeredVia string `json:"triggered_via"`
	CostUSD      float64
}

// RunTree returns the root run plus all descendants (runs whose
// triggered_by_id chains back to root), via a recursive CTE. Flat,
// newest-first within a depth; the caller nests by ParentID. Capped at
// 500 nodes so a pathological fan-out can't blow up the response.
func (s *RunStore) RunTree(ctx context.Context, workspaceID, rootID string) ([]RunTreeNode, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH RECURSIVE tree(id) AS (
    SELECT id FROM pipeline_runs WHERE id = ? AND workspace_id = ?
    UNION
    SELECT r.id FROM pipeline_runs r JOIN tree t ON r.triggered_by_id = t.id
    WHERE r.workspace_id = ?
)
SELECT r.id, COALESCE(r.triggered_by_id,''), COALESCE(r.pipeline_slug,''),
       COALESCE(r.status,''), COALESCE(r.triggered_via,''), COALESCE(r.cost_usd,0)
FROM pipeline_runs r JOIN tree t ON r.id = t.id
WHERE r.workspace_id = ?
ORDER BY r.started_at ASC
LIMIT 500`, rootID, workspaceID, workspaceID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunTreeNode
	for rows.Next() {
		var n RunTreeNode
		if err := rows.Scan(&n.ID, &n.ParentID, &n.PipelineSlug, &n.Status, &n.TriggeredVia, &n.CostUSD); err != nil {
			return nil, err
		}
		// The root's own triggered_by_id points outside the tree (a
		// schedule/webhook/pending id, not a run) — blank it so the
		// caller treats it as the root.
		if n.ID == rootID {
			n.ParentID = ""
		}
		out = append(out, n)
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
