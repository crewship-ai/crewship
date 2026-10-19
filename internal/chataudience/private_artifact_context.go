package chataudience

import (
	"context"
	"errors"
)

var ErrPrivateArtifactContext = errors.New("private artifact conversation provenance is inconsistent")

// These fixed predicates refer only to the outer chats c. Authority expires;
// transcript classification does not. Never filter by consent state, live
// grants, current Page placement, result existence or document revision.
const privateArtifactSourceSQL = `EXISTS(SELECT 1 FROM document_creation_consents s WHERE s.chat_id=c.id)
 OR EXISTS(SELECT 1 FROM document_creations d WHERE d.source_chat_id=c.id)`
const privateArtifactInvalidSQL = `EXISTS(SELECT 1 FROM document_creation_consents s
 LEFT JOIN conversation_messages m ON m.id=s.message_id WHERE s.chat_id=c.id AND
 (s.workspace_id IS NOT c.workspace_id OR s.human_user_id IS NOT c.created_by
 OR m.id IS NULL OR m.session_id IS NOT c.id OR m.agent_id IS NOT c.agent_id
 OR m.author_user_id IS NOT s.human_user_id OR m.role IS NOT 'user'))
 OR EXISTS(SELECT 1 FROM document_creations d WHERE d.source_chat_id=c.id AND
 (d.workspace_id IS NOT c.workspace_id OR d.human_user_id IS NOT c.created_by OR d.agent_id IS NOT c.agent_id))`
const privateArtifactValidSQL = `c.visibility='private' AND COALESCE(c.created_by,'')<>''
 AND COALESCE(c.agent_id,'')<>'' AND NOT (` + privateArtifactInvalidSQL + `)`

// NoPrivateArtifactContextSQL excludes ANY provenance, including inconsistent
// legacy rows. Intended for atomic share admission/validation over chats c.
const NoPrivateArtifactContextSQL = `NOT (` + privateArtifactSourceSQL + `)`

// PrivateArtifactContextSQL is an additional restriction, never a grant. AND it
// with the existing live chat audience. Outer aliases: chats c and the current
// workspace_members wm. No caller identity parameters are accepted here.
const PrivateArtifactContextSQL = `(` + NoPrivateArtifactContextSQL + ` OR ((` + privateArtifactValidSQL + `)
 AND (wm.user_id=c.created_by OR (wm.role='OWNER' AND wm.access_mode='trusted'
 AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted')))))`

type PrivateArtifactContext struct {
	WorkspaceID, ChatID, AuthorUserID string
	Sealed                            bool
}
type PrivateArtifactDecision uint8

const (
	ArtifactContextDenied PrivateArtifactDecision = iota
	ArtifactContextUnsealed
	ArtifactContextAuthor
	ArtifactContextOwner
)

// ResolvePrivateArtifactContext classifies in ONE snapshot, usable inside a
// caller-owned transaction. It does not authenticate a reader or audit access.
// Missing chats and every database/provenance error must deny, never fall back
// to ordinary chat policy. It deliberately does not join current Page state.
func ResolvePrivateArtifactContext(ctx context.Context, q Queryer, workspaceID, chatID string) (PrivateArtifactContext, error) {
	var value PrivateArtifactContext
	if q == nil || workspaceID == "" || chatID == "" {
		return value, ErrPrivateArtifactContext
	}
	var valid bool
	err := q.QueryRowContext(ctx, `SELECT c.workspace_id,c.id,COALESCE(c.created_by,''),(`+privateArtifactSourceSQL+`),(`+privateArtifactValidSQL+`) FROM chats c WHERE c.workspace_id=? AND c.id=?`, workspaceID, chatID).Scan(&value.WorkspaceID, &value.ChatID, &value.AuthorUserID, &value.Sealed, &valid)
	if err != nil {
		return PrivateArtifactContext{}, err
	}
	if value.Sealed && !valid {
		return PrivateArtifactContext{}, ErrPrivateArtifactContext
	}
	return value, nil
}

// Decide consumes a current, trusted host reader identity/role, not model or
// client fields. Unsealed means use existing policy; it does NOT grant access.
// OWNER additionally requires the caller's live trusted-account ceiling.
func (c PrivateArtifactContext) Decide(userID, currentTrustedRole string) PrivateArtifactDecision {
	if !c.Sealed {
		return ArtifactContextUnsealed
	}
	if userID == "" || c.AuthorUserID == "" || c.ChatID == "" || c.WorkspaceID == "" {
		return ArtifactContextDenied
	}
	if userID == c.AuthorUserID {
		return ArtifactContextAuthor
	}
	if currentTrustedRole == "OWNER" {
		return ArtifactContextOwner
	}
	return ArtifactContextDenied
}

// AuditMetadata describes source-context inspection, not a canonical Page read.
// The caller targets entity_type=chat/entity_id=ChatID before releasing bytes.
func (c PrivateArtifactContext) AuditMetadata() map[string]string {
	if !c.Sealed {
		return nil
	}
	return map[string]string{"owner_user_id": c.AuthorUserID, "output_source": "conversation"}
}
