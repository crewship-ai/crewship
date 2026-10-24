package api

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const instanceAuditColumns = `a.id,a.user_id,a.action,a.entity_type,a.entity_id,a.target_workspace_id,a.metadata,a.ip_address,a.user_agent,a.created_at`
const instanceAuditChainColumns = `a.chain_seq,a.prev_hash,a.entry_hash,p.chain_seq,p.entry_hash,a.chain_key_id,a.chain_mac_version`
const instanceAuditPredecessor = ` LEFT JOIN instance_audit_logs p ON p.chain_seq=a.chain_seq-1 `

type instanceAuditProof struct {
	seq, predecessor            sql.NullInt64
	prev, hash, predecessorHash sql.NullString
	keyID                       sql.NullString
	macVersion                  sql.NullInt64
}

func (p *instanceAuditProof) scanArgs(f *journal.InstanceAuditFields) []any {
	return []any{&f.ID, &f.UserID, &f.Action, &f.EntityType, &f.EntityID, &f.TargetWorkspaceID, &f.Metadata, &f.IPAddress, &f.UserAgent, &f.CreatedAt, &p.seq, &p.prev, &p.hash, &p.predecessor, &p.predecessorHash, &p.keyID, &p.macVersion}
}
func (p instanceAuditProof) integrity(f journal.InstanceAuditFields) string {
	keys, err := journal.LoadSigningKeysFromEnv()
	if err != nil {
		return "key_unavailable"
	}
	return p.integrityWithKeys(keys, f)
}
func (p instanceAuditProof) integrityWithKeys(keys *journal.SigningKeys, f journal.InstanceAuditFields) string {
	if !p.seq.Valid && !p.prev.Valid && !p.hash.Valid {
		if !p.keyID.Valid && !p.macVersion.Valid && journal.IsInstanceAuditFallback(f.Metadata) {
			return journal.InstanceAuditFallbackValue
		}
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
	expected, err := journal.InstanceAuditMACForKeys(keys, p.keyID, p.macVersion, p.seq.Int64, p.prev.String, f)
	if errors.Is(err, journal.ErrSigningKeyUnavailable) {
		return "key_unavailable"
	}
	if err != nil || !hmac.Equal([]byte(expected), []byte(p.hash.String)) {
		return "invalid"
	}
	return "entry_verified"
}

// appendInstanceAuditTx shares the mutation transaction. Production connections
// serialize SQLite writers; do not acquire a separate mutex or run callbacks.
//
// Admission authenticates only the tail (its MAC under its own key and its link
// to the predecessor) and the inserted row, through the unique chain_seq index:
// constant cost, whatever the history. Older defects are history, reported by
// journal.VerifyInstanceAuditWithKeys on demand; only a broken tail refuses.
//
// A broken tail refuses every append except a security-reducing action whose
// context carries withSecurityAuditFallback: that one records an unchained
// fallback row instead (see appendInstanceAuditFallbackTx).
func appendInstanceAuditTx(ctx context.Context, tx *sql.Tx, f journal.InstanceAuditFields) error {
	err := appendInstanceAuditTxMode(ctx, tx, f, false)
	var tail *instanceAuditTailError
	if !errors.As(err, &tail) {
		return err
	}
	raiseAuditChainIncident(ctx, auditChainIncident{Kind: "chain_break", Scope: "instance", Seq: tail.seq, Reason: tail.Error()})
	if securityAuditFallback(ctx) {
		return appendInstanceAuditFallbackTx(ctx, tx, f)
	}
	return err
}

// instanceAuditTailError is a refusal before anything was written, because
// the chain tail does not authenticate. Only this refusal may fall back.
type instanceAuditTailError struct {
	cause error
	seq   int64
}

func (e *instanceAuditTailError) Error() string {
	return "instance audit chain tail: " + e.cause.Error()
}
func (e *instanceAuditTailError) Unwrap() error { return e.cause }

type securityAuditFallbackKey struct{}

// withSecurityAuditFallback permits a record outside a broken instance chain
// for revocations, suspension, host-local password recovery, and audit incident
// or workspace reseal notices. Web password change/reset and access-granting
// operations stay chained-only. Callers must explicitly qualify each operation;
// this marker never permits fallback for errors other than a broken tail.
func withSecurityAuditFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, securityAuditFallbackKey{}, true)
}

func securityAuditFallback(ctx context.Context) bool {
	marked, _ := ctx.Value(securityAuditFallbackKey{}).(bool)
	return marked
}

// appendInstanceAuditFallbackTx records f without chain columns, its metadata
// marked journal.InstanceAuditFallbackValue. It cannot extend or endorse the
// broken tail, and verification reports it (UnchainedFallback, never OK). The
// row commits in the action's own transaction, so the action and its record
// stay atomic.
func appendInstanceAuditFallbackTx(ctx context.Context, tx *sql.Tx, f journal.InstanceAuditFields) error {
	mark := map[string]any{}
	if f.Metadata.Valid && json.Unmarshal([]byte(f.Metadata.String), &mark) != nil {
		mark = map[string]any{"original_metadata": f.Metadata.String}
	}
	mark[journal.InstanceAuditFallbackKey] = journal.InstanceAuditFallbackValue
	raw, err := json.Marshal(mark)
	if err != nil {
		return err
	}
	f.Metadata = sql.NullString{String: string(raw), Valid: true}
	res, err := tx.ExecContext(ctx, `INSERT INTO instance_audit_logs(id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, f.ID, f.UserID, f.Action, f.EntityType, f.EntityID, f.TargetWorkspaceID, f.Metadata, f.IPAddress, f.UserAgent, f.CreatedAt)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("instance audit fallback: insert not acknowledged"))
	}
	slog.Default().Error("instance audit chain tail is broken; security action recorded as unchained fallback", "action", f.Action, "audit_id", f.ID)
	return nil
}

func appendInstanceAuditTxMode(ctx context.Context, tx *sql.Tx, f journal.InstanceAuditFields, checkpoint bool) error {
	keys, err := journal.SigningKeysForContext(ctx)
	if err != nil {
		return err
	}
	var tail journal.InstanceAuditFields
	var proof instanceAuditProof
	err = tx.QueryRowContext(ctx, `SELECT `+instanceAuditColumns+`,`+instanceAuditChainColumns+` FROM instance_audit_logs a`+instanceAuditPredecessor+` WHERE a.chain_seq IS NOT NULL ORDER BY a.chain_seq DESC LIMIT 1`).Scan(proof.scanArgs(&tail)...)
	seq := int64(1)
	prev := ""
	if err == nil {
		if !checkpoint {
			integrity := proof.integrityWithKeys(keys, tail)
			if integrity == "key_unavailable" {
				return &instanceAuditTailError{journal.ErrSigningKeyUnavailable, proof.seq.Int64}
			}
			if integrity != "entry_verified" {
				return &instanceAuditTailError{journal.ErrChainIntegrity, proof.seq.Int64}
			}
		}
		if proof.seq.Int64 == math.MaxInt64 {
			return &instanceAuditTailError{journal.ErrChainIntegrity, proof.seq.Int64}
		}
		seq = proof.seq.Int64 + 1
		prev = proof.hash.String
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	keyID, version := journal.ActiveSigningIdentity(keys)
	if checkpoint {
		row := journal.CheckpointRow{ID: f.ID, Scope: "instance", ActorID: f.UserID.String, CreatedAt: f.CreatedAt, Seq: seq, PrevHash: prev, Payload: f.Metadata.String, KeyID: keyID, Version: version}
		if _, valid := journal.ValidateResealRow(keys, row); !valid || f.Action != "audit.resealed" || !f.UserID.Valid {
			return journal.ErrCheckpointConflict
		}
	}
	hash, err := journal.InstanceAuditMACForKeys(keys, keyID, version, seq, prev, f)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO instance_audit_logs(id,user_id,action,entity_type,entity_id,target_workspace_id,metadata,ip_address,user_agent,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, f.ID, f.UserID, f.Action, f.EntityType, f.EntityID, f.TargetWorkspaceID, f.Metadata, f.IPAddress, f.UserAgent, f.CreatedAt, seq, prev, hash, keyID, version)
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
	if stored != f || !ack.seq.Valid || ack.seq.Int64 != seq || ack.prev.String != prev || ack.hash.String != hash || ack.keyID != keyID || ack.macVersion != version || ack.integrityWithKeys(keys, stored) != "entry_verified" {
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
