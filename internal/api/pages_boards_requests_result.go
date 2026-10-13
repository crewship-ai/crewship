package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) checkBoardRequestRun(ctx context.Context, id string, a *artifactRunAuthority, kind string) (string, error) {
	if err := h.checkArtifactRunPrincipal(ctx, a); err != nil {
		return "", err
	}
	var board string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT COALESCE(board_page_id,'') FROM page_board_requests WHERE id=? AND workspace_id=? AND human_user_id=? AND agent_id=? AND chat_id=? AND run_id=? AND kind=? AND status IN ('running','ready_to_insert')`, id, a.WorkspaceID, a.HumanUserID, a.AgentID, a.ChatID, a.RunID, kind).Scan(&board)
	return board, err
}
func (h *PageHandler) GetBoardRequestAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	var kind string
	if tx.QueryRowContext(r.Context(), `SELECT kind FROM page_board_requests WHERE id=?`, r.PathValue("requestId")).Scan(&kind) != nil {
		replyError(w, 403, "job authority required")
		return
	}
	board, err := h.checkBoardRequestRun(r.Context(), r.PathValue("requestId"), a, kind)
	if err != nil {
		replyError(w, 403, "job authority required")
		return
	}
	page, err := h.loadBoardRequestPage(r.Context(), board)
	if err != nil {
		replyError(w, 403, "source board unavailable")
		return
	}
	viewer, err := h.loadViewer(r.Context(), a.WorkspaceID, a.HumanUserID)
	if err != nil {
		replyError(w, 403, "source board unavailable")
		return
	}
	visible, err := h.canSeePage(r.Context(), a.WorkspaceID, page, nil, viewer)
	if err != nil || !visible {
		replyError(w, 403, "source board authority revoked")
		return
	}
	var snapshot, instruction, target string
	if tx.QueryRowContext(r.Context(), `SELECT snapshot_json,instruction,target_json FROM page_board_requests WHERE id=?`, r.PathValue("requestId")).Scan(&snapshot, &instruction, &target) != nil {
		replyError(w, 404, "job unavailable")
		return
	}
	files := []string{}
	sources := []string{}
	for _, selection := range []struct {
		query  string
		target *[]string
	}{{`SELECT file_id FROM page_board_request_files WHERE request_id=? ORDER BY file_id`, &files}, {`SELECT id FROM page_board_request_sources WHERE request_id=? ORDER BY id`, &sources}} {
		rows, e := tx.QueryContext(r.Context(), selection.query, r.PathValue("requestId"))
		if e != nil {
			h.boardError(w, e)
			return
		}
		for rows.Next() {
			var value string
			if e = rows.Scan(&value); e != nil {
				break
			}
			*selection.target = append(*selection.target, value)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			h.boardError(w, e)
			return
		}
	}
	sourceRefs := []map[string]string{}
	rows, e := tx.QueryContext(r.Context(), `SELECT id,placement_id FROM page_board_request_sources WHERE request_id=? ORDER BY id`, r.PathValue("requestId"))
	if e != nil {
		h.boardError(w, e)
		return
	}
	for rows.Next() {
		var sourceID, placementID string
		if e = rows.Scan(&sourceID, &placementID); e != nil {
			break
		}
		sourceRefs = append(sourceRefs, map[string]string{"id": sourceID, "placement_id": placementID})
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		h.boardError(w, e)
		return
	}
	var previewExists int
	if tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_board_request_previews WHERE request_id=?`, r.PathValue("requestId")).Scan(&previewExists) != nil {
		replyError(w, 503, "preview metadata unavailable")
		return
	}
	var preview any
	if previewExists > 0 {
		preview = map[string]any{"kind": "untrusted-human-illustration", "authority": "canonical-snapshot-only", "path": "/board-requests/" + r.PathValue("requestId") + "/preview"}
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("requestId"), "kind": kind, "instruction": instruction, "snapshot": json.RawMessage(snapshot), "target": json.RawMessage(target), "file_ids": files, "source_ids": sources, "source_refs": sourceRefs, "preview": preview, "dialect": boardMermaidDialect, "converter_version": boardMermaidConverter})
}
func (h *PageHandler) PublishBoardRequestMermaid(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		MutationID       string `json:"mutation_id"`
		Source           string `json:"source"`
		Dialect          string `json:"dialect"`
		ConverterVersion string `json:"converter_version"`
	}
	if !decodeBoardRequest(w, r, &req, 65536) {
		return
	}
	if !boardMutationValid(req.MutationID) || req.Dialect != boardMermaidDialect || req.ConverterVersion != boardMermaidConverter {
		replyError(w, 422, "supported source profile required")
		return
	}
	source, err := validateBoardMermaid(req.Source)
	if err != nil {
		replyError(w, 422, err.Error())
		return
	}
	sum := sha256.Sum256([]byte(source))
	hash := hex.EncodeToString(sum[:])
	fingerprint := boardFingerprint("board-mermaid", req)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_board_requests SET updated_at=updated_at WHERE id=?`, r.PathValue("requestId")); err != nil {
		h.boardError(w, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	board, err := h.checkBoardRequestRun(r.Context(), r.PathValue("requestId"), a, "diagram")
	if err != nil || !h.boardRequestMayWrite(r.Context(), board, a.HumanUserID) {
		replyError(w, 403, "live diagram job authority required")
		return
	}
	var oldMutation, oldFingerprint string
	if err = tx.QueryRowContext(r.Context(), `SELECT source_mutation_id,source_fingerprint FROM page_board_requests WHERE id=?`, r.PathValue("requestId")).Scan(&oldMutation, &oldFingerprint); err != nil {
		h.boardError(w, err)
		return
	}
	if oldMutation != "" {
		if oldMutation != req.MutationID || oldFingerprint != fingerprint {
			replyError(w, 409, "diagram result already supplied")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ready_to_insert", "source_hash": hash})
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_board_requests SET mermaid_source=?,source_hash=?,source_mutation_id=?,source_fingerprint=?,status='ready_to_insert',updated_at=? WHERE id=? AND status='running'`, source, hash, req.MutationID, fingerprint, tsformat.Format(time.Now()), r.PathValue("requestId")); err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"status": "ready_to_insert", "source_hash": hash})
}
