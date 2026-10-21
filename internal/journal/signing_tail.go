package journal

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

func loadChainHeadWithKeys(ctx context.Context, tx *sql.Tx, workspace string, keys *SigningKeys) (*chainHead, error) {
	head := &chainHead{prevHash: GenesisPrevHash}
	query, _, err := verifySelectFor(ctx, tx)
	if err != nil {
		return nil, err
	}
	query = strings.Replace(query, "WHERE workspace_id = ?", "WHERE workspace_id = ? AND seq>0", 1)
	query = strings.Replace(query, "ORDER BY seq ASC", "ORDER BY seq DESC LIMIT 1", 1)
	rows, err := tx.QueryContext(ctx, query, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return head, rows.Err()
	}
	f, prev, hash, _, err := scanChainRow(rows)
	if err != nil {
		return nil, err
	}
	expected, err := chainHashForKeys(keys, prev, f)
	if err != nil {
		return nil, err
	}
	if f.Seq < 1 || f.Seq == math.MaxInt64 || !hmac.Equal([]byte(hash), []byte(expected)) {
		return nil, fmt.Errorf("journal: invalid chain tail: %w", ErrChainIntegrity)
	}
	head.seq, head.prevHash = f.Seq, hash
	if rows.Next() {
		return nil, errors.New("journal: ambiguous chain tail")
	}
	return head, rows.Err()
}

func versionedVerifyQuery(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string) (string, error) {
	exists, err := keyColumns(ctx, q, "journal_entries")
	if err != nil {
		return "", err
	}
	if exists {
		query = strings.Replace(query, "FROM journal_entries", ",chain_key_id,chain_mac_version FROM journal_entries", 1)
	}
	return query, nil
}
