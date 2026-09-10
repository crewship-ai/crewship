package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pages"
)

type pageProjectRevision struct {
	Revision  int64  `json:"revision" yaml:"revision"`
	Digest    string `json:"digest"`
	GitCommit string `json:"git_commit"`
	// Actor is the historical opaque string: a user id OR an agent id, with
	// nothing in it that says which. Kept verbatim for existing callers.
	Actor string `json:"actor,omitempty"`
	// ActorKind is what Actor could never carry: "user", "agent", "crew" or
	// "unknown", read from the same actor_json the review snapshot reads. A
	// list that flattens a person and a container into one string cannot be
	// rendered honestly, and guessing from the id's shape is guessing.
	ActorKind  string `json:"actor_kind"`
	CreatedAt  string `json:"created_at"`
	Restorable bool   `json:"restorable"`
}

func (h *PageHandler) ProjectHistory(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			replyError(w, 400, "before must be a positive revision")
			return
		}
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT revision,source_digest,git_commit,COALESCE(actor_user_id,json_extract(actor_json,'$.agent_id'),''),COALESCE(actor_user_id,''),actor_json,created_at,spec_json!='' FROM page_project_revisions WHERE page_id=? AND (?=0 OR revision<?) ORDER BY revision DESC LIMIT 51`, rec.ID, before, before)
	if err != nil {
		replyInternalError(w, h.logger, "read project history", err)
		return
	}
	defer rows.Close()
	result := make([]pageProjectRevision, 0)
	for rows.Next() {
		var v pageProjectRevision
		var actorUser, actorJSON string
		if err := rows.Scan(&v.Revision, &v.Digest, &v.GitCommit, &v.Actor, &actorUser, &actorJSON, &v.CreatedAt, &v.Restorable); err != nil {
			replyInternalError(w, h.logger, "read project revision", err)
			return
		}
		// Kind only: this list renders no labels, and resolving one would be a
		// directory read per row whose result is then discarded.
		v.ActorKind = reviewActorKind(actorUser, actorJSON).Kind
		result = append(result, v)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "read project history", err)
		return
	}
	var next int64
	if len(result) > 50 {
		result = result[:50]
		next = result[49].Revision
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"revisions": result, "next_before": next})
}

func (h *PageHandler) projectRevision(w http.ResponseWriter, r *http.Request, rec *pageRecord, revision int64) (*pageProjectDraft, bool) {
	var d pageProjectDraft
	var spec string
	err := h.db.QueryRowContext(r.Context(), `SELECT revision,source_digest,git_commit,spec_json FROM page_project_revisions WHERE page_id=? AND revision=?`, rec.ID, revision).Scan(&d.Revision, &d.Digest, &d.GitCommit, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "project revision not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "read project revision", err)
		return nil, false
	}
	if spec == "" {
		replyError(w, 409, "This legacy revision has no saved Page definition; export its source from the existing snapshot archive")
		return nil, false
	}
	if d.GitCommit != "" {
		var archived string
		d.Project, archived, err = h.projectStore.ReadCheckpoint(r.Context(), WorkspaceIDFromContext(r.Context()), rec.ID, d.GitCommit)
		if err == nil && archived != spec {
			err = errors.New("checkpoint definition differs from revision record")
		}
	} else {
		d.Project, err = h.projectStore.Get(r.Context(), WorkspaceIDFromContext(r.Context()), d.Digest)
	}
	if err != nil {
		replyInternalError(w, h.logger, "read archived project", err)
		return nil, false
	}
	digest, err := d.Project.Digest()
	if err != nil || digest != d.Digest {
		replyError(w, 500, "project revision failed integrity verification")
		return nil, false
	}
	if err := json.Unmarshal([]byte(spec), &d.Definition); err != nil {
		replyInternalError(w, h.logger, "read archived definition", err)
		return nil, false
	}
	return &d, true
}
func (h *PageHandler) GetProjectRevision(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		replyError(w, 400, "revision must be positive")
		return
	}
	d, ok := h.projectRevision(w, r, rec, revision)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, d)
}
func (h *PageHandler) RestoreProject(w http.ResponseWriter, r *http.Request) {
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
	raw, ok := readCapped(w, r, 1024, "restore project")
	if !ok {
		return
	}
	var request struct {
		ExpectedRevision *int64 `json:"expected_revision" yaml:"expected_revision"`
		Revision         int64  `json:"revision" yaml:"revision"`
	}
	if err := pages.DecodeProjectJSON(raw, &request); err != nil || request.ExpectedRevision == nil || *request.ExpectedRevision < 1 || request.Revision < 1 {
		replyError(w, 400, "revision and expected_revision are required")
		return
	}
	d, ok := h.projectRevision(w, r, rec, request.Revision)
	if !ok {
		return
	}
	// Use the regular save path: revalidate bindings, reauthorize, CAS, commit and
	// audit. Restoring is a new draft save, never a publication or a ref reset.
	body, err := json.Marshal(map[string]any{"expected_revision": *request.ExpectedRevision, "project": d.Project, "definition": d.Definition})
	if err != nil {
		replyInternalError(w, h.logger, "encode restored draft", err)
		return
	}
	copy := r.Clone(r.Context())
	copy.Body = io.NopCloser(bytes.NewReader(body))
	copy.ContentLength = int64(len(body))
	h.PutProject(w, copy)
}
