package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func (h *PageHandler) SetProjectStore(store *pages.ProjectStore) *PageHandler {
	h.projectStore = store
	if store != nil {
		h.pageArtifacts = &pagebuild.Store{Directory: filepath.Join(store.Directory, "artifacts")}
	}
	return h
}

type pageProjectDraft struct {
	GitCommit  string               `json:"git_commit"`
	Revision   int64                `json:"revision"`
	Digest     string               `json:"digest"`
	Definition pages.Document       `json:"definition"`
	Project    *pages.SourceProject `json:"project" yaml:"project"`
}

func (h *PageHandler) projectPage(w http.ResponseWriter, r *http.Request) (*pageRecord, bool) {
	u := UserFromContext(r.Context())
	if u == nil && projectAgentFrom(r.Context()) == nil {
		replyError(w, 401, "Unauthorized")
		return nil, false
	}
	rec, err := h.loadPage(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load project page", err)
		return nil, false
	}
	allowed := false
	if actor := projectAgentFrom(r.Context()); actor != nil {
		allowed = actor.WorkspaceID == WorkspaceIDFromContext(r.Context()) && actor.OwnerCrewID != "" && rec.OwnerCrewID == actor.OwnerCrewID
	} else if u != nil {
		allowed = h.mayEditSpec(r.Context(), WorkspaceIDFromContext(r.Context()), u.ID, RoleFromContext(r.Context()), rec)
	}
	if !allowed {
		replyError(w, 403, "reading or editing project sources requires page edit permission")
		return nil, false
	}
	if h.projectStore == nil {
		replyError(w, 503, "Page project storage is not configured")
		return nil, false
	}
	return rec, true
}

func (h *PageHandler) loadProject(r *http.Request, rec *pageRecord) (*pageProjectDraft, error) {
	var d pageProjectDraft
	var spec string
	err := h.db.QueryRowContext(r.Context(), `SELECT revision, source_digest, spec_json, git_commit FROM page_project_drafts WHERE page_id = ?`, rec.ID).Scan(&d.Revision, &d.Digest, &spec, &d.GitCommit)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(spec), &d.Definition); err != nil {
		return nil, err
	}
	d.Project, err = h.projectStore.Get(r.Context(), WorkspaceIDFromContext(r.Context()), d.Digest)
	return &d, err
}

func (h *PageHandler) GetProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	d, err := h.loadProject(r, rec)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page has no project draft")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read project draft", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, d)
}

// PutProject attaches source to an existing Page, or changes its draft against
// an expected revision. The live Page spec and its actions remain unchanged.
func (h *PageHandler) PutProject(w http.ResponseWriter, r *http.Request) {
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
	b, ok := readCapped(w, r, pages.MaxTransferBytes, "Page project")
	if !ok {
		return
	}
	var req struct {
		Definition       *pages.Document      `json:"definition,omitempty" yaml:"definition,omitempty"`
		ExpectedRevision *int64               `json:"expected_revision" yaml:"expected_revision"`
		Project          *pages.SourceProject `json:"project" yaml:"project"`
	}
	if err := pages.DecodeProjectJSON(b, &req); err != nil || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 {
		replyError(w, 400, "expected_revision and a valid project are required")
		return
	}
	// The source parser rejects duplicate keys and aliases as well as validating
	// the file payload. JSON outer fields are deliberately limited above.
	if _, err := req.Project.MarshalYAMLSource(); err != nil {
		replyError(w, 422, err.Error())
		return
	}
	if err := pageprofile.ValidateSourcePaths(req.Project); err != nil {
		replyError(w, 422, err.Error())
		return
	}
	var rev int64
	var spec, parent string
	err := h.db.QueryRowContext(r.Context(), `SELECT revision, spec_json, git_commit FROM page_project_drafts WHERE page_id=?`, rec.ID).Scan(&rev, &spec, &parent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read draft revision", err)
		return
	}
	if rev != *req.ExpectedRevision {
		replyError(w, 409, "project revision changed; reload before saving")
		return
	}
	if rev == 0 && req.Definition == nil {
		doc, ok := h.currentDocument(w, rec)
		if !ok {
			return
		}
		for i := range doc.Spec.Panels {
			doc.Spec.Panels[i].Public = false
		}
		if err := validatePortableDraft(doc); err != nil {
			replyError(w, 422, err.Error())
			return
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			replyInternalError(w, h.logger, "encode draft definition", err)
			return
		}
		spec = string(raw)
	}
	if req.Definition != nil {
		if req.Definition.Metadata.Slug != rec.Slug {
			replyError(w, 422, "draft definition must keep the page slug")
			return
		}
		if err := validatePortableDraft(req.Definition); err != nil {
			replyError(w, 422, err.Error())
			return
		}
		owner := ""
		if rec.OwnerCrewID != "" {
			owner = h.ownerRef(r.Context(), rec)
		}
		_, _, missing, err := h.bindPageReferences(r, WorkspaceIDFromContext(r.Context()), req.Definition, owner)
		if err != nil {
			replyInternalError(w, h.logger, "validate draft bindings", err)
			return
		}
		if len(missing) > 0 {
			writeJSON(w, 422, map[string]any{"error": pageUnresolvedMessage(missing), "unresolved": missing})
			return
		}
		raw, err := json.Marshal(req.Definition)
		if err != nil {
			replyError(w, 422, "invalid draft definition")
			return
		}
		spec = string(raw)
	}
	if len(spec) > pages.MaxSpecBytes {
		replyError(w, 422, "draft definition exceeds spec limit")
		return
	}
	digest, err := h.projectStore.Put(r.Context(), WorkspaceIDFromContext(r.Context()), req.Project)
	if err != nil {
		h.projectStoreError(w, err)
		return
	}
	actorUser, actorLabel, actorJSON := projectAuthor(r)
	var retainedRevisions int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_project_revisions WHERE page_id=?`, rec.ID).Scan(&retainedRevisions); err != nil {
		replyInternalError(w, h.logger, "count retained source revisions", err)
		return
	}
	if retainedRevisions >= 512 {
		replyError(w, 507, "Page source revision quota reached")
		return
	}
	commit, err := h.projectStore.Checkpoint(r.Context(), WorkspaceIDFromContext(r.Context()), rec.ID, parent, spec, actorLabel, rev+1, req.Project)
	if err != nil {
		h.projectStoreError(w, err)
		return
	}
	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin draft save", err)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(r.Context(), `INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_by,updated_at,git_commit)
VALUES(?,?,1,?,NULLIF(?,''),?,?) ON CONFLICT(page_id) DO UPDATE SET source_digest=excluded.source_digest,spec_json=excluded.spec_json,revision=page_project_drafts.revision+1,updated_by=excluded.updated_by,updated_at=excluded.updated_at,git_commit=excluded.git_commit WHERE page_project_drafts.revision=?`, rec.ID, digest, spec, actorUser, now, commit, rev)
	if err != nil {
		replyInternalError(w, h.logger, "save project draft", err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "save project revision", err)
		return
	}
	if n != 1 {
		replyError(w, 409, "project revision changed; reload before saving")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_project_revisions(page_id,revision,source_digest,actor_user_id,created_at,git_commit,spec_json,actor_json) VALUES(?,?,?,NULLIF(?,''),?,?,?,?)`, rec.ID, rev+1, digest, actorUser, now, commit, spec, actorJSON); err != nil {
		replyInternalError(w, h.logger, "record draft revision", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit draft save", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()), "page.updated", map[string]any{"slug": rec.Slug})
	writeJSON(w, 200, map[string]any{"revision": rev + 1, "digest": digest, "git_commit": commit, "state": "draft"})
}

func (h *PageHandler) projectStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, pages.ErrProjectStoreFull) || errors.Is(err, pages.ErrProjectGitFull) {
		replyError(w, 507, err.Error())
		return
	}
	replyInternalError(w, h.logger, "Page project storage", err)
}

func validatePortableDraft(doc *pages.Document) error {
	if err := doc.Validate(); err != nil {
		return err
	}
	for _, p := range doc.Spec.Panels {
		if p.Public {
			return fmt.Errorf("panel %q: project import cannot publish panels", p.ID)
		}
		for _, a := range p.Actions {
			if a.Kind == pages.ActionLink || a.Kind == pages.ActionCustom {
				return fmt.Errorf("panel %q: action %q is not yet portable in project bundles", p.ID, a.ID)
			}
		}
	}
	return nil
}
