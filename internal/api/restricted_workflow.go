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
		if errors.Is(err, access.ErrDenied) {
			return false
		}
		replyError(w, 500, "failed to load routine")
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
		Inputs                map[string]any `json:"inputs"`
		ExpectedHash          string         `json:"expected_definition_hash,omitempty"`
		ExpectedExecutionHash string         `json:"expected_execution_hash,omitempty"`
		DelaySeconds          int            `json:"delay_seconds,omitempty"`
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
	receipt, err := h.restrictedWorkflow().AdmitManualFrozen(req.Context(), UserFromContext(req.Context()).ID, WorkspaceIDFromContext(req.Context()), req.PathValue("slug"), body.Inputs, body.ExpectedHash, body.ExpectedExecutionHash, time.Duration(body.DelaySeconds)*time.Second, req.Header.Get("Idempotency-Key"))
	if err != nil {
		replyRestrictedWorkflowError(w, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": receipt.ID, "chat_id": receipt.ChatID, "status": receipt.State, "restricted": true, "status_url": "/api/v1/workspaces/" + WorkspaceIDFromContext(req.Context()) + "/restricted-routine-runs/" + receipt.ID})
	return true
}

type restrictedPageIntentKey struct{}

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
	expected, _ := req.Context().Value(restrictedPageIntentKey{}).(string)
	if expected == "" {
		replyError(w, 400, "expected_intent_hash is required")
		return true
	}
	receipt, err := h.restrictedWorkflow().AdmitPageFrozen(req.Context(), UserFromContext(req.Context()).ID, WorkspaceIDFromContext(req.Context()), action, inputs, expected, req.Header.Get("Idempotency-Key"))
	if err != nil {
		replyRestrictedWorkflowError(w, err)
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pending_id": receipt.ID, "run_id": receipt.ID, "status": receipt.State, "deduped": receipt.State == "DEDUPED", "coalesced": false, "routine": routine, "panel": action.PanelID, "action": action.ActionID, "restricted": true, "status_url": "/api/v1/workspaces/" + WorkspaceIDFromContext(req.Context()) + "/restricted-routine-runs/" + receipt.ID})
	return true
}
func replyRestrictedWorkflowError(w http.ResponseWriter, err error) {
	if errors.Is(err, restrictedworkflow.ErrUnsupported) {
		replyError(w, 400, "restricted routine declaration uses unsupported steps or resources")
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

func (r *Router) restrictedRoutineCatalog(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil || r.restrictedWorkflow == nil {
		replyError(w, 404, "routine unavailable")
		return
	}
	items, err := r.restrictedWorkflow.Catalog(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()))
	if err != nil {
		replyError(w, 404, "routine unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
	writeJSON(w, 200, items)
}

func (h *PageHandler) serveRestrictedApplicationStatus(w http.ResponseWriter, req *http.Request) bool {
	restricted, err := restrictedActor(req, h.db)
	if err != nil {
		replyError(w, 404, "Page action not found")
		return true
	}
	if !restricted {
		return false
	}
	user := UserFromContext(req.Context())
	if user == nil || h.restrictedWorkflow == nil || h.restrictedWorkflow() == nil {
		replyError(w, 404, "Page action not found")
		return true
	}
	workspace := WorkspaceIDFromContext(req.Context())
	var one int
	err = h.db.QueryRowContext(req.Context(), `SELECT 1 FROM restricted_workflow_jobs j JOIN pages p ON p.id=j.page_id WHERE j.id=? AND j.workspace_id=? AND j.principal_id=? AND j.source_kind='page' AND p.workspace_id=j.workspace_id AND p.slug=?`, req.PathValue("pendingId"), workspace, user.ID, req.PathValue("slug")).Scan(&one)
	if err != nil {
		replyError(w, 404, "Page action not found")
		return true
	}
	result, err := h.restrictedWorkflow().Result(req.Context(), user.ID, workspace, req.PathValue("pendingId"))
	if err != nil {
		replyError(w, 404, "Page action not found")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"pending_id": result.ID, "run_id": result.ID, "pending_status": result.State, "run_status": result.State, "restricted": true, "routine_revision_pinned": true, "step_outputs": result.Outputs})
	return true
}
