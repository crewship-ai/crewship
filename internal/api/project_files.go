package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

type ProjectFileWriteRequest struct {
	FileID           string `json:"file_id,omitempty"`
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expected_revision"`
	Content          []byte `json:"content_base64"`
}

func (r *Router) projectFiles(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, 401, "Authentication required")
		return
	}
	store := access.Store{DB: r.db}
	workspace, project := WorkspaceIDFromContext(req.Context()), req.PathValue("projectId")
	w.Header().Set("Cache-Control", "no-store")
	switch req.Method {
	case http.MethodPost:
		var body ProjectFileWriteRequest
		req.Body = http.MaxBytesReader(w, req.Body, 2*access.MaxProjectFileBytes+4096)
		d := json.NewDecoder(req.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
			replyError(w, 400, "Invalid project file request")
			return
		}
		v, err := store.PutProjectFile(req.Context(), user.ID, workspace, project, access.ProjectFileWrite{FileID: body.FileID, Name: body.Name, ExpectedRevision: body.ExpectedRevision}, body.Content)
		if err != nil {
			if errors.Is(err, access.ErrDenied) {
				replyError(w, 404, "Project file unavailable")
			} else {
				replyError(w, 409, "Project file revision or capacity conflict")
			}
			return
		}
		writeJSON(w, 201, v)
	case http.MethodDelete:
		var body struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		req.Body = http.MaxBytesReader(w, req.Body, 4096)
		d := json.NewDecoder(req.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
			replyError(w, 400, "Invalid project file request")
			return
		}
		if err := store.RetireProjectFile(req.Context(), user.ID, workspace, project, req.PathValue("fileId"), body.ExpectedRevision); err != nil {
			if errors.Is(err, access.ErrConflict) {
				replyError(w, 409, "Project file revision conflict")
			} else {
				replyError(w, 404, "Project file unavailable")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		id := req.PathValue("versionId")
		if id == "" {
			versions, err := store.ProjectFiles(req.Context(), user.ID, workspace, project)
			if err != nil {
				replyError(w, 404, "Project files unavailable")
				return
			}
			for _, v := range versions {
				if store.CheckProjectFile(req.Context(), user.ID, workspace, project, v.ID) != nil {
					replyError(w, 404, "Project files unavailable")
					return
				}
			}
			writeJSON(w, 200, map[string]any{"files": versions})
			return
		}
		version, content, err := store.ReadProjectFile(req.Context(), user.ID, workspace, project, id)
		if err != nil || store.CheckProjectFile(req.Context(), user.ID, workspace, project, id) != nil {
			replyError(w, 404, "Project file unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(version.Name)}))
		controller := http.NewResponseController(w)
		for offset := 0; offset < len(content); offset += 16384 {
			if store.CheckProjectFile(req.Context(), user.ID, workspace, project, id) != nil {
				return
			}
			end := min(offset+16384, len(content))
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = w.Write(content[offset:end]); err != nil {
				return
			}
			_ = controller.Flush()
		}
	}
}
