package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// replace.go — implementation of `--replace` restore mode.
//
// Replace is the canonical disaster-recovery path: an admin has a
// bundle and wants the target instance to look EXACTLY like the
// source did at backup time, including the original workspace ID.
//
// The previous strict-ID gate in the API handler rejected this
// scenario because a post-`dev.sh nuke` bootstrap generates a fresh
// workspace CUID that never matches the bundle's. The fix here is
// twofold:
//
//   1. The API gate (bundleBelongsToWorkspace) is dropped for
//      restore — admin role is the only authorisation needed.
//   2. Before INSERT, ReplaceWorkspaceContents walks the FK-discovered
//      table set and DELETEs every row belonging to the target
//      workspace by either id or slug match. The bundle then lands
//      its rows preserving the original IDs.
//
// The "by slug" matching is what makes the post-nuke flow work: even
// though the fresh-bootstrap workspace has a new id, its slug matches
// (e.g. "uo-outlands") and gets cleared so the bundle's row with
// the original id can land without UNIQUE-slug collision.
//
// Implementation notes:
//
//   - Discovery + IntentMap is the authoritative table set. New
//     migrations that add workspace-scoped tables are auto-included
//     once their intent is declared.
//   - DELETE order is REVERSE FK dependency: children before parents
//     so FK constraints don't fire. resolveDeletionOrder uses the
//     same JoinPath introspection the dump uses.
//   - All inside the caller-supplied transaction so a failure rolls
//     back cleanly.

// ReplaceWorkspaceContents deletes every workspace-scoped row that
// either has the bundle workspace's id OR shares its slug. After
// this returns, RestoreDumpTx can INSERT the bundle's rows preserving
// the original IDs without UNIQUE / PK conflicts.
//
// Returns the count of rows deleted per table — useful for logging
// what the operator just wiped.
//
// bundleWorkspaceID and bundleWorkspaceSlug come from the bundle's
// dump (NOT from the target — the whole point is that they may
// disagree with whatever the target had under that slug).
func ReplaceWorkspaceContents(ctx context.Context, tx *sql.Tx, bundleWorkspaceID, bundleWorkspaceSlug string) (map[string]int, error) {
	if bundleWorkspaceID == "" {
		return nil, fmt.Errorf("backup: ReplaceWorkspaceContents requires bundleWorkspaceID")
	}
	// Resolve the target's workspace id under the same SLUG as the
	// bundle. The slug is the durable user-visible identifier; if a
	// fresh bootstrap workspace exists under the same slug we need
	// to clear it under THAT id, not the bundle's id.
	targetIDs, err := resolveTargetWorkspaceIDs(ctx, tx, bundleWorkspaceID, bundleWorkspaceSlug)
	if err != nil {
		return nil, err
	}
	if len(targetIDs) == 0 {
		// No matching workspace on the target — nothing to wipe. This
		// is the "restore into completely fresh instance" path and
		// the bundle's INSERT can land with original IDs without
		// any pre-clear.
		return map[string]int{}, nil
	}

	// Discover the FK-scoped table set and apply intent.
	scoped, err := discoverScopedTablesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	include, _, err := CategoriseScopedTables(scoped, BackupTableIntent)
	if err != nil {
		return nil, err
	}
	// DELETE in reverse-FK-dependency order so children land before
	// parents (deletion-side; INSERT-side is the opposite).
	order := resolveDeletionOrder(include)

	deleted := map[string]int{}
	for _, st := range order {
		expr, args := st.WorkspaceScopeFilter("?-placeholder?")
		// Replace the single placeholder with N expansions for each
		// target workspace id. WorkspaceScopeFilter produces
		// exactly one `?` at the deepest level (id = ?), so we
		// expand that into `id IN (?, ?, ...)`. Detect by counting
		// the placeholders and substituting the right pattern.
		if strings.Count(expr, "?") != 1 {
			return nil, fmt.Errorf("backup: unexpected placeholder count in scope filter for %s: %s", st.Name, expr)
		}
		// Find the innermost `= ?` and replace with `IN (?, ?, ...)`.
		// The filter always ends with "workspace_id = ?" or
		// "<col> = ?" at the deepest level, so substring replace is
		// safe (no other = ? in the chain).
		placeholders := make([]string, len(targetIDs))
		ids := make([]any, len(targetIDs))
		for i, id := range targetIDs {
			placeholders[i] = "?"
			ids[i] = id
		}
		_ = args // unused; we rebuild args via targetIDs below
		// Replace the trailing `= ?` with `IN (?, ?, ...)`.
		expr = strings.Replace(expr, "= ?", "IN ("+strings.Join(placeholders, ",")+")", 1)

		query := fmt.Sprintf("DELETE FROM %s WHERE %s", quoteIdent(st.Name), expr)
		res, err := tx.ExecContext(ctx, query, ids...)
		if err != nil {
			return nil, fmt.Errorf("backup: replace delete from %s: %w", st.Name, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			deleted[st.Name] = int(n)
		}
	}

	// Finally, delete the workspace row itself. Done last because every
	// scoped table FKs into it (CASCADE or NO ACTION) and a workspaces
	// DELETE before children would violate FKs.
	placeholders := make([]string, len(targetIDs))
	ids := make([]any, len(targetIDs))
	for i, id := range targetIDs {
		placeholders[i] = "?"
		ids[i] = id
	}
	res, err := tx.ExecContext(ctx,
		"DELETE FROM workspaces WHERE id IN ("+strings.Join(placeholders, ",")+")",
		ids...,
	)
	if err != nil {
		return nil, fmt.Errorf("backup: replace delete workspaces: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		deleted["workspaces"] = int(n)
	}
	return deleted, nil
}

// resolveTargetWorkspaceIDs returns the workspace IDs that
// ReplaceWorkspaceContents must clear. Matches:
//
//   - Direct ID match: target.workspaces.id = bundle.workspace_id
//     (re-restoring into the same instance)
//   - Slug match: target.workspaces.slug = bundle.workspace_slug
//     (post-nuke, fresh bootstrap took the same slug with a new id)
//
// Both lookups are deduplicated. An empty slug skips the slug
// branch (defensive — shouldn't happen for a real bundle but a
// custom-constructed dump might lack one).
func resolveTargetWorkspaceIDs(ctx context.Context, tx *sql.Tx, bundleID, bundleSlug string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}

	// Branch 1: direct id match.
	var direct sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE id = ?`, bundleID).Scan(&direct); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("backup: lookup target by id: %w", err)
	}
	if direct.Valid && direct.String != "" {
		seen[direct.String] = true
		out = append(out, direct.String)
	}

	// Branch 2: slug match (post-nuke DR scenario).
	if bundleSlug != "" {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM workspaces WHERE slug = ?`, bundleSlug)
		if err != nil {
			return nil, fmt.Errorf("backup: lookup target by slug: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// discoverScopedTablesTx is the tx-bound twin of DiscoverScopedTables.
// Used inside RestoreDumpTx so the discovery sees the same schema
// the impending INSERTs will see (matters for in-flight migrations).
func discoverScopedTablesTx(ctx context.Context, tx *sql.Tx) ([]ScopedTable, error) {
	allTables, err := listAllTablesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	outgoing := map[string][]ScopeEdge{}
	for _, t := range allTables {
		edges, err := tableFKEdgesTx(ctx, tx, t)
		if err != nil {
			return nil, err
		}
		outgoing[t] = edges
	}
	reverseFK := map[string][]ScopeEdge{}
	for table, edges := range outgoing {
		for _, e := range edges {
			e.FromTable = table
			reverseFK[e.ToTable] = append(reverseFK[e.ToTable], e)
		}
	}
	visited := map[string][]ScopeEdge{}
	queue := []string{"workspaces"}
	visited["workspaces"] = nil
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		parentPath := visited[parent]
		for _, edge := range reverseFK[parent] {
			if _, seen := visited[edge.FromTable]; seen {
				continue
			}
			path := make([]ScopeEdge, 0, len(parentPath)+1)
			path = append(path, edge)
			path = append(path, parentPath...)
			visited[edge.FromTable] = path
			queue = append(queue, edge.FromTable)
		}
	}
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

func listAllTablesTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table'
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE '%_fts'
		  AND name NOT LIKE '%_fts_%'
		ORDER BY name
	`)
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

func tableFKEdgesTx(ctx context.Context, tx *sql.Tx, table string) ([]ScopeEdge, error) {
	if !sqlIdentifierRe.MatchString(table) {
		return nil, fmt.Errorf("backup: invalid table identifier %q", table)
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return nil, err
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
			return nil, err
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
	return out, rows.Err()
}

// resolveDeletionOrder orders the discovered tables so children land
// before parents. Computed by sorting on JoinPath depth descending
// (deepest first = furthest from workspaces = no other table FKs to
// it inside the scoped set, with the inverse-depth heuristic correct
// in our schema shape).
func resolveDeletionOrder(in []ScopedTable) []ScopedTable {
	out := make([]ScopedTable, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].JoinPath) > len(out[j].JoinPath)
	})
	return out
}
