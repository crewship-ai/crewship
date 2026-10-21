package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// This cutover captures only the known physical HEAD of an existing legacy
// routine with no archive at all. It neither reconstructs older content nor
// changes numeric HEAD/pins. Do not register this as a global restore backfill:
// an old source bundle does not prove unrelated target rows are legacy.
func migrationLegacyPipelineCurrentHeads(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	last := ""
	archived, inconsistent := 0, 0
	for {
		var id, raw, hash string
		var head int
		var origin sql.NullString
		// Require the native old-writer shape before Scan can normalize BLOBs.
		err := tx.QueryRowContext(ctx, `SELECT p.id,p.definition_json,p.definition_hash,p.head_version,p.chat_origin_id FROM pipelines p WHERE p.id>? AND p.head_version>0 AND typeof(p.id)='text' AND typeof(p.head_version)='integer' AND typeof(p.definition_json)='text' AND typeof(p.definition_hash)='text' AND (p.chat_origin_id IS NULL OR typeof(p.chat_origin_id)='text') AND NOT EXISTS(SELECT 1 FROM pipeline_versions v WHERE v.pipeline_id=p.id) ORDER BY p.id LIMIT 1`, last).Scan(&id, &raw, &hash, &head, &origin)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("read legacy current head: %w", err)
		}
		last = id
		// Stored content hashes are the old writer's SHA256 of exact bytes. A
		// mismatch is corruption, not proof authorizing a legacy authority repair.
		if hash != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) {
			inconsistent++
			continue
		}
		// Keep the actual newly minted archive identity and capture time so an
		// AFTER trigger cannot silently replace or rewrite the acknowledged row.
		var archiveID, capturedAt string
		if err = tx.QueryRowContext(ctx, `SELECT 'plnv_'||lower(hex(randomblob(16))),datetime('now','subsec')`).Scan(&archiveID, &capturedAt); err != nil {
			return fmt.Errorf("prepare legacy current archive: %w", err)
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,parent_version,change_summary,created_at,chat_origin_id,authority_fingerprint) VALUES(?,?,?,?,?,'system','legacy-upgrade',NULL,'Current legacy definition archived during upgrade',?,?,'legacy-trusted:v1')`, archiveID, id, head, raw, hash, capturedAt, origin)
		if err != nil {
			return fmt.Errorf("archive legacy current head: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("acknowledge legacy archive insert: %w", err)
		}
		if inserted != 1 {
			return errors.New("legacy current archive insert was not acknowledged")
		}
		var persisted int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_versions WHERE id=? AND pipeline_id=? AND version=? AND definition_json=? AND definition_hash=? AND author_type='system' AND author_id='legacy-upgrade' AND parent_version IS NULL AND change_summary='Current legacy definition archived during upgrade' AND created_at=? AND chat_origin_id IS ? AND authority_fingerprint='legacy-trusted:v1' AND typeof(id)='text' AND typeof(pipeline_id)='text' AND typeof(version)='integer' AND typeof(definition_json)='text' AND typeof(definition_hash)='text' AND typeof(created_at)='text' AND (chat_origin_id IS NULL OR typeof(chat_origin_id)='text')`, archiveID, id, head, raw, hash, capturedAt, origin).Scan(&persisted); err != nil {
			return fmt.Errorf("verify persisted legacy current archive: %w", err)
		}
		if persisted != 1 {
			return errors.New("persisted legacy current archive differs from capture")
		}
		archived++
	}
	logger.Info("archived verified legacy current routine heads", "count", archived, "inconsistent_hashes_skipped", inconsistent)
	return nil
}
