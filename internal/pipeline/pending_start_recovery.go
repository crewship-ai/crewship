package pipeline

import (
	"context"
	"database/sql"
	"fmt"
)

// RecoverPendingStartsAtBoot runs before any dispatcher or run source starts.
// A reservation is released only with its proven unstarted owner, in the same
// transaction that returns that intent to the queue. Linked starts never retry.
func RecoverPendingStartsAtBoot(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Take the writer before reading recovery candidates.
	if _, err = tx.ExecContext(ctx, `UPDATE pending_runs SET status=status WHERE 0`); err != nil {
		return err
	}
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,workspace_id,pipeline_id,COALESCE(run_id,''),COALESCE(idempotency_key,'')
 FROM pending_runs WHERE status='fired' AND COALESCE(fired_run_id,'')=''
 AND COALESCE(reason_code,'')<>'recovery_unknown' ORDER BY id LIMIT 100`)
		if err != nil {
			return err
		}
		type candidate struct{ id, workspace, pipeline, run, key string }
		var batch []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.workspace, &c.pipeline, &c.run, &c.key); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, c)
		}
		scanErr := rows.Err()
		rows.Close()
		if scanErr != nil {
			return scanErr
		}
		if len(batch) == 0 {
			break
		}
		for _, c := range batch {
			if c.run == "" {
				_, err = tx.ExecContext(ctx, `UPDATE pending_runs SET reason_code='recovery_unknown',last_error='Previous dispatch has no durable planned identity; inspect before retrying.' WHERE id=?`, c.id)
			} else {
				var exists int
				err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_runs WHERE id=? AND workspace_id=? AND pipeline_id=?`, c.run, c.workspace, c.pipeline).Scan(&exists)
				if err != nil {
					return err
				}
				if exists != 0 {
					_, err = tx.ExecContext(ctx, `UPDATE pending_runs SET fired_run_id=?,last_error='' WHERE id=?`, c.run, c.id)
				} else {
					if c.key != "" {
						var owner string
						lookupErr := tx.QueryRowContext(ctx, `SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id=? AND pipeline_id=? AND idempotency_key=?`, c.workspace, c.pipeline, c.key).Scan(&owner)
						if lookupErr != nil && lookupErr != sql.ErrNoRows {
							return lookupErr
						}
						if lookupErr == nil && owner != c.run {
							if _, err = tx.ExecContext(ctx, `UPDATE pending_runs SET reason_code='recovery_unknown',last_error='Dispatch reservation belongs to another identity; inspect before retrying.' WHERE id=?`, c.id); err != nil {
								return err
							}
							continue
						}
						if _, err = tx.ExecContext(ctx, `DELETE FROM pipeline_run_idempotency WHERE workspace_id=? AND pipeline_id=? AND idempotency_key=? AND run_id=?`, c.workspace, c.pipeline, c.key, c.run); err != nil {
							return err
						}
					}
					_, err = tx.ExecContext(ctx, `UPDATE pending_runs SET status='pending',next_attempt_at=NULL,reason_code='recovered',last_error='',updated_at=datetime('now','subsec') WHERE id=?`, c.id)
				}
			}
			if err != nil {
				return fmt.Errorf("recover pending start %s: %w", c.id, err)
			}
		}
	}
	return tx.Commit()
}
