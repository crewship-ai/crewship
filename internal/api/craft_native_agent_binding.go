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
	if e != nil && e.chat != nil {
		return m.checkChatRun(ctx, tx, e)
	}
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
	if intent == "write" || intent == "acquire" || intent == "renew" {
		if err := m.human.checkChatSheetWrite(ctx, tx, scope, c.entry.grant.Application); err != nil {
			return mooring.EditLeasePrincipal{}, err
		}
	}
	// A direct save is authorized against the document's current location in
	// the publishing transaction itself, so a move out of the personal space
	// cannot slip between the check and the write (Q15/Q16).
	if intent == "write" && ctx.Value(craftNativeAgentDirectWriteKey{}) != nil {
		if err := m.checkDirectMandate(ctx, tx, c.entry); err != nil {
			return mooring.EditLeasePrincipal{}, err
		}
	}
	return craftNativeAgentPrincipal(c.entry), nil
}

// craftNativeAgentDirectWriteKey marks the apply path's publication; a
// proposal authorizes "write" too, but never changes the document.
type craftNativeAgentDirectWriteKey struct{}

// checkDirectMandate is Q16 at use time: apply needs the apply mode, and an
// implicit apply holds only while the document stays in the author's personal
// space. Moving it to a shared folder turns the rest of the run into proposals.
func (m *craftNativeAgentManager) checkDirectMandate(ctx context.Context, tx *sql.Tx, e *craftNativeAgentEntry) error {
	g := e.grant
	if g.Mode != craftNativeAgentModeApply {
		return errCraftNativeAgentProposalRequired
	}
	if g.ModeExplicit {
		return nil
	}
	rec := &pageRecord{}
	err := tx.QueryRowContext(ctx, `SELECT private_audience, COALESCE(folder_id,''), COALESCE(owner_crew_id,''), COALESCE(owner_user_id,'') FROM pages WHERE id=? AND workspace_id=?`, g.Scope.ArtifactID, g.Scope.WorkspaceID).Scan(&rec.PrivateAudience, &rec.FolderID, &rec.OwnerCrewID, &rec.OwnerUserID)
	if err != nil {
		return errCraftNativeAgentDenied
	}
	if !craftNativePersonalSpace(rec, g.Source.UserID) {
		return errCraftNativeAgentProposalRequired
	}
	return nil
}

func (m *craftNativeAgentManager) authorizeLease(ctx context.Context, tx *sql.Tx, a mooring.EditLeaseAccess) error {
	p, err := m.authorize(ctx, tx, a.Scope, a.Intent)
	if err != nil || p != a.Principal {
		return errCraftNativeAgentDenied
	}
	return nil
}
