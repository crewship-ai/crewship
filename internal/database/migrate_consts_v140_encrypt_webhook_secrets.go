package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// migrationEncryptWebhookSecrets (v140) backfills existing plaintext webhook
// secrets to AES-256-GCM at rest (#1072/#1029): agents.webhook_secret and
// pipeline_webhooks.signing_secret, which stored plaintext despite the schema
// comment claiming otherwise — unlike every credential value.
//
// Fail-open + idempotent, matching the write/read paths:
//   - No usable ENCRYPTION_KEY → skip + WARN. Key-less deployments keep
//     working; the write paths also store plaintext and the read paths decrypt
//     only "vN:"-enveloped values, so mixed state is safe.
//   - A row already carrying an encryption envelope is skipped, so a re-run
//     (or a partial prior run) never double-encrypts.
func migrationEncryptWebhookSecrets(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	if !encryption.KeyConfigured() {
		logger.Warn("v140: no usable ENCRYPTION_KEY — leaving webhook secrets plaintext at rest (#1072 fail-open); set ENCRYPTION_KEY to encrypt")
		return nil
	}

	// Table/column are compile-time constants below (never user input), so the
	// string-built SQL carries no injection surface.
	for _, c := range []struct{ table, column string }{
		{"agents", "webhook_secret"},
		{"pipeline_webhooks", "signing_secret"},
	} {
		rows, err := tx.QueryContext(ctx,
			"SELECT id, "+c.column+" FROM "+c.table+" WHERE "+c.column+" IS NOT NULL AND "+c.column+" != ''")
		if err != nil {
			return fmt.Errorf("v140 select %s.%s: %w", c.table, c.column, err)
		}
		type pending struct{ id, val string }
		var todo []pending
		for rows.Next() {
			var id, val string
			if err := rows.Scan(&id, &val); err != nil {
				rows.Close()
				return fmt.Errorf("v140 scan %s.%s: %w", c.table, c.column, err)
			}
			if encryption.IsEncrypted(val) {
				// A "vN:" prefix is NOT proof of encryption for the
				// user-supplied signing_secret column — a literal value like
				// "v2:my-hook-key" matches the envelope shape. Confirm by
				// decrypting: a genuine same/older-key envelope decrypts and is
				// skipped (idempotent); a look-alike plaintext fails to decrypt
				// and is encrypted below like any other bare value.
				if _, derr := encryption.Decrypt(val); derr == nil {
					continue
				}
			}
			todo = append(todo, pending{id, val})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("v140 iterate %s.%s: %w", c.table, c.column, err)
		}

		n := 0
		for _, p := range todo {
			ct, err := encryption.Encrypt(p.val)
			if err != nil {
				return fmt.Errorf("v140 encrypt %s.%s id=%s: %w", c.table, c.column, p.id, err)
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE "+c.table+" SET "+c.column+" = ? WHERE id = ?", ct, p.id); err != nil {
				return fmt.Errorf("v140 update %s.%s id=%s: %w", c.table, c.column, p.id, err)
			}
			n++
		}
		if n > 0 {
			logger.Info("v140: encrypted webhook secrets at rest", "table", c.table, "column", c.column, "rows", n)
		}
	}
	return nil
}
