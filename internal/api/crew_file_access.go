package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

func validCrewFileAccess(level string) bool {
	return level == "none" || level == "read" || level == "read_write"
}

// UpdateFileAccess changes one direction's shared-file access without granting
// communication, shell access, credentials, or access to private agent homes.
// Authorization is evaluated at request admission; already-open reads may finish.
func (h *CrewConnectionHandler) UpdateFileAccess(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	var body struct {
		RequesterCrewID string `json:"requester_crew_id"`
		Level           string `json:"level"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := readJSON(r, &body); err != nil || !validCrewFileAccess(body.Level) || body.ExpectedVersion < 1 {
		writeProblem(w, r, http.StatusBadRequest, "Valid level and expected_version are required")
		return
	}
	id, ws := r.PathValue("connectionId"), WorkspaceIDFromContext(r.Context())
	var from, to string
	err := h.db.QueryRowContext(r.Context(), `SELECT cc.from_crew_id, cc.to_crew_id
		FROM crew_connections cc
		JOIN crews a ON a.id=cc.from_crew_id AND a.workspace_id=cc.workspace_id AND a.deleted_at IS NULL
		JOIN crews b ON b.id=cc.to_crew_id AND b.workspace_id=cc.workspace_id AND b.deleted_at IS NULL
		WHERE cc.id=? AND cc.workspace_id=?`, id, ws).Scan(&from, &to)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Connection not found")
		return
	}
	if err != nil {
		internalError(w, r, h.logger, "load crew file access", err)
		return
	}
	column := "forward_file_access"
	if body.RequesterCrewID == to {
		column = "reverse_file_access"
	} else if body.RequesterCrewID != from {
		writeProblem(w, r, http.StatusBadRequest, "requester_crew_id must be a member of this connection")
		return
	}
	// The column is selected above from two constants, never request text.
	res, err := h.db.ExecContext(r.Context(), `UPDATE crew_connections SET `+column+`=?,
		access_version=access_version+1, updated_at=? WHERE id=? AND workspace_id=? AND access_version=?`,
		body.Level, time.Now().UTC().Format(time.RFC3339Nano), id, ws, body.ExpectedVersion)
	if err != nil {
		internalError(w, r, h.logger, "update crew file access", err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "check crew file access update", err)
		return
	}
	if n != 1 {
		writeProblem(w, r, http.StatusConflict, "Connection changed; reload before changing access")
		return
	}
	auditFromRequest(r, h.db, "crew_link.file_access", "CREW_LINK", id, map[string]interface{}{
		"requester_crew_id": body.RequesterCrewID, "level": body.Level, "version": body.ExpectedVersion + 1,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"level": body.Level, "access_version": body.ExpectedVersion + 1})
}

// canAccessSharedFiles uses the same directed edge as communication, with an
// additional file permission. Removing a link or soft-deleting either crew
// immediately denies the next request. There is no cached authorization.
func (h *CrewMessagingHandler) canAccessSharedFiles(r *http.Request, from, to string, write bool) (bool, error) {
	var allowed bool
	err := h.db.QueryRowContext(r.Context(), `SELECT EXISTS (
		SELECT 1 FROM crew_connections cc
		JOIN crews a ON a.id=cc.from_crew_id AND a.workspace_id=cc.workspace_id AND a.deleted_at IS NULL
		JOIN crews b ON b.id=cc.to_crew_id AND b.workspace_id=cc.workspace_id AND b.deleted_at IS NULL
		WHERE cc.status='active' AND (
		 (cc.from_crew_id=? AND cc.to_crew_id=? AND
		  cc.forward_file_access IN ('read','read_write') AND (?=0 OR cc.forward_file_access='read_write'))
		 OR (cc.to_crew_id=? AND cc.from_crew_id=? AND cc.direction='bidirectional' AND
		  cc.reverse_file_access IN ('read','read_write') AND (?=0 OR cc.reverse_file_access='read_write'))
		))`, from, to, write, from, to, write).Scan(&allowed)
	return allowed, err
}
