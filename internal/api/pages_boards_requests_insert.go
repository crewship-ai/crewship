package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) InsertBoardRequest(w http.ResponseWriter, r *http.Request) {
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	var req struct {
		MutationID       string `json:"mutation_id"`
		BaseSeq          *int64 `json:"base_seq"`
		SourceHash       string `json:"source_hash"`
		ConverterVersion string `json:"converter_version"`
		Upserts          []struct {
			Element     json.RawMessage `json:"element"`
			ExpectedRev *int64          `json:"expected_rev"`
		} `json:"upserts"`
	}
	if !decodeBoardRequest(w, r, &req, boardRequestSnapshotLimit) {
		return
	}
	if !boardMutationValid(req.MutationID) || req.BaseSeq == nil || req.SourceHash == "" || req.ConverterVersion != boardMermaidConverter || len(req.Upserts) == 0 || len(req.Upserts) > 100 {
		replyError(w, 422, "bounded confirmed conversion required")
		return
	}
	batch := pages.BoardBatch{BaseSeq: *req.BaseSeq}
	for _, upsert := range req.Upserts {
		if upsert.ExpectedRev == nil || *upsert.ExpectedRev != 0 {
			replyError(w, 422, "conversion may only create new elements")
			return
		}
		element, err := pages.ValidateBoardElement(upsert.Element)
		if err != nil || element.Type == "image" || element.Type == "embeddable" {
			replyError(w, 422, "unsupported converted element")
			return
		}
		batch.Upserts = append(batch.Upserts, pages.BoardUpsert{Element: upsert.Element, ExpectedRev: 0})
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	id := r.PathValue("requestId")
	var kind, status, hash, converter string
	var priorSeq *int64
	if err := tx.QueryRowContext(r.Context(), `SELECT kind,status,source_hash,converter_version,result_seq FROM page_board_requests WHERE id=? AND board_page_id=? AND workspace_id=?`, id, rec.ID, WorkspaceIDFromContext(r.Context())).Scan(&kind, &status, &hash, &converter, &priorSeq); err != nil {
		replyError(w, 404, "board request not found")
		return
	}
	if kind != "diagram" || req.SourceHash != hash || req.ConverterVersion != converter {
		replyError(w, 409, "diagram source or converter changed")
		return
	}
	if status == "inserted" {
		writeJSON(w, 200, map[string]any{"id": id, "status": "inserted", "result_seq": priorSeq, "already_inserted": true})
		return
	}
	if status != "ready_to_insert" {
		replyError(w, 409, "diagram not ready for confirmed insertion")
		return
	}
	digest := boardFingerprint("board-request-insert", req)
	if done, err := h.boardRetry(w, r, tx, rec.ID, req.MutationID, digest); done || err != nil {
		if err != nil {
			h.boardError(w, err)
		}
		return
	}
	current, err := loadBoardScene(r.Context(), tx, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	next, err := pages.ApplyBoardBatch(current, batch)
	if err != nil {
		if errors.Is(err, pages.ErrBoardConflict) {
			replyError(w, 409, "board changed; review converted result before insertion")
		} else {
			replyError(w, 422, err.Error())
		}
		return
	}
	revisions := map[string]int64{}
	ids := []string{}
	hashes := map[string]string{}
	for _, upsert := range batch.Upserts {
		element, _ := pages.ValidateBoardElement(upsert.Element)
		record := next.Records[element.ID]
		revisions[element.ID] = record.ElementRev
		sum := sha256.Sum256(record.Element)
		hashes[element.ID] = hex.EncodeToString(sum[:])
		ids = append(ids, element.ID)
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq,origin_request_id) VALUES(?,?,?,?,?,?,?)`, rec.ID, element.ID, record.ElementRev, string(record.Element), UserFromContext(r.Context()).ID, next.Seq, id); err != nil {
			h.boardError(w, err)
			return
		}
	}
	sort.Strings(ids)
	rawIDs, _ := json.Marshal(ids)
	rawHashes, _ := json.Marshal(hashes)
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_boards SET seq=? WHERE page_id=?`, next.Seq, rec.ID); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.boardReceipt(r, tx, rec.ID, req.MutationID, digest, next.Seq, ""); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.recordBoardElementRevisions(r.Context(), tx, rec.ID, req.MutationID, revisions); err != nil {
		h.boardError(w, err)
		return
	}
	undoExpiry, err := boardReceiptUndoExpiry(r.Context(), tx, rec.ID, req.MutationID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_board_requests SET status='inserted',result_seq=?,inserted_element_ids_json=?,inserted_element_hashes_json=?,inserted_by_user_id=?,updated_at=? WHERE id=? AND status='ready_to_insert'`, next.Seq, string(rawIDs), string(rawHashes), UserFromContext(r.Context()).ID, tsformat.Format(time.Now()), id); err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()), "page.updated", map[string]any{"page_id": rec.ID})
	writeJSON(w, 201, map[string]any{"id": id, "status": "inserted", "result_seq": next.Seq, "already_inserted": false, "element_revs": revisions, "undo_expires_at": undoExpiry, "inserted_element_ids": ids})
}
