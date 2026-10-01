package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Count reports how many rows the next sweep would delete from one workspace
// if window k were d. nil (forever) counts nothing. The rows are counted, not
// deleted; an older backup still holds whatever the sweep then removes.
//
// For page panel data the "sweep" is the eviction a panel's next push runs,
// and the count is the rows older than the window except each panel's newest.
func Count(ctx context.Context, db DB, workspaceID string, k Key, d *int, now time.Time) (int64, error) {
	if d == nil || *d <= 0 {
		return 0, nil
	}
	n := *d
	var q string
	var args []any
	switch k {
	case RoutineRuns:
		return pipeline.CountRunRetention(ctx, db, workspaceID, n, pipeline.DefaultKeepLastNRunsPerPipeline, now)
	case Approvals:
		return harbormaster.CountApprovalsRetention(ctx, db, workspaceID, n, now)
	case Audit:
		// Same predicate as sweepAuditTable in internal/api/audit_retention.go.
		q, args = `SELECT COUNT(*) FROM audit_logs WHERE workspace_id = ? AND created_at < ?`, []any{workspaceID, cutoff(now, n)}
	case CredentialAudit:
		q, args = `SELECT COUNT(*) FROM credential_audit WHERE workspace_id = ? AND occurred_at < ?`, []any{workspaceID, cutoff(now, n)}
	case MemoryVersions:
		// Same predicate and cutoff format as memory.SweepStaleVersions.
		c := now.Add(-time.Duration(n) * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
		q, args = `SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ? AND written_at < ?`, []any{workspaceID, c}
	case PagePanelData:
		// pages.EvictRingWithin: older than the window, except each panel's
		// newest payload, which survives the age cut.
		q = `SELECT COUNT(*) FROM page_panel_data d
		       JOIN page_panels p ON p.id = d.panel_id
		       JOIN pages pg ON pg.id = p.page_id
		      WHERE pg.workspace_id = ? AND d.produced_at < ?
		        AND d.seq < (SELECT MAX(seq) FROM page_panel_data m WHERE m.panel_id = d.panel_id)`
		args = []any{workspaceID, tsformat.Format(now.Add(-time.Duration(n) * 24 * time.Hour).UTC())}
	case Inbox:
		q, args = `SELECT COUNT(*) FROM inbox_items WHERE `+inboxEligible, []any{workspaceID, cutoff(now, n)}
	case Chats:
		c := cutoff(now, n)
		q, args = `SELECT COUNT(*) FROM chats c WHERE `+chatEligible, []any{workspaceID, c, c}
	case KeeperDecisions:
		q, args = `SELECT COUNT(*) FROM keeper_requests kr WHERE `+keeperEligible, []any{workspaceID, workspaceID, cutoff(now, n)}
	default:
		return 0, fmt.Errorf("retention: count: unknown window %q", k)
	}
	var out int64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&out); err != nil {
		return 0, fmt.Errorf("retention: count %s for %s: %w", k, workspaceID, err)
	}
	return out, nil
}
