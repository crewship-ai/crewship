package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// List returns workspace-visible, non-ephemeral pipelines for the
// caller's workspace. Sorted by invocation_count DESC by default —
// the natural "what are my crews actually using" view.
//
// GET /api/v1/workspaces/{workspaceId}/pipelines
//
// Query parameters:
//
//	include_ephemeral=1      include auto-generated delegation wraps
//	include_hidden=1         include workspace_visible=0 entries
//	author_crew_id=crew_xyz  filter to one author crew
//	limit=50                 cap at 500 hard
//	order=popularity|recent|name
func (h *PipelineHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	q := r.URL.Query()
	f := pipeline.ListFilters{
		WorkspaceID:      workspaceID,
		IncludeEphemeral: q.Get("include_ephemeral") == "1",
		IncludeHidden:    q.Get("include_hidden") == "1",
		AuthorCrewID:     q.Get("author_crew_id"),
	}
	switch q.Get("order") {
	case "recent":
		f.OrderBy = pipeline.OrderByRecent
	case "name":
		f.OrderBy = pipeline.OrderByName
	default:
		f.OrderBy = pipeline.OrderByPopularity
	}

	rows, err := h.store.List(r.Context(), f)
	if err != nil {
		h.logger.Error("pipeline list", "error", err)
		replyError(w, http.StatusInternalServerError, "list pipelines")
		return
	}
	out := make([]pipelineResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, toPipelineResponse(p, false))
	}
	// Enrich the list with two cross-cutting bits:
	//   - author_agent_name: lets the UI render "Authored by Eva"
	//     instead of a UUID. Single batch query keyed on author_agent_id.
	//   - linked_issue_count + linked_issues: how many issues bind
	//     this routine via missions.routine_id. Single GROUP BY scan;
	//     identifiers truncated to 3 per routine to bound payload.
	// Both are best-effort — if the lookup fails we log and keep the
	// list minus the enrichment rather than failing the whole call.
	enrichPipelineListAuthorNames(r.Context(), h.db, h.logger, out)
	enrichPipelineListLinkedIssues(r.Context(), h.db, h.logger, workspaceID, out)
	writeJSON(w, http.StatusOK, out)
}

// Get returns a single pipeline by slug, including its definition.
// GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}
func (h *PipelineHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline get", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	writeJSON(w, http.StatusOK, toPipelineResponse(p, true))
}

// Delete soft-deletes a pipeline by slug.
// DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}
func (h *PipelineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted (HIGH): MEMBER must NOT delete pipelines.
	// Sibling handlers (Save / Rollback / ImportPipeline) already
	// gate on canRole(role, "delete"|"manage"|"create"); Delete was
	// the gap LIVE-verified by A13.2 (MEMBER did MANAGER+ actions
	// on 8/8 endpoints).
	role := RoleFromContext(r.Context())
	if !canRole(role, "delete") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline delete: lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	if err := h.store.SoftDelete(r.Context(), p.ID); err != nil {
		h.logger.Error("pipeline delete", "error", err)
		replyError(w, http.StatusInternalServerError, "delete pipeline")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExportPipeline returns a portable bundle for a pipeline. The
// bundle is a self-contained JSON document a downstream workspace
// (or marketplace consumer) can import via POST .../import.
//
// Bundle shape (format = "crewship-pipeline-bundle/v1"):
//
//	{
//	  "format": "crewship-pipeline-bundle/v1",
//	  "pipeline": { name, description, definition, dsl_version,
//	                authored_via, change_summary },
//	  "history":  [{version, definition_hash, change_summary, ...}],
//	  "metadata": { exported_at, source_workspace_id, ... }
//	}
//
// We deliberately exclude author_crew_id, author_agent_id, runtime
// stats (invocation_count, last_invoked_at), and any
// installation-specific data — the receiving workspace will fill
// those in at import time. This keeps marketplace bundles
// installation-independent.
//
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/export
func (h *PipelineHandler) ExportPipeline(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	includeHistory := r.URL.Query().Get("include_history") == "1"
	type historyItem struct {
		Version       int             `json:"version"`
		Hash          string          `json:"definition_hash"`
		Definition    json.RawMessage `json:"definition,omitempty"`
		ChangeSummary string          `json:"change_summary,omitempty"`
		ParentVersion *int            `json:"parent_version,omitempty"`
		CreatedAt     string          `json:"created_at"`
	}
	var history []historyItem
	if includeHistory {
		versions, _ := h.store.ListVersions(r.Context(), p.ID, 500)
		for _, v := range versions {
			history = append(history, historyItem{
				Version:       v.Version,
				Hash:          v.DefinitionHash,
				Definition:    json.RawMessage(v.DefinitionJSON),
				ChangeSummary: v.ChangeSummary,
				ParentVersion: v.ParentVersion,
				CreatedAt:     v.CreatedAt.Format(time.RFC3339Nano),
			})
		}
	}
	bundle := map[string]any{
		"format": "crewship-pipeline-bundle/v1",
		"pipeline": map[string]any{
			"name":        p.Name,
			"description": p.Description,
			"slug":        p.Slug,
			"dsl_version": p.DSLVersion,
			"definition":  json.RawMessage(p.DefinitionJSON),
		},
		"metadata": map[string]any{
			"exported_at":         time.Now().UTC().Format(time.RFC3339Nano),
			"source_workspace_id": workspaceID,
			"definition_hash":     p.DefinitionHash,
			"head_version":        p.InvocationCount, // misnamed — leave for caller transparency
		},
	}
	if includeHistory {
		bundle["history"] = history
	}
	writeJSON(w, http.StatusOK, bundle)
}

// ImportPipeline creates a pipeline from a portable bundle. Used by
// marketplace install flows + cross-workspace transfer. The
// receiving workspace becomes the author crew context (via
// X-Author-Crew header or body field), and the bundle's source
// metadata is preserved on the pipeline row for audit.
//
// POST /api/v1/workspaces/{ws}/pipelines/import
// Body: <pipeline-bundle>  + { "author_crew_id": "..." }
func (h *PipelineHandler) ImportPipeline(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted: importing a pipeline is the same effective
	// privilege as Save (creates a new pipeline row). Same gate.
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	var bundle struct {
		Format   string `json:"format"`
		Pipeline struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Slug        string          `json:"slug"`
			DSLVersion  string          `json:"dsl_version"`
			Definition  json.RawMessage `json:"definition"`
		} `json:"pipeline"`
		Metadata map[string]any `json:"metadata"`
		// Caller-supplied author_crew_id; required since the
		// bundle deliberately doesn't carry one.
		AuthorCrewID string `json:"author_crew_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
		replyError(w, http.StatusBadRequest, "invalid bundle")
		return
	}
	if bundle.Format != "crewship-pipeline-bundle/v1" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "unsupported bundle format",
			"format": bundle.Format,
		})
		return
	}
	if bundle.Pipeline.Name == "" || len(bundle.Pipeline.Definition) == 0 {
		replyError(w, http.StatusBadRequest, "bundle missing pipeline.name or pipeline.definition")
		return
	}
	if bundle.AuthorCrewID == "" {
		replyError(w, http.StatusBadRequest, "author_crew_id required (the crew that will own this imported pipeline)")
		return
	}
	// Run validation at import — we don't want a malformed bundle
	// to land in the workspace registry. Cross-references against
	// the receiving workspace's agents are checked too.
	dsl, err := pipeline.Parse(bundle.Pipeline.Definition)
	if err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	agentSlugs, _ := h.lookupAgentSlugs(r, bundle.AuthorCrewID)
	pipelineSlugs, _ := h.lookupPipelineSlugs(r, workspaceID)
	if err := pipeline.Validate(dsl, agentSlugs, pipelineSlugs); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "imported pipeline failed validation: " + err.Error(),
			"hint":  "the receiving workspace must have all referenced agent slugs",
		})
		return
	}
	// Slug: prefer bundle's slug, fall back to slugifyName(name).
	slug := bundle.Pipeline.Slug
	if slug == "" {
		// Reuse the runtime helper — same shape as sidecar/CLI.
		slug = bundle.Pipeline.Name // best-effort; Save will reject
		// invalid shapes, which prompts the importer to rename.
	}
	importedFromURL := ""
	if bundle.Metadata != nil {
		if v, ok := bundle.Metadata["source_workspace_id"].(string); ok {
			importedFromURL = "workspace:" + v
		}
	}
	now := time.Now().UTC()
	in := pipeline.SaveInput{
		WorkspaceID:    workspaceID,
		Slug:           slug,
		Name:           bundle.Pipeline.Name,
		Description:    bundle.Pipeline.Description,
		DSLVersion:     bundle.Pipeline.DSLVersion,
		DefinitionJSON: string(bundle.Pipeline.Definition),
		Author: pipeline.AuthorMeta{
			CrewID:      bundle.AuthorCrewID,
			Via:         pipeline.AuthoredViaImported,
			ImportedURL: importedFromURL,
		},
		// Imports skip the test_run gate by design — a marketplace
		// bundle is presumed to have passed test_run in its source
		// workspace. The receiving operator can run a manual
		// test_run from the UI before invoking.
		LastTestRunAt:     &now,
		LastTestRunPassed: true,
	}
	saved, err := h.store.Save(r.Context(), in)
	if errors.Is(err, pipeline.ErrSlugConflict) {
		replyError(w, http.StatusConflict, "slug already exists in workspace")
		return
	}
	if err != nil {
		h.logger.Error("pipeline import save", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to import pipeline")
		return
	}
	writeJSON(w, http.StatusCreated, toPipelineResponse(saved, true))
}

// ListVersions returns the version history for a pipeline.
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/versions
func (h *PipelineHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline list versions: load", "error", err)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parseSmallInt(v); err == nil {
			limit = n
		}
	}
	versions, err := h.store.ListVersions(r.Context(), p.ID, limit)
	if err != nil {
		h.logger.Error("pipeline list versions: query", "error", err)
		replyError(w, http.StatusInternalServerError, "list versions")
		return
	}
	type versionRow struct {
		Version       int    `json:"version"`
		Hash          string `json:"definition_hash"`
		AuthorType    string `json:"author_type"`
		AuthorID      string `json:"author_id"`
		ParentVersion *int   `json:"parent_version,omitempty"`
		ChangeSummary string `json:"change_summary,omitempty"`
		CreatedAt     string `json:"created_at"`
	}
	out := make([]versionRow, 0, len(versions))
	for _, v := range versions {
		out = append(out, versionRow{
			Version:       v.Version,
			Hash:          v.DefinitionHash,
			AuthorType:    v.AuthorType,
			AuthorID:      v.AuthorID,
			ParentVersion: v.ParentVersion,
			ChangeSummary: v.ChangeSummary,
			CreatedAt:     v.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetVersion returns one specific version including the full DSL.
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/versions/{n}
func (h *PipelineHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	versionStr := r.PathValue("n")
	versionNum, perr := parseSmallInt(versionStr)
	if perr != nil {
		replyError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	v, err := h.store.GetVersion(r.Context(), p.ID, versionNum)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "version not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline get version", "pipeline_id", p.ID, "version", versionNum, "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to load pipeline version")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         v.Version,
		"definition_hash": v.DefinitionHash,
		"definition":      json.RawMessage(v.DefinitionJSON),
		"author_type":     v.AuthorType,
		"author_id":       v.AuthorID,
		"parent_version":  v.ParentVersion,
		"change_summary":  v.ChangeSummary,
		"created_at":      v.CreatedAt.Format(time.RFC3339Nano),
	})
}

// Rollback rolls the head pointer + definition_json back to a
// previous version. History is preserved (rollback doesn't delete).
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/rollback
// Body: { "version": N }
func (h *PipelineHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	// Audit M2-promoted: rollback rewrites the active definition --
	// destructive equivalent of an update. Manage tier matches Save's
	// gating once promoted (see Save below for create-vs-update split).
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	slug := r.PathValue("slug")
	var body struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Version < 1 {
		replyError(w, http.StatusBadRequest, "version must be >= 1")
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	rolled, err := h.store.Rollback(r.Context(), p.ID, body.Version)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "version not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline rollback", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to roll back pipeline")
		return
	}
	writeJSON(w, http.StatusOK, toPipelineResponse(rolled, true))
}

// internalSaveRequest carries the IPC body sidecar→main forwards
// for an agent emitting a new pipeline definition. Test-run gate
// is enforced by the handler — the sidecar must call test_run
// first and pass the resulting timestamp through.
type internalSaveRequest struct {
	WorkspaceID       string          `json:"workspace_id"`
	Slug              string          `json:"slug"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Definition        json.RawMessage `json:"definition"`
	AuthorCrewID      string          `json:"author_crew_id"`
	AuthorAgentID     string          `json:"author_agent_id"`
	AuthorChatID      string          `json:"author_chat_id"`
	AuthorRunID       string          `json:"author_run_id"`
	LastTestRunAt     string          `json:"last_test_run_at"` // RFC3339
	LastTestRunPassed bool            `json:"last_test_run_passed"`
}

// userSaveRequest is the body shape for the workspace-scoped save
// endpoint. Workspace_id comes from the path (wsCtx middleware), not
// the body. Author identity is inferred from the JWT — user_id +
// authored_via = "user_api". The optional author_crew_id lets UI
// authors pin a specific crew context for runtime; without it, runs
// fall back to the first crew the saving user belongs to.
type userSaveRequest struct {
	Slug              string          `json:"slug"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Definition        json.RawMessage `json:"definition"`
	AuthorCrewID      string          `json:"author_crew_id,omitempty"`
	LastTestRunAt     string          `json:"last_test_run_at,omitempty"` // RFC3339
	LastTestRunPassed bool            `json:"last_test_run_passed,omitempty"`
	// SkipTestGate is honored only when the caller's role is
	// OWNER or ADMIN; lower roles get a 403 if they try. Used by
	// UI flows that have already test-run'd the definition through
	// the /test_run endpoint and pass last_test_run_at + true here.
	SkipTestGate bool `json:"skip_test_gate,omitempty"`
	// SaveToken is the HMAC-signed proof returned by /test_run that
	// THIS user just successfully ran THIS definition_hash. When
	// present and valid, supersedes the body-trust on
	// last_test_run_at — that field can be omitted entirely. See
	// pipelines_save_token.go for the threat model rationale.
	SaveToken string `json:"save_token,omitempty"`
}

// Save is the workspace-scoped save endpoint that backs the UI's
// "New routine" flow. JWT auth + MANAGER+ role required. The
// distinction from InternalSave: author identity is extracted from
// the user context (not trusted from the body), and authored_via is
// always "user_api" so audit logs show real human authorship.
//
// POST /api/v1/workspaces/{wsId}/pipelines/save
func (h *PipelineHandler) Save(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to save routines")
		return
	}

	var body userSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Slug == "" || len(body.Definition) == 0 {
		replyError(w, http.StatusBadRequest, "slug + definition required")
		return
	}
	if body.SkipTestGate && role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "skip_test_gate requires OWNER or ADMIN role",
		})
		return
	}

	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// If author_crew_id is provided, validate the user's agent slugs
	// against THAT crew (cross-crew validation). If absent, skip
	// agent-slug validation so the routine saves with whatever the
	// DSL declares — runtime resolution at first invocation surfaces
	// any mismatch with a clear error.
	var agentSlugs map[string]struct{}
	if body.AuthorCrewID != "" {
		var lookupErr error
		agentSlugs, lookupErr = h.lookupAgentSlugs(r, body.AuthorCrewID)
		if lookupErr != nil {
			h.logger.Warn("pipeline user save: lookup agent slugs", "error", lookupErr, "crew", body.AuthorCrewID)
			agentSlugs = nil
		}
	}
	pipelineSlugs, err := h.lookupPipelineSlugs(r, workspaceID)
	if err != nil {
		h.logger.Warn("pipeline user save: lookup pipeline slugs", "error", err, "workspace", workspaceID)
		pipelineSlugs = nil
	}
	if err := pipeline.Validate(dsl, agentSlugs, pipelineSlugs); err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := pipeline.CycleDetect(dsl, h.cycleResolver(r.Context(), workspaceID)); err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	in := pipeline.SaveInput{
		WorkspaceID:    workspaceID,
		Slug:           body.Slug,
		Name:           body.Name,
		Description:    body.Description,
		DefinitionJSON: string(body.Definition),
		Author: pipeline.AuthorMeta{
			CrewID: body.AuthorCrewID,
			UserID: user.ID,
			Via:    pipeline.AuthoredViaUser,
		},
		LastTestRunPassed: body.LastTestRunPassed || body.SkipTestGate,
	}

	// Three paths to clearing the test-gate gate, in priority order:
	// 1. SaveToken (HMAC, no body trust) — preferred path
	// 2. SkipTestGate (OWNER/ADMIN role-gated escape hatch)
	// 3. body's last_test_run_at + last_test_run_passed (legacy body
	//    trust, kept for sidecar back-compat; will be retired once all
	//    callers migrate to SaveToken).
	switch {
	case body.SaveToken != "":
		defHash := definitionHashHex(body.Definition)
		if err := verifySaveToken(h.saveTokenSecret, body.SaveToken, workspaceID, defHash, user.ID); err != nil {
			h.logger.Warn("pipeline save: save_token rejected", "user_id", user.ID, "slug", body.Slug, "err", err)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "save_token invalid (expired, malformed, or signed for a different definition/user)",
			})
			return
		}
		// Token verified — synthesize a passing-now timestamp so the
		// store gate doesn't fire on the body's missing fields.
		now := time.Now().UTC()
		in.LastTestRunAt = &now
		in.LastTestRunPassed = true
		h.logger.Info("pipeline save: cleared via save_token", "user_id", user.ID, "slug", body.Slug)
	case body.SkipTestGate:
		now := time.Now().UTC()
		in.LastTestRunAt = &now
		h.logger.Info("pipeline save: test gate skipped", "user_id", user.ID, "role", role, "slug", body.Slug)
	default:
		if t, err := parseRFC3339(body.LastTestRunAt); err == nil {
			in.LastTestRunAt = &t
		}
	}

	saved, err := h.store.Save(r.Context(), in)
	if errors.Is(err, pipeline.ErrTestRunGateFailed) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "save requires a fresh, passing test_run within 5 minutes (or skip_test_gate for OWNER/ADMIN)",
		})
		return
	}
	if errors.Is(err, pipeline.ErrSlugConflict) {
		replyError(w, http.StatusConflict, "slug already exists in workspace")
		return
	}
	if err != nil {
		h.logger.Error("pipeline user save", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to save pipeline")
		return
	}
	writeJSON(w, http.StatusCreated, toPipelineResponse(saved, true))
}

// InternalSave is the trusted endpoint the sidecar forwards to.
// X-Internal-Token authentication runs upstream of this handler;
// here we just trust the caller's claim about author identity.
//
// POST /api/v1/internal/pipelines/save
func (h *PipelineHandler) InternalSave(w http.ResponseWriter, r *http.Request) {
	var body internalSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.WorkspaceID == "" || body.Slug == "" || len(body.Definition) == 0 {
		replyError(w, http.StatusBadRequest, "workspace_id, slug, definition required")
		return
	}

	// Parse + validate before save so the agent gets a clean error
	// message at this layer rather than at the next /run.
	//
	// Semantic checks: pass real agent + pipeline slug sets so the
	// validator catches cross-crew references (agent_slug not in the
	// author crew, call_pipeline target not in the workspace) at
	// save time rather than letting them blow up at first run.
	// Cycle detection runs in a separate pass with a workspace-
	// scoped resolver since CycleDetect needs to walk the call
	// graph beyond `dsl` itself.
	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	agentSlugs, err := h.lookupAgentSlugs(r, body.AuthorCrewID)
	if err != nil {
		h.logger.Warn("pipeline internal save: lookup agent slugs", "error", err, "crew", body.AuthorCrewID)
		// Non-fatal: fall back to nil-set validation (the original
		// schema-only path) rather than blocking the save on a
		// crew lookup hiccup. The runtime still surfaces unknown
		// agent_slug at first invocation.
		agentSlugs = nil
	}
	pipelineSlugs, err := h.lookupPipelineSlugs(r, body.WorkspaceID)
	if err != nil {
		h.logger.Warn("pipeline internal save: lookup pipeline slugs", "error", err, "workspace", body.WorkspaceID)
		pipelineSlugs = nil
	}
	if err := pipeline.Validate(dsl, agentSlugs, pipelineSlugs); err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Cycle detection over the workspace's saved pipelines plus this
	// candidate. The resolver loads target DSLs lazily; nodes that
	// aren't in the workspace yet stop the walk on that branch (no
	// false positives — see pipeline.CycleDetect docstring).
	if err := pipeline.CycleDetect(dsl, h.cycleResolver(r.Context(), body.WorkspaceID)); err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	in := pipeline.SaveInput{
		WorkspaceID:    body.WorkspaceID,
		Slug:           body.Slug,
		Name:           body.Name,
		Description:    body.Description,
		DefinitionJSON: string(body.Definition),
		Author: pipeline.AuthorMeta{
			CrewID:  body.AuthorCrewID,
			AgentID: body.AuthorAgentID,
			ChatID:  body.AuthorChatID,
			RunID:   body.AuthorRunID,
			Via:     pipeline.AuthoredViaAgent,
		},
		LastTestRunPassed: body.LastTestRunPassed,
	}
	if t, err := parseRFC3339(body.LastTestRunAt); err == nil {
		in.LastTestRunAt = &t
	}

	saved, err := h.store.Save(r.Context(), in)
	if errors.Is(err, pipeline.ErrTestRunGateFailed) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "save requires a fresh, passing test_run (within 5 minutes)",
		})
		return
	}
	if errors.Is(err, pipeline.ErrSlugConflict) {
		replyError(w, http.StatusConflict, "slug already exists in workspace")
		return
	}
	if err != nil {
		h.logger.Error("pipeline internal save", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to save pipeline")
		return
	}
	writeJSON(w, http.StatusCreated, toPipelineResponse(saved, true))
}
