package api

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Expired admissions are no longer authority. A short grace period retains
// diagnostics while bounded batches release ephemeral FK dependents.
func (h *PageHandler) cleanupArtifactRunPrincipals(ctx context.Context, now time.Time) {
	_, _ = h.db.ExecContext(ctx, `DELETE FROM artifact_run_principals WHERE run_id IN (SELECT run_id FROM artifact_run_principals WHERE expires_at < ? ORDER BY expires_at LIMIT 200)`, tsformat.Format(now.UTC().Add(-24*time.Hour)))
}
