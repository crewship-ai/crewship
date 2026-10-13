package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) boardRequestRecord(ctx context.Context, q pageAuthQueryer, id, board string, detail bool) (map[string]any, error) {
	var kind, status, instruction, selected, placements, target, agent, name, run, human, created, updated, message, dialect, converter, hash, source, inserted, insertedBy string
	var resultSeq sql.NullInt64
	var base int64
	err := q.QueryRowContext(ctx, `SELECT r.kind,r.status,r.base_seq,r.instruction,r.selected_element_ids_json,r.selected_placement_ids_json,r.target_json,COALESCE(r.agent_id,''),COALESCE(a.name,''),COALESCE(r.run_id,''),COALESCE(r.human_user_id,''),r.created_at,r.updated_at,r.error,r.dialect,r.converter_version,r.source_hash,r.mermaid_source,r.result_seq,r.inserted_element_ids_json,COALESCE(r.inserted_by_user_id,'') FROM page_board_requests r LEFT JOIN agents a ON a.id=r.agent_id WHERE r.id=? AND r.board_page_id=? AND r.workspace_id=?`, id, board, WorkspaceIDFromContext(ctx)).Scan(&kind, &status, &base, &instruction, &selected, &placements, &target, &agent, &name, &run, &human, &created, &updated, &message, &dialect, &converter, &hash, &source, &resultSeq, &inserted, &insertedBy)
	if err != nil {
		return nil, err
	}
	var ids, pins, insertedIDs []string
	var geometry map[string]any
	if json.Unmarshal([]byte(selected), &ids) != nil || json.Unmarshal([]byte(placements), &pins) != nil || json.Unmarshal([]byte(target), &geometry) != nil || json.Unmarshal([]byte(inserted), &insertedIDs) != nil {
		return nil, sql.ErrNoRows
	}
	if ids == nil {
		ids = []string{}
	}
	if pins == nil {
		pins = []string{}
	}
	if insertedIDs == nil {
		insertedIDs = []string{}
	}
	rec, err := h.loadBoardRequestPage(ctx, board)
	if err != nil {
		return nil, err
	}
	user := UserFromContext(ctx)
	canWrite := user != nil && h.mayEditSpec(ctx, WorkspaceIDFromContext(ctx), user.ID, RoleFromContext(ctx), rec)
	out := map[string]any{"converter_url": h.boardConverterURL(), "id": id, "kind": kind, "status": status, "base_seq": base, "instruction": instruction, "selected_element_ids": ids, "selected_placement_ids": pins, "target": geometry, "agent_id": agent, "agent_name": name, "run_id": run, "human_user_id": human, "created_at": created, "updated_at": updated, "error": message, "dialect": dialect, "converter_version": converter, "source_hash": hash, "result_seq": nil, "inserted_by_user_id": insertedBy, "can_insert": canWrite && status == "ready_to_insert", "can_cancel": canWrite && (status == "queued" || status == "running" || status == "ready_to_insert")}
	if resultSeq.Valid {
		out["result_seq"] = resultSeq.Int64
	}
	var pinStatus, pinReason string
	var pinID sql.NullString
	if q.QueryRowContext(ctx, `SELECT pin_status,pin_reason,result_placement_id FROM page_board_requests WHERE id=?`, id).Scan(&pinStatus, &pinReason, &pinID) == nil {
		out["pin_status"] = pinStatus
		out["pin_reason"] = pinReason
		if pinID.Valid {
			out["result_placement_id"] = pinID.String
		}
	}
	if detail {
		out["mermaid_source"] = source
		out["inserted_element_ids"] = insertedIDs
		out["edited_since_insert"] = false
		if status == "inserted" {
			var rawHashes string
			hashes := map[string]string{}
			if q.QueryRowContext(ctx, `SELECT inserted_element_hashes_json FROM page_board_requests WHERE id=?`, id).Scan(&rawHashes) != nil || json.Unmarshal([]byte(rawHashes), &hashes) != nil {
				out["edited_since_insert"] = true
			} else {
				for elementID, expected := range hashes {
					var raw sql.NullString
					if q.QueryRowContext(ctx, `SELECT element_json FROM page_board_elements WHERE board_page_id=? AND element_id=?`, board, elementID).Scan(&raw) != nil || !raw.Valid {
						out["edited_since_insert"] = true
						break
					}
					sum := sha256.Sum256([]byte(raw.String))
					if hex.EncodeToString(sum[:]) != expected {
						out["edited_since_insert"] = true
						break
					}
				}
			}
		}
	}
	var resultPage string
	var revision int64
	if q.QueryRowContext(ctx, `SELECT result_page_id,result_revision FROM page_board_requests WHERE id=? AND result_page_id IS NOT NULL`, id).Scan(&resultPage, &revision) == nil {
		sourcePage, e := h.loadBoardRequestPage(ctx, resultPage)
		if e == nil && user != nil {
			viewer, e := h.loadViewer(ctx, WorkspaceIDFromContext(ctx), user.ID)
			if e == nil {
				visible, e := h.canSeePage(ctx, WorkspaceIDFromContext(ctx), sourcePage, nil, viewer)
				if e == nil && visible {
					var exists int
					if q.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=?`, resultPage, revision).Scan(&exists) == nil && exists == 1 {
						out["result"] = map[string]any{"page_slug": sourcePage.Slug, "title": sourcePage.Name, "revision": revision}
					}
				}
			}
		}
	}
	return out, nil
}
func (h *PageHandler) ListBoardRequests(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.boardPage(w, r, false)
	if !ok {
		return
	}
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id FROM page_board_requests WHERE board_page_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		h.boardError(w, err)
		return
	}
	requests := []map[string]any{}
	for _, id := range ids {
		record, e := h.boardRequestRecord(r.Context(), tx, id, rec.ID, false)
		if e != nil {
			h.boardError(w, e)
			return
		}
		requests = append(requests, record)
	}
	user := UserFromContext(r.Context())
	can := rec.OwnerUserID != "" && h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID, RoleFromContext(r.Context()), rec)
	targets := []artifactRequestTarget{}
	if can {
		targets = h.boardRequestTargets(r.Context(), user.ID)
	}
	writeJSON(w, 200, map[string]any{"can_dispatch": can, "eligible_chats": targets, "converter_url": h.boardConverterURL(), "requests": requests})
}
func (h *PageHandler) GetBoardRequest(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.boardPage(w, r, false)
	if !ok {
		return
	}
	out, err := h.boardRequestRecord(r.Context(), tx, r.PathValue("requestId"), rec.ID, true)
	if err != nil {
		replyError(w, 404, "board request not found")
		return
	}
	writeJSON(w, 200, out)
}
func (h *PageHandler) CancelBoardRequest(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	id := r.PathValue("requestId")
	var status string
	if tx.QueryRowContext(r.Context(), `SELECT status FROM page_board_requests WHERE id=? AND board_page_id=?`, id, rec.ID).Scan(&status) != nil {
		replyError(w, 404, "board request not found")
		return
	}
	if status != "cancelled" && status != "queued" && status != "running" && status != "ready_to_insert" {
		replyError(w, 409, "board request is terminal")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE page_board_requests SET status='cancelled',cancelled_by_user_id=?,updated_at=? WHERE id=? AND status!='cancelled'`, UserFromContext(r.Context()).ID, tsformat.Format(time.Now()), id); err != nil {
		h.boardError(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE artifact_creation_authorizations SET revoked_at=? WHERE board_request_id=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), id); err != nil {
		h.boardError(w, err)
		return
	}
	out, err := h.boardRequestRecord(r.Context(), tx, id, rec.ID, true)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	if cancel, ok := h.artifactRequestCancels.Load("board:" + id); ok {
		cancel.(context.CancelFunc)()
	}
	writeJSON(w, 200, out)
}

func (h *PageHandler) loadBoardRequestPage(ctx context.Context, id string) (*pageRecord, error) {
	var slug string
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, id, WorkspaceIDFromContext(ctx)).Scan(&slug); err != nil {
		return nil, err
	}
	return h.loadPage(ctx, WorkspaceIDFromContext(ctx), slug)
}

func (h *PageHandler) boardRequestTargets(ctx context.Context, user string) []artifactRequestTarget {
	out := []artifactRequestTarget{}
	q := h.pageAuthReader(ctx)
	rows, err := q.QueryContext(ctx, `SELECT c.id,COALESCE(c.title,''),a.id,a.name,a.slug FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL AND a.expired_at IS NULL WHERE c.workspace_id=? AND c.created_by=? AND c.visibility='private' ORDER BY c.updated_at DESC LIMIT 50`, WorkspaceIDFromContext(ctx), user)
	if err != nil {
		return out
	}
	candidates := []artifactRequestTarget{}
	for rows.Next() {
		var v artifactRequestTarget
		if rows.Scan(&v.ChatID, &v.Title, &v.AgentID, &v.AgentName, &v.AgentSlug) == nil {
			candidates = append(candidates, v)
		}
	}
	rows.Close()
	for _, v := range candidates {
		if h.artifactRequestChatPrivate(ctx, v.ChatID, user) {
			receipt, e := chataudience.CaptureTrusted(ctx, h.db, v.ChatID, user, nil)
			if e == nil && receipt != nil && boardReceiptCurrent(ctx, q, receipt) {
				out = append(out, v)
			}
		}
	}
	return out
}
