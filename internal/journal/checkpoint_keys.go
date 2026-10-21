package journal

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func versionedCheckpointMAC(keys *SigningKeys, id, workspace, stamp string, removed []RemovedEntry, keyID sql.NullString, version sql.NullInt64) (string, error) {
	key, err := keys.verification(keyID, version)
	if err != nil {
		return "", err
	}
	old := CheckpointMAC(key, workspace, removed)
	if !keyID.Valid {
		return old, nil
	}
	framed, err := json.Marshal([]string{id, stamp, old})
	if err != nil {
		return "", err
	}
	return versionedMAC(key, "crewship retention checkpoint", keyID.String, string(framed)), nil
}

// WriteChainCheckpointWithKeys is the actual versioned retention/purge writer.
// The legacy raw-key function remains solely for old-format compatibility.
func WriteChainCheckpointWithKeys(ctx context.Context, tx *sql.Tx, keys *SigningKeys, workspace string, removed []RemovedEntry) error {
	chained := make([]RemovedEntry, 0, len(removed))
	for _, entry := range removed {
		if entry.Seq > 0 {
			chained = append(chained, entry)
		}
	}
	if len(chained) == 0 {
		return nil
	}
	id, stamp := newID(), tsformat.Format(time.Now())
	raw, err := json.Marshal(chained)
	if err != nil {
		return err
	}
	keyID, version := ActiveSigningIdentity(keys)
	if !keyID.Valid {
		key, err := keys.verification(keyID, version)
		if err != nil {
			return err
		}
		return WriteChainCheckpoint(ctx, tx, key, workspace, chained)
	}
	mac, err := versionedCheckpointMAC(keys, id, workspace, stamp, chained, keyID, version)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO journal_chain_checkpoints(id,workspace_id,created_at,removed_json,mac,chain_key_id,chain_mac_version) VALUES(?,?,?,?,?,?,?)`, id, workspace, stamp, string(raw), mac, keyID, version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(err, errors.New("journal checkpoint insert not acknowledged"))
	}
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM journal_chain_checkpoints WHERE id=? AND workspace_id=? AND created_at=? AND removed_json=? AND mac=? AND chain_key_id IS ? AND chain_mac_version IS ?)`, id, workspace, stamp, string(raw), mac, keyID, version).Scan(&exact)
	if err != nil || !exact {
		return errors.Join(err, errors.New("journal checkpoint changed during insert"))
	}
	return nil
}

func loadCheckpointedRemovalsWithKeys(ctx context.Context, db chainReader, keys *SigningKeys, workspace string) (map[int64]string, int, bool, error) {
	versioned, err := keyColumns(ctx, db, "journal_chain_checkpoints")
	if err != nil {
		return nil, 0, false, err
	}
	columns := `id,created_at,removed_json,mac,NULL,NULL`
	if versioned {
		columns = `id,created_at,removed_json,mac,chain_key_id,chain_mac_version`
	}
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM journal_chain_checkpoints WHERE workspace_id=?`, workspace)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	out := map[int64]string{}
	applied := 0
	unavailable := false
	for rows.Next() {
		var id, stamp, raw, mac string
		var keyID sql.NullString
		var version sql.NullInt64
		if err := rows.Scan(&id, &stamp, &raw, &mac, &keyID, &version); err != nil {
			return nil, 0, false, err
		}
		var removed []RemovedEntry
		if json.Unmarshal([]byte(raw), &removed) != nil {
			continue
		}
		expected, err := versionedCheckpointMAC(keys, id, workspace, stamp, removed, keyID, version)
		if errors.Is(err, ErrSigningKeyUnavailable) {
			unavailable = true
		}
		if err != nil || !hmac.Equal([]byte(mac), []byte(expected)) {
			continue
		}
		for _, entry := range removed {
			if old, exists := out[entry.Seq]; exists && old != entry.Hash {
				return nil, 0, false, fmt.Errorf("journal: conflicting signed removal checkpoints")
			}
			out[entry.Seq] = entry.Hash
		}
		applied++
	}
	return out, applied, unavailable, rows.Err()
}

// CheckpointMACForKeys lets a fork authenticate the original retained checkpoint
// before remapping its identity; it must never re-sign an unverified bridge.
func CheckpointMACForKeys(keys *SigningKeys, id, workspace, stamp string, removed []RemovedEntry, keyID sql.NullString, version sql.NullInt64) (string, error) {
	return versionedCheckpointMAC(keys, id, workspace, stamp, removed, keyID, version)
}
