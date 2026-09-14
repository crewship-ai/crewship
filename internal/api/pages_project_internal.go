package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/policy"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

type projectAgentKey struct{}
type projectAgent struct {
	WorkspaceID string `json:"workspace_id"`
	CrewID      string `json:"crew_id"`
	AgentID     string `json:"agent_id"`
	OwnerCrewID string `json:"owner_crew_id"`
}

func projectAgentFrom(ctx context.Context) *projectAgent {
	a, _ := ctx.Value(projectAgentKey{}).(*projectAgent)
	return a
}
func projectAuthor(r *http.Request) (user, label, encoded string) {
	if a := projectAgentFrom(r.Context()); a != nil {
		b, _ := json.Marshal(a)
		return "", "crew/" + a.CrewID + " agent/" + a.AgentID, string(b)
	}
	if u := UserFromContext(r.Context()); u != nil {
		b, _ := json.Marshal(map[string]string{"user_id": u.ID})
		return u.ID, u.ID, string(b)
	}
	return "", "", "{}"
}

type projectToolRequest struct {
	WorkspaceID      string              `json:"workspace_id" yaml:"workspace_id"`
	CrewID           string              `json:"crew_id" yaml:"crew_id"`
	AgentID          string              `json:"agent_id" yaml:"agent_id"`
	TargetCrew       string              `json:"target_crew_slug,omitempty" yaml:"target_crew_slug,omitempty"`
	Operation        string              `json:"operation" yaml:"operation"`
	Slug             string              `json:"slug" yaml:"slug"`
	ExpectedRevision *int64              `json:"expected_revision,omitempty" yaml:"expected_revision,omitempty"`
	Files            []pages.ProjectFile `json:"files,omitempty" yaml:"files,omitempty"`
	DeletePaths      []string            `json:"delete_paths,omitempty" yaml:"delete_paths,omitempty"`
	Path             string              `json:"path,omitempty" yaml:"path,omitempty"`
	BuildID          string              `json:"build_id,omitempty" yaml:"build_id,omitempty"`
	Definition       *pages.Document     `json:"definition,omitempty" yaml:"definition,omitempty"`
}

// InternalProject is draft-only. Its private principal is minted only after the
// internal token, acting agent, exact owner crew and autonomy gates agree. No
// public header or body field can produce a user/admin identity or publication.
func (h *PageHandler) InternalProject(w http.ResponseWriter, r *http.Request) {
	raw, ok := readCapped(w, r, (1<<20)+pageInternalSaveEnvelopeSlack, "Page project tool")
	if !ok {
		return
	}
	var req projectToolRequest
	if err := pages.DecodeProjectJSON(raw, &req); err != nil || req.WorkspaceID == "" || req.Slug == "" || req.AgentID == "" {
		replyError(w, 400, "workspace, Page slug and acting agent are required")
		return
	}
	switch req.Operation {
	case "init", "read", "save", "build", "status", "check":
	default:
		replyError(w, 400, "unsupported project operation; publication is a separate reviewed user action")
		return
	}
	if len(req.Files) > pages.MaxProjectFiles || len(req.DeletePaths) > pages.MaxProjectFiles {
		replyError(w, 400, "too many project file changes")
		return
	}
	if (req.Operation != "save" && (len(req.Files) > 0 || len(req.DeletePaths) > 0 || req.Definition != nil)) || (req.Operation != "read" && req.Path != "") || (req.Operation != "check" && req.BuildID != "") {
		replyError(w, 400, "fields do not apply to this project operation")
		return
	}
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) || !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &req.CrewID) {
		return
	}
	if req.CrewID == "" {
		replyError(w, 400, "crew_id is required")
		return
	}
	if !h.assertAuthorAgentInCrewPages(w, r, req.WorkspaceID, req.CrewID, req.AgentID) {
		return
	}
	actor := &projectAgent{WorkspaceID: req.WorkspaceID, CrewID: req.CrewID, AgentID: req.AgentID, OwnerCrewID: req.CrewID}
	if !resolveDelegatedAuthorCrew(w, r, h.db, h.logger, req.WorkspaceID, req.TargetCrew, &actor.OwnerCrewID) {
		return
	}
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, req.WorkspaceID)
	ctx = context.WithValue(ctx, projectAgentKey{}, actor)
	r = r.Clone(ctx)
	r.SetPathValue("slug", req.Slug)
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	if req.Operation == "init" || req.Operation == "save" || req.Operation == "build" {
		gate, ok := gateInternalAction(w, r, h.policyResolver, h.logger, actor.CrewID, policy.ActionPageCreate, "Page project authoring")
		if !ok {
			return
		}
		if gate.held() {
			replyError(w, 403, "Page project authoring held by page_create policy; no draft or build was changed")
			return
		}
	}
	if req.Operation == "init" || req.Operation == "save" || req.Operation == "build" {
		if !h.prepareProjectWrite(w, r) {
			return
		}
	}
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	call := func(body any, handler func(http.ResponseWriter, *http.Request)) {
		data, err := json.Marshal(body)
		if err != nil {
			replyError(w, 400, "invalid project request")
			return
		}
		copy := r.Clone(r.Context())
		copy.Body = io.NopCloser(bytes.NewReader(data))
		copy.ContentLength = int64(len(data))
		handler(w, copy)
	}
	switch req.Operation {
	case "status":
		h.GetProjectPreview(w, r)
	case "check":
		call(map[string]any{"build_id": req.BuildID, "expected_revision": req.ExpectedRevision}, h.CheckProject)
	case "build":
		call(map[string]any{"expected_revision": req.ExpectedRevision}, h.BuildProject)
	case "init":
		if req.ExpectedRevision == nil || *req.ExpectedRevision != 0 {
			replyError(w, 400, "init requires expected_revision 0; use save to change an existing project")
			return
		}
		call(map[string]any{"expected_revision": 0, "project": pageprofile.Source()}, h.PutProject)
	case "read", "save":
		draft, err := h.loadProject(r, rec)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "Page has no source project; use init first")
			return
		}
		if err != nil {
			replyInternalError(w, h.logger, "read agent project", err)
			return
		}
		if !h.requireProjectDefinitions(w, r, &draft.Definition) {
			return
		}
		if req.Operation == "read" {
			w.Header().Set("Cache-Control", "no-store")
			if req.Path != "" {
				for _, f := range draft.Project.Files {
					if f.Path == req.Path {
						writeJSON(w, 200, map[string]any{"revision": draft.Revision, "file": f})
						return
					}
				}
				replyError(w, 404, "project file not found")
				return
			}
			files := make([]map[string]any, 0, len(draft.Project.Files))
			for _, f := range draft.Project.Files {
				data, _ := f.Bytes()
				files = append(files, map[string]any{"path": f.Path, "encoding": f.Encoding, "bytes": len(data)})
			}
			writeJSON(w, 200, map[string]any{"revision": draft.Revision, "digest": draft.Digest, "git_commit": draft.GitCommit, "files": files, "profile": pageprofile.Contract(), "sdk": map[string]string{"module": "@crewship/pages", "source": pageprofile.SDKSource()}, "pages_theme": h.workspacePagesTheme(r), "definition": draft.Definition, "page_path": "/pages/" + url.PathEscape(rec.Slug)})
			return
		}
		if req.ExpectedRevision == nil || *req.ExpectedRevision != draft.Revision {
			replyError(w, 409, "project revision changed; read before saving")
			return
		}
		replacements := map[string]pages.ProjectFile{}
		deletions := map[string]bool{}
		for _, f := range req.Files {
			if _, exists := replacements[f.Path]; exists {
				replyError(w, 400, "duplicate file replacement")
				return
			}
			replacements[f.Path] = f
		}
		for _, path := range req.DeletePaths {
			if _, exists := replacements[path]; exists || deletions[path] {
				replyError(w, 400, "duplicate or conflicting file deletion")
				return
			}
			deletions[path] = true
		}
		existing := map[string]bool{}
		for _, file := range draft.Project.Files {
			existing[file.Path] = true
		}
		for path := range deletions {
			if !existing[path] {
				replyError(w, 400, "cannot delete a missing project file")
				return
			}
		}
		source := &pages.SourceProject{Format: draft.Project.Format, Runtime: draft.Project.Runtime}
		for _, f := range draft.Project.Files {
			if deletions[f.Path] {
				continue
			}
			if replacement, exists := replacements[f.Path]; exists {
				source.Files = append(source.Files, replacement)
				delete(replacements, f.Path)
			} else {
				source.Files = append(source.Files, f)
			}
		}
		for _, f := range replacements {
			source.Files = append(source.Files, f)
		}
		body := map[string]any{"expected_revision": req.ExpectedRevision, "project": source}
		if req.Definition != nil {
			body["definition"] = req.Definition
		}
		call(body, h.PutProject)
	}
}

// Theme is workspace-scoped and reaches only an already-authorized agent reader.
func (h *PageHandler) workspacePagesTheme(r *http.Request) json.RawMessage {
	var raw string
	if err := h.db.QueryRowContext(r.Context(), "SELECT pages_theme FROM workspaces WHERE id=? AND deleted_at IS NULL", WorkspaceIDFromContext(r.Context())).Scan(&raw); err != nil {
		return json.RawMessage(`{}`)
	}
	if validateWorkspacePagesTheme(json.RawMessage(raw)) != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}
