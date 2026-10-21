package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func (h *PageHandler) artifactAgentRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	ws, crew := InternalTokenWorkspaceFromContext(r.Context()), InternalTokenCrewFromContext(r.Context())
	fields := strings.Fields(r.Header.Get("Authorization"))
	if ws == "" || crew == "" || h.artifactMasterToken == "" || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		replyError(w, 403, "crew transport and run bearer required")
		return r, false
	}
	tw, agent, run, valid := internaltoken.ValidateAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, crew), fields[1])
	if !valid || tw != ws {
		replyError(w, 403, "invalid run capability")
		return r, false
	}
	a := &artifactRunAuthority{WorkspaceID: ws, CrewID: crew, AgentID: agent, RunID: run}
	var role string
	err := h.db.QueryRowContext(r.Context(), `SELECT p.human_user_id,p.chat_id,p.message_id,m.role FROM artifact_run_principals p JOIN workspace_members m ON m.workspace_id=p.workspace_id AND m.user_id=p.human_user_id WHERE p.run_id=? AND p.workspace_id=? AND p.agent_id=? AND p.crew_id=?`, run, ws, agent, crew).Scan(&a.HumanUserID, &a.ChatID, &a.MessageID, &role)
	if err != nil {
		replyError(w, 403, "no_human_principal")
		return r, false
	}
	ctx := context.WithValue(r.Context(), artifactRunKey{}, a)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: a.HumanUserID})
	ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
	ctx = context.WithValue(ctx, ctxRole, role)
	return r.WithContext(ctx), true
}

var artifactUploadSlots = make(chan struct{}, 2)

func acquireArtifactUpload(w http.ResponseWriter, r *http.Request) (func(), bool) {
	select {
	case artifactUploadSlots <- struct{}{}:
		return func() { <-artifactUploadSlots }, true
	case <-r.Context().Done():
		replyError(w, http.StatusRequestTimeout, "artifact upload cancelled while waiting for capacity")
		return nil, false
	}
}

func artifactCommentHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
