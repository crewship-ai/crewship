package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func (h *PageHandler) artifactAgentRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	return h.artifactAgentRequestMode(w, r, false)
}

// artifactAgentRequestMode is artifactAgentRequest; with unattended set (the
// artifact project tool only) a run without a human may also be admitted from
// its host-recorded run and capability (#5339). Every other agent surface
// keeps requiring a human principal.
func (h *PageHandler) artifactAgentRequestMode(w http.ResponseWriter, r *http.Request, unattended bool) (*http.Request, bool) {
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
	if unattended && errors.Is(err, sql.ErrNoRows) {
		// A run without a human (#5339): only a host-recorded run with its
		// host-minted capability, flag on. It carries no user and no role.
		if u := h.unattendedArtifactRequest(r, ws, crew, agent, run); u != nil {
			ctx := context.WithValue(r.Context(), artifactRunKey{}, u)
			ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
			return r.WithContext(ctx), true
		}
		// A recorded, live run whose call arrives without its capability
		// lost it in the sidecar (a restart forgets it). Say so plainly
		// instead of the generic refusal, and journal it once.
		if h.unattendedCapabilityLost(r, ws, crew, agent, run) {
			replyErrorCode(w, 403, unattendedCapabilityLostMessage, errCodeRunCapabilityLost, "")
			return r, false
		}
	}
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
