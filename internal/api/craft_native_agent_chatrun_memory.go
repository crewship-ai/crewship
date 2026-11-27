package api

import (
	"context"
	"database/sql"
)

// artifactScopedRun reports whether a human message bound this run to attached
// artifacts (artifact_run_scope). Such a run learns nothing from them: no
// memory write, no outcome verdict and no post-run consolidation, until the
// owner of that decision allows it. A lookup error is returned, never read as
// "not scoped".
func artifactScopedRun(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID string) (bool, error) {
	var scoped bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_run_scope WHERE run_id=?)`, runID).Scan(&scoped)
	return scoped, err
}
