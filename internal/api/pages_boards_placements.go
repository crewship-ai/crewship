package api

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
)

type boardPlacementGeometry struct {
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
}
type boardPlacementUpdate struct {
	ExpectedRev *int64 `json:"expected_rev,omitempty"`
	ID          string `json:"id"`
	boardPlacementGeometry
}
type boardPlacementDelete struct {
	ExpectedRev *int64 `json:"expected_rev,omitempty"`
	ID          string `json:"id"`
}

func (g boardPlacementGeometry) valid() bool {
	for _, v := range []*float64{g.X, g.Y, g.Width, g.Height} {
		if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < -1000000 || *v > 1000000 {
			return false
		}
	}
	return *g.Width > 0 && *g.Height > 0
}
func (h *PageHandler) loadBoardPlacements(r *http.Request, pageID string, q pageAuthQueryer) ([]map[string]any, error) {
	rows, err := q.QueryContext(r.Context(), `SELECT id,COALESCE(source_page_id,''),COALESCE(source_revision,0),x,y,width,height,placement_rev FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at IS NULL ORDER BY id LIMIT 101`, pageID)
	if err != nil {
		return nil, err
	}
	type entry struct {
		id, source             string
		revision, placementRev int64
		x, y, width, height    float64
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.source, &e.revision, &e.x, &e.y, &e.width, &e.height, &e.placementRev); err != nil {
			rows.Close()
			return nil, err
		}
		if !(boardPlacementGeometry{X: &e.x, Y: &e.y, Width: &e.width, Height: &e.height}).valid() {
			rows.Close()
			return nil, errors.New("invalid stored placement geometry")
		}
		entries = append(entries, e)
		if len(entries) > 100 {
			rows.Close()
			return nil, errors.New("board live placement limit exceeded")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(entries))
	viewer, err := h.loadViewer(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID)
	if err != nil {
		return nil, err
	}
	var role string
	if err := q.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&role); err != nil {
		return nil, err
	}
	viewer.Role = role
	_, delegated := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	for _, e := range entries {
		item := map[string]any{"id": e.id, "x": e.x, "y": e.y, "width": e.width, "height": e.height, "placement_rev": e.placementRev}
		var slug string
		if !delegated && e.source != "" && q.QueryRowContext(r.Context(), `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, e.source, WorkspaceIDFromContext(r.Context())).Scan(&slug) == nil {
			source, err := h.loadPage(r.Context(), WorkspaceIDFromContext(r.Context()), slug)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil {
				visible, err := h.canSeePage(r.Context(), WorkspaceIDFromContext(r.Context()), source, nil, viewer)
				if err != nil {
					return nil, err
				}
				if visible {
					item["source"] = map[string]any{"page_slug": source.Slug, "revision": e.revision, "title": source.Name}
				}
			}
		}
		result = append(result, item)
	}
	return result, nil
}
func (h *PageHandler) AddBoardPlacement(w http.ResponseWriter, r *http.Request) {
	if artifactAgentID(r) != "" {
		replyError(w, 403, "agent placement edits are not delegated")
		return
	}
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	var req struct {
		MutationID     string `json:"mutation_id"`
		BaseSeq        *int64 `json:"base_seq"`
		SourceSlug     string `json:"source_page_slug"`
		SourceRevision *int64 `json:"source_revision"`
		boardPlacementGeometry
	}
	if _, ok := boardDecode(w, r, &req); !ok {
		return
	}
	if !boardMutationValid(req.MutationID) || req.BaseSeq == nil || *req.BaseSeq < 0 || *req.BaseSeq >= 9007199254740991 || req.SourceRevision == nil || *req.SourceRevision < 1 || !req.boardPlacementGeometry.valid() {
		replyError(w, 400, "invalid placement")
		return
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	source, err := h.loadPage(r.Context(), WorkspaceIDFromContext(r.Context()), req.SourceSlug)
	if err != nil {
		replyError(w, 404, "artifact not found")
		return
	}
	viewer, err := h.loadViewer(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	visible, err := h.canSeePage(r.Context(), WorkspaceIDFromContext(r.Context()), source, nil, viewer)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if !visible {
		replyError(w, 404, "artifact not found")
		return
	}
	var exists int
	if tx.QueryRowContext(r.Context(), `SELECT 1 FROM page_artifact_revisions WHERE page_id=? AND revision=?`, source.ID, *req.SourceRevision).Scan(&exists) != nil {
		replyError(w, 404, "artifact revision not found")
		return
	}
	digest := boardFingerprint("placement-add", req)
	if done, err := h.boardRetry(w, r, tx, rec.ID, req.MutationID, digest); err != nil {
		h.boardError(w, err)
		return
	} else if done {
		return
	}
	var seq int64
	var count, total int
	if err = tx.QueryRowContext(r.Context(), `SELECT seq,(SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at IS NULL),(SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=?) FROM page_boards WHERE page_id=?`, rec.ID, rec.ID, rec.ID).Scan(&seq, &count, &total); err != nil {
		h.boardError(w, err)
		return
	}
	if seq != *req.BaseSeq {
		replyError(w, 409, "board sequence changed")
		return
	}
	if total >= 1000 {
		h.boardHistoryCapacity(w, r, tx, rec.ID)
		return
	}
	if count >= 100 {
		replyError(w, 409, "board placement limit reached")
		return
	}
	id := generateCUID()
	seq++
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, rec.ID, source.ID, *req.SourceRevision, *req.X, *req.Y, *req.Width, *req.Height, seq, UserFromContext(r.Context()).ID); err != nil {
		h.boardError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_boards SET seq=? WHERE page_id=?`, seq, rec.ID); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.boardReceipt(r, tx, rec.ID, req.MutationID, digest, seq, id); err != nil {
		h.boardError(w, err)
		return
	}
	revisions := map[string]int64{id: 1}
	if err := recordBoardPlacementRevisions(r.Context(), tx, rec.ID, req.MutationID, revisions); err != nil {
		h.boardError(w, err)
		return
	}
	undoExpiry, err := boardReceiptUndoExpiry(r.Context(), tx, rec.ID, req.MutationID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()), "page.updated", map[string]any{"page_id": rec.ID})
	writeJSON(w, 201, map[string]any{"seq": seq, "result_seq": seq, "placement_id": id, "placement_revs": revisions, "element_revs": map[string]int64{}, "undo_expires_at": undoExpiry})
}
func (h *PageHandler) DeleteBoardPlacement(w http.ResponseWriter, r *http.Request) {
	if artifactAgentID(r) != "" {
		replyError(w, 403, "agent placement edits are not delegated")
		return
	}
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	var req struct {
		MutationID  string `json:"mutation_id"`
		BaseSeq     *int64 `json:"base_seq"`
		ExpectedRev *int64 `json:"expected_rev,omitempty"`
	}
	if _, ok := boardDecode(w, r, &req); !ok {
		return
	}
	if !boardMutationValid(req.MutationID) || req.BaseSeq == nil || *req.BaseSeq < 0 || *req.BaseSeq >= 9007199254740991 || !validBoardPlacementRevision(req.ExpectedRev) {
		replyError(w, 400, "invalid placement deletion")
		return
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	id := r.PathValue("placementId")
	digest := boardFingerprint("placement-delete", []any{req, id})
	if done, err := h.boardRetry(w, r, tx, rec.ID, req.MutationID, digest); err != nil {
		h.boardError(w, err)
		return
	} else if done {
		return
	}
	var seq int64
	if err := tx.QueryRowContext(r.Context(), `SELECT seq FROM page_boards WHERE page_id=?`, rec.ID).Scan(&seq); err != nil {
		h.boardError(w, err)
		return
	}
	if seq != *req.BaseSeq {
		replyError(w, 409, "board sequence changed")
		return
	}
	rev, err := h.deleteBoardPlacement(r.Context(), tx, rec.ID, id, req.ExpectedRev, seq+1)
	if err != nil {
		h.boardPlacementError(w, err)
		return
	}
	seq++
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_boards SET seq=? WHERE page_id=?`, seq, rec.ID); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.boardReceipt(r, tx, rec.ID, req.MutationID, digest, seq, id); err != nil {
		h.boardError(w, err)
		return
	}
	revisions := map[string]int64{id: rev}
	if err := recordBoardPlacementRevisions(r.Context(), tx, rec.ID, req.MutationID, revisions); err != nil {
		h.boardError(w, err)
		return
	}
	undoExpiry, err := boardReceiptUndoExpiry(r.Context(), tx, rec.ID, req.MutationID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()), "page.updated", map[string]any{"page_id": rec.ID})
	writeJSON(w, 200, map[string]any{"seq": seq, "result_seq": seq, "placement_id": id, "placement_revs": revisions, "element_revs": map[string]int64{}, "undo_expires_at": undoExpiry})
}
