package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pages"
)

func pageRoutineDigest(definition string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(definition)))
}

// A publication records the routine definitions observed during review. These
// hashes are provenance, not a promise to freeze routine scripts or execution.
func (h *PageHandler) pageRoutineDigests(ctx context.Context, ws string, doc *pages.Document) (map[string]string, error) {
	result := map[string]string{}
	for _, panel := range doc.Spec.Panels {
		for _, action := range panel.Actions {
			if action.Kind != pages.ActionCall || result[action.Routine] != "" {
				continue
			}
			var definition string
			if err := h.db.QueryRowContext(ctx, `SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, ws, action.Routine).Scan(&definition); err != nil {
				return nil, fmt.Errorf("read action routine %q: %w", action.Routine, err)
			}
			result[action.Routine] = pageRoutineDigest(definition)
		}
	}
	return result, nil
}

type pageRoutineChecks struct {
	Definitions map[string]string `json:"routine_definitions"`
}

func (h *PageHandler) annotateApplicationRoutines(w http.ResponseWriter, r *http.Request, rec *pageRecord, actions []actionWire) bool {
	value := r.URL.Query().Get("publication")
	if value == "" {
		return true
	}
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version < 1 {
		replyError(w, 400, "publication must be positive")
		return false
	}
	var raw string
	if err := h.db.QueryRowContext(r.Context(), `SELECT checks_json FROM page_project_publications WHERE page_id=? AND version=?`, rec.ID, version).Scan(&raw); err != nil {
		replyError(w, 409, "Publication changed; reload the application")
		return false
	}
	var checks pageRoutineChecks
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		replyInternalError(w, h.logger, "read application routine review", err)
		return false
	}
	for i := range actions {
		a := &actions[i]
		if a.Routine == "" || checks.Definitions[a.Routine] == "" {
			continue
		}
		var definition string
		if err := h.db.QueryRowContext(r.Context(), `SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, WorkspaceIDFromContext(r.Context()), a.Routine).Scan(&definition); err != nil {
			replyError(w, 409, "The application routine is no longer available")
			return false
		}
		changed := pageRoutineDigest(definition) != checks.Definitions[a.Routine]
		a.RoutineChanged = &changed
	}
	return true
}
