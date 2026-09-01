package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// sqlIdentifierRe is the conservative SQLite identifier shape:
// leading letter / underscore, then letters / digits / underscores.
// Used to gate every string interpolated into DDL/PRAGMA contexts
// where the SQL driver cannot parametrise the value (PRAGMA names
// are not parameter-bindable). Today every caller of this validator
// passes a constant from BackupTables, but pinning the contract here
// keeps a future caller that forwards external input from opening a
// SQL-injection vector by accident.
var sqlIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// remapCounter is the monotonic counter portion of backup-generated
// CUIDs. Kept package-local so it does not collide with the one in
// internal/api; the formats are compatible either way.
var remapCounter atomic.Uint64

// nonRemappablePKTables enumerates BackupTables entries whose primary
// keys must NOT be regenerated during --as-workspace / --as-crew
// remap. These tables are globally namespaced and have a UNIQUE
// constraint on a human-readable column the bundle and target both
// own — so regenerating the PK in the dump and INSERT OR IGNOREing
// the row collides on that UNIQUE constraint, drops the bundle row,
// and leaves dependent FK rows pointing at the new id (which never
// landed). See RemapIDs pass 1 for the full failure-mode write-up.
//
// The list intentionally stays small: only tables where a stable id
// is correct AND a constraint blocks the renamed-id workaround.
// workspaces/crews/agents/etc. are per-workspace and the whole point
// of --as-workspace is to fork them under new ids, so they remain
// remappable.
var nonRemappablePKTables = map[string]bool{
	// Skills have UNIQUE(name) and UNIQUE(slug). Bundled skills
	// (skill_coding_01, skill_research_01, …) are seeded on every
	// boot by SeedBundledSkills with stable IDs; the target row
	// already exists when restore lands.
	"skills": true,
	// Users have UNIQUE(email). An admin restoring to an instance
	// where their email is already provisioned would otherwise lose
	// the bundle's user row to the UNIQUE collision.
	"users": true,
}

// newRemapCUID produces a lowercase CUID-shaped string suitable for
// every primary-key column Crewship uses. The format matches
// internal/api.generateCUID (`c<base36 ts><4-hex counter><8-hex rand>`)
// so a remapped row is indistinguishable at a glance from a row that
// came out of the normal API paths.
//
// Direct byte-append into a stack buffer — the previous
// fmt.Sprintf + manual base36-prepend + hex.EncodeToString(..)[:8]
// version paid ~20 heap allocations per ID on the restore hot path.
func newRemapCUID() string {
	ts := time.Now().UnixMilli()
	c := remapCounter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// On RNG failure salt the random slot with counter + ts so
		// collisions are still astronomically unlikely for a single
		// restore batch.
		b[0] = byte(c >> 24)
		b[1] = byte(c >> 16)
		b[2] = byte(ts >> 8)
		b[3] = byte(ts)
	}

	// "c" + base36(ts) (~8 chars) + %04x counter + 8 hex chars = ≤ 21 chars;
	// 32-byte stack buffer is ample.
	var buf [32]byte
	out := append(buf[:0], 'c')
	out = strconv.AppendInt(out, ts, 36)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	out = append(out,
		hexdigits[(tail>>12)&0xF],
		hexdigits[(tail>>8)&0xF],
		hexdigits[(tail>>4)&0xF],
		hexdigits[tail&0xF],
	)
	out = hex.AppendEncode(out, b)
	return string(out)
}

// virtualForeignKeys names columns that are foreign keys in INTENT but carry
// no REFERENCES clause in the schema, so `PRAGMA foreign_key_list` — and
// therefore RemapIDs pass 2 — never sees them.
//
// A missing REFERENCES clause is not a licence to leave the value alone. Pass
// 1 regenerates the row's own primary key regardless, so a forked restore
// INSERTs a brand-new row that still points at the SOURCE workspace: the fork
// cannot see it, and the source's audit state silently grows a duplicate. That
// is exactly what happened to journal_chain_checkpoints (#2226).
//
// Keep this list to columns whose remap is genuinely required for correctness,
// and add the real FK in a migration when one becomes possible — this map is a
// bridge over a schema gap, not a substitute for the schema.
var virtualForeignKeys = map[string][]foreignKeyEdge{
	// journal_chain_checkpoints.workspace_id (v152,
	// migrate_consts_v152_journal_hash_chain.go) is a bare TEXT column:
	// the table stores removed (seq, hash) JSON rather than row refs, so
	// it was written with no FK anywhere. Its workspace_id still has to
	// follow the fork, or the signed record of a legitimate compaction
	// stays behind and the gap it covered reads as a malicious mid-chain
	// delete in the new workspace.
	//
	// Re-pointing the column is necessary but NOT sufficient: the MAC
	// frames the workspace id (journal.CheckpointMAC), so the row must
	// also be re-signed. rechainForkedJournal does that, and it runs
	// immediately after RemapIDs for that reason.
	"journal_chain_checkpoints": {{column: "workspace_id", refTable: "workspaces", refColumn: "id"}},
	// issue_counters.crew_id (#2034/#1797) is the same gap in the opposite
	// direction: a PRE-#1797 bundle's issue_counters rows still carry
	// crew_id, but on a post-#1797 TARGET that column does not exist in the
	// schema at all — introspectForeignKeys queries the target, sees no
	// issue_counters.crew_id column and so no edge, and pass 2 leaves the
	// bundle row's crew_id pointing at the crew's OLD id while pass 1 just
	// gave that crew a brand-new one.
	//
	// migrateIssueCounterRows (issue_counters_restore.go) then can't find
	// the remapped crew by its NEW id in the bundle's own (already-remapped)
	// crews table, falls back to querying crew_id against the TARGET
	// database, and — on the realistic --as-workspace use case, forking
	// beside the still-present source workspace — finds a crew that really
	// does exist under that old id: the SOURCE crew. The migrated counter
	// then lands under the source's workspace_id instead of the fork's,
	// while RestoreStats.IssueCountersMigrated reports success.
	//
	// Declaring the edge here (used only when the target's real schema does
	// not already declare crew_id, i.e. only for a post-#1797 target — see
	// the "declared" skip in RemapIDs below) makes pass 2 rewrite crew_id
	// alongside every other FK, so migrateIssueCounterRows resolves the row
	// through the bundle's own remapped crews table like everything else.
	"issue_counters": {{column: "crew_id", refTable: "crews", refColumn: "id"}},
}

// forkRegeneratedColumns names columns a forked restore must give a NEW value
// to because they carry an identity that is UNIQUE across the whole INSTANCE,
// beyond the row's own primary key.
//
// Pass 1 regenerating `id` is what lets a fork land beside its source, and for
// most tables that is the entire identity. It is not for a table that carries a
// SECOND unique column: the source row is still sitting on the same instance
// holding that value. RestoreDump inserts with INSERT OR IGNORE — deliberately,
// so re-restoring a bundle is idempotent — so the forked row does not fail
// loudly on the collision, it is dropped in silence. Every child row whose FK
// pass 2 correctly rewrote to the fork's brand-new parent id then dangles, and
// the deferred foreign_key_check blames the CHILD for a parent that never
// arrived. That is #2260, where a single missions row took every
// mission_activity row down with it and made --as-workspace unusable for any
// workspace that had ever run a mission.
//
// Membership test, and it is the whole test: is the column unique per INSTANCE?
// A column that is UNIQUE per workspace or per crew needs nothing here —
// crews.slug, missions.identifier and pages.slug are all scoped by a column
// that pass 2 already remaps, so the fork's rows land in a namespace of their
// own. Only a bare `UNIQUE` with no scope column in the key belongs in this
// map.
//
// Regenerating a value is only safe while nothing else stores it. Before adding
// an entry, check that no other table references the column BY VALUE — a
// regenerated token that some sibling row still quotes is worse than the
// collision it fixes. missions.trace_id passes: it is read back on the mission
// row itself (missions_internal.go) and handed to the progress writer for live
// SSE fan-out, and the trace_id that journal_entries and eval_runs carry is an
// agent RUN id (journal/runs.go groups runs by it), never a mission's.
var forkRegeneratedColumns = map[string][]forkRegeneratedColumn{
	// missions.trace_id is `TEXT NOT NULL UNIQUE` with no workspace in the
	// key (migrate_consts_v02_v15.go). Its sibling `identifier` is NOT here:
	// #1733 rescoped that one to UNIQUE(workspace_id, identifier), which is
	// exactly the shape that needs no fork handling.
	"missions": {{column: "trace_id", regenerate: newMissionTraceID}},
}

// forkRegeneratedColumn is one such column and the mint that replaces it. The
// mint takes the OLD value so it can preserve whatever the value's shape says
// about the row — see newMissionTraceID.
type forkRegeneratedColumn struct {
	column     string
	regenerate func(old string) string
}

// missionTracePrefixRe matches the short lowercase tag the mission-creating
// paths put in front of a trace id's random tail: `mission-` from the mission
// and task-state creators, `issue-` from the two issue creators, `tr_` from
// cartographer.Fork. Matching the prefix rather than enumerating the known
// tags means a fifth creator with a fifth tag needs no change here.
var missionTracePrefixRe = regexp.MustCompile(`^[a-z]{1,12}[-_]`)

// defaultMissionTracePrefix is what a regenerated trace id gets when the
// bundle's value carries no recognisable prefix — an old row, or one written
// by something outside the current mint sites. `mission-` is the shape
// internal/api mints for a mission created through the API.
const defaultMissionTracePrefix = "mission-"

// newMissionTraceID mints a replacement for a forked mission's trace_id,
// keeping the source value's prefix so the fork's traces still say how the
// mission was created, and swapping the identifying tail for a fresh CUID —
// the same `<tag> + generateCUID()` shape internal/api writes.
func newMissionTraceID(old string) string {
	prefix := defaultMissionTracePrefix
	if p := missionTracePrefixRe.FindString(old); p != "" {
		prefix = p
	}
	return prefix + newRemapCUID()
}

// foreignKeyEdge captures one FK column's destination.
type foreignKeyEdge struct {
	column    string // column on the source table
	refTable  string // table the FK references
	refColumn string // column on that table (typically "id")
}

// introspectForeignKeys asks SQLite for the FK edges of each table we
// care about. Used exclusively by RemapIDs so we do not have to
// hard-code the schema here — a future migration that adds a new FK
// will be picked up automatically.
//
// `PRAGMA foreign_key_list(<name>)` cannot be parametrised by the SQL
// driver (PRAGMA names are not bindable placeholders), so the table
// identifier is concatenated into the query string. Gate that with
// sqlIdentifierRe so a future caller that forwards external input can
// only inject through a name shaped like a real identifier; the
// regex denies anything containing whitespace, quotes, semicolons,
// or parentheses.
func introspectForeignKeys(ctx context.Context, db *sql.DB, table string) ([]foreignKeyEdge, error) {
	if !sqlIdentifierRe.MatchString(table) {
		return nil, fmt.Errorf("backup: invalid table identifier %q", table)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("backup: foreign_key_list(%s): %w", table, err)
	}
	defer rows.Close()
	var out []foreignKeyEdge
	for rows.Next() {
		var (
			id, seq                  int
			refTable, from, to       string
			onUpdate, onDelete, mtch string
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &mtch); err != nil {
			return nil, err
		}
		if from == "" || refTable == "" {
			continue
		}
		if to == "" {
			// Some schemas omit the target column name; SQLite treats it
			// as the referenced table's PK. "id" is our universal PK.
			to = "id"
		}
		out = append(out, foreignKeyEdge{column: from, refTable: refTable, refColumn: to})
	}
	return out, rows.Err()
}

// RemapIDs rewrites every primary-key value in dump and threads the
// mapping through every FK column so the resulting dump can be
// INSERT'd into a database that already contains the source rows
// without a collision. Called only when the admin supplied
// --as-workspace / --as-crew to signal "I want a NEW workspace or
// crew alongside the existing ones".
//
// Scope:
//   - PKs: regenerate the "id" column on every row in BackupTables.
//     Tables that lack an id column (none in the current MVP schema
//     but safe to tolerate) pass through unchanged.
//   - FKs: rewrite any column whose SQLite foreign_key_list names a
//     table we have already remapped. Unknown FK targets are left
//     alone so a row referencing users.id (we do not remap users)
//     still points at the original user.
//
// Introspection runs on the TARGET database so we get the real live
// schema, not whatever the bundle's origin might have had. A table
// missing on the target is treated as "no FKs" and the remap for
// that table is a no-op (RestoreDump later skips the insert too).
func RemapIDs(ctx context.Context, db *sql.DB, dump *DBDump) error {
	if dump == nil {
		return nil
	}
	// table → edges. Build once so the two-pass walk stays fast.
	fks := map[string][]foreignKeyEdge{}
	for _, table := range BackupTables {
		edges, err := introspectForeignKeys(ctx, db, table)
		if err != nil {
			return err
		}
		// Union in the columns the schema does not declare (see
		// virtualForeignKeys), skipping any the target's schema has
		// since grown a real REFERENCES clause for — a future
		// migration that adds the FK must not produce a duplicate
		// edge here.
		for _, v := range virtualForeignKeys[table] {
			declared := false
			for _, e := range edges {
				if e.column == v.column {
					declared = true
					break
				}
			}
			if !declared {
				edges = append(edges, v)
			}
		}
		fks[table] = edges
	}

	// Pass 1: regenerate PKs. Walk in BackupTables order so the
	// mapping for a parent table is populated before any child sees
	// its FK rewritten in pass 2.
	//
	// Tables in nonRemappablePKTables are SKIPPED — their IDs are
	// globally namespaced and protected by UNIQUE constraints on
	// human-readable columns (skills.name/.slug, users.email). The
	// target instance already has rows with these IDs (bundled skills
	// from SeedBundledSkills on every boot; admin users from prior
	// installs); regenerating PKs in the dump then INSERT OR IGNORE
	// would collide on the UNIQUE constraint, swallow the bundle row,
	// and leave dependent FK rows (agent_skills.skill_id,
	// crew_members.user_id, chats.created_by) pointing at the new id
	// — which never landed. The cascade aborts the whole restore on
	// the deferred FK check. Skipping these tables means dependent
	// FKs pass through unchanged in pass 2 (no idMap entry), and the
	// target's stable row satisfies the FK at restore time.
	idMap := map[string]map[string]string{}
	for _, table := range BackupTables {
		if nonRemappablePKTables[table] {
			continue
		}
		rows := dump.Tables[table]
		if len(rows) == 0 {
			continue
		}
		for _, row := range rows {
			// Non-PK unique identity first, and independent of the id
			// rewrite below: a row the bundle carries without an id is
			// not remapped at all, but a row that HAS one must not keep
			// an instance-unique value the source still holds. See
			// forkRegeneratedColumns for the failure mode.
			for _, rc := range forkRegeneratedColumns[table] {
				oldVal, ok := row[rc.column].(string)
				if !ok || oldVal == "" {
					continue
				}
				row[rc.column] = rc.regenerate(oldVal)
			}
			oldID, ok := row["id"].(string)
			if !ok || oldID == "" {
				continue
			}
			newID := newRemapCUID()
			if idMap[table] == nil {
				idMap[table] = map[string]string{}
			}
			idMap[table][oldID] = newID
			row["id"] = newID
		}
	}

	// Pass 2: rewrite FK columns via idMap. An FK that points at a
	// table we did not remap (e.g. users) keeps its old value.
	for _, table := range BackupTables {
		rows := dump.Tables[table]
		if len(rows) == 0 {
			continue
		}
		edges := fks[table]
		if len(edges) == 0 {
			continue
		}
		for _, row := range rows {
			for _, edge := range edges {
				oldVal, ok := row[edge.column].(string)
				if !ok || oldVal == "" {
					continue
				}
				if submap, ok := idMap[edge.refTable]; ok {
					if newVal, ok := submap[oldVal]; ok {
						row[edge.column] = newVal
					}
				}
			}
		}
	}
	return nil
}
