package api

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const instanceAuditColumns = `a.id,a.user_id,a.action,a.entity_type,a.entity_id,a.target_workspace_id,a.metadata,a.ip_address,a.user_agent,a.created_at`
const instanceAuditChainColumns = `a.chain_seq,a.prev_hash,a.entry_hash,p.chain_seq,p.entry_hash`
const instanceAuditPredecessor = ` LEFT JOIN instance_audit_logs p ON p.chain_seq=a.chain_seq-1 `

type instanceAuditProof struct {
	seq, predecessor            sql.NullInt64
	prev, hash, predecessorHash sql.NullString
}

func (p *instanceAuditProof) scanArgs(f *journal.InstanceAuditFields) []any {
	return []any{&f.ID, &f.UserID, &f.Action, &f.EntityType, &f.EntityID, &f.TargetWorkspaceID, &f.Metadata, &f.IPAddress, &f.UserAgent, &f.CreatedAt, &p.seq, &p.prev, &p.hash, &p.predecessor, &p.predecessorHash}
}
func (p instanceAuditProof) integrity(f journal.InstanceAuditFields) string {
	if !p.seq.Valid && !p.prev.Valid && !p.hash.Valid {
		return "legacy_unverified"
	}
	if !p.seq.Valid || p.seq.Int64 < 1 || !p.prev.Valid || !p.hash.Valid {
		return "invalid"
	}
	if p.seq.Int64 == 1 {
		if p.prev.String != "" || p.predecessor.Valid {
			return "invalid"
		}
	} else if !p.predecessor.Valid || p.predecessor.Int64 != p.seq.Int64-1 || !p.predecessorHash.Valid || p.prev.String != p.predecessorHash.String {
		return "invalid"
	}
	if !hmac.Equal([]byte(journal.InstanceAuditMAC(p.seq.Int64, p.prev.String, f)), []byte(p.hash.String)) {
		return "invalid"
	}
	return "entry_verified"
}

// appendInstanceAuditTx shares the mutation transaction. Production connections
// serialize SQLite writers; do not acquire a separate mutex or run callbacks.
func appendInstanceAuditTx(ctx context.Context, tx *sql.Tx, f journal.InstanceAuditFields) error {
	var tail journal.InstanceAuditFields
	var proof instanceAuditProof
	err := tx.QueryRowContext(ctx, `SELECT `+instanceAuditColumns+`,`+instanceAuditChainColumns+` FROM instance_audit_logs a`+instanceAuditPredecessor+` WHERE a.chain_seq IS NOT NULL ORDER BY a.chain_seq DESC LIMIT 1`).Scan(proof.scanArgs(&tail)...)
	seq := int64(1)
	prev := ""
	if err == nil {
		if proof.integrity(tail) != "entry_verified" || proof.seq.Int64 == math.MaxInt64 {
			return errors.New("instance audit: invalid chain tail")
		}
		seq = proof.seq.Int64 + 1
		prev = proof.hash.String
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hash := journal.InstanceAuditMAC(seq, prev, f)
	res, err := tx.ExecContext(ctx, `INSERT INTO instance_audit_logs(id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at,chain_seq,prev_hash,entry_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, f.ID, f.UserID, f.Action, f.EntityType, f.EntityID, f.TargetWorkspaceID, f.Metadata, f.IPAddress, f.UserAgent, f.CreatedAt, seq, prev, hash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("instance audit: insert not acknowledged")
	}
	var stored journal.InstanceAuditFields
	var ack instanceAuditProof
	if err := tx.QueryRowContext(ctx, `SELECT `+instanceAuditColumns+`,`+instanceAuditChainColumns+` FROM instance_audit_logs a`+instanceAuditPredecessor+` WHERE a.id=?`, f.ID).Scan(ack.scanArgs(&stored)...); err != nil {
		return err
	}
	if stored != f || !ack.seq.Valid || ack.seq.Int64 != seq || ack.prev.String != prev || ack.hash.String != hash || ack.integrity(stored) != "entry_verified" {
		return errors.New("instance audit: persisted row changed")
	}
	return nil
}

func appendInstanceAudit(ctx context.Context, target instanceAuditExecer, f journal.InstanceAuditFields) error {
	switch db := target.(type) {
	case *sql.Tx:
		return appendInstanceAuditTx(ctx, db, f)
	case *sql.DB:
		// Four existing operational callbacks only commit the audit here; their
		// external effect is not retroactively part of this transaction.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if err := appendInstanceAuditTx(ctx, tx, f); err != nil {
			return err
		}
		return tx.Commit()
	default:
		return errors.New("instance audit requires SQL transaction or database")
	}
}

func auditCLITokenTx(ctx context.Context, tx *sql.Tx, actor, tokenID, action string) error {
	return appendInstanceAuditTx(ctx, tx, journal.InstanceAuditFields{ID: generateCUID(), UserID: sql.NullString{String: actor, Valid: true}, Action: action, EntityType: "cli_token", EntityID: sql.NullString{String: tokenID, Valid: true}, CreatedAt: tsformat.Format(time.Now())})
}

func revokeCLITokensAuditedTx(ctx context.Context, tx *sql.Tx, actor, userID, now string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM cli_tokens WHERE user_id=? AND revoked_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE cli_tokens SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != int64(len(ids)) {
		return nil, errors.New("token revoke count changed")
	}
	for _, id := range ids {
		if err := auditCLITokenTx(ctx, tx, actor, id, "auth.cli_token_revoked"); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func ackCLITokenMutationTx(ctx context.Context, tx *sql.Tx, res sql.Result, id, userID string, revokedAt *string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("token mutation not acknowledged")
	}
	var stored sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM cli_tokens WHERE id=? AND user_id=?`, id, userID).Scan(&stored); err != nil {
		return err
	}
	if revokedAt == nil {
		if stored.Valid {
			return errors.New("created token is revoked")
		}
	} else if !stored.Valid || stored.String != *revokedAt {
		return errors.New("token revocation not acknowledged")
	}
	return nil
}

func ackCLITokenRevocationsTx(ctx context.Context, tx *sql.Tx, ids []string, userID, now string) error {
	for _, id := range ids {
		var at string
		if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM cli_tokens WHERE id=? AND user_id=?`, id, userID).Scan(&at); err != nil {
			return err
		}
		if at != now {
			return errors.New("token revoke not acknowledged")
		}
	}
	return nil
}
