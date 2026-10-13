package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) boardPage(w http.ResponseWriter, r *http.Request, write bool) (*pageRecord, bool) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	authority, delegated := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if user == nil || (!delegated && (agent || projectAgentFrom(r.Context()) != nil)) {
		replyError(w, 401, "human authentication required")
		return nil, false
	}
	ws := WorkspaceIDFromContext(r.Context())
	var role string
	if h.pageAuthReader(r.Context()).QueryRowContext(r.Context(), `SELECT wm.role FROM workspace_members wm JOIN users human ON human.id=wm.user_id AND human.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=?`, ws, user.ID).Scan(&role) != nil {
		replyError(w, 403, "workspace membership required")
		return nil, false
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, role))
	rec, err := h.loadPage(r.Context(), ws, r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load board page", err)
		return nil, false
	}
	viewer, err := h.loadViewer(r.Context(), ws, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load board viewer", err)
		return nil, false
	}
	viewer.Role = role
	visible, err := h.canSeePage(r.Context(), ws, rec, nil, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "authorize board", err)
		return nil, false
	}
	if !visible {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if write && !h.mayEditSpec(r.Context(), ws, user.ID, role, rec) {
		replyError(w, 403, "page write permission required")
		return nil, false
	}
	if write && (rec.OwnerUserID == "" || rec.OwnerCrewID != "") {
		replyError(w, 409, "boards require a human-owned page")
		return nil, false
	}
	if delegated {
		if authority.HumanUserID != user.ID || authority.WorkspaceID != ws {
			replyError(w, 403, "board principal mismatch")
			return nil, false
		}
		if err := h.checkBoardRun(r.Context(), rec, authority); err != nil {
			replyError(w, 403, err.Error())
			return nil, false
		}
	}
	return rec, true
}
func boardDecode(w http.ResponseWriter, r *http.Request, target any) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pages.MaxBoardBatchBytes))
	if err != nil {
		replyError(w, 413, "board payload exceeds limit")
		return nil, false
	}
	if err := checkBoardJSON(raw); err != nil {
		replyError(w, 400, "invalid board JSON")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(target); err != nil || dec.Decode(new(any)) != io.EOF {
		replyError(w, 400, "invalid board JSON")
		return nil, false
	}
	return raw, true
}
func boardMutationValid(id string) bool {
	return len(id) > 0 && len(id) <= 128 && strings.TrimSpace(id) == id
}
func boardFingerprint(kind string, v any) string {
	raw, _ := json.Marshal(v)
	digest := sha256.Sum256(append([]byte(kind+"\n"), raw...))
	return hex.EncodeToString(digest[:])
}
func (h *PageHandler) boardTransaction(w http.ResponseWriter, r *http.Request) (*sql.Tx, *http.Request, *pageRecord, bool) {
	rec, ok := h.boardPage(w, r, true)
	if !ok {
		return nil, r, nil, false
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin board mutation", err)
		return nil, r, nil, false
	}
	fail := func() { _ = tx.Rollback() }
	if _, err = tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=?`, rec.ID); err != nil {
		fail()
		replyInternalError(w, h.logger, "fence board mutation", err)
		return nil, r, nil, false
	}
	var role string
	if tx.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&role) != nil {
		fail()
		replyError(w, 403, "workspace membership required")
		return nil, r, nil, false
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, ctxRole, role)
	r = r.WithContext(ctx)
	rec, ok = h.boardPage(w, r, true)
	if !ok {
		fail()
		return nil, r, nil, false
	}
	return tx, r, rec, true
}
func (h *PageHandler) boardRetry(w http.ResponseWriter, r *http.Request, tx *sql.Tx, pageID, id, fingerprint string) (bool, error) {
	var author, digest, placement, authorAgent, authorRun, placementRevs string
	var result, seq int64
	var elements, created string
	err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(author_user_id,''),fingerprint,result_seq,placement_id,COALESCE(author_agent_id,''),COALESCE(author_run_id,''),placement_revs_json,element_revs_json,created_at FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?`, pageID, id).Scan(&author, &digest, &result, &placement, &authorAgent, &authorRun, &placementRevs, &elements, &created)
	if errors.Is(err, sql.ErrNoRows) {
		expired, e := boardHistoryExpired(r.Context(), tx, pageID, id)
		if e != nil {
			return false, e
		}
		if expired {
			return false, errBoardHistoryExpired
		}
		return false, h.compactBoardHistory(r.Context(), tx, pageID)
	}
	if err != nil {
		return false, err
	}
	if author != UserFromContext(r.Context()).ID || digest != fingerprint || authorAgent != artifactAgentID(r) || authorRun != artifactRunID(r) {
		replyError(w, 409, "mutation_id was already used for a different request")
		return true, nil
	}
	if err = tx.QueryRowContext(r.Context(), `SELECT seq FROM page_boards WHERE page_id=?`, pageID).Scan(&seq); err != nil {
		return false, err
	}
	var revisions map[string]int64
	if err := json.Unmarshal([]byte(placementRevs), &revisions); err != nil {
		return false, err
	}
	var elementRevs map[string]int64
	if err = json.Unmarshal([]byte(elements), &elementRevs); err != nil {
		return false, err
	}
	writeJSON(w, 200, map[string]any{"seq": seq, "result_seq": result, "placement_id": placement, "placement_revs": revisions, "element_revs": elementRevs, "undo_expires_at": boardUndoExpiry(created)})
	return true, nil
}
func (h *PageHandler) boardReceipt(r *http.Request, tx *sql.Tx, pageID, id, fingerprint string, seq int64, placement string) error {
	if expired, err := boardHistoryExpired(r.Context(), tx, pageID, id); err != nil {
		return err
	} else if expired {
		return errBoardHistoryExpired
	}
	if err := h.compactBoardHistory(r.Context(), tx, pageID); err != nil {
		return err
	}
	var count, hour int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(created_at>=?),0) FROM page_board_mutations WHERE board_page_id=?`, tsformat.Format(h.evaluator().Now().Add(-time.Hour)), pageID).Scan(&count, &hour); err != nil {
		return err
	}
	if count >= 10000 || hour >= 200 {
		return errBoardQuota
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=? WHERE id=?`, tsformat.Format(h.evaluator().Now()), pageID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_board_write_audit(board_page_id,mutation_id,actor_user_id,actor_agent_id,actor_run_id,result_seq,request_digest,created_at) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?)`, pageID, id, UserFromContext(r.Context()).ID, artifactAgentID(r), artifactRunID(r), seq, fingerprint, tsformat.Format(h.evaluator().Now())); err != nil {
		return err
	}
	_, err := tx.ExecContext(r.Context(), `INSERT INTO page_board_mutations(board_page_id,mutation_id,author_user_id,fingerprint,result_seq,placement_id,created_at,author_agent_id,author_run_id) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))`, pageID, id, UserFromContext(r.Context()).ID, fingerprint, seq, placement, tsformat.Format(h.evaluator().Now()), artifactAgentID(r), artifactRunID(r))
	return err
}

var errBoardQuota = errors.New("board mutation quota reached")

func (h *PageHandler) boardError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBoardHistoryExpired) {
		artifactCommentError(w, 409, "history_expired", err.Error())
		return
	}
	if errors.Is(err, errBoardQuota) {
		replyError(w, 429, err.Error())
	} else {
		replyInternalError(w, h.logger, "board mutation", err)
	}
}
func (h *PageHandler) InitBoard(w http.ResponseWriter, r *http.Request) {
	if artifactAgentID(r) != "" {
		replyError(w, 403, "human board creation required")
		return
	}
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	var req struct {
		MutationID string `json:"mutation_id"`
	}
	if _, ok := boardDecode(w, r, &req); !ok {
		return
	}
	if !boardMutationValid(req.MutationID) {
		replyError(w, 400, "mutation_id required")
		return
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	fingerprint := boardFingerprint("init", req)
	if rec.HasBoard {
		var author, digest string
		err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(author_user_id,''),fingerprint FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?`, rec.ID, req.MutationID).Scan(&author, &digest)
		if err != nil || author != UserFromContext(r.Context()).ID || digest != fingerprint {
			replyError(w, 409, "page is already a board")
			return
		}
		h.writeBoard(w, r, rec, tx)
		return
	}
	var panels, projects int
	if err := tx.QueryRowContext(r.Context(), `SELECT (SELECT COUNT(*) FROM page_panels WHERE page_id=?),(SELECT COUNT(*) FROM page_project_drafts WHERE page_id=?)+(SELECT COUNT(*) FROM page_project_live WHERE page_id=?)`, rec.ID, rec.ID, rec.ID).Scan(&panels, &projects); err != nil {
		h.boardError(w, err)
		return
	}
	if rec.HasArtifact || panels != 0 || projects != 0 {
		replyError(w, 409, "board requires an empty page")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,?)`, rec.ID, pages.BoardFormat); err != nil {
		h.boardError(w, err)
		return
	}
	if err := h.boardReceipt(r, tx, rec.ID, req.MutationID, fingerprint, 0, ""); err != nil {
		h.boardError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	rec.HasBoard = true
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()), "page.updated", map[string]any{"page_id": rec.ID})
	h.GetBoard(w, r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, (*sql.Tx)(nil))))
}
func (h *PageHandler) GetBoard(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.boardPage(w, r, false)
	if !ok {
		return
	}
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	rec, ok = h.boardPage(w, r, false)
	if !ok {
		return
	}
	h.writeBoard(w, r, rec, tx)
}
func loadBoardScene(ctx context.Context, q pageAuthQueryer, pageID string) (pages.BoardScene, error) {
	s := pages.BoardScene{Records: map[string]pages.BoardRecord{}}
	if err := q.QueryRowContext(ctx, `SELECT seq FROM page_boards WHERE page_id=?`, pageID).Scan(&s.Seq); err != nil {
		return s, err
	}
	rows, err := q.QueryContext(ctx, `SELECT element_id,element_rev,element_json FROM page_board_elements WHERE board_page_id=? ORDER BY element_id`, pageID)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var rev int64
		var raw sql.NullString
		if err = rows.Scan(&id, &rev, &raw); err != nil {
			return s, err
		}
		s.Records[id] = pages.BoardRecord{ElementRev: rev, Deleted: !raw.Valid}
		if raw.Valid {
			record := s.Records[id]
			record.Element = json.RawMessage(raw.String)
			s.Records[id] = record
		}
	}
	return s, rows.Err()
}
func (h *PageHandler) writeBoard(w http.ResponseWriter, r *http.Request, rec *pageRecord, q pageAuthQueryer) {
	s, err := loadBoardScene(r.Context(), q, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if err := pages.ValidateBoardScene(s); err != nil {
		h.boardError(w, err)
		return
	}
	authors, err := loadBoardAuthors(r.Context(), q, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	records := make([]map[string]any, 0, len(s.Records))
	for id, record := range s.Records {
		var element any
		if !record.Deleted {
			element = record.Element
		}
		records = append(records, map[string]any{"element_id": id, "element_rev": record.ElementRev, "element": element, "deleted": record.Deleted, "author_user_id": authors[id].UserID, "author_agent_id": authors[id].AgentID, "author_run_id": authors[id].RunID})
	}
	sort.Slice(records, func(i, j int) bool { return records[i]["element_id"].(string) < records[j]["element_id"].(string) })
	placements, err := h.loadBoardPlacements(r, rec.ID, q)
	if err != nil {
		h.boardError(w, err)
		return
	}
	var liveRole string
	if err := q.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&liveRole); err != nil {
		replyError(w, 403, "workspace membership required")
		return
	}
	tombstones, err := loadBoardPlacementTombstones(r.Context(), q, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	response := map[string]any{"format": pages.BoardFormat, "seq": s.Seq, "records": records, "placements": placements, "placement_tombstones": tombstones, "write_scope": boardWriteScope(r), "can_write": h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID, liveRole, rec)}
	if authority, ok := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority); ok && artifactAgentID(r) != "" {
		permissions, err := h.boardAgentElementPermissions(r.Context(), rec, authority)
		if err != nil {
			h.boardError(w, err)
			return
		}
		response["element_permissions"] = permissions
	}
	writeJSON(w, 200, response)
}
func (h *PageHandler) ApplyBoardBatch(w http.ResponseWriter, r *http.Request) {
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	var req struct {
		MutationID          string `json:"mutation_id"`
		ElementPermissionID string `json:"element_permission_id,omitempty"`
		pages.BoardBatch
		PlacementUpdates  []boardPlacementUpdate  `json:"placement_updates"`
		PlacementDeletes  []boardPlacementDelete  `json:"placement_deletes"`
		PlacementRestores []boardPlacementRestore `json:"placement_restores,omitempty"`
		PlacementModes    []boardPlacementMode    `json:"placement_modes,omitempty"`
	}
	raw, ok := boardDecode(w, r, &req)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "mutation_id")
	delete(fields, "element_permission_id")
	var base *int64
	if err := json.Unmarshal(fields["base_seq"], &base); err != nil || base == nil || *base < 0 || *base >= 9007199254740991 {
		replyError(w, 400, "explicit board sequence required")
		return
	}
	delete(fields, "placement_updates")
	delete(fields, "placement_deletes")
	delete(fields, "placement_restores")
	delete(fields, "placement_modes")
	batchRaw, _ := json.Marshal(fields)
	batch := req.BoardBatch
	batch.BaseSeq = *base
	drawingCount := len(batch.Upserts) + len(batch.Deletes)
	var err error
	if drawingCount > 0 {
		batch, err = pages.DecodeBoardBatch(batchRaw)
	}
	count := drawingCount + len(req.PlacementUpdates) + len(req.PlacementDeletes) + len(req.PlacementRestores) + len(req.PlacementModes)
	if err != nil || !boardMutationValid(req.MutationID) || count < 1 || count > pages.MaxBoardBatchOperations {
		replyError(w, 400, "invalid board batch")
		return
	}
	seen := map[string]bool{}
	for _, placement := range req.PlacementUpdates {
		if !boardMutationValid(placement.ID) || !placement.boardPlacementGeometry.valid() || !validBoardPlacementRevision(placement.ExpectedRev) || seen[placement.ID] {
			replyError(w, 400, "invalid placement update")
			return
		}
		seen[placement.ID] = true
	}
	for _, placement := range req.PlacementDeletes {
		if !boardMutationValid(placement.ID) || !validBoardPlacementRevision(placement.ExpectedRev) || seen[placement.ID] {
			replyError(w, 400, "invalid placement deletion")
			return
		}
		seen[placement.ID] = true
	}
	for _, placement := range req.PlacementRestores {
		if !boardMutationValid(placement.ID) || placement.ExpectedRev == nil || !validBoardPlacementRevision(placement.ExpectedRev) || seen[placement.ID] {
			replyError(w, 400, "invalid placement restore")
			return
		}
		seen[placement.ID] = true
	}
	for _, placement := range req.PlacementModes {
		if !validBoardPlacementMode(placement) || seen[placement.ID] {
			replyError(w, 400, "invalid placement mode operation")
			return
		}
		seen[placement.ID] = true
	}
	if artifactAgentID(r) != "" && (len(req.PlacementUpdates) > 0 || len(req.PlacementDeletes) > 0 || len(req.PlacementRestores) > 0 || len(req.PlacementModes) > 0) {
		replyError(w, 403, "agent placement edits are not delegated")
		return
	}
	req.BoardBatch = batch
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	if req.ElementPermissionID != "" && artifactAgentID(r) == "" {
		replyError(w, 400, "element permission is only valid for the delegated agent batch")
		return
	}
	digest := boardFingerprint("batch", req)
	if done, err := h.boardRetry(w, r, tx, rec.ID, req.MutationID, digest); err != nil {
		h.boardError(w, err)
		return
	} else if done {
		return
	}
	current, err := loadBoardScene(r.Context(), tx, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	var next pages.BoardScene
	if drawingCount > 0 {
		next, err = pages.ApplyBoardBatch(current, batch)
	} else {
		if current.Seq != batch.BaseSeq {
			err = pages.ErrBoardConflict
		} else if err = pages.ValidateBoardScene(current); err == nil {
			next = current
			next.Seq++
		}
	}
	if err != nil {
		if errors.Is(err, pages.ErrBoardConflict) {
			replyError(w, 409, err.Error())
		} else if strings.Contains(err.Error(), "record limit") {
			h.boardHistoryCapacity(w, r, tx, rec.ID)
		} else {
			replyError(w, 400, err.Error())
		}
		return
	}
	preservedHumanAuthors := map[string]boardElementAuthor{}
	if agentID := artifactAgentID(r); agentID != "" {
		authors, err := loadBoardAuthors(r.Context(), tx, rec.ID)
		if err != nil {
			h.boardError(w, err)
			return
		}
		authority, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
		if authority == nil {
			replyError(w, 403, "verified board run required")
			return
		}
		preservedHumanAuthors, err = h.useBoardElementPermission(r.Context(), tx, rec, req.ElementPermissionID, req.MutationID, current, next, batch, authors, authority)
		if err != nil {
			replyError(w, 403, err.Error())
			return
		}
	}
	if err := h.validateBoardFileReferences(r.Context(), tx, rec.ID, next); err != nil {
		replyError(w, 400, err.Error())
		return
	}
	for id, record := range next.Records {
		old, exists := current.Records[id]
		if exists && old.ElementRev == record.ElementRev {
			continue
		}
		var raw any
		if !record.Deleted {
			raw = string(record.Element)
		}
		var deletedAt any
		if record.Deleted {
			deletedAt = tsformat.Format(h.evaluator().Now())
		}
		var authorUser any = UserFromContext(r.Context()).ID
		authorAgent, authorRun := artifactAgentID(r), artifactRunID(r)
		if original, ok := preservedHumanAuthors[id]; ok {
			authorUser = original.UserID
			if original.UserID == "" {
				authorUser = nil
			}
			authorAgent = original.AgentID
			authorRun = original.RunID
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq,author_agent_id,author_run_id,deleted_at) VALUES(?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?) ON CONFLICT(board_page_id,element_id) DO UPDATE SET element_rev=excluded.element_rev,element_json=excluded.element_json,author_user_id=excluded.author_user_id,updated_seq=excluded.updated_seq,author_agent_id=excluded.author_agent_id,author_run_id=excluded.author_run_id,deleted_at=excluded.deleted_at`, rec.ID, id, record.ElementRev, raw, authorUser, next.Seq, authorAgent, authorRun, deletedAt); err != nil {
			h.boardError(w, err)
			return
		}
	}
	revisions := map[string]int64{}
	for _, placement := range req.PlacementUpdates {
		rev, err := h.updateBoardPlacement(r.Context(), tx, rec.ID, placement.ID, placement.ExpectedRev, placement.boardPlacementGeometry, next.Seq)
		if err != nil {
			h.boardPlacementError(w, err)
			return
		}
		revisions[placement.ID] = rev
	}
	for _, placement := range req.PlacementDeletes {
		rev, err := h.deleteBoardPlacement(r.Context(), tx, rec.ID, placement.ID, placement.ExpectedRev, next.Seq)
		if err != nil {
			h.boardPlacementError(w, err)
			return
		}
		revisions[placement.ID] = rev
	}
	for _, placement := range req.PlacementRestores {
		rev, err := h.restoreBoardPlacement(r.Context(), tx, rec.ID, placement.ID, placement.ExpectedRev, next.Seq)
		if err != nil {
			h.boardPlacementError(w, err)
			return
		}
		revisions[placement.ID] = rev
	}
	for _, placement := range req.PlacementModes {
		rev, err := h.updateBoardPlacementMode(r.Context(), tx, rec.ID, placement, next.Seq, req.MutationID)
		if err != nil {
			if errors.Is(err, errBoardSourceHistoryCapacity) {
				h.boardHistoryCapacity(w, r, tx, rec.ID, true)
			} else {
				h.boardPlacementError(w, err)
			}
			return
		}
		revisions[placement.ID] = rev
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_boards SET seq=? WHERE page_id=?`, next.Seq, rec.ID); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.boardReceipt(r, tx, rec.ID, req.MutationID, digest, next.Seq, ""); err != nil {
		h.boardError(w, err)
		return
	}
	if err := recordBoardPlacementRevisions(r.Context(), tx, rec.ID, req.MutationID, revisions); err != nil {
		h.boardError(w, err)
		return
	}
	elementRevs := map[string]int64{}
	for id, record := range next.Records {
		old, present := current.Records[id]
		if !present || old.ElementRev != record.ElementRev {
			elementRevs[id] = record.ElementRev
		}
	}
	if err = h.recordBoardElementRevisions(r.Context(), tx, rec.ID, req.MutationID, elementRevs); err != nil {
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
	writeJSON(w, 201, map[string]any{"seq": next.Seq, "result_seq": next.Seq, "placement_revs": revisions, "element_revs": elementRevs, "undo_expires_at": undoExpiry})
}

func boardWriteScope(r *http.Request) string {
	if artifactAgentID(r) != "" {
		return "own-elements"
	}
	return "all-elements"
}
