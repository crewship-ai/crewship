package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"io"
	"net/http"
	"time"
)

// RestrictedTextExecutor is host-owned; public requests cannot install builders,
// runtime plans, handles, identities, provider models or token limits.
type RestrictedTextExecutor interface {
	Execute(context.Context, string, string, string, string, func(string, string) error) error
	ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error
}

// SetRestrictedTextRunner is called once during bootstrap, before serving.
func (r *Router) SetRestrictedTextRunner(executor RestrictedTextExecutor) {
	r.restrictedText = executor
}

func WithRestrictedTextRunner(executor RestrictedTextExecutor) RouterOption {
	return func(r *Router) { r.restrictedText = executor }
}

func (r *Router) restrictedTextRun(w http.ResponseWriter, req *http.Request) {
	r.restrictedTextExecute(w, req, false)
}
func (r *Router) restrictedCLIRun(w http.ResponseWriter, req *http.Request) {
	r.restrictedTextExecute(w, req, true)
}
func (r *Router) restrictedTextExecute(w http.ResponseWriter, req *http.Request, runOperation bool) {
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	if user == nil || workspace == "" {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if r.restrictedText == nil {
		replyError(w, http.StatusServiceUnavailable, "restricted text runtime unavailable")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	req.Body = http.MaxBytesReader(w, req.Body, 128<<10)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Content == "" || len(body.Content) > 32768 {
		replyError(w, http.StatusBadRequest, "invalid text request")
		return
	}
	controller := http.NewResponseController(w)
	started := false
	send := func(kind, text string) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		data, _ := json.Marshal(map[string]string{"type": kind, "text": text})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		return controller.Flush()
	}
	execute := r.restrictedText.Execute
	if runOperation {
		execute = r.restrictedText.ExecuteRun
	}
	err := execute(req.Context(), user.ID, workspace, req.PathValue("chatId"), body.Content, send)
	if err != nil {
		// No error frame after revocation: it would itself be output delivery.
		if !started {
			replyError(w, http.StatusForbidden, "restricted text run denied or unavailable")
		}
		return
	}
}

func (r *Router) executionProfile(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	ok, err := chataudience.CanReadInWorkspace(req.Context(), r.db, req.PathValue("chatId"), user.ID, workspace)
	if !ok && err == nil {
		var one int
		err = r.db.QueryRowContext(req.Context(), `SELECT 1 FROM chats c JOIN access_grants g ON g.agent_id=c.agent_id AND g.resource_kind='agent' AND g.operation='run' JOIN workspace_members wm ON wm.id=g.member_id AND wm.workspace_id=c.workspace_id AND wm.user_id=? WHERE c.id=? AND c.workspace_id=? AND c.created_by=? AND c.visibility='private'`, user.ID, req.PathValue("chatId"), workspace, user.ID).Scan(&one)
		ok = err == nil
	}
	if err != nil || !ok {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	member, err := (access.Store{DB: r.db}).Membership(req.Context(), user.ID, workspace)
	if err != nil {
		replyError(w, http.StatusForbidden, "execution unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": member.Mode})
}
