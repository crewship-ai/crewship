package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// LegacyWaitpointPublicID maps a legacy bearer to its stable workspace-scoped identity.
func LegacyWaitpointPublicID(workspaceID, old string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("crewship waitpoint identity v1 %d:%s%s", len(workspaceID), workspaceID, old)))
	return "wp_" + hex.EncodeToString(sum[:16])
}

var legacyWaitpointRefTables = []struct{ table, stmt string }{
	{"inbox_item_reads", `UPDATE inbox_item_reads AS r SET inbox_item_id = replace(r.inbox_item_id, l.old, l.id)
 FROM temp.waitpoint_identity_map AS l JOIN inbox_items AS i ON i.workspace_id = l.workspace AND i.source_id = l.old AND i.kind = 'waitpoint'
 WHERE r.inbox_item_id = i.id`},
	{"inbox_items", `UPDATE inbox_items AS i SET id = replace(i.id, l.old, l.id), source_id = l.id FROM temp.waitpoint_identity_map AS l
 WHERE i.workspace_id = l.workspace AND i.source_id = l.old AND i.kind = 'waitpoint'`},
	{"notification_deliveries", `UPDATE notification_deliveries AS n SET source_id = l.id, dedup_key = replace(n.dedup_key, l.old, l.id)
 FROM temp.waitpoint_identity_map AS l WHERE n.workspace_id = l.workspace AND n.source_id = l.old AND n.source_kind = 'waitpoint'`},
	{"audit_logs", `UPDATE audit_logs AS a SET entity_id = l.id FROM temp.waitpoint_identity_map AS l
 WHERE a.workspace_id = l.workspace AND a.entity_id = l.old AND a.entity_type = 'waitpoint'`},
}

// ConvertWaitpointIdentities runs under the startup migration or restore transaction.
// Build one indexed map so each dependent table is updated only once.
func ConvertWaitpointIdentities(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT token,workspace_id FROM pipeline_waitpoints WHERE token < 'wp_' OR token >= 'wp`'")
	if err != nil {
		return err
	}
	type identity struct{ old, workspace string }
	var ids []identity
	for rows.Next() {
		var i identity
		if err := rows.Scan(&i.old, &i.workspace); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON;
 CREATE TEMP TABLE waitpoint_identity_map(old TEXT PRIMARY KEY,workspace TEXT NOT NULL,id TEXT NOT NULL) WITHOUT ROWID`); err != nil {
		return err
	}
	for len(ids) > 0 {
		n := min(256, len(ids))
		args := make([]any, 0, 3*n)
		for _, i := range ids[:n] {
			args = append(args, i.old, i.workspace, LegacyWaitpointPublicID(i.workspace, i.old))
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO temp.waitpoint_identity_map VALUES "+strings.Repeat("(?,?,?),", n-1)+"(?,?,?)", args...); err != nil {
			return fmt.Errorf("build waitpoint identity map: %w", err)
		}
		ids = ids[n:]
	}
	if _, err = tx.ExecContext(ctx, `UPDATE pipeline_waitpoints AS w SET token=l.id,callback_token_hash=NULL FROM temp.waitpoint_identity_map AS l WHERE w.token=l.old AND w.workspace_id=l.workspace`); err != nil {
		return fmt.Errorf("convert waitpoint identities: %w", err)
	}
	for _, ref := range legacyWaitpointRefTables {
		if _, err := tx.ExecContext(ctx, ref.stmt); err != nil {
			return fmt.Errorf("convert waitpoint references in %s: %w", ref.table, err)
		}
	}
	_, err = tx.ExecContext(ctx, "DROP TABLE temp.waitpoint_identity_map")
	return err
}
