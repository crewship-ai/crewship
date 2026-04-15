package backup

import (
	"context"
	"database/sql"
	"fmt"
)

// EncryptedCredential is the wire shape for credstore rows exported
// into an instance-scope bundle. The encrypted_value field keeps its
// "v1:<base64>" prefix exactly — we deliberately do NOT decrypt at
// backup time so the bundle has two layers of protection (credstore
// master key on the plaintext, AGE recipient on the outer payload).
type EncryptedCredential struct {
	ID             string
	WorkspaceID    string
	Name           string
	Type           string
	Status         string
	Provider       string
	SecurityLevel  int
	KeeperCrewID   string
	EncryptedValue string
	CreatedAt      string
	UpdatedAt      string
}

// ExportEncryptedCredentials reads every ACTIVE credential row across
// all workspaces, returning the cipher blobs WITHOUT decrypting them.
// The runner writes these straight into the instance bundle payload
// (inside the outer AGE seal). Restore writes them back via
// ImportEncryptedCredentials.
//
// Only ACTIVE rows are exported. DELETED / revoked credentials stay
// out of the bundle so a restore does not silently resurrect them.
func ExportEncryptedCredentials(ctx context.Context, db *sql.DB) ([]EncryptedCredential, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, workspace_id, name, type, COALESCE(status,''),
       COALESCE(provider,''), COALESCE(security_level,0),
       COALESCE(keeper_crew_id,''), encrypted_value,
       COALESCE(created_at,''), COALESCE(updated_at,'')
FROM credentials
WHERE deleted_at IS NULL AND status = 'ACTIVE'
ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("backup: export credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []EncryptedCredential
	for rows.Next() {
		var c EncryptedCredential
		if err := rows.Scan(
			&c.ID, &c.WorkspaceID, &c.Name, &c.Type, &c.Status,
			&c.Provider, &c.SecurityLevel, &c.KeeperCrewID,
			&c.EncryptedValue, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("backup: scan credential: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: iterate credentials: %w", err)
	}
	return out, nil
}

// ImportEncryptedCredentials inserts the supplied rows back into the
// credentials table on restore. INSERT OR IGNORE so an existing
// credential with the same id is left untouched — an operator who
// wants to force-replace should restore into a clean instance.
//
// encrypted_value is written verbatim. The target instance must share
// the source's ENCRYPTION_KEY env var (or a compatible key version)
// for the credstore to decrypt these at runtime; if the keys diverge,
// keeper.Reload will log per-row "decrypt failed" entries and the
// operator must re-enter affected credentials.
func ImportEncryptedCredentials(ctx context.Context, db *sql.DB, creds []EncryptedCredential) (int, error) {
	if db == nil || len(creds) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("backup: begin credential restore: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	// ON CONFLICT(id) DO NOTHING rather than INSERT OR IGNORE so only
	// duplicate PKs are silently skipped. A UNIQUE(workspace_id, name)
	// collision — which would indicate that the target instance has an
	// unrelated credential under the same name — surfaces as an error
	// instead of silently losing the restored row.
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO credentials
  (id, workspace_id, name, type, status, provider, security_level,
   keeper_crew_id, encrypted_value, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("backup: prepare credential insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	var inserted int
	for _, c := range creds {
		res, err := stmt.ExecContext(ctx,
			c.ID, c.WorkspaceID, c.Name, c.Type, c.Status,
			c.Provider, c.SecurityLevel, c.KeeperCrewID,
			c.EncryptedValue, c.CreatedAt, c.UpdatedAt,
		)
		if err != nil {
			return 0, fmt.Errorf("backup: insert credential %s: %w", c.ID, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			inserted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("backup: commit credential restore: %w", err)
	}
	committed = true
	return inserted, nil
}
