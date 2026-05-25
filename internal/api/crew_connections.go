package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// CrewConnectionHandler manages connections between crews that enable cross-crew communication.
type CrewConnectionHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewCrewConnectionHandler creates a CrewConnectionHandler with the given database and logger.
func NewCrewConnectionHandler(db *sql.DB, logger *slog.Logger) *CrewConnectionHandler {
	return &CrewConnectionHandler{db: db, logger: logger}
}

type crewConnectionResponse struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	FromCrewID   string `json:"from_crew_id"`
	FromCrewName string `json:"from_crew_name,omitempty"`
	FromCrewSlug string `json:"from_crew_slug,omitempty"`
	ToCrewID     string `json:"to_crew_id"`
	ToCrewName   string `json:"to_crew_name,omitempty"`
	ToCrewSlug   string `json:"to_crew_slug,omitempty"`
	Direction    string `json:"direction"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// List handles GET /api/v1/crew-connections
func (h *CrewConnectionHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cc.id, cc.workspace_id, cc.from_crew_id, cc.to_crew_id,
		       cc.direction, cc.status, cc.created_at, cc.updated_at,
		       fc.name, fc.slug, tc.name, tc.slug
		FROM crew_connections cc
		JOIN crews fc ON fc.id = cc.from_crew_id
		JOIN crews tc ON tc.id = cc.to_crew_id
		WHERE cc.workspace_id = ?
		ORDER BY cc.created_at DESC`, wsID)
	if err != nil {
		h.logger.Error("list crew connections", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	result := []crewConnectionResponse{}
	for rows.Next() {
		var c crewConnectionResponse
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.FromCrewID, &c.ToCrewID,
			&c.Direction, &c.Status, &c.CreatedAt, &c.UpdatedAt,
			&c.FromCrewName, &c.FromCrewSlug, &c.ToCrewName, &c.ToCrewSlug); err != nil {
			h.logger.Error("scan crew connection", "error", err)
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// Create handles POST /api/v1/crew-connections
func (h *CrewConnectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		FromCrewID string `json:"from_crew_id"`
		ToCrewID   string `json:"to_crew_id"`
		Direction  string `json:"direction"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.FromCrewID == "" || req.ToCrewID == "" {
		writeProblem(w, r, http.StatusBadRequest, "from_crew_id and to_crew_id required")
		return
	}
	if req.FromCrewID == req.ToCrewID {
		writeProblem(w, r, http.StatusBadRequest, "Cannot connect a crew to itself")
		return
	}
	if req.Direction == "" {
		req.Direction = "bidirectional"
	}
	if req.Direction != "bidirectional" && req.Direction != "unidirectional" {
		writeProblem(w, r, http.StatusBadRequest, "direction must be 'bidirectional' or 'unidirectional'")
		return
	}

	// Verify both crews exist in this workspace
	fromFound, fromErr := crewExists(r.Context(), h.db, req.FromCrewID, wsID)
	toFound, toErr := crewExists(r.Context(), h.db, req.ToCrewID, wsID)
	if fromErr != nil || toErr != nil {
		err := fromErr
		if err == nil {
			err = toErr
		}
		h.logger.Error("check crew", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !fromFound || !toFound {
		writeProblem(w, r, http.StatusNotFound, "One or both crews not found in this workspace")
		return
	}

	id := generateConnID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'active', ?, ?)`,
		id, wsID, req.FromCrewID, req.ToCrewID, req.Direction, now, now)
	if err != nil {
		h.logger.Error("create crew connection", "error", err)
		writeProblem(w, r, http.StatusConflict, "Connection already exists or constraint violation")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// Delete handles DELETE /api/v1/crew-connections/{connectionId}
func (h *CrewConnectionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	connID := r.PathValue("connectionId")

	result, err := h.db.ExecContext(r.Context(),
		`DELETE FROM crew_connections WHERE id = ? AND workspace_id = ?`, connID, wsID)
	if err != nil {
		h.logger.Error("delete crew connection", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeProblem(w, r, http.StatusNotFound, "Connection not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AreCrewsConnected checks if two crews have an active connection.
func AreCrewsConnected(ctx context.Context, db *sql.DB, crewA, crewB string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT 1 FROM crew_connections
		WHERE status = 'active' AND (
			(from_crew_id = ? AND to_crew_id = ?)
			OR (from_crew_id = ? AND to_crew_id = ? AND direction = 'bidirectional')
		)`, crewA, crewB, crewB, crewA).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func generateConnID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cc_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("cc_%x", b)
}
