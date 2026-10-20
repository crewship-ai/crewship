package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type privateMetadataReadKey struct{}

// A batch belongs to one metadata response, never a reusable authority receipt.
type privateMetadataRead struct {
	tx     *sql.Tx
	writer bool
	audits [][8]string
}

func (h *PageHandler) startMetadataRead(r *http.Request, writer bool) (*http.Request, *privateMetadataRead, error) {
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: !writer})
	if err != nil {
		return r, nil, err
	}
	b := &privateMetadataRead{tx: tx, writer: writer}
	if writer {
		// Production uses _txlock=immediate. Also acquire the native writer in
		// test/custom pools with deferred BEGIN, without touching any audit row.
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM audit_logs WHERE 0`); err != nil {
			tx.Rollback()
			return r, nil, err
		}
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, privateMetadataReadKey{}, b)
	return r.WithContext(ctx), b, nil
}

func (h *PageHandler) beginPageMetadataRead(w http.ResponseWriter, r *http.Request) (*http.Request, *privateMetadataRead, bool) {
	r, b, err := h.startMetadataRead(r, false)
	if err == nil {
		column, reference := "slug", r.PathValue("slug")
		if strings.HasPrefix(reference, "@") {
			column, reference = "id", strings.TrimPrefix(reference, "@")
		}
		var private bool
		var custodian string
		err = b.tx.QueryRowContext(r.Context(), `SELECT private_audience,COALESCE(owner_user_id,'') FROM pages WHERE workspace_id=? AND `+column+`=?`, WorkspaceIDFromContext(r.Context()), reference).Scan(&private, &custodian)
		// Classification chooses a branch only. Authorization and custody are read
		// again inside the writer transaction; ordinary metadata keeps this snapshot.
		if err == nil && private {
			// Refuse an unavailable caller before contending for the writer.
			// This snapshot grants no disclosure: the writer still reloads the
			// Page, custody and current authority before actual metadata/audit.
			var viewer *pageViewer
			if user := UserFromContext(r.Context()); user != nil {
				viewer, err = h.livePrivatePageReader(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
			}
			if err == nil && !privatePageViewer(&pageRecord{OwnerUserID: custodian}, viewer) {
				b.tx.Rollback()
				replyError(w, 404, "page not found")
				return r, nil, false
			}
			if err == nil {
				b.tx.Rollback()
				r, b, err = h.startMetadataRead(r, true)
			}
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	}
	if err != nil {
		if b != nil {
			b.tx.Rollback()
		}
		replyInternalError(w, h.logger, "begin Page metadata read", err)
		return r, nil, false
	}
	return r, b, true
}

func (h *PageHandler) finishMetadataRead(w http.ResponseWriter, r *http.Request, b *privateMetadataRead) bool {
	// A later trigger can alter an earlier append. Recheck the entire batch only
	// after all appends, under the same transaction and before any response bytes.
	for _, expected := range b.audits {
		var actual [8]any
		if err := b.tx.QueryRowContext(r.Context(), `SELECT id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at FROM audit_logs WHERE id=?`, expected[0]).Scan(&actual[0], &actual[1], &actual[2], &actual[3], &actual[4], &actual[5], &actual[6], &actual[7]); err != nil || !privateAuditRowMatches(actual, expected) {
			replyInternalError(w, h.logger, "verify private read audit", fmt.Errorf("private audit postcondition: %w", errors.Join(err, errPrivateMetadataAudit)))
			return false
		}
		role, err := h.liveContentHumanRole(r.Context(), expected[1], expected[2])
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "verify private reader", err)
			return false
		}
		if role != "OWNER" {
			replyError(w, 404, "page not found")
			return false
		}
	}
	if err := b.tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit private read audit", err)
		return false
	}
	return true
}

var errPrivateMetadataAudit = errors.New("private read audit unavailable")

func (h *PageHandler) appendPrivateReadAudit(r *http.Request, b *privateMetadataRead, rec *pageRecord, metadata string) error {
	if !b.writer {
		return errPrivateMetadataAudit
	}
	row := [8]string{generateCUID(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, "artifact.private_owner_accessed", "page", rec.ID, metadata, tsformat.Format(time.Now())}
	res, err := b.tx.ExecContext(r.Context(), `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, row[0], row[1], row[2], row[3], row[4], row[5], row[6], row[7])
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return errors.Join(err, errPrivateMetadataAudit)
	}
	b.audits = append(b.audits, row)
	return nil
}

func privateAuditRowMatches(actual [8]any, expected [8]string) bool {
	for i, value := range actual {
		text, ok := value.(string)
		if !ok || text != expected[i] {
			return false
		}
	}
	return true
}
