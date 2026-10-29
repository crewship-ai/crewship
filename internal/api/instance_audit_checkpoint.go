package api

import (
	"context"
	"crypto/hmac"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/journal"
)

func latestInstanceAuditCheckpoint(ctx context.Context, db *sql.DB, keys *journal.SigningKeys) (*journal.ResealCheckpoint, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+instanceAuditColumns+`,`+instanceAuditChainColumns+` FROM instance_audit_logs a`+instanceAuditPredecessor+` WHERE a.action='audit.resealed' ORDER BY a.chain_seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fields journal.InstanceAuditFields
		var proof instanceAuditProof
		if err := rows.Scan(proof.scanArgs(&fields)...); err != nil {
			return nil, err
		}
		expected, err := journal.InstanceAuditMACForKeys(keys, proof.keyID, proof.macVersion, proof.seq.Int64, proof.prev.String, fields)
		if err != nil || !proof.seq.Valid || !hmac.Equal([]byte(expected), []byte(proof.hash.String)) || !fields.UserID.Valid {
			continue
		}
		row := journal.CheckpointRow{ID: fields.ID, Scope: "instance", ActorID: fields.UserID.String, CreatedAt: fields.CreatedAt, Seq: proof.seq.Int64, PrevHash: proof.prev.String, Payload: fields.Metadata.String, KeyID: proof.keyID, Version: proof.macVersion}
		if checkpoint, valid := journal.ValidateResealRow(keys, row); valid {
			return checkpoint, nil
		}
	}
	return nil, rows.Err()
}
