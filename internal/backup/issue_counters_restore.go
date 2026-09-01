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
	"strconv"
	"strings"
)

// issueCountersTable names the table this file's restore-time transform
// targets. A constant, not a repeated literal, so a typo in one of several
// call sites cannot quietly disable the whole path.
const issueCountersTable = "issue_counters"

// crewIssueScope is a crew's resolved workspace and effective issue prefix —
// the two pieces migrateIssueCounterRows needs to re-key a pre-#1797 row.
type crewIssueScope struct{ workspaceID, prefix string }

// issueCounterPrefixKey is the (workspace, prefix) namespace a migrated
// counter row lands under — the same pair issue_counters' own PRIMARY KEY is
// defined on post-#1797. Named (not an inline struct literal) so
// migrateIssueCounterRows and missionIssueHighWaterMarks can share it.
type issueCounterPrefixKey struct{ workspaceID, prefix string }

// effectiveIssuePrefix derives a crew's issue prefix exactly as
// migrations/20260820125000_issue_counters_prefix_scope.sql's backfill and
// the runtime allocator (crewIssuePrefix, internal/api/issue_create_core.go)
// both do: the crew's own issue_prefix VERBATIM if it set one, else the
// first three characters of its slug, upper-cased.
//
// An explicit issue_prefix is returned as-is, not upper-cased.
// validIssuePrefixRe allows lowercase (`^[A-Za-z0-9_-]{1,16}$`), and the
// allocator's runtime lookup (`WHERE prefix = ?`) is an exact match against
// whatever crews.issue_prefix holds — it does not upper-case its argument
// either. Upper-casing here would resolve a crew with issue_prefix="eng" to
// a counter row the allocator can never find by that same key, which is
// exactly #2034's stated worst case: the crew reseeds from missions (none,
// if they were all deleted before the backup) and restarts at 1.
func effectiveIssuePrefix(crew map[string]any) string {
	if p := rowString(crew, "issue_prefix"); p != "" {
		return p
	}
	slug := strings.ToUpper(rowString(crew, "slug"))
	if len(slug) > 3 {
		slug = slug[:3]
	}
	return slug
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

// missionIssueHighWaterMarks reads, out of a bundle's OWN missions rows, the
// highest mission number already minted under each (workspace, prefix)
// namespace — the second arm of the UNION ALL that
// migrations/20260820125000_issue_counters_prefix_scope.sql's backfill takes
// MAX over alongside the per-crew counters. migrateIssueCounterRows must not
// skip this arm: the two failure modes are not symmetric. An absent counter
// self-heals (nextIssueIdentifierTx reseeds from missions on first use — see
// internal/api/issue_create_core.go), but a PRESENT counter that is too low
// never does — the allocator takes the `next_number + 1` UPDATE branch
// forever. Writing back only the bundled counter for a crew that was wedged
// below another crew's high-water mark under the same prefix (#2034's
// blocker case: crew A minted ENG-1..40 and was deleted, crew B is the
// pre-#1797 collision victim wedged at next_number=1) reproduces the
// original bug in a form the allocator can never self-heal from: the very
// next create collides with idx_mission_workspace_identifier, the shared
// transaction rolls the increment back, and the crew can never file an
// issue again.
//
// The prefix is recovered by stripping the "-<number>" suffix from
// identifier, mirroring the migration's SUBSTR/LENGTH reconstruction — and,
// like the migration, discarding any identifier that does not actually end
// in "-" + number rather than trusting the arithmetic blindly.
//
// dump.Tables["missions"] is the only correct source for this — see the
// package doc comment: missions restores AFTER issue_counters
// (internal/backup/dbdump.go), so this transform runs before this restore's
// own mission rows have landed in tx, and querying tx would only ever see
// what predates this restore (nothing, on a fresh target).
func missionIssueHighWaterMarks(missions []map[string]any) map[issueCounterPrefixKey]int64 {
	out := map[issueCounterPrefixKey]int64{}
	for _, m := range missions {
		identifier := rowString(m, "identifier")
		workspaceID := rowString(m, "workspace_id")
		if identifier == "" || workspaceID == "" {
			continue
		}
		if _, present := m["number"]; !present || m["number"] == nil {
			continue
		}
		number, err := rowInt64(m, "number")
		if err != nil {
			continue
		}
		suffix := "-" + strconv.FormatInt(number, 10)
		if len(identifier) <= len(suffix) || !strings.HasSuffix(identifier, suffix) {
			continue
		}
		prefix := identifier[:len(identifier)-len(suffix)]
		k := issueCounterPrefixKey{workspaceID: workspaceID, prefix: prefix}
		if cur, ok := out[k]; !ok || number > cur {
			out[k] = number
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
// The high-water mark from missions (missionIssueHighWaterMarks) is folded
// into every namespace this loop touches, mirroring the migration's
// UNION ALL ... GROUP BY ... MAX(...) exactly — see that function's doc
// comment for why the arm cannot be skipped.
//
// Returns the (possibly rewritten) row set and how many bundle rows were
// folded into a migrated counter — the number for RestoreStats.
// IssueCountersMigrated, not the number of rows in the returned slice (a
// merge can turn several rows into one).
func migrateIssueCounterRows(ctx context.Context, tx *sql.Tx, rows []map[string]any, crews []map[string]any, missions []map[string]any, workspaces []map[string]any) ([]map[string]any, int, error) {
	byCrew := crewIssueScopesFromDump(crews)
	highWater := missionIssueHighWaterMarks(missions)

	// The migration guards the same class of row with an explicit
	// `WHERE EXISTS (SELECT 1 FROM workspaces w WHERE w.id = ws)` — a crew (or
	// mission) that outlived its own workspace under a historical
	// `PRAGMA foreign_keys=OFF` window. issue_counters.workspace_id is NOT
	// NULL with a real FK on the post-#1797 target, and this transform's
	// output is inserted with foreign_keys ON and the FK check deferred to
	// commit (assertNoFKViolationsTx) — so a row this function emits for a
	// workspace that does not exist does not fail quietly the way the OLD
	// crew_id NOT NULL violation did. It fails assertNoFKViolationsTx and
	// aborts the ENTIRE restore, naming neither the table nor the row.
	// Checked here so that class of row instead falls through to
	// passthrough, exactly like an unresolvable crew: a counted drop, not a
	// restore-ending surprise.
	bundleWorkspaceIDs := make(map[string]bool, len(workspaces))
	for _, w := range workspaces {
		if id := rowString(w, "id"); id != "" {
			bundleWorkspaceIDs[id] = true
		}
	}
	workspaceExistsCache := map[string]bool{}
	workspaceExists := func(id string) (bool, error) {
		if id == "" {
			return false, nil
		}
		if bundleWorkspaceIDs[id] {
			return true, nil
		}
		if v, ok := workspaceExistsCache[id]; ok {
			return v, nil
		}
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ?`, id).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			workspaceExistsCache[id] = false
			return false, nil
		case err != nil:
			return false, err
		default:
			workspaceExistsCache[id] = true
			return true, nil
		}
	}

	merged := map[issueCounterPrefixKey]int64{}
	var order []issueCounterPrefixKey
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
		exists, err := workspaceExists(scope.workspaceID)
		if err != nil {
			return nil, 0, fmt.Errorf("backup: resolve workspace %q for issue_counters: %w", scope.workspaceID, err)
		}
		if !exists {
			// The crew's workspace is gone (bundle and target both lack
			// it) — see the WHERE-EXISTS note above. Leave the row for the
			// ordinary drop-and-report path rather than emitting an INSERT
			// the deferred FK check will reject at commit and take the
			// whole restore down with it.
			passthrough = append(passthrough, row)
			continue
		}
		k := issueCounterPrefixKey{workspaceID: scope.workspaceID, prefix: scope.prefix}
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
		next := merged[k]
		if hw, ok := highWater[k]; ok && hw > next {
			next = hw
		}
		out = append(out, map[string]any{
			"workspace_id": k.workspaceID,
			"prefix":       k.prefix,
			"next_number":  next,
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
