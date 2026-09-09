package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

type pageBuildCoordinator struct {
	worker pagebuild.Builder
	store  *pagebuild.Store
	slot   chan struct{}
}

func (h *PageHandler) SetBuildWorker(worker pagebuild.Builder, store *pagebuild.Store) *PageHandler {
	h.pageArtifacts = store
	h.builds = &pageBuildCoordinator{worker: worker, store: store, slot: make(chan struct{}, 1)}
	return h
}

// A server restart cannot certify the output of an interrupted worker. Its
// independent container deadline bounds orphan lifetime; a new build is explicit.
func (h *PageHandler) recoverPageBuilds(ctx context.Context) error {
	_, err := h.db.ExecContext(ctx, `UPDATE page_project_builds SET state='interrupted',error='Server restarted during build; start a new preview build',completed_at=? WHERE state='running'`, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type pageBuildRecord struct {
	ID             string `json:"id"`
	SourceRevision int64  `json:"source_revision"`
	SourceDigest   string `json:"source_digest"`
	State          string `json:"state"`
	ArtifactDigest string `json:"artifact_digest,omitempty"`
	Error          string `json:"error,omitempty"`
	CreatedAt      string `json:"created_at"`
	CompletedAt    string `json:"completed_at,omitempty"`
}

func (h *PageHandler) BuildProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	if !h.prepareProjectWrite(w, r) {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	if h.builds == nil {
		replyError(w, 503, "Page build worker is not configured")
		return
	}
	body, ok := readCapped(w, r, 1024, "Page build request")
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision int64 `json:"expected_revision" yaml:"expected_revision"`
	}
	if err := pages.DecodeProjectJSON(body, &req); err != nil || req.ExpectedRevision < 1 {
		replyError(w, 400, "expected_revision is required")
		return
	}
	// Admit before decoding the stored source so concurrent callers fail cheaply.
	coordinator := h.builds
	select {
	case coordinator.slot <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "5")
		replyError(w, 429, "A Page build is already running; retry when it finishes")
		return
	}
	started := false
	defer func() {
		if !started {
			<-coordinator.slot
		}
	}()
	draft, err := h.loadProject(r, rec)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page has no project draft")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load build source", err)
		return
	}
	if draft.Revision != req.ExpectedRevision {
		replyError(w, 409, "project revision changed; reload before building")
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	var count int
	if err := h.db.QueryRowContext(r.Context(), `SELECT count(*) FROM page_project_builds b JOIN pages p ON p.id=b.page_id WHERE p.workspace_id=?`, ws).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "count Page builds", err)
		return
	}
	if count >= 512 {
		replyError(w, 507, "Page build history quota reached")
		return
	}
	job := pageBuildRecord{ID: generateCUID(), SourceRevision: draft.Revision, SourceDigest: draft.Digest, State: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	actorUser, _, actorJSON := projectAuthor(r)
	if _, err := h.db.ExecContext(r.Context(), `INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,requested_by,created_at,actor_json) VALUES(?,?,?,?,?,NULLIF(?,''),?,?)`, job.ID, rec.ID, job.SourceRevision, job.SourceDigest, job.State, actorUser, job.CreatedAt, actorJSON); err != nil {
		replyInternalError(w, h.logger, "create Page build", err)
		return
	}
	started = true
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		h.runPageBuild(coordinator, ws, rec.Slug, job.ID, draft.Project)
	}()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 202, job)
}
func (h *PageHandler) runPageBuild(c *pageBuildCoordinator, ws, slug, id string, p *pages.SourceProject) {
	defer func() { <-c.slot }()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	artifact, buildErr := c.worker.Build(ctx, p)
	cancel()
	digest, state, message := "", "ready", ""
	if buildErr != nil {
		// Compiler diagnostics are authoritative; maintenance must not overwrite them.
		state, message = "failed", buildErr.Error()
	} else {
		persistCtx, stopPersist := context.WithTimeout(context.Background(), 2*time.Minute)
		var release func()
		var persistErr error
		for persistCtx.Err() == nil {
			release, persistErr = h.projectStore.Lease(persistCtx, ws, false)
			if persistErr == nil || !errors.Is(persistErr, context.DeadlineExceeded) {
				break
			}
		}
		if release != nil {
			digest, persistErr = c.store.Put(persistCtx, ws, artifact)
		}
		// Keep the lease through the SQL pointer update below.
		if release != nil {
			defer release()
		}
		stopPersist()
		if persistErr != nil {
			state, message = "interrupted", "Build completed but its artifact could not be saved; retry the build after storage recovers: "+persistErr.Error()
		}
	}
	if len(message) > 64<<10 {
		message = message[:64<<10]
	}
	saveCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if _, saveErr := h.db.ExecContext(saveCtx, `UPDATE page_project_builds SET state=?,artifact_digest=NULLIF(?,''),error=?,completed_at=? WHERE id=? AND state='running'`, state, digest, message, time.Now().UTC().Format(time.RFC3339Nano), id); saveErr != nil {
		h.logger.Error("record Page build result", "build_id", id, "error", saveErr)
		return
	}
	broadcastWorkspaceEvent(h.hub, ws, "page.updated", map[string]any{"slug": slug})
}

// GetProjectPreview returns only an editor-authorized candidate. No public asset
// URL is introduced: the host fetches JSON and boots an opaque sandbox frame.
func (h *PageHandler) GetProjectPreview(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	if h.builds == nil {
		replyError(w, 503, "Page build worker is not configured")
		return
	}
	var revision int64
	err := h.db.QueryRowContext(r.Context(), `SELECT revision FROM page_project_drafts WHERE page_id=?`, rec.ID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page has no project draft")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read preview revision", err)
		return
	}
	var job pageBuildRecord
	err = h.db.QueryRowContext(r.Context(), `SELECT id,source_revision,source_digest,state,COALESCE(artifact_digest,''),error,created_at,COALESCE(completed_at,'') FROM page_project_builds WHERE page_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, rec.ID).Scan(&job.ID, &job.SourceRevision, &job.SourceDigest, &job.State, &job.ArtifactDigest, &job.Error, &job.CreatedAt, &job.CompletedAt)
	runtimeURL := ""
	if h.pageRuntimeOrigin != "" {
		// Also check the actual API host: a reverse-proxy alias must not silently
		// put the runtime back on the Studio site validated in configuration.
		scheme := "http://"
		if strings.HasPrefix(h.pageRuntimeOrigin, "https://") {
			scheme = "https://"
		}
		if err := pagebuild.ValidateRuntimeOriginForDevelopment(h.pageRuntimeOrigin, scheme+r.Host, h.pageRuntimeDevelopmentSameOrigin); err != nil {
			replyError(w, 503, "Page preview domain does not isolate this Studio host")
			return
		}
		runtimeURL = strings.TrimSuffix(h.pageRuntimeOrigin, "/") + pagebuild.RuntimePath
	}
	result := map[string]any{"revision": revision, "build": nil, "runtime_url": runtimeURL, "development_same_origin": h.pageRuntimeDevelopmentSameOrigin}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read Page build", err)
		return
	}
	if err == nil {
		result["build"] = job
		if job.State == "ready" && projectAgentFrom(r.Context()) == nil {
			artifact, err := h.builds.store.Get(r.Context(), WorkspaceIDFromContext(r.Context()), job.ArtifactDigest)
			if err != nil {
				replyInternalError(w, h.logger, "read Page preview artifact", err)
				return
			}
			result["artifact"] = artifact
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

// PageRuntime exposes only fixed bootstrap HTML, never project code or data.
func (h *PageHandler) PageRuntime(w http.ResponseWriter, r *http.Request) {
	if h.pageRuntimeOrigin == "" || !pagebuild.RuntimeMatchesHost(h.pageRuntimeOrigin, r.Host) {
		replyError(w, 404, "Page runtime is not served on this host")
		return
	}
	pagebuild.ServeRuntime(w, h.pageStudioOrigin)
}
