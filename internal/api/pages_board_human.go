package api

// Empty Board initialization reuses the pinned Board foundation's profile,
// sequence and mutation digest conventions. Drawing/agent writes are separate.
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
	"strings"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const emptyBoardFormat = "crewship-board-excalidraw-0.18.1-v1"

// boardHumanPage runs through the operation's authority reader, after the
// writer fence for initialization. Middleware role/auth snapshots do not grant
// authority. Only explicit private human Pages are supported by this slice.
func (h *PageHandler) boardHumanPage(r *http.Request, write bool) (*pageRecord, error) {
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil {
		return nil, sql.ErrNoRows
	}
	scope := "pages:read"
	if write {
		scope = "pages:write"
	}
	if validateContentHumanCredential(r, h.pageAuthReader(r.Context()), scope) != nil {
		return nil, sql.ErrNoRows
	}
	role, err := h.liveContentHumanRole(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
	if err != nil {
		return nil, sql.ErrNoRows
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, role))
	rec, err := h.loadPage(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	if !rec.PrivateAudience || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || !privatePageViewer(rec, &pageViewer{UserID: user.ID, Role: role}) {
		return nil, sql.ErrNoRows
	}
	// The workspace OWNER's override is audited read authority, not authoring.
	if write && rec.OwnerUserID != user.ID {
		return nil, sql.ErrNoRows
	}
	return rec, nil
}

func (h *PageHandler) boardHumanFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "board not found")
		return
	}
	replyInternalError(w, h.logger, "Board authority", err)
}

// InitBoard initializes an existing private Page exactly once. The stable
// mutation ID is required for lost-response retries; a different ID conflicts.
func (h *PageHandler) InitBoard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if reference := r.PathValue("slug"); !strings.HasPrefix(reference, "@") || len(reference) < 2 {
		replyError(w, http.StatusBadRequest, "Board initialization requires the original @PageID")
		return
	}
	if _, err := h.boardHumanPage(r, true); err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	var request struct {
		MutationID string `json:"mutation_id"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		replyError(w, 413, "Board initialization exceeds limit")
		return
	}
	// Match exact historical wire shape and reject duplicate/unknown keys.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || !validBoardInitJSON(raw) || len(request.MutationID) == 0 || len(request.MutationID) > 128 || strings.TrimSpace(request.MutationID) != request.MutationID {
		replyError(w, 400, "one mutation_id is required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	defer tx.Rollback()
	// SQLite writer reservation orders membership/token/custody revocation with
	// this attachment. The query is scoped and never changes Page identity.
	if _, err = tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE workspace_id=? AND id=?`, WorkspaceIDFromContext(r.Context()), strings.TrimPrefix(r.PathValue("slug"), "@")); err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, err := h.boardHumanPage(r, true)
	if err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	fingerprintRaw, _ := json.Marshal(request)
	digest := sha256.Sum256(append([]byte("init\n"), fingerprintRaw...))
	fingerprint := hex.EncodeToString(digest[:])
	var present bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM page_boards WHERE page_id=?)`, rec.ID).Scan(&present); err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	if present {
		var author, stored string
		err = tx.QueryRowContext(r.Context(), `SELECT author_user_id,fingerprint FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?`, rec.ID, request.MutationID).Scan(&author, &stored)
		if err != nil || author != UserFromContext(r.Context()).ID || stored != fingerprint {
			replyError(w, 409, "page is already a Board")
			return
		}
	} else {
		var occupied bool
		err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM documents WHERE page_id=?) OR EXISTS(SELECT 1 FROM slides_heads WHERE page_id=?) OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=?) OR EXISTS(SELECT 1 FROM page_project_drafts WHERE page_id=?) OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=?)`, rec.ID, rec.ID, rec.ID, rec.ID, rec.ID).Scan(&occupied)
		if err != nil {
			h.boardHumanFailure(w, err)
			return
		}
		if occupied {
			replyError(w, 409, "Board requires an empty private Page")
			return
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,?)`, rec.ID, emptyBoardFormat); err != nil {
			h.boardHumanFailure(w, err)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_mutations(board_page_id,mutation_id,author_user_id,fingerprint,result_seq,created_at) VALUES(?,?,?,?,0,?)`, rec.ID, request.MutationID, rec.OwnerUserID, fingerprint, tsformat.Format(h.evaluator().Now())); err != nil {
			h.boardHumanFailure(w, err)
			return
		}
	}
	// Validate before commit, including exact retries of unsupported newer state.
	if err = h.validateEmptyBoard(r, rec.ID); err != nil {
		replyError(w, 409, "Board profile or revision is not supported by this reader")
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	if !present {
		broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()), "page.updated", map[string]any{})
	}
	writeEmptyBoard(w, rec)
}

func validBoardInitJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	fields := 0
	for d.More() {
		key, err := d.Token()
		if err != nil || key != "mutation_id" {
			return false
		}
		var value string
		if d.Decode(&value) != nil {
			return false
		}
		fields++
	}
	return fields == 1
}

func (h *PageHandler) validateEmptyBoard(r *http.Request, pageID string) error {
	var seq int64
	var format string
	if err := h.pageAuthReader(r.Context()).QueryRowContext(r.Context(), `SELECT seq,format FROM page_boards WHERE page_id=?`, pageID).Scan(&seq, &format); err != nil {
		return err
	}
	if seq != 0 || format != emptyBoardFormat {
		return errors.New("unsupported Board state")
	}
	return nil
}

// GetBoard releases a confirmed empty snapshot only after live authorization
// and the workspace OWNER audit have succeeded. Unsupported state is explicit.
func (h *PageHandler) GetBoard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	defer tx.Rollback()
	scoped := r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, err := h.boardHumanPage(scoped, false)
	if err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	if err = h.validateEmptyBoard(scoped, rec.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.boardHumanFailure(w, err)
		} else {
			replyError(w, 409, "Board profile or revision is not supported by this reader")
		}
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardHumanFailure(w, err)
		return
	}
	if !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	writeEmptyBoard(w, rec)
}

func writeEmptyBoard(w http.ResponseWriter, rec *pageRecord) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, 200, map[string]any{"page_id": rec.ID, "format": emptyBoardFormat, "seq": 0, "records": []any{}, "placements": []any{}, "placement_tombstones": []any{}, "can_write": false, "write_scope": "unavailable"})
}
