package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/google/uuid"
)

// TestResult is the outcome of the latest Test run of one definition
// (ODCH-RT-13). It belongs to the definition hash, not to a draft revision or
// a version number, so it can be shown on a draft and on any version holding
// that exact definition.
type TestResult struct {
	Passed   bool   `json:"passed"`
	Status   string `json:"status"`
	TestedAt string `json:"tested_at"`
	TestedBy string `json:"tested_by,omitempty"`
}

// RecordTestResult stores (or replaces) the result of testing the definition
// with this hash.
func (s *Store) RecordTestResult(ctx context.Context, workspaceID, definitionHash string, passed bool, status, testedBy string) error {
	if workspaceID == "" || definitionHash == "" {
		return errors.New("pipeline: workspace_id and definition_hash required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO routine_test_results (id, workspace_id, definition_hash, passed, status, tested_by, tested_at)
VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?)
ON CONFLICT (workspace_id, definition_hash) DO UPDATE SET
    passed = excluded.passed, status = excluded.status, tested_by = excluded.tested_by, tested_at = excluded.tested_at`,
		newTestResultID(), workspaceID, definitionHash, boolToInt(passed), status, testedBy, tsformat.Format(s.now()))
	return err
}

// TestResults returns the stored result for each of the given definition
// hashes that has one.
func (s *Store) TestResults(ctx context.Context, workspaceID string, hashes []string) (map[string]*TestResult, error) {
	out := map[string]*TestResult{}
	if len(hashes) == 0 {
		return out, nil
	}
	args := []any{workspaceID}
	marks := make([]string, 0, len(hashes))
	for _, h := range hashes {
		marks = append(marks, "?")
		args = append(args, h)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT definition_hash, passed, status, COALESCE(tested_by,''), tested_at
FROM routine_test_results WHERE workspace_id = ? AND definition_hash IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var passed int
		var r TestResult
		if err := rows.Scan(&hash, &passed, &r.Status, &r.TestedBy, &r.TestedAt); err != nil {
			return nil, err
		}
		r.Passed = passed != 0
		out[hash] = &r
	}
	return out, rows.Err()
}

func newTestResultID() string { return "trs_" + uuid.NewString() }
