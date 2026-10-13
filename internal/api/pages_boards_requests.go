package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const boardRequestSnapshotLimit = 256 << 10
const boardMermaidDialect = "flowchart-v1"
const boardMermaidConverter = "2.2.2"

type boardRequestCreate struct {
	PreviewPNG           []byte                 `json:"preview_png,omitempty"`
	Kind                 string                 `json:"kind"`
	ChatID               string                 `json:"chat_id"`
	Instruction          string                 `json:"instruction"`
	BaseSeq              *int64                 `json:"base_seq"`
	SubmissionID         string                 `json:"submission_id"`
	SelectedElementIDs   []string               `json:"selected_element_ids"`
	SelectedPlacementIDs []string               `json:"selected_placement_ids"`
	Target               boardPlacementGeometry `json:"target"`
	Dialect              string                 `json:"dialect"`
	ConverterVersion     string                 `json:"converter_version"`
}
type boardRequestSnapshot struct {
	Elements   []json.RawMessage `json:"elements"`
	Placements []map[string]any  `json:"placements"`
}

func decodeBoardRequest(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		replyError(w, 413, "board request exceeds limit")
		return false
	}
	if err = checkBoardJSON(raw); err != nil {
		replyError(w, 400, "invalid board request JSON")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "one board request with known fields required")
		return false
	}
	return true
}
func boardRequestSelectionValid(ids, placements []string) bool {
	if len(ids)+len(placements) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !boardMutationValid(id) || seen["e:"+id] {
			return false
		}
		seen["e:"+id] = true
	}
	for _, id := range placements {
		if !boardMutationValid(id) || seen["p:"+id] {
			return false
		}
		seen["p:"+id] = true
	}
	return true
}
func (h *PageHandler) CreateBoardRequest(w http.ResponseWriter, r *http.Request) {
	release, ok := acquireBoardWrite(w, r)
	if !ok {
		return
	}
	defer release()
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 401, "human authentication required")
		return
	}
	preflight, allowed := h.boardPage(w, r, true)
	if !allowed {
		return
	}
	if !preflight.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	var req boardRequestCreate
	if !decodeBoardRequest(w, r, &req, boardRequestBodyLimit) {
		return
	}
	if (req.Kind != "diagram" && req.Kind != "artifact") || req.ChatID == "" || req.BaseSeq == nil || *req.BaseSeq < 0 || *req.BaseSeq >= 9007199254740991 || !boardMutationValid(req.SubmissionID) || !utf8.ValidString(req.Instruction) || strings.TrimSpace(req.Instruction) == "" || len(req.Instruction) > 4096 || !req.Target.valid() || !boardRequestSelectionValid(req.SelectedElementIDs, req.SelectedPlacementIDs) {
		replyError(w, 400, "invalid board request")
		return
	}
	if req.Kind == "diagram" && (req.Dialect != boardMermaidDialect || req.ConverterVersion != boardMermaidConverter) {
		replyError(w, 422, "supported Mermaid flowchart profile and converter version required")
		return
	}
	if req.Kind == "artifact" && len(req.SelectedElementIDs)+len(req.SelectedPlacementIDs) == 0 {
		replyError(w, 422, "select board elements or placements first")
		return
	}
	if req.Kind == "artifact" && ((req.Dialect != "" && req.Dialect != boardMermaidDialect) || (req.ConverterVersion != "" && req.ConverterVersion != boardMermaidConverter)) {
		replyError(w, 422, "unsupported optional diagram profile")
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "human authentication required")
		return
	}
	receipt, err := chataudience.CaptureTrusted(r.Context(), h.db, req.ChatID, user.ID, nil)
	if err != nil {
		replyInternalError(w, h.logger, "authorize board request chat", err)
		return
	}
	if receipt == nil || receipt.WorkspaceID != WorkspaceIDFromContext(r.Context()) || !h.artifactRequestChatPrivate(r.Context(), req.ChatID, user.ID) {
		replyError(w, 403, "use your eligible private chat without read shares")
		return
	}
	digest := boardFingerprint("board-request", req)
	preview, unlock, prepared := h.prepareBoardRequestPreview(w, r, req.PreviewPNG)
	if !prepared {
		return
	}
	defer unlock()
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}

	if req.Kind == "artifact" && !h.artifactPrincipalMayCreate(r.Context(), receipt.WorkspaceID, user.ID) {
		replyError(w, 403, "page.create authority required")
		return
	}
	if req.Kind == "artifact" && !h.artifactAgentIDPolicyAllows(r.Context(), receipt.WorkspaceID, receipt.AgentID) {
		artifactCommentError(w, 403, "artifact_policy_disabled", "Agent artifact creation is disabled for this crew")
		return
	}
	if h.artifactDispatch == nil && h.hub == nil {
		replyError(w, 503, "chat runtime unavailable")
		return
	}
	if !h.artifactRequestChatPrivate(r.Context(), req.ChatID, user.ID) || !boardReceiptCurrent(r.Context(), tx, receipt) {
		replyError(w, 403, "chat authority changed")
		return
	}
	var prior, old string
	err = tx.QueryRowContext(r.Context(), `SELECT id,fingerprint FROM page_board_requests WHERE board_page_id=? AND human_user_id=? AND submission_id=?`, rec.ID, user.ID, req.SubmissionID).Scan(&prior, &old)
	if err == nil {
		if old != digest {
			replyError(w, 409, "submission_id was already used for another request")
			return
		}
		writeJSON(w, 202, map[string]any{"id": prior})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.boardError(w, err)
		return
	}
	scene, err := loadBoardScene(r.Context(), tx, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if scene.Seq != *req.BaseSeq {
		replyError(w, 409, "board sequence changed; review the selection again")
		return
	}
	if err = pages.ValidateBoardScene(scene); err != nil {
		h.boardError(w, err)
		return
	}
	var active, hour, total, workspaceTotal int
	if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(status IN ('queued','running')),0),COALESCE(SUM(created_at>=?),0),COUNT(*) FROM page_board_requests WHERE board_page_id=?`, tsformat.Format(time.Now().Add(-time.Hour)), rec.ID).Scan(&active, &hour, &total); err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_board_requests WHERE workspace_id=?`, receipt.WorkspaceID).Scan(&workspaceTotal); err != nil {
		h.boardError(w, err)
		return
	}
	if active >= 3 || hour >= 30 || total >= 500 || workspaceTotal >= 2000 {
		replyError(w, 429, "board request quota reached")
		return
	}
	snapshot, files, sources, err := h.snapshotBoardRequest(r.Context(), tx, rec, scene, req, receipt.AgentID)
	if err != nil {
		replyError(w, 422, "selected input is unavailable or exceeds the snapshot limit")
		return
	}
	snapshotJSON, _ := json.Marshal(snapshot)
	targetJSON, _ := json.Marshal(req.Target)
	idsJSON, _ := json.Marshal(req.SelectedElementIDs)
	pinsJSON, _ := json.Marshal(req.SelectedPlacementIDs)
	receiptJSON, _ := json.Marshal(receipt)
	id := generateCUID()
	now := tsformat.Format(time.Now())
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_requests(id,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'queued',?,?)`, id, receipt.WorkspaceID, rec.ID, rec.ID, user.ID, receipt.AgentID, req.ChatID, req.Kind, *req.BaseSeq, req.SubmissionID, digest, req.Instruction, string(idsJSON), string(pinsJSON), string(snapshotJSON), string(targetJSON), req.Dialect, req.ConverterVersion, string(receiptJSON), now, now); err != nil {
		h.boardError(w, err)
		return
	}
	if err = h.saveBoardRequestPreview(r.Context(), tx, id, receipt.WorkspaceID, preview); err != nil {
		if errors.Is(err, errBoardPreviewQuota) {
			replyError(w, 409, err.Error())
		} else {
			h.boardError(w, err)
		}
		return
	}
	for _, file := range files {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_request_files(request_id,file_id) VALUES(?,?)`, id, file); err != nil {
			h.boardError(w, err)
			return
		}
	}
	for _, source := range sources {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_request_sources(id,request_id,placement_id,source_page_id,source_revision) VALUES(?,?,?,?,?)`, generateCUID(), id, source.PlacementID, source.PageID, source.Revision); err != nil {
			h.boardError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	broadcastWorkspaceEvent(h.hub, receipt.WorkspaceID, "page.updated", map[string]any{"page_id": rec.ID})
	writeJSON(w, 202, map[string]any{"id": id})
}

type boardRequestSource struct {
	PlacementID, PageID string
	Revision            int64
}

func (h *PageHandler) snapshotBoardRequest(ctx context.Context, tx *sql.Tx, rec *pageRecord, scene pages.BoardScene, req boardRequestCreate, agentID string) (boardRequestSnapshot, []string, []boardRequestSource, error) {
	snapshot := boardRequestSnapshot{Elements: []json.RawMessage{}, Placements: []map[string]any{}}
	files := []string{}
	sources := []boardRequestSource{}
	selected := map[string]bool{}
	for _, id := range req.SelectedElementIDs {
		selected[id] = true
	}
	fileSet := map[string]bool{}
	ids := append([]string(nil), req.SelectedElementIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		record, present := scene.Records[id]
		if !present || record.Deleted {
			return snapshot, nil, nil, sql.ErrNoRows
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.Element, &fields); err != nil {
			return snapshot, nil, nil, err
		}
		// References to unselected elements cannot expand the immutable selection.
		for _, key := range []string{"frameId", "containerId"} {
			var target *string
			_ = json.Unmarshal(fields[key], &target)
			if target != nil && !selected[*target] {
				fields[key] = json.RawMessage("null")
			}
		}
		for _, key := range []string{"startBinding", "endBinding"} {
			var binding *pages.BoardBinding
			_ = json.Unmarshal(fields[key], &binding)
			if binding != nil && !selected[binding.ElementID] {
				fields[key] = json.RawMessage("null")
			}
		}
		if raw, present := fields["boundElements"]; present {
			var bound []pages.BoardBoundElement
			_ = json.Unmarshal(raw, &bound)
			kept := []pages.BoardBoundElement{}
			for _, item := range bound {
				if selected[item.ID] {
					kept = append(kept, item)
				}
			}
			fields["boundElements"], _ = json.Marshal(kept)
		}
		raw, _ := json.Marshal(fields)
		element, err := pages.ValidateBoardElement(raw)
		if err != nil {
			return snapshot, nil, nil, err
		}
		snapshot.Elements = append(snapshot.Elements, raw)
		if element.FileID != nil && !fileSet[*element.FileID] {
			var exists int
			if tx.QueryRowContext(ctx, `SELECT 1 FROM page_board_files WHERE board_page_id=? AND id=?`, rec.ID, *element.FileID).Scan(&exists) != nil {
				return snapshot, nil, nil, sql.ErrNoRows
			}
			fileSet[*element.FileID] = true
			files = append(files, *element.FileID)
		}
	}
	for _, id := range req.SelectedPlacementIDs {
		var source string
		var revision int64
		var x, y, width, height float64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(source_page_id,''),CASE mode WHEN 'follow_latest' THEN COALESCE((SELECT head_rev FROM page_artifacts WHERE page_id=source_page_id),0) ELSE COALESCE(source_revision,0) END,x,y,width,height FROM page_board_artifact_placements WHERE board_page_id=? AND id=? AND deleted_at IS NULL`, rec.ID, id).Scan(&source, &revision, &x, &y, &width, &height); err != nil {
			return snapshot, nil, nil, err
		}
		snapshot.Placements = append(snapshot.Placements, map[string]any{"id": id, "x": x, "y": y, "width": width, "height": height})
		if source != "" && h.boardJobSourceVisible(ctx, source, agentID) {
			sources = append(sources, boardRequestSource{PlacementID: id, PageID: source, Revision: revision})
		}
	}
	selectedScene := pages.BoardScene{Seq: scene.Seq, Records: map[string]pages.BoardRecord{}}
	for _, raw := range snapshot.Elements {
		element, err := pages.ValidateBoardElement(raw)
		if err != nil {
			return snapshot, nil, nil, err
		}
		selectedScene.Records[element.ID] = pages.BoardRecord{Element: raw, ElementRev: 1}
	}
	if err := pages.ValidateBoardScene(selectedScene); err != nil {
		return snapshot, nil, nil, err
	}
	raw, _ := json.Marshal(snapshot)
	if len(raw) > boardRequestSnapshotLimit {
		return snapshot, nil, nil, errors.New("snapshot exceeds limit")
	}
	return snapshot, files, sources, nil
}
func boardReceiptCurrent(ctx context.Context, q pageAuthQueryer, receipt *chataudience.Receipt) bool {
	if receipt == nil {
		return false
	}
	var one int
	args := []any{receipt.UserID, receipt.MemberID, receipt.MemberRevision, receipt.ChatID, receipt.WorkspaceID, receipt.AgentID, receipt.ChatGeneration, receipt.ChatRevision, receipt.UserID}
	args = append(args, chataudience.Args(receipt.UserID)...)
	return q.QueryRowContext(ctx, `SELECT 1 FROM chats c JOIN workspace_members m ON m.workspace_id=c.workspace_id AND m.user_id=? AND m.id=? AND m.access_revision=? JOIN users human ON human.id=m.user_id AND human.suspended_at IS NULL JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL WHERE c.id=? AND c.workspace_id=? AND c.agent_id=? AND c.authority_generation=? AND c.authority_revision=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted') AND (`+chataudience.VisibleSQL+`)`, args...).Scan(&one) == nil
}
func (h *PageHandler) boardJobSourceVisible(ctx context.Context, pageID, agentID string) bool {
	user := UserFromContext(ctx)
	if user == nil {
		return false
	}
	workspace := WorkspaceIDFromContext(ctx)
	var slug string
	if h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, pageID, workspace).Scan(&slug) != nil {
		return false
	}
	rec, err := h.loadPage(ctx, workspace, slug)
	if err != nil || !rec.HasArtifact {
		return false
	}
	viewer, err := h.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		return false
	}
	visible, err := h.canSeePage(ctx, workspace, rec, nil, viewer)
	if err != nil || !visible {
		return false
	}
	grants, err := h.agentGrants(ctx, workspace, rec, agentID)
	if err != nil {
		return false
	}
	for _, grant := range grants {
		if reachesPage(grant.Level) {
			return true
		}
	}
	return false
}
