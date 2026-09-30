package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/restrictedpreflight"
)

// restrictedIssuePreflight explicitly selects a declared private routine for an
// assigned project-backed issue. Actor and issue brief are server selected.
func (r *Router) restrictedIssuePreflight(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil || r.restrictedWorkflow == nil {
		replyError(w, 503, "private workflow unavailable")
		return
	}
	var body struct {
		Agent   string `json:"agent_id"`
		Routine string `json:"routine_slug"`
	}
	req.Body = http.MaxBytesReader(w, req.Body, 4096)
	d := json.NewDecoder(req.Body)
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || body.Agent == "" || body.Routine == "" {
		replyError(w, 400, "invalid issue preflight request")
		return
	}
	s := restrictedpreflight.Service{DB: r.db, Workflow: r.restrictedWorkflow}
	receipt, err := s.ClaimAssignedIssue(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()), body.Agent, req.PathValue("issueId"), body.Routine)
	if err != nil {
		if errors.Is(err, restrictedpreflight.ErrBusy) || errors.Is(err, restrictedpreflight.ErrReconciliation) {
			replyError(w, 409, "issue is busy or requires reconciliation")
			return
		}
		replyError(w, 404, "authorized runnable issue unavailable")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": receipt.ID, "chat_id": receipt.ChatID, "status": receipt.State, "restricted": true})
}
