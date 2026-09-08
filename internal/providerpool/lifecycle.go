package providerpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrStale = errors.New("provider pool changed; reload before retrying")

// Update replaces editable definition fields, not the provider/mode identity.
// It preserves round-robin history for retained members and never grants access.
func (s *Store) Update(ctx context.Context, pool Pool, revision int64) error {
	if revision < 1 || revision == math.MaxInt64 || strings.TrimSpace(pool.Name) == "" || utf8.RuneCountInString(pool.Name) > 200 || len(pool.Members) < 1 || len(pool.Members) > 100 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider pool update: %w", err)
	}
	defer tx.Rollback()
	if err := checkRevision(ctx, tx, pool.WorkspaceID, pool.ID, revision); err != nil {
		return err
	}
	// Identity is server-owned: callers cannot convert a bound pool to another
	// provider or auth mode while changing its membership.
	if err := tx.QueryRowContext(ctx, `SELECT provider,mode FROM provider_login_pools WHERE id=? AND workspace_id=?`, pool.ID, pool.WorkspaceID).Scan(&pool.Policy.Provider, &pool.Policy.Mode); err != nil {
		return fmt.Errorf("read provider pool identity: %w", err)
	}
	var duplicate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_login_pools WHERE workspace_id=? AND name=? AND id!=?)`, pool.WorkspaceID, strings.TrimSpace(pool.Name), pool.ID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return ErrConflict
	}
	seen := make(map[string]bool, len(pool.Members))
	for _, member := range pool.Members {
		if member.CredentialID == "" || seen[member.CredentialID] {
			return ErrInvalid
		}
		seen[member.CredentialID] = true
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM credentials WHERE id=? AND workspace_id=? AND deleted_at IS NULL)`, member.CredentialID, pool.WorkspaceID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO provider_login_pool_members(pool_id,credential_id,priority) VALUES (?,?,?) ON CONFLICT(pool_id,credential_id) DO UPDATE SET priority=excluded.priority`, pool.ID, member.CredentialID, member.Priority); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT credential_id FROM provider_login_pool_members WHERE pool_id=?`, pool.ID)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !seen[id] {
			removed = append(removed, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_login_pool_members WHERE pool_id=? AND credential_id=?`, pool.ID, id); err != nil {
			return err
		}
	}
	candidates, err := loadCandidates(ctx, tx, pool.WorkspaceID, pool.ID)
	if err != nil {
		return err
	}
	if _, err := Select(pool.Policy, candidates, time.Now()); err != nil && !errors.Is(err, ErrUnavailable) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_login_pools SET name=?,allow_cross_owner=?,revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND workspace_id=?`, strings.TrimSpace(pool.Name), pool.Policy.AllowCrossOwner, pool.ID, pool.WorkspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete retires a definition. Credentials and membership history remain intact;
// subsequent selections fail closed. No upstream account is revoked.
func (s *Store) Delete(ctx context.Context, workspaceID, poolID string, revision int64) error {
	if revision < 1 || revision == math.MaxInt64 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkRevision(ctx, tx, workspaceID, poolID, revision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_login_pools SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1 WHERE id=? AND workspace_id=?`, poolID, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

func checkRevision(ctx context.Context, tx *sql.Tx, workspaceID, poolID string, revision int64) error {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM provider_login_pools WHERE workspace_id=? AND id=? AND deleted_at IS NULL`, workspaceID, poolID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != revision {
		return ErrStale
	}
	return nil
}
