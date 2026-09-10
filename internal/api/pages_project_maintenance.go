package api

import (
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pages"
)

// CompactPageProjects uses the workspace administrator gate because retention
// affects every Page in the workspace, never just one author's project.
func (h *PageHandler) CompactPageProjects(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if UserFromContext(r.Context()) == nil || (role != "OWNER" && role != "ADMIN") {
		replyError(w, 403, "Page storage maintenance requires workspace administration")
		return
	}
	if h.projectStore == nil {
		replyError(w, 503, "Page project storage is not configured")
		return
	}
	data, ok := readCapped(w, r, 1024, "Page maintenance")
	if !ok {
		return
	}
	var req struct {
		DiscardHistory bool `json:"discard_history" yaml:"discard_history"`
		Confirm        bool `json:"confirm" yaml:"confirm"`
	}
	if err := pages.DecodeProjectJSON(data, &req); err != nil {
		replyError(w, 400, "Invalid Page maintenance request")
		return
	}
	if req.DiscardHistory && !req.Confirm {
		replyError(w, 400, "Discarding optional workspace history requires confirm=true")
		return
	}
	if err := h.retainPageProjects(r.Context(), WorkspaceIDFromContext(r.Context()), req.DiscardHistory); err != nil {
		replyInternalError(w, h.logger, "compact Page workspace storage", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"compacted": true, "discarded_optional_history": req.DiscardHistory, "preserved": "current drafts, live publication pointers and running builds"})
}

// VerifyProjectStorage checks SQL roots against immutable source, Git and
// artifact contents. It does not publish, repair or expose executable payloads.
func (h *PageHandler) VerifyProjectStorage(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	release, ok := h.pageLease(w, r)
	if !ok {
		return
	}
	defer release()
	ws := WorkspaceIDFromContext(r.Context())
	releaseGit, err := h.projectStore.GitLease(r.Context(), ws, false)
	if err != nil {
		replyError(w, 503, "Page Git maintenance is busy; try again shortly")
		return
	}
	defer releaseGit()
	rows, err := h.db.QueryContext(r.Context(), `SELECT source_digest,git_commit,spec_json,'' FROM page_project_drafts WHERE page_id=? UNION SELECT source_digest,git_commit,spec_json,'' FROM page_project_revisions WHERE page_id=? UNION SELECT source_digest,'','',COALESCE(artifact_digest,'') FROM page_project_builds WHERE page_id=? UNION SELECT source_digest,git_commit,spec_json,artifact_digest FROM page_project_publications WHERE page_id=?`, rec.ID, rec.ID, rec.ID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read Page storage roots", err)
		return
	}
	defer rows.Close()
	type root struct{ source, commit, spec, artifact string }
	var roots []root
	for rows.Next() {
		var row root
		if err := rows.Scan(&row.source, &row.commit, &row.spec, &row.artifact); err != nil {
			replyInternalError(w, h.logger, "read Page storage root", err)
			return
		}
		roots = append(roots, row)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "read Page storage roots", err)
		return
	}
	rows.Close()
	failures := make([]map[string]string, 0)
	sources, commits, artifacts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	add := func(kind, id string, err error) {
		if err != nil {
			failures = append(failures, map[string]string{"kind": kind, "id": id, "error": err.Error()})
		}
	}
	for _, row := range roots {
		if row.source != "" && !sources[row.source] {
			_, err := h.projectStore.Get(r.Context(), ws, row.source)
			add("source", row.source, err)
			sources[row.source] = true
		}
		if row.commit != "" {
			source, spec, err := h.projectStore.ReadCheckpoint(r.Context(), ws, rec.ID, row.commit)
			if err == nil {
				digest, digestErr := source.Digest()
				err = digestErr
				if err == nil && (digest != row.source || spec != row.spec) {
					err = errors.New("checkpoint contents differ from their SQL source/definition")
				}
			}
			add("checkpoint", row.commit, err)
			commits[row.commit] = true
		}
		if row.artifact != "" && !artifacts[row.artifact] {
			_, err := h.pageArtifacts.Get(r.Context(), ws, row.artifact)
			add("artifact", row.artifact, err)
			artifacts[row.artifact] = true
		}
	}
	checkedObjects := 0
	ids := make([]string, 0, len(commits))
	for id := range commits {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		err := h.projectStore.VisitGitObjects(r.Context(), ws, rec.ID, ids, func(_, _ string, _ []byte) error { checkedObjects++; return nil })
		add("git_objects", rec.ID, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"healthy": len(failures) == 0, "checked_sources": len(sources), "checked_checkpoints": len(commits), "checked_artifacts": len(artifacts), "checked_git_objects": checkedObjects, "failures": failures})
}
