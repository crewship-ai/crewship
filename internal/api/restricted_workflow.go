package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

// SetRestrictedWorkflow attaches the private queue once before serving. Its
// lifecycle belongs to server bootstrap, alongside the isolated managers.
func (r *Router) SetRestrictedWorkflow(service *restrictedworkflow.Service) {
	r.restrictedWorkflow = service
}
func restrictedActor(req *http.Request, db *sql.DB) (bool, error) {
	user := UserFromContext(req.Context())
	if user == nil {
		return false, nil
	}
	m, err := (access.Store{DB: db}).Membership(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()))
	return m.Mode == "restricted", err
}
func (h *PipelineHandler) serveRestrictedWorkflow(w http.ResponseWriter, req *http.Request) bool {
	restricted, err := restrictedActor(req, h.db)
	if err != nil {
		replyError(w, 404, "routine unavailable")
		return true
	}
	if !restricted {
		return false
	}
	if h.restrictedWorkflow == nil || h.restrictedWorkflow() == nil {
		replyError(w, 503, "restricted workflow unavailable")
		return true
	}
	var body struct {
		Inputs       map[string]any `json:"inputs"`
		ExpectedHash string         `json:"expected_definition_hash,omitempty"`
		DelaySeconds int            `json:"delay_seconds,omitempty"`
	}
	req.Body = http.MaxBytesReader(w, req.Body, 32<<10)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil && !(err == io.EOF && req.ContentLength <= 0) {
		replyError(w, 400, "invalid restricted routine request")
		return true
	}
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "invalid restricted routine request")
		return true
	}
	if body.DelaySeconds < 0 || body.DelaySeconds > 86400 {
		replyError(w, 400, "invalid restricted routine delay")
		return true
	}
	receipt, err := h.restrictedWorkflow().AdmitManual(req.Context(), UserFromContext(req.Context()).ID, WorkspaceIDFromContext(req.Context()), req.PathValue("slug"), body.Inputs, body.ExpectedHash, time.Duration(body.DelaySeconds)*time.Second, req.Header.Get("Idempotency-Key"))
	if err != nil {
		replyRestrictedWorkflowError(w, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": receipt.ID, "chat_id": receipt.ChatID, "status": receipt.State, "restricted": true, "status_url": "/api/v1/workspaces/" + WorkspaceIDFromContext(req.Context()) + "/restricted-routine-runs/" + receipt.ID})
	return true
}
func (h *PageHandler) serveRestrictedPageWorkflow(w http.ResponseWriter, req *http.Request, action pipeline.PageActionInvocation, inputs map[string]any, routine string) bool {
	restricted, err := restrictedActor(req, h.db)
	if err != nil {
		replyError(w, 404, "Page action unavailable")
		return true
	}
	if !restricted {
		return false
	}
	if h.restrictedWorkflow == nil || h.restrictedWorkflow() == nil {
		replyError(w, 503, "restricted workflow unavailable")
		return true
	}
	receipt, err := h.restrictedWorkflow().AdmitPage(req.Context(), UserFromContext(req.Context()).ID, WorkspaceIDFromContext(req.Context()), action, inputs, req.Header.Get("Idempotency-Key"))
	if err != nil {
		replyRestrictedWorkflowError(w, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pending_id": receipt.ID, "run_id": receipt.ID, "status": receipt.State, "deduped": receipt.State == "DEDUPED", "coalesced": false, "routine": routine, "panel": action.PanelID, "action": action.ActionID, "restricted": true, "status_url": "/api/v1/workspaces/" + WorkspaceIDFromContext(req.Context()) + "/restricted-routine-runs/" + receipt.ID})
	return true
}
func replyRestrictedWorkflowError(w http.ResponseWriter, err error) {
	if errors.Is(err, restrictedworkflow.ErrUnsupported) {
		replyError(w, 400, "restricted routines support at most 16 linear text agent steps for one declared agent; other steps and resources are unavailable")
		return
	}
	if errors.Is(err, restrictedworkflow.ErrDenied) {
		replyError(w, 404, "workflow unavailable")
		return
	}
	replyError(w, 503, "restricted workflow unavailable")
}
func (r *Router) restrictedWorkflowResult(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil || r.restrictedWorkflow == nil {
		replyError(w, 404, "workflow unavailable")
		return
	}
	result, err := r.restrictedWorkflow.Result(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()), req.PathValue("runId"))
	if err != nil {
		replyError(w, 404, "workflow unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (r *Router) restrictedWorkflowResults(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil || r.restrictedWorkflow == nil {
		replyError(w, 404, "workflow unavailable")
		return
	}
	results, err := r.restrictedWorkflow.ResultsForActor(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()))
	if err != nil {
		replyError(w, 404, "workflow unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(results)))
	writeJSON(w, 200, results)
}
