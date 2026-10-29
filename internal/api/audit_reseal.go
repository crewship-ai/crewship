package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type auditResealRequest struct {
	Scope            string  `json:"scope"`
	WorkspaceID      string  `json:"workspace_id,omitempty"`
	ExpectedTailSeq  *int64  `json:"expected_tail_seq"`
	ExpectedTailHash *string `json:"expected_tail_hash"`
	Reason           string  `json:"reason"`
}

var errResealDenied = errors.New("audit reseal requires current OWNER")
var errResealGuest = errors.New("audit reseal unavailable")

func auditResealAuthority(r *http.Request, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope, workspace string) error {
	actor := UserFromContext(r.Context())
	if actor == nil {
		return errResealDenied
	}
	var email string
	var restricted bool
	err := q.QueryRowContext(r.Context(), `SELECT email,EXISTS(SELECT 1 FROM workspace_members WHERE user_id=u.id AND access_mode='restricted') FROM users u WHERE id=? AND suspended_at IS NULL`, actor.ID).Scan(&email, &restricted)
	if errors.Is(err, sql.ErrNoRows) {
		return errResealDenied
	}
	if err != nil {
		return err
	}
	if restricted {
		return errResealGuest
	}
	if scope == "instance" {
		if !backup.IsInstanceOwner(email) || !canScope(r.Context(), scopeInstanceAdmin) {
			return errResealDenied
		}
		return nil
	}
	if !canScope(r.Context(), "workspace:admin") {
		return errResealDenied
	}
	var allowed bool
	err = q.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id WHERE m.user_id=? AND m.workspace_id=? AND m.role='OWNER' AND m.access_mode='trusted' AND w.deleted_at IS NULL)`, actor.ID, workspace).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return errResealDenied
	}
	return nil
}
func (r *Router) replyResealFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errResealGuest):
		replyError(w, 404, "Not found")
	case errors.Is(err, errResealDenied):
		replyError(w, 403, "Current OWNER authority required")
	case errors.Is(err, journal.ErrCheckpointConflict):
		replyError(w, 409, "Audit tail changed; inspect it before retrying")
	default:
		replyInternalError(w, r.logger, "explicit audit checkpoint", err)
	}
}

// resealAudit starts a visibly qualified segment. It never rewrites old rows,
// or pretends a lost key authenticated them.
// A3 step-up is intentionally not claimed before that facility exists.
func (r *Router) resealAudit(w http.ResponseWriter, request *http.Request) {
	var input auditResealRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		replyError(w, 400, "Invalid audit checkpoint request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		replyError(w, 400, "Invalid audit checkpoint request")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if (input.Scope != "instance" && input.Scope != "workspace") || (input.Scope == "workspace") != (input.WorkspaceID != "") || input.ExpectedTailSeq == nil || input.ExpectedTailHash == nil || *input.ExpectedTailSeq < 0 || len(*input.ExpectedTailHash) > 128 || (*input.ExpectedTailSeq > 0 && *input.ExpectedTailHash == "") || len(input.Reason) < 8 || len(input.Reason) > 512 {
		replyError(w, 400, "Scope, exact old tail and an 8-512 character reason are required")
		return
	}
	if err := auditResealAuthority(request, r.db, input.Scope, input.WorkspaceID); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	keys, err := journal.SigningKeysForContext(request.Context())
	if err != nil {
		r.replyResealFailure(w, err)
		return
	}
	tx, err := r.db.BeginTx(request.Context(), nil)
	if err != nil {
		r.replyResealFailure(w, err)
		return
	}
	defer tx.Rollback()
	if err := auditResealAuthority(request, tx, input.Scope, input.WorkspaceID); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	table, sequence := "instance_audit_logs", "chain_seq"
	if input.Scope == "workspace" {
		table, sequence = "journal_entries", "seq"
	}
	query := "SELECT id," + sequence + ",entry_hash FROM " + table + " WHERE " + sequence + ">0"
	args := []any{}
	if input.Scope == "workspace" {
		query += " AND workspace_id=?"
		args = append(args, input.WorkspaceID)
	}
	query += " ORDER BY " + sequence + " DESC LIMIT 1"
	var oldID, oldHash string
	var oldSeq int64
	err = tx.QueryRowContext(request.Context(), query, args...).Scan(&oldID, &oldSeq, &oldHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		r.replyResealFailure(w, err)
		return
	}
	if oldSeq != *input.ExpectedTailSeq || oldHash != *input.ExpectedTailHash {
		r.replyResealFailure(w, journal.ErrCheckpointConflict)
		return
	}
	var oldFrame []any
	if oldID != "" {
		oldFrame, err = auditResealNativeRow(request.Context(), tx, table, oldID)
		if err != nil {
			r.replyResealFailure(w, err)
			return
		}
	}
	historical := "historical_unverified"
	if oldSeq == 0 {
		var hasHistory bool
		historyQuery := "SELECT EXISTS(SELECT 1 FROM " + table
		if input.Scope == "workspace" {
			historyQuery += " WHERE workspace_id=?"
		}
		historyQuery += ")"
		if err := tx.QueryRowContext(request.Context(), historyQuery, args...).Scan(&hasHistory); err != nil {
			r.replyResealFailure(w, err)
			return
		}
		if !hasHistory {
			historical = "verified"
		}
	}
	id := generateCUID()
	now := time.Now().UTC()
	stamp := tsformat.Format(now)
	if input.Scope == "workspace" {
		stamp = now.Format("2006-01-02T15:04:05.000Z")
	}
	checkpoint, err := journal.SealResealCheckpoint(keys, journal.ResealCheckpoint{ID: id, Scope: input.Scope, WorkspaceID: input.WorkspaceID, ActorID: UserFromContext(request.Context()).ID, CreatedAt: stamp, OldTailSeq: oldSeq, OldTailHash: oldHash, Reason: input.Reason, HistoricalStatus: historical})
	if err != nil {
		r.replyResealFailure(w, err)
		return
	}
	var notify func()
	if input.Scope == "instance" {
		raw, err := json.Marshal(journal.ResealPayload(checkpoint))
		if err != nil {
			r.replyResealFailure(w, err)
			return
		}
		err = appendInstanceAuditTxMode(journal.WithSigningKeys(request.Context(), keys), tx, journal.InstanceAuditFields{ID: id, UserID: sql.NullString{String: checkpoint.ActorID, Valid: true}, Action: "audit.resealed", EntityType: "audit_chain", Metadata: sql.NullString{String: string(raw), Valid: true}, CreatedAt: stamp}, true)
		if err != nil {
			r.replyResealFailure(w, err)
			return
		}
	} else {
		writer, ok := r.journal.(interface {
			AppendResealTx(context.Context, *sql.Tx, journal.Entry) (func(), error)
		})
		if !ok {
			r.replyResealFailure(w, errors.New("configured journal does not support OWNER checkpoints"))
			return
		}
		notify, err = writer.AppendResealTx(request.Context(), tx, journal.Entry{ID: id, WorkspaceID: input.WorkspaceID, TS: now, Type: journal.EntryAuditEntityRestored, ActorType: journal.ActorUser, ActorID: checkpoint.ActorID, Summary: "Explicit OWNER audit checkpoint", Payload: journal.ResealPayload(checkpoint)})
		if err != nil {
			r.replyResealFailure(w, err)
			return
		}
		// Instance admins see every reseal in the instance trail too. Like an
		// incident record, it falls back unchained if that chain's tail is broken.
		raw, err := json.Marshal(map[string]any{"checkpoint_id": id, "old_tail_seq": oldSeq, "historical_status": historical, "reason": checkpoint.Reason})
		if err == nil {
			err = appendInstanceAuditTx(withSecurityAuditFallback(request.Context()), tx, journal.InstanceAuditFields{ID: generateCUID(), UserID: sql.NullString{String: checkpoint.ActorID, Valid: true}, Action: "audit.workspace_resealed", EntityType: "audit_chain", EntityID: sql.NullString{String: id, Valid: true}, TargetWorkspaceID: sql.NullString{String: input.WorkspaceID, Valid: true}, Metadata: sql.NullString{String: string(raw), Valid: true}, CreatedAt: tsformat.Format(now)})
		}
		if err != nil {
			r.replyResealFailure(w, err)
			return
		}
	}
	prepared, err := auditResealNativeRow(request.Context(), tx, table, id)
	if err != nil {
		r.replyResealFailure(w, err)
		return
	}
	var hash string
	if err := tx.QueryRowContext(request.Context(), "SELECT entry_hash FROM "+table+" WHERE id=?", id).Scan(&hash); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	if err := auditResealAuthority(request, tx, input.Scope, input.WorkspaceID); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	if oldID != "" {
		actual, err := auditResealNativeRow(request.Context(), tx, table, oldID)
		if err != nil || !reflect.DeepEqual(actual, oldFrame) {
			r.replyResealFailure(w, errors.New("old audit tail changed during checkpoint"))
			return
		}
	}
	actual, err := auditResealNativeRow(request.Context(), tx, table, id)
	if err != nil || !reflect.DeepEqual(actual, prepared) {
		r.replyResealFailure(w, errors.New("checkpoint row changed after append"))
		return
	}
	// A verified empty-history checkpoint must still describe an empty source
	// after every native trigger has run. Other historical statuses retain their
	// explicit, qualified OWNER attestation without rewriting old rows.
	if historical == "verified" {
		where := "id<>?"
		checkArgs := []any{id}
		if input.Scope == "workspace" {
			where += " AND workspace_id=?"
			checkArgs = append(checkArgs, input.WorkspaceID)
		}
		var other bool
		if err := tx.QueryRowContext(request.Context(), "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE "+where+")", checkArgs...).Scan(&other); err != nil {
			r.replyResealFailure(w, err)
			return
		}
		if other {
			r.replyResealFailure(w, errors.New("empty audit history changed during checkpoint"))
			return
		}
	}
	var finalID, finalHash string
	var finalSeq int64
	if err := tx.QueryRowContext(request.Context(), query, args...).Scan(&finalID, &finalSeq, &finalHash); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	if finalID != id || finalSeq != oldSeq+1 || finalHash != hash {
		r.replyResealFailure(w, errors.New("audit checkpoint is not the final chain tail"))
		return
	}
	if err := tx.Commit(); err != nil {
		r.replyResealFailure(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	raiseAuditChainIncident(request.Context(), auditChainIncident{Kind: "reseal", Scope: input.Scope, WorkspaceID: input.WorkspaceID, Seq: oldSeq, CheckpointID: id})
	writeJSON(w, 200, map[string]any{"scope": input.Scope, "workspace_id": input.WorkspaceID, "checkpoint_id": id, "tail_seq": oldSeq + 1, "tail_hash": hash, "continuation_verified": true, "historical_status": historical})
}

func auditResealNativeRow(ctx context.Context, tx *sql.Tx, table, id string) ([]any, error) {
	if table != "journal_entries" && table != "instance_audit_logs" {
		return nil, errors.New("invalid checkpoint table")
	}
	rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table+" WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values, dest := make([]any, len(names)), make([]any, len(names))
	for i := range values {
		dest[i] = &values[i]
	}
	if !rows.Next() {
		return nil, errors.Join(rows.Err(), sql.ErrNoRows)
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	if rows.Next() {
		return nil, errors.New("ambiguous checkpoint identity")
	}
	return values, rows.Err()
}
