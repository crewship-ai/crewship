package api

// Admin › Backups: plans, runs and the overview. Instance admins only
// (authedInstance); nothing here needs a workspace in the request.
//
//	GET    /api/v1/admin/instance/backups/plans                   every plan
//	POST   /api/v1/admin/instance/backups/plans                   create (preset defaults fill what is not sent)
//	GET    /api/v1/admin/instance/backups/plans/{id}              one plan
//	PUT    /api/v1/admin/instance/backups/plans/{id}              change (fields not sent keep their value)
//	DELETE /api/v1/admin/instance/backups/plans/{id}              delete (its runs stay as history)
//	GET    /api/v1/admin/instance/backups/plans/{id}/next?n=5     next due times, and which take environments
//	GET    /api/v1/admin/instance/backups/plans/{id}/calendar     ?from=&to= (YYYY-MM-DD in the plan's zone)
//	POST   /api/v1/admin/instance/backups/plans/preview-contents  {preset, contents, env_mode} → included/required/excluded
//	GET    /api/v1/admin/instance/backups/runs                    ?plan=&scope=&ws=&limit= run history with phases
//	POST   /api/v1/admin/instance/backups/run                     start a manual run; answers at once with its id
//	GET    /api/v1/admin/instance/backups/overview                ?scope=instance|workspaces&ws=
//
// Plan create, update and delete, and a manual run, write their instance
// audit entry (the plan changes in the same transaction).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backupplan"
)

// InstanceBackupPlansHandler serves plans, runs and the overview.
type InstanceBackupPlansHandler struct {
	h   *InstanceBackupsHandler
	svc *backupplan.Service
	// backupsDir reports where bundles land (for the overview).
	backupsDir func() (string, error)
}

func NewInstanceBackupPlansHandler(h *InstanceBackupsHandler, svc *backupplan.Service) *InstanceBackupPlansHandler {
	return &InstanceBackupPlansHandler{h: h, svc: svc, backupsDir: backup.DefaultBackupsDir}
}

func (p *InstanceBackupPlansHandler) now() time.Time { return p.svc.Clock.Now().UTC() }

func (p *InstanceBackupPlansHandler) planError(w http.ResponseWriter, what string, err error) {
	switch {
	case backupplan.IsValidation(err):
		replyError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, backupplan.ErrNotFound):
		replyError(w, http.StatusNotFound, "no backup plan with that id")
	default:
		p.h.fail(w, what, err)
	}
}

// ListPlans is GET …/backups/plans.
func (p *InstanceBackupPlansHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := backupplan.ListPlans(r.Context(), p.h.db)
	if err != nil {
		p.h.fail(w, "list plans", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": plans})
}

// GetPlan is GET …/backups/plans/{id}.
func (p *InstanceBackupPlansHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := backupplan.GetPlan(r.Context(), p.h.db, r.PathValue("id"))
	if err != nil {
		p.planError(w, "get plan", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

// CreatePlan is POST …/backups/plans. The preset's defaults fill every
// field the body leaves out.
func (p *InstanceBackupPlansHandler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := readBody(r)
	if err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var head struct {
		Preset string `json:"preset"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	plan := backupplan.Defaults(head.Preset)
	if err := json.Unmarshal(body, &plan); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	plan.ID, plan.NextRunAt, plan.LastRunAt = "", nil, nil
	if !p.resolvePlanWorkspaces(w, r, &plan) {
		return
	}
	if err := backupplan.Normalize(ctx, &plan, backupplan.DBWorkspaces{DB: p.h.db}, p.svc.Recipients); err != nil {
		p.planError(w, "create plan", err)
		return
	}
	if err := backupplan.ValidatePlanDestinations(ctx, p.h.db, &plan); err != nil {
		p.planError(w, "create plan", err)
		return
	}
	now := p.now()
	backupplan.SetNextRun(&plan, now)
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "create plan", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	actor := ""
	if u := UserFromContext(ctx); u != nil {
		actor = u.ID
	}
	if err := backupplan.InsertPlan(ctx, tx, &plan, actor, now); err != nil {
		p.h.fail(w, "insert plan", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_plan_created", "backup_plan", plan.ID, "", planAuditMeta(&plan)); err != nil {
		p.h.fail(w, "audit plan", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "create plan", err)
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

// resolvePlanWorkspaces lets workspace_ids name workspaces by id or slug.
// An unknown one is a 400; false means the reply was written.
func (p *InstanceBackupPlansHandler) resolvePlanWorkspaces(w http.ResponseWriter, r *http.Request, plan *backupplan.Plan) bool {
	if len(plan.WorkspaceIDs) == 0 {
		return true
	}
	all, err := instanceWorkspaces(r.Context(), p.h.db)
	if err != nil {
		p.h.fail(w, "list workspaces", err)
		return false
	}
	picked, err := pick(all, plan.WorkspaceIDs)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return false
	}
	plan.WorkspaceIDs = make([]string, 0, len(picked))
	for _, ws := range picked {
		plan.WorkspaceIDs = append(plan.WorkspaceIDs, ws.ID)
	}
	return true
}

func planAuditMeta(pl *backupplan.Plan) map[string]any {
	return map[string]any{
		"name": pl.Name, "preset": pl.Preset, "scope": pl.Scope, "workspace_ids": pl.WorkspaceIDs, "contents": pl.Contents,
		"cadence": pl.Cadence, "timezone": pl.Timezone, "enabled": pl.Enabled, "recipient_ids": pl.RecipientIDs,
	}
}

// UpdatePlan is PUT …/backups/plans/{id}. Fields the body leaves out keep
// their stored value; next_run_at is recomputed from now.
func (p *InstanceBackupPlansHandler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	existing, err := backupplan.GetPlan(ctx, p.h.db, r.PathValue("id"))
	if err != nil {
		p.planError(w, "get plan", err)
		return
	}
	body, err := readBody(r)
	if err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	plan := *existing
	if err := json.Unmarshal(body, &plan); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	plan.ID, plan.LastRunAt = existing.ID, existing.LastRunAt
	if !p.resolvePlanWorkspaces(w, r, &plan) {
		return
	}
	if err := backupplan.Normalize(ctx, &plan, backupplan.DBWorkspaces{DB: p.h.db}, p.svc.Recipients); err != nil {
		p.planError(w, "update plan", err)
		return
	}
	if err := backupplan.ValidatePlanDestinations(ctx, p.h.db, &plan); err != nil {
		p.planError(w, "update plan", err)
		return
	}
	now := p.now()
	backupplan.SetNextRun(&plan, now)
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "update plan", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	actor := ""
	if u := UserFromContext(ctx); u != nil {
		actor = u.ID
	}
	if err := backupplan.UpdatePlan(ctx, tx, &plan, actor, now); err != nil {
		p.planError(w, "update plan", err)
		return
	}
	meta := planAuditMeta(&plan)
	meta["before"] = planAuditMeta(existing)
	if err := auditInstance(ctx, r, tx, "instance.backup_plan_updated", "backup_plan", plan.ID, "", meta); err != nil {
		p.h.fail(w, "audit plan", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "update plan", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// DeletePlan is DELETE …/backups/plans/{id}.
func (p *InstanceBackupPlansHandler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	existing, err := backupplan.GetPlan(ctx, p.h.db, r.PathValue("id"))
	if err != nil {
		p.planError(w, "get plan", err)
		return
	}
	tx, err := p.h.db.BeginTx(ctx, nil)
	if err != nil {
		p.h.fail(w, "delete plan", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err := backupplan.DeletePlan(ctx, tx, existing.ID); err != nil {
		p.planError(w, "delete plan", err)
		return
	}
	if err := auditInstance(ctx, r, tx, "instance.backup_plan_deleted", "backup_plan", existing.ID, "", planAuditMeta(existing)); err != nil {
		p.h.fail(w, "audit plan", err)
		return
	}
	if err := tx.Commit(); err != nil {
		p.h.fail(w, "delete plan", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// NextRuns is GET …/backups/plans/{id}/next?n=5 (1..50).
func (p *InstanceBackupPlansHandler) NextRuns(w http.ResponseWriter, r *http.Request) {
	plan, err := backupplan.GetPlan(r.Context(), p.h.db, r.PathValue("id"))
	if err != nil {
		p.planError(w, "get plan", err)
		return
	}
	n := 5
	if raw := r.URL.Query().Get("n"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 50 {
			replyError(w, http.StatusBadRequest, "n must be 1..50")
			return
		}
		n = v
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": backupplan.NextRuns(plan, p.now(), n)})
}

// Calendar is GET …/backups/plans/{id}/calendar?from=&to=. Without them it
// covers the last 7 and the next 28 days in the plan's zone.
func (p *InstanceBackupPlansHandler) Calendar(w http.ResponseWriter, r *http.Request) {
	plan, err := backupplan.GetPlan(r.Context(), p.h.db, r.PathValue("id"))
	if err != nil {
		p.planError(w, "get plan", err)
		return
	}
	now := p.now()
	today := now.In(plan.Location())
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from == "" {
		from = today.AddDate(0, 0, -7).Format("2006-01-02")
	}
	if to == "" {
		to = today.AddDate(0, 0, 28).Format("2006-01-02")
	}
	days, err := backupplan.Calendar(r.Context(), p.h.db, plan, from, to, now)
	if err != nil {
		p.planError(w, "calendar", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}

type previewContentsRequest struct {
	Preset   string   `json:"preset"`
	Contents []string `json:"contents"`
	EnvMode  string   `json:"env_mode"`
}

// PreviewContents is POST …/backups/plans/preview-contents.
func (p *InstanceBackupPlansHandler) PreviewContents(w http.ResponseWriter, r *http.Request) {
	var req previewContentsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	res, err := backupplan.PreviewContents(req.Preset, req.Contents, req.EnvMode)
	if err != nil {
		p.planError(w, "preview contents", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// wsSelection resolves ?ws= (ids or slugs, comma-separated; "none" is the
// empty selection). nil means every workspace.
func (p *InstanceBackupPlansHandler) wsSelection(r *http.Request) ([]string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("ws"))
	if raw == "" {
		return nil, nil
	}
	if raw == "none" {
		return []string{}, nil
	}
	all, err := instanceWorkspaces(r.Context(), p.h.db)
	if err != nil {
		return nil, err
	}
	picked, err := pick(all, strings.Split(raw, ","))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, w := range picked {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

func scopeParam(r *http.Request) (string, bool) {
	switch s := r.URL.Query().Get("scope"); s {
	case "", backupplan.ScopeInstance, backupplan.ScopeWorkspaces:
		return s, true
	default:
		return "", false
	}
}

// ListRuns is GET …/backups/runs?plan=&scope=&ws=&limit= (default 100, max 500).
func (p *InstanceBackupPlansHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeParam(r)
	if !ok {
		replyError(w, http.StatusBadRequest, "scope must be instance or workspaces")
		return
	}
	ws, err := p.wsSelection(r)
	var unknown errUnknownWorkspace
	if errors.As(err, &unknown) {
		replyError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		p.h.fail(w, "list workspaces", err)
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			replyError(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
		limit = n
	}
	if ws != nil && len(ws) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"data": []backupplan.RunView{}})
		return
	}
	if ws != nil && scope == "" {
		scope = backupplan.ScopeWorkspaces
	}
	runs, err := backupplan.ListRunViews(r.Context(), p.h.db, p.svc.Recipients, backupplan.RunFilter{
		PlanID: r.URL.Query().Get("plan"), Scope: scope, WorkspaceIDs: ws, Limit: limit,
	})
	if err != nil {
		p.h.fail(w, "list runs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": runs})
}

// startRunResponse: id and run_id name the first run; run_ids every run
// queued (one per workspace for scope=workspaces). Status is always
// "running" — follow each with GET …/backups/run/{runId}.
type startRunResponse struct {
	ID     string   `json:"id"`
	RunID  string   `json:"run_id"`
	RunIDs []string `json:"run_ids"`
	Status string   `json:"status"`
}

// StartRun is POST …/backups/run. It queues the run(s) and answers 202 at
// once; the backup runs in the server's backup service, so a client that
// disconnects (or the CLI's 30-second timeout) cannot cancel it.
func (p *InstanceBackupPlansHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req backupplan.ManualRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.WorkspaceIDs) > 0 {
		all, err := instanceWorkspaces(ctx, p.h.db)
		if err != nil {
			p.h.fail(w, "list workspaces", err)
			return
		}
		picked, err := pick(all, req.WorkspaceIDs)
		if err != nil {
			replyError(w, http.StatusNotFound, err.Error())
			return
		}
		req.WorkspaceIDs = nil
		for _, ws := range picked {
			req.WorkspaceIDs = append(req.WorkspaceIDs, ws.ID)
		}
	}
	actor := ""
	if u := UserFromContext(ctx); u != nil {
		actor = u.ID
	}
	ids, err := p.svc.StartManual(ctx, req, actor)
	if err != nil {
		p.planError(w, "start run", err)
		return
	}
	if err := auditInstance(ctx, r, p.h.db, "instance.backup_run_started", "backup_run", ids[0], "", map[string]any{
		"run_ids": ids, "plan_id": req.PlanID, "scope": req.Scope, "workspace_ids": req.WorkspaceIDs, "preset": req.Preset,
		"contents": req.Contents, "note": req.Note, "recipients": len(req.RecipientIDs) + len(req.Recipients),
		"passphrase": req.Passphrase != "",
	}); err != nil {
		p.h.logger.Warn("instance backups: audit manual run", "error", err)
	}
	writeJSON(w, http.StatusAccepted, startRunResponse{ID: ids[0], RunID: ids[0], RunIDs: ids, Status: backupplan.StatusRunning})
}

// Overview is GET …/backups/overview?scope=instance|workspaces&ws=.
func (p *InstanceBackupPlansHandler) Overview(w http.ResponseWriter, r *http.Request) {
	scope, ok := scopeParam(r)
	if !ok {
		replyError(w, http.StatusBadRequest, "scope must be instance or workspaces")
		return
	}
	if scope == "" {
		scope = backupplan.ScopeInstance
	}
	ws, err := p.wsSelection(r)
	var unknown errUnknownWorkspace
	if errors.As(err, &unknown) {
		replyError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		p.h.fail(w, "list workspaces", err)
		return
	}
	dir := ""
	if p.backupsDir != nil {
		dir, _ = p.backupsDir()
	}
	ov, err := backupplan.BuildOverview(r.Context(), p.h.db, backupplan.OverviewInput{
		Scope: scope, WorkspaceIDs: ws, BackupsDir: dir, Kit: vaultKitInfo{db: p.h.db},
	}, p.now())
	if err != nil {
		p.h.fail(w, "overview", err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}
