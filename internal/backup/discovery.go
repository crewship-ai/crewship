package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
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
// could not reach, ranking what is left by how many nullable hops the
// WHOLE path carries — a NOT NULL column hanging off a parent that was
// itself reached through a nullable one loses rows just the same. A
// longer NOT NULL path always beats a shorter nullable one.
//
// Two rules sit above that, and both are about agreeing with somebody
// else. A table with its own foreign key into `workspaces` is anchored
// on that column even when it is nullable, because DumpWorkspace
// short-circuits to `workspace_id = ?` for any table carrying it and a
// `--replace` DELETE that scopes differently would remove rows the
// bundle never contained. And where two paths are otherwise equal, an
// ON DELETE CASCADE edge wins: a row the database deletes with its
// parent belongs to that parent, and belonging is what scope means.
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
	// outgoing[table] = that table's own FK edges, in the order
	// PRAGMA reports them; notNull[table][column] and cascade[table][column]
	// answer "declared NOT NULL" and "declared ON DELETE CASCADE".
	//
	// All three are filled by iterating allTables in the sorted order
	// listAllTables returns, so nothing downstream depends on Go's map
	// iteration order.
	outgoing := map[string][]ScopeEdge{}
	notNull := map[string]map[string]bool{}
	cascade := map[string]map[string]bool{}
	for _, t := range allTables {
		edges, err := tableFKEdges(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("backup: discover scoped tables: introspect %q: %w", t, err)
		}
		outgoing[t] = edges
		onDelete, err := cascadingFKColumns(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("backup: discover scoped tables: fk actions of %q: %w", t, err)
		}
		cascade[t] = onDelete
		cols, err := notNullColumns(ctx, q, t)
		if err != nil {
			return nil, fmt.Errorf("backup: discover scoped tables: columns of %q: %w", t, err)
		}
		notNull[t] = cols
	}

	// The walk is a shortest-path relaxation, not a plain BFS, because
	// the cost being minimised is not distance. cost[table] is
	// (nullable hops, total hops) and a table's assignment is REVISED
	// whenever a cheaper route turns up — which is the whole point: a
	// breadth-first walk commits a table the first level it is reachable
	// from, and the cheaper route usually lies through a parent that is
	// itself further out and therefore not resolved yet.
	path := map[string][]ScopeEdge{"workspaces": nil}
	cost := map[string]scopeCost{"workspaces": {}}
	pinned := map[string]bool{"workspaces": true}
	rank := scopeRank{path: path, cost: cost, notNull: notNull, cascade: cascade}

	// A table with its OWN foreign key into `workspaces` is anchored on
	// that column and nothing else — even when the column is nullable,
	// and even when a cheaper route exists. Hence `pinned`: no later
	// relaxation may move it.
	//
	// Not a preference, a requirement. DumpWorkspace short-circuits to
	// `workspace_id = ?` for any table carrying that column (see
	// dbdump.go), while ReplaceWorkspaceContents scopes its DELETE by
	// the path found here. Let the two disagree and `--replace` deletes
	// rows the bundle it is making room for never contained —
	// credential_audit, whose workspace_id is nullable and whose
	// credential_id is not, is exactly that shape.
	//
	// A NULL here is also not the loss this file is about: it means the
	// row belongs to no workspace, and leaving it out of a workspace
	// bundle is the right answer. See the note in
	// TestScopedFilters_NeverTraverseANullableFK.
	for _, table := range allTables {
		var anchor *ScopeEdge
		for _, edge := range outgoing[table] {
			if edge.ToTable != "workspaces" {
				continue
			}
			if anchor == nil || rank.betterEdge(edge, *anchor) {
				e := edge
				anchor = &e
			}
		}
		if anchor != nil {
			path[table] = []ScopeEdge{*anchor}
			cost[table] = scopeCost{nullHops: boolToInt(!notNull[table][anchor.FromColumn]), hops: 1}
			pinned[table] = true
		}
	}

	// Relax until nothing improves. Each pass sweeps the tables in sorted
	// order and each assignment strictly lowers that table's cost, so the
	// loop settles; the bound is belt-and-braces against a comparison bug
	// turning a cycle in the FK graph into a hang.
	for round := 0; round <= len(allTables); round++ {
		changed := false
		for _, table := range allTables {
			if pinned[table] {
				continue
			}
			for _, edge := range outgoing[table] {
				parentPath, resolved := path[edge.ToTable]
				if !resolved {
					continue
				}
				// Never route a table through a path that already passes
				// through it: the filter would be circular and the FK graph
				// does contain cycles.
				if pathVisits(parentPath, table) {
					continue
				}
				if _, have := path[table]; have && !rank.betterEdge(edge, path[table][0]) {
					continue
				}
				next := make([]ScopeEdge, 0, len(parentPath)+1)
				next = append(next, edge)
				next = append(next, parentPath...)
				path[table] = next
				cost[table] = rank.costVia(edge)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	visited := path

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

// scopeCost is what the walk minimises for each table, in order:
// nullable hops first, then total hops.
//
// Nullable hops come first because they are the only part of a filter
// that can be WRONG. Every hop of a path costs a subquery; a NULLABLE
// hop costs rows. So a four-hop path with no nullable column beats a
// one-hop path with one, every time.
type scopeCost struct {
	nullHops int
	hops     int
}

func (c scopeCost) cheaperThan(o scopeCost) bool {
	if c.nullHops != o.nullHops {
		return c.nullHops < o.nullHops
	}
	return c.hops < o.hops
}

// scopeRank holds what the walk needs to compare two candidate edges.
// Bundled into one value because every criterion below reads a
// different map and threading four of them through a comparison
// function is how the criteria get quietly reordered.
type scopeRank struct {
	path    map[string][]ScopeEdge
	cost    map[string]scopeCost
	notNull map[string]map[string]bool
	cascade map[string]map[string]bool
}

// costVia is what reaching edge.FromTable would cost if it went through
// this edge. The parent must already be resolved.
func (r scopeRank) costVia(edge ScopeEdge) scopeCost {
	parent := r.cost[edge.ToTable]
	return scopeCost{
		nullHops: parent.nullHops + boolToInt(!r.notNull[edge.FromTable][edge.FromColumn]),
		hops:     parent.hops + 1,
	}
}

// betterEdge reports whether reaching a.FromTable through `a` beats
// reaching it through the incumbent `b`. Both edges point at tables
// that are already resolved, so the comparison is total and does not
// depend on iteration order. In priority:
//
//  1. the cheaper scopeCost — fewer nullable hops, then fewer hops.
//     Nullability is measured over the WHOLE path, not the edge,
//     because a NOT NULL column hanging off a parent that was itself
//     reached through a nullable one loses rows just the same.
//  2. ON DELETE CASCADE over anything else. A row the database deletes
//     with its parent belongs to that parent, and belonging is what
//     scope means: page_panels can be reached through its page
//     (CASCADE) or its owning crew (RESTRICT), and the page is the
//     answer that stays true if a crew is ever allowed to sit in
//     another workspace.
//  3. column then target name, alphabetically — an arbitrary but STABLE
//     last resort, which is the whole point.
func (r scopeRank) betterEdge(a, b ScopeEdge) bool {
	aCost, bCost := r.costVia(a), r.costVia(b)
	if aCost != bCost {
		return aCost.cheaperThan(bCost)
	}
	aCascade := r.cascade[a.FromTable][a.FromColumn]
	bCascade := r.cascade[b.FromTable][b.FromColumn]
	if aCascade != bCascade {
		return aCascade
	}
	if a.FromColumn != b.FromColumn {
		return a.FromColumn < b.FromColumn
	}
	if a.ToTable != b.ToTable {
		return a.ToTable < b.ToTable
	}
	return a.ToColumn < b.ToColumn
}

// pathVisits reports whether `table` already appears as a hop on the
// path. The FK graph has cycles, and a filter that routed a table
// through itself would be nonsense.
func pathVisits(path []ScopeEdge, table string) bool {
	for _, e := range path {
		if e.FromTable == table || e.ToTable == table {
			return true
		}
	}
	return false
}

// cascadingFKColumns returns the set of FK columns on `table` declared
// ON DELETE CASCADE — the schema's own statement that a row here is
// owned by the row it points at.
func cascadingFKColumns(ctx context.Context, q scopeQuerier, table string) (map[string]bool, error) {
	if !sqlIdentifierRe.MatchString(table) {
		return nil, fmt.Errorf("backup: invalid table identifier %q", table)
	}
	rows, err := q.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("backup: foreign_key_list(%s): %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			id, seq            int
			refTable, from, to string
			onUpdate, onDelete sql.NullString
			matchClause        sql.NullString
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &matchClause); err != nil {
			return nil, fmt.Errorf("backup: scan FK action row for %q: %w", table, err)
		}
		if strings.EqualFold(onDelete.String, "CASCADE") {
			out[from] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: iterate FK action rows for %q: %w", table, err)
	}
	return out, nil
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
