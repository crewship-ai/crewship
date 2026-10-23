package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/crewship-ai/crewship/internal/journal"
)

var errPageGrantIssuerNotCurrent = errors.New("page grant issuer is no longer authorized")

// Keep the complete stored tuple, including NULL versus an empty panel scope.
// Use-time liveness and malformed-scope filtering must not erase removal facts.
type pageGrantSnapshot struct {
	PageID      string         `json:"page_id"`
	SubjectType string         `json:"subject_type"`
	SubjectID   string         `json:"subject_id"`
	Level       string         `json:"level"`
	PanelIDs    sql.NullString `json:"-"`
	GrantedBy   string         `json:"granted_by_user_id"`
	GrantedAt   string         `json:"granted_at"`
}

func (g pageGrantSnapshot) MarshalJSON() ([]byte, error) {
	type fields pageGrantSnapshot
	var panels any
	if g.PanelIDs.Valid {
		panels = g.PanelIDs.String
	}
	return json.Marshal(struct {
		fields
		Panels any `json:"panel_ids"`
	}{fields(g), panels})
}

func readPageGrantSubject(ctx context.Context, tx *sql.Tx, page, kind, subject string) ([]pageGrantSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at FROM page_grants WHERE page_id=? AND (?='' OR (subject_type=? AND subject_id=?)) ORDER BY subject_type,subject_id,level`, page, kind, kind, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pageGrantSnapshot{}
	for rows.Next() {
		var g pageGrantSnapshot
		if err := rows.Scan(&g.PageID, &g.SubjectType, &g.SubjectID, &g.Level, &g.PanelIDs, &g.GrantedBy, &g.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (h *PageHandler) pageGrantIssuerCurrent(ctx context.Context, workspace, user string, rec *pageRecord) error {
	role, err := h.liveContentHumanRole(ctx, workspace, user)
	if err != nil {
		return err
	}
	if canRole(role, "manage") || rec.OwnerUserID == user {
		return nil
	}
	var owner bool
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM crew_members WHERE crew_id=? AND user_id=?)`, rec.OwnerCrewID, user).Scan(&owner); err != nil {
		return err
	}
	if !owner {
		return sql.ErrNoRows
	}
	return nil
}

func (h *PageHandler) beginPageGrantMutation(w http.ResponseWriter, r *http.Request, workspace string) (*sql.Tx, *http.Request, *pageRecord, bool) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin page grant mutation", err)
		return nil, r, nil, false
	}
	// Production uses immediate transactions. The fence also serializes reduced
	// test connections before any live authorization or grant snapshot is read.
	if _, err = tx.ExecContext(r.Context(), `UPDATE workspaces SET updated_at=updated_at WHERE id=?`, workspace); err != nil {
		tx.Rollback()
		replyInternalError(w, h.logger, "fence page grant mutation", err)
		return nil, r, nil, false
	}
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.grantPage(w, r, workspace)
	if !ok {
		tx.Rollback()
		return nil, r, nil, false
	}
	if err := h.pageGrantIssuerCurrent(r.Context(), workspace, UserFromContext(r.Context()).ID, rec); err != nil {
		tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusForbidden, pageGrantAdminRefusal)
		} else {
			replyInternalError(w, h.logger, "check current page grant issuer", err)
		}
		return nil, r, nil, false
	}
	return tx, r, rec, true
}

func requirePageGrantRows(result sql.Result, err error, want int64) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("page grant mutation affected %d rows, want %d", n, want)
	}
	return nil
}

type pageGrantChange struct {
	entry          journal.Entry
	previous, next *pageGrantSnapshot
}

func (h *PageHandler) commitPageGrantMutation(ctx context.Context, tx *sql.Tx, rec *pageRecord, actor, kind, subject string, expected []pageGrantSnapshot, changes []pageGrantChange) error {
	var notify []func()
	type receipt struct {
		id     string
		native []any
	}
	var receipts []receipt
	for _, change := range changes {
		e := change.entry
		e.ID = generateCUID()
		e.Payload["previous_grant"], e.Payload["grant"] = change.previous, change.next
		publish, err := appendAuthorizationAudit(ctx, tx, h.journal, e)
		if err != nil {
			return err
		}
		notify = append(notify, publish)
		native, err := pageGrantEventFrame(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		receipts = append(receipts, receipt{e.ID, native})
	}
	// A later audit trigger must not erase/change an earlier event or undo the
	// business mutation. Check all acknowledgments after the final append.
	for _, receipt := range receipts {
		current, err := pageGrantEventFrame(ctx, tx, receipt.id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, receipt.native) {
			return errors.New("page grant audit did not survive unchanged")
		}
	}

	actual, err := readPageGrantSubject(ctx, tx, rec.ID, kind, subject)
	if err != nil {
		return err
	}
	if expected == nil {
		expected = []pageGrantSnapshot{}
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].SubjectType != expected[j].SubjectType {
			return expected[i].SubjectType < expected[j].SubjectType
		}
		if expected[i].SubjectID != expected[j].SubjectID {
			return expected[i].SubjectID < expected[j].SubjectID
		}
		return expected[i].Level < expected[j].Level
	})
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("page grant final state differs from audited mutation")
	}
	current, err := h.loadPage(ctx, changes[0].entry.WorkspaceID, rec.Slug)
	if err != nil {
		return err
	}
	if current.ID != rec.ID || current.PrivateAudience {
		return errors.New("page identity or audience changed during grant mutation")
	}
	if err := h.pageGrantIssuerCurrent(ctx, changes[0].entry.WorkspaceID, actor, current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errPageGrantIssuerNotCurrent
		}
		return err
	}
	closedUsers, err := h.closeRevokedWorkbookSessions(ctx, tx, changes[0].entry.WorkspaceID, rec)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if rec.HasWorkbook && h.hub != nil {
		h.hub.RecheckWorkbookAccess(ctx, rec.ID, closedUsers...)
	}
	for _, publish := range notify {
		publish()
	}
	return nil
}

// AppendTx has already acknowledged the prepared keyed row. Retain every
// native field so a later append cannot tamper with an earlier event.
func pageGrantEventFrame(ctx context.Context, tx *sql.Tx, id string) ([]any, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM journal_entries WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, errors.Join(sql.ErrNoRows, rows.Err())
	}
	values := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	if rows.Next() {
		return nil, errors.New("page grant audit identity is not unique")
	}
	return values, errors.Join(rows.Err(), rows.Close())
}
