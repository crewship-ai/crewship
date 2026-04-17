package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/cartographer"
	"github.com/crewship-ai/crewship/internal/journal"
)

// CartographerHandler serves the checkpoint/restore/fork API that powers
// the mission timeline page. All writes route through the cartographer
// package so the checkpoint row, state snapshot, and journal emit stay
// in one place. The handler's job is strictly: authenticate, scope to
// the caller's workspace, marshal the body, dispatch, shape the response.
//
// Workspace isolation is enforced at the handler layer by resolving the
// mission (or checkpoint) back to a workspace_id and comparing against
// the caller's context. cartographer.Get / Delete also take a
// workspaceID so we get belt-and-braces scoping inside the package too.
type CartographerHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewCartographerHandler constructs a handler with a no-op journal
// emitter; the router wires the real one via SetJournal after the
// Router's journal option has resolved.
func NewCartographerHandler(db *sql.DB, logger *slog.Logger) *CartographerHandler {
	return &CartographerHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter. nil collapses to the no-op so the
// handler never nil-panics on audit emits.
func (h *CartographerHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		j = noopEmitter{}
		h.journal = j
		return
	}
	h.journal = j
}

// List serves GET /api/v1/missions/{missionId}/checkpoints.
// Returns newest-first checkpoints for the given mission. Limit defaults
// to 50 and is capped at 200 to keep timeline payloads bounded.
func (h *CartographerHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	missionID := r.PathValue("missionId")
	if missionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mission_id required"})
		return
	}
	if _, _, ok := h.resolveMission(w, r, missionID, workspaceID); !ok {
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	rows, err := cartographer.List(r.Context(), h.db, missionID, limit)
	if err != nil {
		h.logger.Error("cartographer list failed", "err", err, "mission_id", missionID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checkpoints": rows,
		"count":       len(rows),
		"mission_id":  missionID,
	})
}

// Create serves POST /api/v1/missions/{missionId}/checkpoints.
// Body: {"label": "..."}. Captures the current mission state + journal
// cursor, writes a checkpoint row, and returns it.
func (h *CartographerHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	missionID := r.PathValue("missionId")
	if missionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mission_id required"})
		return
	}
	_, crewID, ok := h.resolveMission(w, r, missionID, workspaceID)
	if !ok {
		return
	}

	var body struct {
		Label string `json:"label"`
	}
	// Body is optional — an unlabelled checkpoint is a valid "bookmark
	// right now" gesture. Ignore EOF/invalid-JSON on empty bodies.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	snap, cursor, err := cartographer.Capture(r.Context(), h.db, missionID)
	if err != nil {
		h.logger.Error("cartographer capture failed", "err", err, "mission_id", missionID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "capture failed"})
		return
	}
	if cursor == "" {
		// A mission with zero journal entries can't anchor a meaningful
		// checkpoint — Restore would have nothing to diverge from.
		// Surface this as a 409 so the UI can show a friendly "nothing
		// to checkpoint yet" rather than a generic 500.
		writeJSON(w, http.StatusConflict, map[string]string{"error": "mission has no journal entries to anchor a checkpoint"})
		return
	}

	cp := cartographer.Checkpoint{
		WorkspaceID:   workspaceID,
		CrewID:        crewID,
		MissionID:     missionID,
		Label:         body.Label,
		JournalCursor: cursor,
		State:         snap,
		CreatedBy:     userID,
	}
	id, err := cartographer.Create(r.Context(), h.db, h.journal, cp)
	if err != nil {
		h.logger.Error("cartographer create failed", "err", err, "mission_id", missionID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create failed"})
		return
	}

	created, err := cartographer.Get(r.Context(), h.db, workspaceID, id)
	if err != nil {
		// Row just committed — a failure here is logged but we still
		// return a minimal success shape so the client can navigate to
		// the checkpoint by ID.
		h.logger.Error("cartographer reload after create failed", "err", err, "id", id)
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// Get serves GET /api/v1/checkpoints/{id}. 404 if not found or not in
// caller's workspace — cartographer.Get already scopes by workspace_id.
func (h *CartographerHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	cp, err := cartographer.Get(r.Context(), h.db, workspaceID, id)
	if err != nil {
		if errors.Is(err, cartographer.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "checkpoint not found"})
			return
		}
		h.logger.Error("cartographer get failed", "err", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "get failed"})
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

// Restore serves POST /api/v1/checkpoints/{id}/restore.
// Advisory only — the cartographer package returns the checkpoint plus
// the list of journal entries that would be abandoned by a rewind, but
// no mission state is mutated. The UI is expected to surface the
// divergence list to the operator before any further action.
func (h *CartographerHandler) Restore(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	result, err := cartographer.Restore(r.Context(), h.db, h.journal, workspaceID, id)
	if err != nil {
		if errors.Is(err, cartographer.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "checkpoint not found"})
			return
		}
		h.logger.Error("cartographer restore failed", "err", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "restore failed"})
		return
	}
	// Normalize the divergence slice so the UI always sees [] not null.
	divergence := result.WarnDivergence
	if divergence == nil {
		divergence = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checkpoint":      result.Checkpoint,
		"journal_cursor":  result.JournalCursor,
		"warn_divergence": divergence,
	})
}

// Fork serves POST /api/v1/checkpoints/{id}/fork.
// Body: {"label": "..."}. Creates a new mission + fork checkpoint
// anchored at the source checkpoint's cursor and returns their ids so
// the UI can redirect into the new mission.
func (h *CartographerHandler) Fork(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	user := UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated user required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}

	var body struct {
		Label string `json:"label"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	newMissionID, newCheckpointID, err := cartographer.Fork(r.Context(), h.db, h.journal, workspaceID, id, body.Label, user.ID)
	if err != nil {
		if errors.Is(err, cartographer.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "checkpoint not found"})
			return
		}
		h.logger.Error("cartographer fork failed", "err", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "fork failed"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"new_mission_id":    newMissionID,
		"new_checkpoint_id": newCheckpointID,
	})
}

// Delete serves DELETE /api/v1/checkpoints/{id}. 204 on success,
// 404 when the id doesn't exist or belongs to a different workspace.
// Forks that reference this checkpoint via fork_of are orphaned, not
// cascaded — see migration 52 and cartographer.Delete's doc comment.
func (h *CartographerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	if err := cartographer.Delete(r.Context(), h.db, workspaceID, id); err != nil {
		if errors.Is(err, cartographer.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "checkpoint not found"})
			return
		}
		h.logger.Error("cartographer delete failed", "err", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveMission checks the mission exists in the caller's workspace
// and returns (workspaceID, crewID, ok). On failure it already wrote
// the appropriate error response — callers just return when ok is false.
//
// This is the belt-and-braces check required by the endpoints that take
// a missionId in the URL (List, Create). The checkpoint-by-id endpoints
// lean on cartographer.Get/Delete scoping internally.
func (h *CartographerHandler) resolveMission(w http.ResponseWriter, r *http.Request, missionID, workspaceID string) (string, string, bool) {
	var (
		wsID   string
		crewID sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(),
		`SELECT workspace_id, crew_id FROM missions WHERE id = ?`, missionID).Scan(&wsID, &crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "mission not found"})
			return "", "", false
		}
		h.logger.Error("resolve mission failed", "err", err, "mission_id", missionID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return "", "", false
	}
	if wsID != workspaceID {
		// Same 404 shape as "mission not found" so we don't leak the
		// existence of rows in other workspaces.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mission not found"})
		return "", "", false
	}
	return wsID, crewID.String, true
}
