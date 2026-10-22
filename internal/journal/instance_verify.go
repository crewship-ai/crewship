package journal

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
)

// VerifyInstanceAuditWithKeys is the recovery drill's instance-chain consumer.
// A valid explicit OWNER anchor proves only its new segment; it never turns
// an unavailable or invalid historical MAC into a verified historical row.
func VerifyInstanceAuditWithKeys(ctx context.Context, db chainReader, keys *SigningKeys) (*VerifyResult, error) {
	result := &VerifyResult{OK: true}
	versioned, err := keyColumns(ctx, db, "instance_audit_logs")
	if err != nil {
		return nil, err
	}
	columns := `id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at,chain_seq,prev_hash,entry_hash,NULL,NULL`
	if versioned {
		columns = `id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version`
	}
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM instance_audit_logs ORDER BY chain_seq,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var previous string
	expectedSeq := int64(1)
	// historicalBad: a defect followed by an authentic row. It is history:
	// reported here, never an obstacle to appending (only the tail is).
	// postBad: the defect is in a keyring-signed row or after a reseal — damage
	// since the upgrade, reported as post_upgrade_invalid rather than history.
	currentBad, provenBad, postBad, historicalBad, anchored, legacy, chained := false, false, false, false, false, false, false
	for rows.Next() {
		var fields InstanceAuditFields
		var seq, version sql.NullInt64
		var prev, hash, keyID sql.NullString
		if err := rows.Scan(&fields.ID, &fields.UserID, &fields.Action, &fields.EntityType, &fields.EntityID, &fields.TargetWorkspaceID, &fields.Metadata, &fields.IPAddress, &fields.UserAgent, &fields.CreatedAt, &seq, &prev, &hash, &keyID, &version); err != nil {
			return nil, err
		}
		result.Count++
		if !seq.Valid && !prev.Valid && !hash.Valid && !keyID.Valid && !version.Valid {
			if IsInstanceAuditFallback(fields.Metadata) {
				result.UnchainedFallback++
				continue
			}
			legacy = true
			result.HistoricalStatus = "legacy_unverified"
			continue
		}
		chained = true
		wanted, macErr := InstanceAuditMACForKeys(keys, keyID, version, seq.Int64, prev.String, fields)
		macValid := macErr == nil && hmac.Equal([]byte(wanted), []byte(hash.String))
		structural := !seq.Valid || seq.Int64 < 1 || !prev.Valid || !hash.Valid || seq.Int64 != expectedSeq || prev.String != previous
		if structural || !macValid {
			currentBad = true
			if keyID.Valid || anchored {
				postBad = true
			}
			if macErr == nil || errors.Is(macErr, ErrChainIntegrity) {
				provenBad = true
			}
			result.Reason = "instance audit history contains an unverifiable entry"
			if result.BrokenSeq == 0 {
				result.BrokenSeq = seq.Int64
			}
		}
		if macValid && fields.Action == "audit.resealed" && fields.UserID.Valid {
			row := CheckpointRow{ID: fields.ID, Scope: "instance", ActorID: fields.UserID.String, CreatedAt: fields.CreatedAt, Seq: seq.Int64, PrevHash: prev.String, Payload: fields.Metadata.String, KeyID: keyID, Version: version}
			if checkpoint, valid := ValidateResealRow(keys, row); valid {
				status := checkpoint.HistoricalStatus
				if status == "verified" && (currentBad || legacy || historicalBad) {
					status = "historical_unverified"
					if provenBad {
						status = "historical_invalid"
					}
				}
				if historicalBad {
					status = worseHistorical(status, result.HistoricalStatus)
				}
				anchored = true
				historicalBad = false
				currentBad = false
				provenBad = false
				postBad = false
				result.HistoricalStatus = status
			}
		}
		if currentBad && !structural && macValid {
			status := "historical_unverified"
			if postBad {
				status = "post_upgrade_invalid"
			} else if provenBad {
				status = "historical_invalid"
			}
			result.HistoricalStatus = worseHistorical(result.HistoricalStatus, status)
			historicalBad, currentBad, provenBad, postBad = true, false, false, false
		}
		expectedSeq = seq.Int64 + 1
		previous = hash.String
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result.ContinuationVerified = !currentBad && (!legacy || chained)
	result.OK = !currentBad && !legacy && !historicalBad && result.UnchainedFallback == 0 && (!anchored || result.HistoricalStatus == "verified")
	return result, nil
}
