package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// discovery.go — runtime schema introspection. Walks SQLite FK graph
// from the `workspaces` table outward to identify every table that
// transitively scopes to a workspace. Used by:
//
//   1. DumpWorkspace — validate that BackupTables (the authoritative
//      allowlist) does not silently drop a workspace-scoped table that
//      a new migration added. Drift surfaces as ErrDiscoveryDrift so
//      CI catches the gap before a bundle ships missing rows.
//
//   2. RestoreReplace — the `--replace` flag must wipe every
//      workspace-scoped row before INSERT. Walking the schema means we
//      cannot leave orphan rows behind because we forgot to add a
//      table to a hand-maintained list.
//
// The walk is BREADTH-FIRST from `workspaces`, following REVERSE FK
// edges (i.e. "which tables reference this one?"). Any table reachable
// from `workspaces` by reverse-FK traversal is workspace-scoped. The
// path back to `workspaces` is recorded so callers can synthesise a
// JOIN-based WHERE clause without hardcoding it.
//
// NULLABILITY IS PART OF THE CHOICE (#1973). The path a table gets is
// not merely the shortest one: a filter on a NULLABLE column omits
// every row where that column is NULL, and it omits them silently —
// the backup succeeds, the bundle verifies, and the rows are simply
// not in it. So the walk runs in two passes. The first follows only
// NOT NULL foreign keys, which yields a filter that is TOTAL over the
// table: every row is either in the workspace or in another one, and
// none falls through. The second pass picks up whatever the first
// could not reach, where a nullable hop is the only hop there is.
// A longer NOT NULL path always beats a shorter nullable one.
//
// The walk is also LEVEL-SYNCHRONOUS and its adjacency is built in
// sorted table order, because two parents at the same depth used to
// race to claim a child through map iteration order — the same schema
// produced `keeper_requests` scoped through `credential_id` on one run
// and `requesting_agent_id` on the next, which made a bundle's
// contents depend on the Go runtime.
//
// What this deliberately does NOT do: probe content. A table that
// references workspaces but is "operational state that stays with the
// destination instance" (audit_logs, backup_locks, backup_catalog) is
// still discovered as workspace-scoped — that's mechanically correct.
// The allowlist decides intent ("do we want this in the bundle?"),
// discovery decides safety ("did we forget any?"). See
// CategoriseScopedTables for the exclude-list semantics.

// ScopedTable describes a table that transitively scopes to a workspace.
type ScopedTable struct {
	// Name is the SQLite table identifier.
	Name string
	// JoinPath is the chain of FK edges from this table back to
	// `workspaces`. JoinPath[0] is the edge directly on this table;
	// JoinPath[len-1] points at workspaces. A direct-scoped table
	// (one with a workspace_id column) has a single-element JoinPath.
	JoinPath []ScopeEdge
}

// ScopeEdge is one hop along the FK chain back to `workspaces`.
type ScopeEdge struct {
	// FromTable is the table holding the FK column.
	FromTable string
	// FromColumn is the FK column on FromTable.
	FromColumn string
	// ToTable is the table the FK references.
	ToTable string
	// ToColumn is the column on ToTable the FK targets (typically "id").
	ToColumn string
}

// WorkspaceScopeFilter returns a parametrised WHERE clause fragment
// (and arg list) that selects only rows on this table belonging to
// the given workspace. Convenience wrapper for the single-id case.
func (st ScopedTable) WorkspaceScopeFilter(workspaceID string) (string, []any) {
	return st.WorkspaceScopeFilterIDs([]string{workspaceID})
}

// WorkspaceScopeFilterIDs is the multi-workspace variant. Used by
// ReplaceWorkspaceContents which clears every target workspace that
// matches the bundle by either id OR slug — possibly multiple rows.
//
// Depth 1 (direct workspace_id column) collapses to `col = ?` for
// a single id, or `col IN (?, ?, ...)` for many.
// Depth N expands inside-out into a chain of IN-subqueries that
// traces JoinPath back to workspaces. The deepest level uses an
// equality (or IN) against workspaces.id directly so we avoid the
// otherwise-trailing `id IN (SELECT id FROM workspaces WHERE id = ?)`
// no-op that the SQLite query planner can't always fold.
//
// Empty workspaceIDs returns `1=0` — fail closed rather than
// exfiltrate every row.
//
// Example expansions:
//
//	depth 1 / 1 id (chats):
//	    workspace_id = ?
//
//	depth 1 / N ids (chats):
//	    workspace_id IN (?, ?, ?)
//
//	depth 2 / 1 id (agents):
//	    crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)
//
//	depth 3 / N ids (agent_skills):
//	    agent_id IN (SELECT id FROM agents WHERE crew_id IN
//	      (SELECT id FROM crews WHERE workspace_id IN (?, ?)))
func (st ScopedTable) WorkspaceScopeFilterIDs(workspaceIDs []string) (string, []any) {
	if len(st.JoinPath) == 0 {
		// Workspaces itself has an empty JoinPath. We never dump it
		// via filter (it's the anchor), but a misuse should fail
		// closed rather than exfiltrating every row.
		return "1=0", nil
	}
	if len(workspaceIDs) == 0 {
		return "1=0", nil
	}
	args := make([]any, len(workspaceIDs))
	for i, id := range workspaceIDs {
		args[i] = id
	}

	// Innermost predicate runs on the table CLOSEST to workspaces:
	// either "<col> = ?" or "<col> IN (?, ?, ...)" depending on id
	// count.
	last := st.JoinPath[len(st.JoinPath)-1]
	leaf := equalityOrIN(quoteIdent(last.FromColumn), len(workspaceIDs))

	// Walk outward from second-to-last edge back to JoinPath[0]
	// (which is the edge directly on st). Each level wraps the
	// previous `where` in a subquery against the closer-to-this-table
	// FK column.
	where := leaf
	for i := len(st.JoinPath) - 2; i >= 0; i-- {
		edge := st.JoinPath[i]
		where = fmt.Sprintf("%s IN (SELECT %s FROM %s WHERE %s)",
			quoteIdent(edge.FromColumn),
			quoteIdent(edge.ToColumn),
			quoteIdent(edge.ToTable),
			where,
		)
	}
	return where, args
}

// equalityOrIN renders either `<col> = ?` (single id, lets SQLite use
// equality index lookup) or `<col> IN (?, ?, ...)` (multiple ids).
// The single-id branch is the hot path — every workspace-scoped
// SELECT during normal dump goes through it.
func equalityOrIN(quotedCol string, n int) string {
	if n == 1 {
		return quotedCol + " = ?"
	}
	placeholders := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		if i > 0 {
			placeholders = append(placeholders, ',', ' ')
		}
		placeholders = append(placeholders, '?')
	}
	return fmt.Sprintf("%s IN (%s)", quotedCol, placeholders)
}

// DiscoverScopedTables walks the FK graph from `workspaces` outward
// and returns every table that transitively scopes to a workspace.
// The result is deterministic (alphabetical by table name) so test
// fixtures stay stable across runs.
//
// Cycles in the FK graph are tolerated: a table seen twice gets the
// shortest path (BFS guarantees this) and is not revisited.
//
// Tables without an `id` PK column that aren't reachable as anchors
// still get included — the caller may need additional logic to dump
// them.
func DiscoverScopedTables(ctx context.Context, db *sql.DB) ([]ScopedTable, error) {
	return discoverScopedTables(ctx, db)
}

// scopeQuerier is the read surface the walk needs. *sql.DB and *sql.Tx
// both satisfy it, which is why there is one implementation instead of
// the two that used to drift apart — the tx-bound twin never got the
// fixes the exported one did, and it is the twin that decides what a
// `--replace` restore deletes.
type scopeQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// discoverScopedTables is the shared implementation behind
// DiscoverScopedTables and discoverScopedTablesTx. See the file header
// for why the walk prefers NOT NULL edges and is level-synchronous.
func discoverScopedTables(ctx context.Context, q scopeQuerier) ([]ScopedTable, error) {
	allTables, err := listAllTables(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("backup: discover scoped tables: %w", err)
	}
	// reverseFK[parent] = edges that name `parent` as ToTable, and
	// notNull[table][column] = "this column is declared NOT NULL".
	//
	// Both are filled by iterating allTables in the sorted order
	// listAllTables returns, so every adjacency list is in a stable
	// order regardless of Go's map iteration.
	reverseFK := map[string][]ScopeEdge{}
	notNull := map[string]map[string]bool{}
	for _, t := range allTables {
		edges, err := tableFKEdges(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("backup: discover scoped tables: introspect %q: %w", t, err)
		}
		for _, e := range edges {
			reverseFK[e.ToTable] = append(reverseFK[e.ToTable], e)
		}
		cols, err := notNullColumns(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("backup: discover scoped tables: columns of %q: %w", t, err)
		}
		notNull[t] = cols
	}

	// visited[table] = the JoinPath we settled on. `workspaces` is the
	// anchor and carries the empty path.
	visited := map[string][]ScopeEdge{"workspaces": nil}

	// walk expands `visited` one level at a time. requireNotNull=true
	// is the first pass: it only follows NOT NULL foreign keys, so a
	// table it reaches gets a filter that holds for EVERY one of its
	// rows. The second pass (false) fills in the tables for which no
	// such chain exists at all.
	walk := func(requireNotNull bool) {
		frontier := sortedKeys(visited)
		for len(frontier) > 0 {
			// Collect every candidate edge for the whole level BEFORE
			// choosing any, so no child is claimed by whichever parent
			// happened to be dequeued first.
			best := map[string]ScopeEdge{}
			for _, parent := range frontier {
				for _, edge := range reverseFK[parent] {
					if _, seen := visited[edge.FromTable]; seen {
						continue
					}
					if requireNotNull && !notNull[edge.FromTable][edge.FromColumn] {
						continue
					}
					cur, have := best[edge.FromTable]
					if !have || betterScopeEdge(edge, cur, visited, notNull) {
						best[edge.FromTable] = edge
					}
				}
			}
			next := sortedKeys(best)
			for _, table := range next {
				edge := best[table]
				parentPath := visited[edge.ToTable]
				path := make([]ScopeEdge, 0, len(parentPath)+1)
				path = append(path, edge)
				path = append(path, parentPath...)
				visited[table] = path
			}
			frontier = next
		}
	}
	walk(true)
	walk(false)

	// Result excludes `workspaces` itself (it's the anchor, not a
	// "scoped" table). Sort for determinism.
	out := make([]ScopedTable, 0, len(visited)-1)
	for table, path := range visited {
		if table == "workspaces" {
			continue
		}
		out = append(out, ScopedTable{Name: table, JoinPath: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// betterScopeEdge reports whether candidate `a` is a better way to
// reach a.FromTable than the incumbent `b`. Both edges are known to
// point at tables that are already resolved, so the comparison is
// total and does not depend on iteration order:
//
//  1. fewer hops back to `workspaces` — a shorter filter is a cheaper
//     one, and among correct filters brevity is the tiebreak we want;
//  2. a NOT NULL column beats a nullable one. Within the first walk
//     every candidate is NOT NULL so this never fires; in the second
//     it picks the least-lossy of the remaining options;
//  3. column then target name, alphabetically — an arbitrary but
//     STABLE last resort, which is the whole point.
func betterScopeEdge(a, b ScopeEdge, visited map[string][]ScopeEdge, notNull map[string]map[string]bool) bool {
	aHops, bHops := len(visited[a.ToTable]), len(visited[b.ToTable])
	if aHops != bHops {
		return aHops < bHops
	}
	aNotNull := notNull[a.FromTable][a.FromColumn]
	bNotNull := notNull[b.FromTable][b.FromColumn]
	if aNotNull != bNotNull {
		return aNotNull
	}
	if a.FromColumn != b.FromColumn {
		return a.FromColumn < b.FromColumn
	}
	if a.ToTable != b.ToTable {
		return a.ToTable < b.ToTable
	}
	return a.ToColumn < b.ToColumn
}

// sortedKeys returns a map's keys in ascending order. The walk uses it
// everywhere it would otherwise iterate a map, because "which table
// claimed this child" must not be a coin flip.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// notNullColumns returns the set of columns on `table` declared NOT
// NULL.
//
// Note what this deliberately does NOT count: a bare `col TEXT PRIMARY
// KEY`. SQLite's rowid tables accept NULL in a non-INTEGER primary key
// — a legacy quirk it documents and keeps for compatibility — so the
// declaration is not the guarantee it looks like, and treating it as
// one would be exactly the assumption that loses rows. A table that
// wants its key to be a usable scope path must say NOT NULL.
func notNullColumns(ctx context.Context, q scopeQuerier, table string) (map[string]bool, error) {
	if !sqlIdentifierRe.MatchString(table) {
		return nil, fmt.Errorf("backup: invalid table identifier %q", table)
	}
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("backup: table_info(%s): %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("backup: scan table_info row for %q: %w", table, err)
		}
		if notnull == 1 {
			out[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: iterate table_info for %q: %w", table, err)
	}
	return out, nil
}

// listAllTables returns every user table in the current schema,
// excluding sqlite internal tables (sqlite_*, sqlite_sequence) and
// FTS5 virtual table shadow tables (*_fts_*) which would otherwise
// be re-discovered through FK edges to journal_entries etc.
func listAllTables(ctx context.Context, q scopeQuerier) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE '%_fts'
		  AND name NOT LIKE '%_fts_%'
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("backup: list tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("backup: scan sqlite_master row: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: iterate sqlite_master: %w", err)
	}
	return out, nil
}

// tableFKEdges returns the FK edges out of `table`. Wraps
// introspectForeignKeys from remap.go but accepts a tx-or-db and
// returns the lighter ScopeEdge shape that includes FromTable.
func tableFKEdges(ctx context.Context, q scopeQuerier, table string) ([]ScopeEdge, error) {
	if !sqlIdentifierRe.MatchString(table) {
		return nil, fmt.Errorf("backup: invalid table identifier %q", table)
	}
	rows, err := q.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("backup: foreign_key_list(%s): %w", table, err)
	}
	defer rows.Close()
	var out []ScopeEdge
	for rows.Next() {
		var (
			id, seq            int
			refTable, from, to string
			onUpdate, onDelete sql.NullString
			matchClause        sql.NullString
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &matchClause); err != nil {
			return nil, fmt.Errorf("backup: scan FK row for %q: %w", table, err)
		}
		if from == "" || refTable == "" {
			continue
		}
		if to == "" {
			to = "id"
		}
		out = append(out, ScopeEdge{
			FromTable:  table,
			FromColumn: from,
			ToTable:    refTable,
			ToColumn:   to,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: iterate FK rows for %q: %w", table, err)
	}
	return out, nil
}

// CategoriseScopedTables splits discovered tables into three buckets
// according to ScopedTableIntent (defined below). The intent map is
// the AUTHORITATIVE allowlist — every discovered table must have an
// entry, otherwise CategoriseScopedTables returns ErrDiscoveryDrift
// listing the unknowns. That's the safety net: a new migration that
// adds a workspace-scoped table forces a developer to make an
// explicit "include / exclude" decision rather than getting silent
// data loss at backup time.
func CategoriseScopedTables(discovered []ScopedTable, intent map[string]ScopedTableIntent) (include []ScopedTable, exclude []ScopedTable, err error) {
	var unknown []string
	for _, st := range discovered {
		i, ok := intent[st.Name]
		if !ok {
			unknown = append(unknown, st.Name)
			continue
		}
		switch i {
		case IntentInclude:
			include = append(include, st)
		case IntentExcludeOperational, IntentExcludeRuntime:
			exclude = append(exclude, st)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, fmt.Errorf("%w: %v (add to BackupTableIntent in intent.go)", ErrDiscoveryDrift, unknown)
	}
	return include, exclude, nil
}

// ScopedTableIntent describes what a developer wants the backup
// system to do with a discovered workspace-scoped table.
type ScopedTableIntent int

const (
	// IntentInclude — round-trip the table contents in workspace
	// bundles. The default for almost every user-facing entity.
	IntentInclude ScopedTableIntent = iota
	// IntentExcludeOperational — table is local to the instance and
	// MUST NOT be carried across restores (audit_logs, backup_locks,
	// backup_catalog, journal_embeddings).
	IntentExcludeOperational
	// IntentExcludeRuntime — table is populated by the running agent
	// or background services and gets re-created naturally
	// (sessions, rate-limit buckets, cli_pairings). Including these
	// in a bundle would resurrect stale connections after restore.
	IntentExcludeRuntime
)
