package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type gridcraftHTTP struct {
	pages      *PageHandler
	store      *appengine.Store
	engine     appengine.Engine
	operations artifacts.GridCraftOperations
}

// RegisterGridCraft installs only the explicit experimental offline surface.
// Disabled installs no routes. The engine must be an operator-qualified worker.
func (r *Router) RegisterGridCraft(options appengine.Options, engine appengine.Engine) {
	if !options.Enabled || engine == nil {
		return
	}
	store := appengine.New(r.db, options)
	adapter := artifacts.NewGridCraftAdapter(store, engine, true)
	operations, ok := artifacts.NewRegistry(artifacts.EnabledTypes{"gridcraft-workbook": true}, nil, adapter).GridCraft()
	if !ok {
		return
	}
	h := &gridcraftHTTP{pages: r.pages, store: store, engine: engine, operations: operations}
	authed, wsCtx := r.authMw.RequireAuth, r.authMw.RequireWorkspace
	internalAuth := func(next http.Handler) http.Handler {
		return r.internalHandler.requireInternal(withHostChatOrigin(next))
	}
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/workbook", authed(wsCtx(http.HandlerFunc(h.read))))
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/operations", roleInline, h.importFile)
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.mux.Handle("GET /api/v1/pages/{slug}/workbook/operations/{mutation}", authed(wsCtx(http.HandlerFunc(h.status))))
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("POST", "/api/v1/pages/{slug}/workbook/mandates", roleInline, h.mandate)
	// openapi: responses 200,400,401,403,404,409,429,500,503
	r.authedMut("DELETE", "/api/v1/pages/{slug}/workbook/mandates/{mandate}", roleInline, h.revoke)
	// The crew IPC authenticates transport; the original signed run bearer is
	// independently verified by artifactAgentRequest and checked again in SQL.
	// openapi: responses 200,400,403,404,409,429,500,503
	r.mux.Handle("POST /api/v1/internal/gridcraft", internalAuth(http.HandlerFunc(h.agent)))
}
func gridcraftError(w http.ResponseWriter, err error) {
	code, message := 500, "Workbook operation failed"
	switch {
	case errors.Is(err, appengine.ErrForbidden), errors.Is(err, appengine.ErrNotFound):
		code, message = 404, "Workbook not found"
	case errors.Is(err, appengine.ErrInvalid), errors.Is(err, appengine.ErrCandidate):
		code, message = 400, "Invalid workbook operation"
	case errors.Is(err, appengine.ErrConflict):
		code, message = 409, "conflict"
	case errors.Is(err, appengine.ErrPrecondition):
		code, message = 409, "precondition_failed"
	case errors.Is(err, appengine.ErrEffects):
		code, message = 403, "effect_out_of_mandate"
	case errors.Is(err, appengine.ErrInProgress):
		code, message = 409, "in_progress"
	case errors.Is(err, appengine.ErrFence):
		code, message = 409, "stale_generation"
	case errors.Is(err, appengine.ErrCapacity):
		code, message = 429, "capacity_exhausted"
	case errors.Is(err, appengine.ErrQuota):
		code, message = 429, "history_quota_exceeded"
	case errors.Is(err, appengine.ErrDisabled), errors.Is(err, appengine.ErrEngineUnavailable):
		code, message = 503, "Workbook unavailable"
	}
	replyError(w, code, message)
}
func (h *gridcraftHTTP) humanAuthority(r *http.Request, k appengine.Key, write bool, capture func(*pageRecord)) appengine.Authorize {
	return func(ctx context.Context, tx *sql.Tx) (appengine.Principal, error) {
		if capture != nil {
			capture(nil)
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		request := r.WithContext(ctx)
		user := UserFromContext(ctx)
		agent, _ := pageGrantCallerIsAgent(request)
		if user == nil || agent || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		scope := "pages:read"
		if write {
			scope = "pages:write"
		}
		if validateContentHumanCredential(request, tx, scope) != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		viewer, err := h.pages.livePrivatePageReader(ctx, k.WorkspaceID, user.ID)
		if err != nil || viewer == nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		ctx = context.WithValue(ctx, ctxRole, viewer.Role)
		rec, err := h.pages.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
		if err != nil || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || !rec.PrivateAudience || rec.FolderID != "" || rec.HasDocument || rec.HasProject || rec.HasApplication {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if write {
			if rec.OwnerUserID != user.ID || !h.pages.mayEditSpec(ctx, k.WorkspaceID, user.ID, viewer.Role, rec) {
				return appengine.Principal{}, appengine.ErrForbidden
			}
		} else if !privatePageViewer(rec, viewer) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if capture != nil {
			capture(rec)
		}
		return appengine.Principal{AuthorityID: "user:" + user.ID, MandateID: "human-import", Targets: []string{"*"}}, nil
	}
}
func (h *gridcraftHTTP) humanKey(r *http.Request, write bool) (appengine.Key, error) {
	k := appengine.Key{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	column, ref := "slug", r.PathValue("slug")
	if strings.HasPrefix(ref, "@") {
		column, ref = "id", strings.TrimPrefix(ref, "@")
	}
	if h.pages.db.QueryRowContext(r.Context(), "SELECT id FROM pages WHERE workspace_id=? AND "+column+"=?", k.WorkspaceID, ref).Scan(&k.PageID) != nil {
		return k, appengine.ErrForbidden
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		return k, err
	}
	defer tx.Rollback()
	_, err = h.humanAuthority(r, k, write, nil)(r.Context(), tx)
	return k, err
}
func (h *gridcraftHTTP) read(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, false)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var record *pageRecord
	state, err := h.operations.Read(r.Context(), k, h.humanAuthority(r, k, false, func(p *pageRecord) { record = p }))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if record == nil || !h.pages.auditPrivateOwnerAccess(w, r, record) {
		return
	}
	writeJSON(w, 200, state)
}
func decodeGridCraftBody(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, appengine.MaxSnapshotBytes+16384)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return appengine.ErrInvalid
	}
	return appengine.DecodeStrict(raw, target)
}
func (h *gridcraftHTTP) importFile(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var req appengine.Request
	if err = decodeGridCraftBody(w, r, &req); err != nil {
		gridcraftError(w, err)
		return
	}
	if req.Command.Kind != "import" || req.MandateID != "human-import" {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	result, err := h.operations.Apply(r.Context(), k, req, h.humanAuthority(r, k, true, nil))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *gridcraftHTTP) status(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, false)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var record *pageRecord
	result, err := h.store.Status(r.Context(), k, r.PathValue("mutation"), h.humanAuthority(r, k, false, func(p *pageRecord) { record = p }))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if record == nil || !h.pages.auditPrivateOwnerAccess(w, r, record) {
		return
	}
	writeJSON(w, 200, result)
}

type gridcraftMandate struct {
	MandateID  string   `json:"mandate_id"`
	RunID      string   `json:"run_id"`
	BaseRev    int64    `json:"base_rev"`
	Generation int64    `json:"generation"`
	Targets    []string `json:"targets"`
}

func (h *gridcraftHTTP) mandate(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var req gridcraftMandate
	if err = decodeGridCraftBody(w, r, &req); err != nil {
		gridcraftError(w, err)
		return
	}
	if len(req.MandateID) == 0 || len(req.MandateID) > 256 || len(req.RunID) == 0 || len(req.Targets) != 1 || req.Targets[0] == "*" || len(req.Targets[0]) > 256 || req.BaseRev < 1 || req.Generation < 1 {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		gridcraftError(w, err)
		return
	}
	p, err := h.humanAuthority(r, k, true, nil)(r.Context(), tx)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	a, err := h.run(ctx, tx, req.RunID, k.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		err = h.retainOrdinaryHumanRun(r, tx, req.RunID, k.WorkspaceID, p.AuthorityID)
		if err == nil {
			a, err = h.run(ctx, tx, req.RunID, k.WorkspaceID)
		}
	}

	if err != nil || p.AuthorityID != "user:"+a.HumanUserID || h.pages.checkArtifactRunPrincipal(ctx, &a) != nil || !h.pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	// The private chat seal is durable before the first agent read or prompt copy.
	var sealed int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private' AND (private_artifact_author_user_id IS NULL OR private_artifact_author_user_id=?)`, a.ChatID, a.WorkspaceID, a.HumanUserID, a.HumanUserID).Scan(&sealed) != nil || sealed != 1 {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	var rev, generation, pageVersion, policyVersion int64
	if tx.QueryRowContext(ctx, `SELECT g.head_rev,g.generation,p.pages_version FROM gridcraft_heads g JOIN pages p ON p.id=g.page_id AND p.workspace_id=g.workspace_id WHERE g.workspace_id=? AND g.page_id=?`, k.WorkspaceID, k.PageID).Scan(&rev, &generation, &pageVersion) != nil {
		gridcraftError(w, appengine.ErrNotFound)
		return
	}
	if tx.QueryRowContext(ctx, `SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1`, k.WorkspaceID).Scan(&policyVersion) != nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	targets, _ := json.Marshal(req.Targets)
	payload, _ := json.Marshal(req)
	digest := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(digest[:])
	var old string
	e := tx.QueryRowContext(ctx, `SELECT payload_hash FROM gridcraft_intents WHERE workspace_id=? AND page_id=? AND mandate_id=? AND human_user_id=? AND revoked_at IS NULL`, k.WorkspaceID, k.PageID, req.MandateID, a.HumanUserID).Scan(&old)
	if e == nil {
		if old != fingerprint {
			gridcraftError(w, appengine.ErrConflict)
			return
		}
	} else {
		if !errors.Is(e, sql.ErrNoRows) {
			gridcraftError(w, e)
			return
		}
		if req.BaseRev != rev || req.Generation != generation {
			gridcraftError(w, appengine.ErrPrecondition)
			return
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_intents(workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, req.MandateID, a.HumanUserID, a.RunID, req.BaseRev, req.Generation, pageVersion, policyVersion, string(targets), fingerprint)
		if err != nil {
			gridcraftError(w, appengine.ErrConflict)
			return
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=?`, a.HumanUserID, a.ChatID); err != nil {
		gridcraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, req)
}

// Explicit human workbook admission may retain an ordinary host-dispatched
// chat run. It uses only the original durable run receipt, never caller/agent
// metadata, and adds no consent or admission behavior to the Docs subsystem.
func (h *gridcraftHTTP) retainOrdinaryHumanRun(r *http.Request, tx *sql.Tx, run, workspace, authority string) error {
	var agent, crew string
	if err := tx.QueryRowContext(r.Context(), `SELECT agent_id,crew_id FROM journal_entries WHERE trace_id=? AND workspace_id=? AND entry_type='run.started' AND json_extract(payload,'$.host_admitted')=1 AND json_extract(payload,'$.trigger_type')='USER'`, run, workspace).Scan(&agent, &crew); err != nil {
		return appengine.ErrForbidden
	}
	origin, err := runauthority.Resolve(r.Context(), tx, run, agent, workspace)
	if err != nil || origin.HumanAuthority == nil || origin.ChatOriginID != "" || origin.TriggerMessageID == "" || authority != "user:"+origin.HumanAuthority.UserID {
		return appengine.ErrForbidden
	}
	return captureArtifactChatPrincipalQuery(r, tx, run, workspace, agent, crew, origin.ChatID, origin.TriggerMessageID, origin.HumanAuthority)
}

func (h *gridcraftHTTP) run(ctx context.Context, tx *sql.Tx, run, workspace string) (a artifactRunAuthority, err error) {
	err = tx.QueryRowContext(ctx, `SELECT run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id FROM artifact_run_principals WHERE run_id=? AND workspace_id=?`, run, workspace).Scan(&a.RunID, &a.WorkspaceID, &a.AgentID, &a.CrewID, &a.HumanUserID, &a.ChatID, &a.MessageID)
	return
}
func (h *gridcraftHTTP) revoke(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	k, err := h.humanKey(r, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		gridcraftError(w, err)
		return
	}
	p, err := h.humanAuthority(r, k, true, nil)(r.Context(), tx)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_intents SET revoked_at=? WHERE workspace_id=? AND page_id=? AND mandate_id=? AND human_user_id=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), k.WorkspaceID, k.PageID, r.PathValue("mandate"), strings.TrimPrefix(p.AuthorityID, "user:"))
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		gridcraftError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}

type gridcraftAgentRequest struct {
	Action     string             `json:"action"`
	ArtifactID string             `json:"artifact_id"`
	MandateID  string             `json:"mandate_id"`
	MutationID string             `json:"mutation_id,omitempty"`
	Request    *appengine.Request `json:"request,omitempty"`
}

func (h *gridcraftHTTP) agent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.pages.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	var body gridcraftAgentRequest
	if err := decodeGridCraftBody(w, r, &body); err != nil {
		gridcraftError(w, err)
		return
	}
	a, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if a == nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	k := appengine.Key{WorkspaceID: a.WorkspaceID, PageID: body.ArtifactID}
	authority := func(ctx context.Context, tx *sql.Tx) (appengine.Principal, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if h.pages.checkArtifactRunPrincipal(ctx, a) != nil || !h.pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		role, err := h.pages.liveContentHumanRole(ctx, a.WorkspaceID, a.HumanUserID)
		if err != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		rec, err := h.pages.loadPage(ctx, a.WorkspaceID, "@"+k.PageID)
		if err != nil || rec.OwnerUserID != a.HumanUserID || rec.OwnerCrewID != "" || !rec.PrivateAudience || rec.FolderID != "" || !h.pages.mayEditSpec(ctx, a.WorkspaceID, a.HumanUserID, role, rec) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		var targets string
		var base, generation, pageVersion, policyVersion int64
		if tx.QueryRowContext(ctx, `SELECT targets_json,base_rev,generation,page_version,policy_version FROM gridcraft_intents WHERE workspace_id=? AND page_id=? AND mandate_id=? AND run_id=? AND human_user_id=? AND revoked_at IS NULL`, k.WorkspaceID, k.PageID, body.MandateID, a.RunID, a.HumanUserID).Scan(&targets, &base, &generation, &pageVersion, &policyVersion) != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		var livePolicy bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1 AND version=?)`, k.WorkspaceID, policyVersion).Scan(&livePolicy) != nil || !livePolicy {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if rec.PagesVersion != pageVersion {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		if body.Request != nil && (body.Request.MandateID != body.MandateID || body.Request.BaseRev != base || body.Request.Generation != generation) {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		var bounds []string
		if json.Unmarshal([]byte(targets), &bounds) != nil {
			return appengine.Principal{}, appengine.ErrForbidden
		}
		return appengine.Principal{AuthorityID: "agent:" + a.AgentID + ":run:" + a.RunID + ":mandate:" + body.MandateID, MandateID: body.MandateID, Targets: bounds}, nil
	}
	switch body.Action {
	case "read":
		if body.Request != nil || body.MutationID != "" {
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		result, err := h.operations.Read(r.Context(), k, authority)
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
	case "apply":
		if body.Request == nil || body.Request.Command.Kind != "cell.set" || body.MutationID != "" {
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		result, err := h.operations.Apply(r.Context(), k, *body.Request, authority)
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
	case "status":
		if body.Request != nil || body.MutationID == "" {
			gridcraftError(w, appengine.ErrInvalid)
			return
		}
		result, err := h.store.Status(r.Context(), k, body.MutationID, authority)
		if err != nil {
			gridcraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
	default:
		gridcraftError(w, appengine.ErrInvalid)
	}
}
