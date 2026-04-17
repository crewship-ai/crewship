package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// remapCounter is the monotonic counter portion of backup-generated
// CUIDs. Kept package-local so it does not collide with the one in
// internal/api; the formats are compatible either way.
var remapCounter atomic.Uint64

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

// base36 remains a thin wrapper around strconv.FormatInt so any external
// users (or tests) that depend on the public shape keep working.
func base36(n int64) string {
	return strconv.FormatInt(n, 36)
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
func introspectForeignKeys(ctx context.Context, db *sql.DB, table string) ([]foreignKeyEdge, error) {
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
		fks[table] = edges
	}

	// Pass 1: regenerate PKs. Walk in BackupTables order so the
	// mapping for a parent table is populated before any child sees
	// its FK rewritten in pass 2.
	idMap := map[string]map[string]string{}
	for _, table := range BackupTables {
		rows := dump.Tables[table]
		if len(rows) == 0 {
			continue
		}
		for _, row := range rows {
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
