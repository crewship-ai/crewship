package backup

// The narrow half of #2034.
//
// dropped_columns.go stops a bundle column the target schema does not have
// from being dropped IN SILENCE — it counts and reports the loss. It cannot
// stop the loss itself: rewriting a bundle row into a shape the current
// schema likes is a per-table judgement, and dropped_columns.go deliberately
// does not make it (see that file's doc comment).
//
// issue_counters is the one table where the judgement is worth making. #1797
// re-keyed it from `crew_id` to `(workspace_id, prefix)`. A bundle taken
// before that migration carries `{crew_id, next_number}`; on a post-#1797
// target `crew_id` is not a column, the statement degenerates to
// `INSERT OR IGNORE INTO issue_counters (next_number) VALUES (?)`, and the
// NOT NULL on the two key columns turns the drop into a silently-swallowed
// constraint violation. Every counter row is gone, and the loss is invisible
// except for a crew whose issues were ALL deleted before the backup — its
// counter was the only remaining record of how far it had counted, and the
// crew restarts at 1 over identifiers that outlived it.
//
// This file resolves each row's crew (already restored — crews precedes
// issue_counters in BackupTables) to its workspace and its effective prefix,
// exactly as migrations/20260820125000_issue_counters_prefix_scope.sql does,
// and rewrites the row into the shape the target actually has.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// issueCountersTable names the table this file's restore-time transform
// targets. A constant, not a repeated literal, so a typo in one of several
// call sites cannot quietly disable the whole path.
const issueCountersTable = "issue_counters"

// crewIssueScope is a crew's resolved workspace and effective issue prefix —
// the two pieces migrateIssueCounterRows needs to re-key a pre-#1797 row.
type crewIssueScope struct{ workspaceID, prefix string }

// effectiveIssuePrefix derives a crew's issue prefix exactly as
// migrations/20260820125000_issue_counters_prefix_scope.sql's backfill and
// the runtime allocator both do: the crew's own issue_prefix if it set one,
// else the first three characters of its slug, upper-cased.
func effectiveIssuePrefix(crew map[string]any) string {
	if p := rowString(crew, "issue_prefix"); p != "" {
		return strings.ToUpper(p)
	}
	slug := rowString(crew, "slug")
	if len(slug) > 3 {
		slug = slug[:3]
	}
	return strings.ToUpper(slug)
}

// crewIssueScopesFromDump indexes a bundle's own crews rows by id, so a
// pre-#1797 issue_counters row can be resolved without a database round trip
// — crucial for the dry run, which writes nothing and so cannot see a bundle
// row from inside its read-only transaction (that transaction only ever
// contains what was ALREADY on the target before this restore started).
func crewIssueScopesFromDump(crews []map[string]any) map[string]crewIssueScope {
	out := make(map[string]crewIssueScope, len(crews))
	for _, c := range crews {
		id := rowString(c, "id")
		if id == "" {
			continue
		}
		out[id] = crewIssueScope{
			workspaceID: rowString(c, "workspace_id"),
			prefix:      effectiveIssuePrefix(c),
		}
	}
	return out
}

// migrateIssueCounterRows rewrites pre-#1797 issue_counters rows into the
// post-#1797 shape by resolving each row's crew_id to that crew's
// workspace_id and effective prefix. crews is the bundle's own crews table —
// checked first, and sufficient for the case #2034 names (a crew whose
// issues, not the crew itself, were deleted before the backup: the crew row
// is right there in the same bundle). tx is a fallback for a crew that is
// NOT part of this bundle but already exists on the target (a re-restore
// into an instance that already has it) — read-only, and a no-op for a dry
// run against a target that also lacks the crew.
//
// Two rows whose crews share an effective prefix collapse onto one, taking
// the MAX next_number rather than either individual value or first-wins:
// writing back the smaller of the two would let the allocator re-issue
// identifiers that already exist, which is worse than the counter being
// absent (an absent counter is reseeded above whatever identifiers restored
// alongside it — see the allocator, and #2034's "mostly self-healing" note).
// This mirrors the GROUP BY ... MAX(...) collapse in
// migrations/20260820125000_issue_counters_prefix_scope.sql.
//
// A row already in the new shape (workspace_id or prefix present — a bundle
// taken after #1797) and a row whose crew_id resolves to nothing at all
// (not in the bundle, not on the target) both pass through unchanged. The
// second case is deliberate, not a gap: inventing a workspace for an
// unresolvable row would be a worse failure than losing it, so it is left
// for the ordinary column whitelist to drop and dropped_columns.go to count
// and report.
//
// Returns the (possibly rewritten) row set and how many bundle rows were
// folded into a migrated counter — the number for RestoreStats.
// IssueCountersMigrated, not the number of rows in the returned slice (a
// merge can turn several rows into one).
func migrateIssueCounterRows(ctx context.Context, tx *sql.Tx, rows []map[string]any, crews []map[string]any) ([]map[string]any, int, error) {
	byCrew := crewIssueScopesFromDump(crews)

	type prefixKey struct{ workspaceID, prefix string }
	merged := map[prefixKey]int64{}
	var order []prefixKey
	passthrough := make([]map[string]any, 0, len(rows))
	migrated := 0

	for _, row := range rows {
		crewID := rowString(row, "crew_id")
		if crewID == "" || rowString(row, "workspace_id") != "" || rowString(row, "prefix") != "" {
			passthrough = append(passthrough, row)
			continue
		}
		scope, ok := byCrew[crewID]
		if !ok {
			var workspaceID, prefix string
			err := tx.QueryRowContext(ctx, `
				SELECT workspace_id, COALESCE(NULLIF(issue_prefix, ''), UPPER(SUBSTR(slug, 1, 3)))
				FROM crews WHERE id = ?`, crewID).Scan(&workspaceID, &prefix)
			if errors.Is(err, sql.ErrNoRows) {
				// Crew unresolved (not in this bundle, not on the target
				// either) — leave the row for the ordinary drop-and-report
				// path rather than guessing at a workspace to put it under.
				passthrough = append(passthrough, row)
				continue
			}
			if err != nil {
				return nil, 0, fmt.Errorf("backup: resolve crew %q for issue_counters: %w", crewID, err)
			}
			scope = crewIssueScope{workspaceID: workspaceID, prefix: prefix}
		}
		next, err := rowInt64(row, "next_number")
		if err != nil {
			// A next_number this function cannot parse is exactly as
			// unusable as an unresolvable crew — do not fabricate one.
			passthrough = append(passthrough, row)
			continue
		}
		k := prefixKey{workspaceID: scope.workspaceID, prefix: scope.prefix}
		if cur, ok := merged[k]; !ok || next > cur {
			if !ok {
				order = append(order, k)
			}
			merged[k] = next
		}
		migrated++
	}

	if migrated == 0 {
		return passthrough, 0, nil
	}
	out := passthrough
	for _, k := range order {
		out = append(out, map[string]any{
			"workspace_id": k.workspaceID,
			"prefix":       k.prefix,
			"next_number":  merged[k],
		})
	}
	return out, migrated, nil
}

// warnIssueCountersMigrated emits the operator-facing note for a restore
// that translated pre-#1797 issue_counters rows. Shared by the dry-run and
// the committed path — the same arrangement warnDroppedColumns and
// warnSecurityLevelClamps use, and for the same reason: the two must not
// describe the same bundle differently.
//
// This is informational, not a warning: unlike a dropped column or a clamped
// security tier, a migrated counter is the restore doing exactly what it
// should. It is still surfaced, because "your counter came from a different
// key than the one on disk" is worth an admin's attention once, not silence.
func warnIssueCountersMigrated(logger func(string), migrated int, dryRun bool) {
	if migrated == 0 || logger == nil {
		return
	}
	verb := "migrated"
	if dryRun {
		verb = "would be migrated"
	}
	logger(fmt.Sprintf(
		"%d issue_counters row(s) from a pre-#1797 bundle %s from crew_id to (workspace_id, prefix) "+
			"by resolving each row's crew. Rows whose crew could not be resolved were left for the "+
			"schema-skew report above, if one appears.",
		migrated, verb))
}
