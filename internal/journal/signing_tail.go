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

// appendStart is a workspace chain tip as a batch found it. trusted marks an
// explicit OWNER reseal, which chains onto a tail it deliberately does not
// authenticate (ValidateResealRow already bound the checkpoint to it).
type appendStart struct {
	seq     int64
	hash    string
	trusted bool
}

// verifyAppendTail is append admission: it authenticates the previous tail
// under its own key from the keyring, that tail's link to its predecessor, and
// every row the batch inserted (seq start.seq+1 .. end). It reads at most
// end-start.seq+2 rows from the top of the (workspace_id, seq) index, so its
// cost does not grow with history.
//
// Defects further back are deliberately not consulted. They are historical:
// VerifyChainWithKeys reports them on demand, and a new row that chains onto
// an authentic tail is no less authentic for them. Only an inconsistent tail
// blocks the append, because a row chained onto it would launder it.
func verifyAppendTail(ctx context.Context, tx *sql.Tx, workspace string, keys *SigningKeys, start appendStart, end int64) error {
	query, _, err := verifySelectFor(ctx, tx)
	if err != nil {
		return err
	}
	// Read newest-first under LIMIT, then restore seq order: with only a
	// range, SQLite may take the partial index's seq>0 as the scan start and
	// walk the whole workspace. Any row above end (impossible inside the
	// writer transaction) or a missing one fails the link checks below.
	query = strings.Replace(query, "WHERE workspace_id = ?", "WHERE workspace_id = ? AND seq > 0 AND seq >= ?", 1)
	query = "SELECT * FROM (" + strings.Replace(query, "ORDER BY seq ASC", "ORDER BY seq DESC LIMIT ?", 1) + ") ORDER BY seq ASC"
	from, expectedSeq, expectedPrev, linked := start.seq-1, int64(1), GenesisPrevHash, start.seq <= 1
	switch {
	case start.trusted:
		from, expectedSeq, expectedPrev, linked = start.seq+1, start.seq+1, start.hash, true
	case start.seq <= 1:
		from = 1
	}
	rows, err := tx.QueryContext(ctx, query, workspace, from, end-from+1)
	if err != nil {
		return fmt.Errorf("journal: read chain tail: %w", err)
	}
	defer rows.Close()
	tail := fmt.Errorf("journal: invalid chain tail: %w", ErrChainIntegrity)
	for rows.Next() {
		f, prev, hash, livePriority, err := scanChainRow(rows)
		if err != nil {
			return fmt.Errorf("journal: scan chain tail: %w", err)
		}
		if !start.trusted && f.Seq < start.seq {
			// The predecessor only supplies the link the old tail must match.
			// Its own MAC is history.
			expectedSeq, expectedPrev, linked = f.Seq+1, hash, true
			continue
		}
		if !linked {
			// The old tail's predecessor is gone. Only a signed compaction
			// checkpoint may account for it; an unexplained gap refuses.
			// (Rare, so the checkpoint read stays off the common path.)
			removed, _, _, err := loadCheckpointedRemovalsWithKeys(ctx, tx, keys, workspace)
			if err != nil {
				return err
			}
			hash, covered := removed[f.Seq-1]
			if !covered {
				return fmt.Errorf("journal: chain tail predecessor missing: %w", ErrChainIntegrity)
			}
			expectedSeq, expectedPrev = f.Seq, hash
		}
		if f.Seq != expectedSeq || prev != expectedPrev {
			return tail
		}
		want, err := chainHashForKeys(keys, prev, f)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(want), []byte(hash)) {
			return tail
		}
		// A row this batch wrote has no recorded operator edit yet.
		if f.Seq > start.seq && livePriority != f.Priority {
			return tail
		}
		expectedSeq, expectedPrev, linked = f.Seq+1, hash, true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("journal: iterate chain tail: %w", err)
	}
	if expectedSeq != end+1 {
		return fmt.Errorf("journal: appended rows missing from chain tail: %w", ErrChainIntegrity)
	}
	return nil
}
