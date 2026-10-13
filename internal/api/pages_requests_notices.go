package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
)

const pageRequestRetryWarning = "The previous runtime outcome is unconfirmed. This new submission may duplicate work that is still running."

func (h *PageHandler) validatePageRequestRetry(ctx context.Context, q pageAuthQueryer, kind, parent, page, workspace, user, jobKind string) (string, error) {
	if parent == "" {
		return "", nil
	}
	if len(parent) > 128 || strings.TrimSpace(parent) != parent {
		return "", errors.New("invalid retry_of_request_id")
	}
	var source, actor, state, previousKind string
	query := `SELECT page_id,COALESCE(human_user_id,''),status,'' FROM page_artifact_requests WHERE id=?`
	if kind == "board" {
		query = `SELECT COALESCE(board_page_id,''),COALESCE(human_user_id,''),status,kind FROM page_board_requests WHERE id=? AND workspace_id=?`
	}
	args := []any{parent}
	if kind == "board" {
		args = append(args, workspace)
	}
	if err := q.QueryRowContext(ctx, query, args...).Scan(&source, &actor, &state, &previousKind); err != nil {
		return "", errors.New("retry parent is unavailable")
	}
	if source != page || actor != user || (state != "failed" && state != "cancelled") || (kind == "board" && previousKind != jobKind) {
		return "", errors.New("retry requires your failed or cancelled request on the same source")
	}
	job, err := loadPageRequestIdentity(ctx, q, kind, parent)
	if err != nil || job.Workspace != workspace {
		return "", errors.New("retry parent is unavailable")
	}
	if job.Run != "" {
		observed, err := readPageRequestLifecycle(ctx, q, job, time.Now())
		if err != nil || observed.RunStatus == "" {
			return pageRequestRetryWarning, nil
		}
	}
	return "", nil
}

// Notice rows contain only generic text and opaque result pointers. The
// recipient still needs current Page ACL to resolve/read a result. No external
// notification hook is called: source state and inbox dedupe commit together.
func (h *PageHandler) pageRequestNoticeTx(ctx context.Context, tx *sql.Tx, kind, id string) error {
	var workspace, user, page, resultPage, state string
	var resultRevision sql.NullInt64
	var pending int
	query := `SELECT p.workspace_id,COALESCE(r.human_user_id,''),r.page_id,r.page_id,r.result_revision,r.status,r.notice_pending FROM page_artifact_requests r JOIN pages p ON p.id=r.page_id WHERE r.id=?`
	if kind == "board" {
		query = `SELECT workspace_id,COALESCE(human_user_id,''),COALESCE(board_page_id,''),COALESCE(result_page_id,''),result_revision,status,notice_pending FROM page_board_requests WHERE id=?`
	}
	if err := tx.QueryRowContext(ctx, query, id).Scan(&workspace, &user, &page, &resultPage, &resultRevision, &state, &pending); err != nil {
		return err
	}
	if pending == 0 || state == "queued" || state == "running" {
		return nil
	}
	eligible := false
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL WHERE m.workspace_id=? AND m.user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=m.user_id AND access_mode='restricted')`, workspace, user).Scan(&role)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		target := page
		if resultPage != "" {
			target = resultPage
		}
		var slug string
		err = tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, target, workspace).Scan(&slug)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			auth := context.WithValue(ctx, pageAuthReaderKey{}, tx)
			rec, err := h.loadPage(auth, workspace, slug)
			if err != nil {
				return err
			}
			viewer, err := h.loadViewer(auth, workspace, user)
			if err != nil {
				return err
			}
			eligible, err = h.canSeePage(auth, workspace, rec, nil, viewer)
			if err != nil {
				return err
			}
			if eligible {
				page = target
			}
		}
	}
	if eligible {
		payload := map[string]any{"subkind": "page_request_result", "request_kind": kind, "request_id": id, "page_id": page}
		if state == "succeeded" && resultRevision.Valid {
			payload["result_revision"] = resultRevision.Int64
		}
		if err := inbox.InsertTx(ctx, tx, h.logger, inbox.Item{WorkspaceID: workspace, Kind: inbox.KindMessage, SourceID: "page-request:" + kind + ":" + id, TargetUserID: user, Title: "Agent request updated", BodyMD: "Your request has an update. Review the available result in Pages.", SenderType: "system", SenderName: "Crewship", Payload: payload}); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE `+pageRequestTable(kind)+` SET notice_pending=0 WHERE id=?`, id)
	return err
}

func (h *PageHandler) reconcilePageRequestNotices(ctx context.Context) error {
	_, err := h.reconcilePageRequestNoticesAfter(ctx, "")
	return err
}

func (h *PageHandler) reconcilePageRequestNoticesAfter(ctx context.Context, after string) (string, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT kind,id FROM (SELECT 'artifact' AS kind,id FROM page_artifact_requests WHERE notice_pending=1 AND status NOT IN('queued','running') UNION ALL SELECT 'board' AS kind,id FROM page_board_requests WHERE notice_pending=1 AND status NOT IN('queued','running')) WHERE kind||':'||id>? ORDER BY kind,id LIMIT 100`, after)
	if err != nil {
		return after, err
	}
	var jobs []struct{ kind, id string }
	for rows.Next() {
		var j struct{ kind, id string }
		if err = rows.Scan(&j.kind, &j.id); err != nil {
			rows.Close()
			return after, err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return after, err
	}
	var problems []error
	for _, job := range jobs {
		tx, err := h.db.BeginTx(ctx, nil)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE `+pageRequestTable(job.kind)+` SET notice_pending=notice_pending WHERE id=?`, job.id)
			if err == nil {
				err = h.pageRequestNoticeTx(ctx, tx, job.kind, job.id)
			}
			if err == nil {
				err = tx.Commit()
			}
			tx.Rollback()
		}
		if err != nil {
			problems = append(problems, err)
		}
	}
	if len(jobs) < 100 {
		return "", errors.Join(problems...)
	}
	last := jobs[len(jobs)-1]
	return last.kind + ":" + last.id, errors.Join(problems...)
}

func (h *PageHandler) pageRequestRetryWarning(ctx context.Context, q pageAuthQueryer, kind, parent string) string {
	if parent == "" {
		return ""
	}
	job, err := loadPageRequestIdentity(ctx, q, kind, parent)
	if err != nil {
		return pageRequestRetryWarning
	}
	if job.Run == "" {
		return ""
	}
	evidence, err := readPageRequestLifecycle(ctx, q, job, time.Now())
	if err != nil || evidence.RunStatus == "" {
		return pageRequestRetryWarning
	}
	return ""
}

func pageRequestCreateResponse(id, parent, warning string) map[string]any {
	out := map[string]any{"id": id}
	if parent != "" {
		out["retry_of_request_id"] = parent
	}
	if warning != "" {
		out["retry_warning"] = warning
	}
	return out
}
