package chataudience

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/crewship-ai/crewship/internal/auth"
)

// Receipt is an admission fence, not a bearer capability. Only authenticated
// host code may issue or carry it. A client-provided receipt grants no access.
// Every use must compare it to a freshly authorized database snapshot.
type Receipt struct {
	Kind       string `json:"kind,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	CrewID     string `json:"crew_id,omitempty"`
	UserID     string `json:"user_id"`
	// Decimal text preserves the original user-wide revocation fence across
	// JSON consumers which cannot represent every SQLite integer exactly.
	UserAuthorityRevision     string `json:"user_authority_revision,omitempty"`
	SourceCredentialKind      string `json:"source_credential_kind,omitempty"`
	SourceCredentialID        string `json:"source_credential_id,omitempty"`
	SourceCredentialExpiresAt string `json:"source_credential_expires_at,omitempty"`
	SourceCredentialBinding   string `json:"source_credential_binding,omitempty"`
	ChatID                    string `json:"chat_id"`
	WorkspaceID               string `json:"workspace_id"`
	AgentID                   string `json:"agent_id"`
	MemberID                  string `json:"member_id"`
	MemberRevision            int64  `json:"member_revision"`
	ChatGeneration            string `json:"chat_generation"`
	ChatRevision              int64  `json:"chat_revision"`
}

type receiptKey struct{}

// WithReceipt carries server-owned authority through in-process dispatch and
// host IPC. WebSocket request metadata must never populate this context key.
func WithReceipt(ctx context.Context, r *Receipt) context.Context {
	return context.WithValue(ctx, receiptKey{}, r)
}
func ReceiptFromContext(ctx context.Context) *Receipt {
	r, _ := ctx.Value(receiptKey{}).(*Receipt)
	return r
}

// CaptureTrusted authorizes and captures the epoch in one SQL snapshot. An
// expected receipt fences retries: re-admission cannot silently refresh it.
type Queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func CaptureTrusted(ctx context.Context, db *sql.DB, chat, user string, expected *Receipt) (*Receipt, error) {
	if db == nil {
		return nil, nil
	}
	return CaptureTrustedQuery(ctx, db, chat, user, expected)
}

// CaptureTrustedQuery allows admission to be fenced in the caller's write transaction.
func CaptureTrustedQuery(ctx context.Context, db Queryer, chat, user string, expected *Receipt) (*Receipt, error) {
	if db == nil || chat == "" || user == "" {
		return nil, nil
	}
	r := Receipt{UserID: user, ChatID: chat}
	args := []any{user, chat, user}
	args = append(args, Args(user)...)
	err := db.QueryRowContext(ctx, `SELECT c.workspace_id,c.agent_id,wm.id,wm.access_revision,c.authority_generation,c.authority_revision,CAST(u.authority_revision AS TEXT)
 FROM chats c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
 JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
 JOIN workspaces w ON w.id=c.workspace_id AND w.deleted_at IS NULL
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 WHERE c.id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')
 AND (`+VisibleSQL+`)`, args...).Scan(&r.WorkspaceID, &r.AgentID, &r.MemberID, &r.MemberRevision, &r.ChatGeneration, &r.ChatRevision, &r.UserAuthorityRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("capture human authority: %w", err)
	}
	if !ValidUserAuthorityRevision(r.UserAuthorityRevision) {
		return nil, nil
	}
	if expected == nil {
		if source := auth.OriginalCredentialFromContext(ctx); source != nil {
			if source.UserID != user || source.UserAuthorityRevision != r.UserAuthorityRevision {
				return nil, nil
			}
			r.setOriginalCredential(*source)
		}
	} else {
		// A retry carries its original credential even if a new socket or human
		// request is now available. No field may be recaptured from that caller.
		r.SourceCredentialKind = expected.SourceCredentialKind
		r.SourceCredentialID = expected.SourceCredentialID
		r.SourceCredentialExpiresAt = expected.SourceCredentialExpiresAt
		r.SourceCredentialBinding = expected.SourceCredentialBinding
		if expected.hasCredentialFields() && !expected.OriginalCredential().Valid() {
			return nil, nil
		}
	}
	// A legacy receipt remains legacy on every retry. Looking up today's
	// counter must never upgrade an old queued message into fenced authority.
	if expected != nil && expected.UserAuthorityRevision == "" {
		r.UserAuthorityRevision = ""
	}
	if r.ChatGeneration == "" || r.MemberRevision < 1 || r.ChatRevision < 1 || (expected != nil && *expected != r) {
		return nil, nil
	}
	return &r, nil
}

func (r Receipt) OriginalCredential() auth.OriginalCredential {
	return auth.OriginalCredential{UserID: r.UserID, Kind: r.SourceCredentialKind, ID: r.SourceCredentialID, ExpiresAt: r.SourceCredentialExpiresAt, Binding: r.SourceCredentialBinding, UserAuthorityRevision: r.UserAuthorityRevision}
}

func (r *Receipt) setOriginalCredential(source auth.OriginalCredential) {
	r.SourceCredentialKind = source.Kind
	r.SourceCredentialID = source.ID
	r.SourceCredentialExpiresAt = source.ExpiresAt
	r.SourceCredentialBinding = source.Binding
}

func (r Receipt) hasCredentialFields() bool {
	return r.SourceCredentialKind != "" || r.SourceCredentialID != "" || r.SourceCredentialExpiresAt != "" || r.SourceCredentialBinding != ""
}

// ValidUserAuthorityRevision recognizes exact positive SQLite INTEGER values.
// It deliberately accepts neither a JSON number nor a rounded float value.
func ValidUserAuthorityRevision(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == value
}

// RevalidateOriginalUserQuery requires an original user fence. New Mooring
// admission must use this boundary instead of refreshing an old/missing
// receipt. Existing legacy routes can retain CaptureTrustedQuery semantics.
func RevalidateOriginalUserQuery(ctx context.Context, db Queryer, expected *Receipt) (*Receipt, error) {
	if expected == nil || expected.Kind != "" || !ValidUserAuthorityRevision(expected.UserAuthorityRevision) {
		return nil, nil
	}
	return CaptureTrustedQuery(ctx, db, expected.ChatID, expected.UserID, expected)
}
