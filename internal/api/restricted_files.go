package api

import (
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

func (r *Router) restrictedFiles(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	workspace, chat := WorkspaceIDFromContext(req.Context()), req.PathValue("chatId")
	var agent string
	if err := r.db.QueryRowContext(req.Context(), `SELECT agent_id FROM chats WHERE id=? AND workspace_id=?`, chat, workspace).Scan(&agent); err != nil {
		replyError(w, http.StatusNotFound, "Files not found")
		return
	}
	store := access.Store{DB: r.db}
	w.Header().Set("Cache-Control", "no-store")
	if id := req.PathValue("fileId"); id != "" {
		version, content, err := store.ReadFileForChat(req.Context(), user.ID, workspace, agent, chat, id)
		if err != nil {
			replyError(w, http.StatusNotFound, "File not found")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Header().Set("X-Content-SHA256", version.SHA256)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(version.Name)}))
		// Bound each delivery and recheck the exact current audience. Do not
		// send a filename or byte after a known revocation, even mid-download.
		controller := http.NewResponseController(w)
		for offset := 0; offset < len(content); offset += 16384 {
			if err = store.CheckFileForChat(req.Context(), user.ID, workspace, agent, chat, id); err != nil {
				return
			}
			end := min(offset+16384, len(content))
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = w.Write(content[offset:end]); err != nil {
				return
			}
			_ = controller.Flush()
		}
		return
	}
	files, err := store.FilesForChat(req.Context(), user.ID, workspace, agent, chat)
	if err != nil {
		replyError(w, http.StatusNotFound, "Files not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
