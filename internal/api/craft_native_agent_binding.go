package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

type craftNativeAgentConfirmed struct {
	entry      *craftNativeAgentEntry
	attempt    collaborationAgentAttemptBinding
	bearerHash [sha256.Size]byte
}
type craftNativeAgentCall struct {
	manager    *craftNativeAgentManager
	entry      *craftNativeAgentEntry
	bearerHash [sha256.Size]byte
}

func craftNativeAgentPrincipal(e *craftNativeAgentEntry) mooring.EditLeasePrincipal {
	return mooring.EditLeasePrincipal{ActorID: "agent:" + e.grant.AgentID, SessionID: "native-agent:" + e.attempt.runID}
}

func (m *craftNativeAgentManager) prepareConfirmed(ctx context.Context, tx *sql.Tx, e *craftNativeAgentEntry, run string) (craftNativeAgentConfirmed, error) {
	var zero craftNativeAgentConfirmed
	if err := m.checkGrant(ctx, tx, e); err != nil {
		return zero, err
	}
	a, _, err := loadCollaborationAgentAttempt(ctx, tx, run)
	g := e.grant
	if err != nil || a.workID != e.workID || a.workspaceID != g.Scope.WorkspaceID || a.agentID != g.AgentID || a.crewID != g.CrewID || a.humanUserID != g.Source.UserID || a.chatID != g.Original.ChatID || a.source != "chat" || a.sourceRef != g.MessageID || a.domainKind != craftNativeAgentDomain || a.domainID != g.Scope.ArtifactID || a.authorizedScope != "pages:write" || a.inputSHA256 != e.inputSHA256 || a.targetRevision != "whole-native" {
		return zero, errCraftNativeAgentDenied
	}
	origin, err := runauthority.Resolve(ctx, tx, run, g.AgentID, g.Scope.WorkspaceID)
	if err != nil || origin.HumanAuthority == nil || *origin.HumanAuthority != g.Original || origin.ChatID != g.Original.ChatID || origin.TriggerMessageID != g.MessageID {
		return zero, errCraftNativeAgentDenied
	}
	if m.human.pages.artifactMasterToken == "" {
		return zero, errCraftNativeAgentDenied
	}
	bearer := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(m.human.pages.artifactMasterToken, g.Scope.WorkspaceID, g.CrewID), g.Scope.WorkspaceID, g.AgentID, run)
	if bearer == "" {
		return zero, errCraftNativeAgentDenied
	}
	return craftNativeAgentConfirmed{e, a, sha256.Sum256([]byte(bearer))}, nil
}

// Called only after confirmed work_attempt/host-origin COMMIT. A SQL row or a
// token alone never publishes a native agent. Publication consumes this one
// pending entry; a later run cannot silently adopt the old explicit consent.
func (m *craftNativeAgentManager) publishConfirmed(c craftNativeAgentConfirmed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := c.entry
	if e == nil || e.seal != m.seal || m.entries[e.workID] != e || e.closed || e.attempt.runID != "" || c.attempt.runID == "" || m.runs[c.attempt.runID] != nil {
		return errCraftNativeAgentDenied
	}
	e.attempt, e.bearerHash = c.attempt, c.bearerHash
	m.runs[c.attempt.runID] = e
	return nil
}

func (m *craftNativeAgentManager) checkRun(ctx context.Context, tx *sql.Tx, e *craftNativeAgentEntry) error {
	if err := m.checkGrant(ctx, tx, e); err != nil {
		return err
	}
	m.mu.Lock()
	attached := e.attempt.runID != "" && m.runs[e.attempt.runID] == e
	original := e.attempt
	m.mu.Unlock()
	if !attached {
		return errCraftNativeAgentDenied
	}
	current, _, err := loadCollaborationAgentAttempt(ctx, tx, original.runID)
	if err != nil || current != original {
		return errCraftNativeAgentDenied
	}
	origin, err := runauthority.Resolve(ctx, tx, original.runID, e.grant.AgentID, e.grant.Scope.WorkspaceID)
	if err != nil || origin.HumanAuthority == nil || *origin.HumanAuthority != e.grant.Original || origin.TriggerMessageID != e.grant.MessageID {
		return errCraftNativeAgentDenied
	}
	return nil
}

func (m *craftNativeAgentManager) bindTool(r *http.Request) (*http.Request, *craftNativeAgentEntry, error) {
	if m == nil || !m.enabled || r == nil || len(r.Header.Get("Authorization")) > 2048 {
		return nil, nil, errCraftNativeAgentDenied
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, nil, errCraftNativeAgentDenied
	}
	ws, crew := InternalTokenWorkspaceFromContext(r.Context()), InternalTokenCrewFromContext(r.Context())
	if ws == "" || crew == "" {
		return nil, nil, errCraftNativeAgentDenied
	}
	workspace, agent, run, ok := internaltoken.ValidateAgentRunToken(internaltoken.DeriveAgentRunKey(m.human.pages.artifactMasterToken, ws, crew), parts[1])
	if !ok || workspace != ws {
		return nil, nil, errCraftNativeAgentDenied
	}
	hash := sha256.Sum256([]byte(parts[1]))
	m.mu.Lock()
	e := m.runs[run]
	valid := e != nil && !e.closed && e.grant.Scope.WorkspaceID == ws && e.grant.AgentID == agent && e.grant.CrewID == crew && e.bearerHash == hash
	m.mu.Unlock()
	if !valid {
		return nil, nil, errCraftNativeAgentDenied
	}
	c := &craftNativeAgentCall{m, e, hash}
	return r.WithContext(context.WithValue(r.Context(), craftNativeAgentContextKey{}, c)), e, nil
}

func (m *craftNativeAgentManager) authorize(ctx context.Context, tx *sql.Tx, scope mooring.EditLeaseScope, intent string) (mooring.EditLeasePrincipal, error) {
	switch intent {
	case "read", "write", "acquire", "renew", "release":
	default:
		return mooring.EditLeasePrincipal{}, errCraftNativeAgentDenied
	}
	c, _ := ctx.Value(craftNativeAgentContextKey{}).(*craftNativeAgentCall)
	if c == nil || c.manager != m || c.entry == nil || c.entry.grant.Scope != scope || c.bearerHash != c.entry.bearerHash || intent == "revoke" {
		return mooring.EditLeasePrincipal{}, errCraftNativeAgentDenied
	}
	if err := m.checkRun(ctx, tx, c.entry); err != nil {
		return mooring.EditLeasePrincipal{}, err
	}
	return craftNativeAgentPrincipal(c.entry), nil
}

func (m *craftNativeAgentManager) authorizeLease(ctx context.Context, tx *sql.Tx, a mooring.EditLeaseAccess) error {
	p, err := m.authorize(ctx, tx, a.Scope, a.Intent)
	if err != nil || p != a.Principal {
		return errCraftNativeAgentDenied
	}
	return nil
}
