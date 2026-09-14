package api

import (
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pages"
)

// A write grant does not widen panel visibility. Whole-document authoring must
// therefore refuse partial readers, rather than handing them a filtered
// document that a subsequent PUT could mistake for a deliberate deletion.
func (h *PageHandler) requireProjectDefinitions(w http.ResponseWriter, r *http.Request, definitions ...*pages.Document) bool {
	auth, err := h.reviewDefinitionAuthorizer(r.Context(), WorkspaceIDFromContext(r.Context()))
	if err != nil {
		replyInternalError(w, h.logger, "authorize project definition", err)
		return false
	}
	for _, doc := range definitions {
		if doc == nil {
			continue
		}
		for _, panel := range doc.Spec.Panels {
			if !auth.visible(panel.Owner) {
				replyError(w, http.StatusForbidden, "Whole-document authoring requires access to every panel in the document. Ask a workspace administrator to read or edit it.")
				return false
			}
		}
	}
	return true
}

func (h *PageHandler) requireStoredProjectDefinitions(w http.ResponseWriter, r *http.Request, specs ...string) bool {
	for _, spec := range specs {
		if spec == "" {
			continue
		}
		var doc pages.Document
		if err := json.Unmarshal([]byte(spec), &doc); err != nil {
			replyInternalError(w, h.logger, "read project definition for authorization", err)
			return false
		}
		if !h.requireProjectDefinitions(w, r, &doc) {
			return false
		}
	}
	return true
}

// Source review has Page edit authority, just like existing source access, but
// never bundles a panel declaration. The sources are page-wide authored code;
// this endpoint does not claim to redact references authors put in that code.
type pageProjectSource struct {
	GitCommit string               `json:"git_commit"`
	Revision  int64                `json:"revision"`
	Digest    string               `json:"digest"`
	Project   *pages.SourceProject `json:"project"`
}

func projectSource(d *pageProjectDraft) pageProjectSource {
	return pageProjectSource{d.GitCommit, d.Revision, d.Digest, d.Project}
}

func (h *PageHandler) GetProjectSource(w http.ResponseWriter, r *http.Request) {
	h.getProject(w, r, true)
}

func (h *PageHandler) GetProjectRevisionSource(w http.ResponseWriter, r *http.Request) {
	h.getProjectRevision(w, r, true)
}

// Used by history to advertise only restores the caller can actually author.
func projectDefinitionVisible(spec string, auth pageDefinitionAuthorizer) bool {
	if spec == "" {
		return false
	}
	var doc pages.Document
	if json.Unmarshal([]byte(spec), &doc) != nil {
		return false
	}
	for _, panel := range doc.Spec.Panels {
		if !auth.visible(panel.Owner) {
			return false
		}
	}
	return true
}
