package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Set only by the private selected transport after resolving immutable request
// identity. No public header/body may populate this context marker.
type slidesSelectedTransportKey struct{}
type slidesSelectedTransport struct{ RequestID, PageID string }

// captureSelectedAdmission captures identities only. Its writer-fenced caller
// must revalidate this exact snapshot; capture itself grants no capability.
func (h *slideHTTP) captureSelectedAdmission(r *http.Request, chat string) (slides.SelectedAdmission, error) {
	var admission slides.SelectedAdmission
	user := UserFromContext(r.Context())
	if user == nil {
		return admission, errSlideAccess
	}
	receipt, err := chataudience.CaptureTrusted(r.Context(), h.pages.db, chat, user.ID, nil)
	if err != nil || receipt == nil || receipt.WorkspaceID != WorkspaceIDFromContext(r.Context()) {
		return admission, errSlideAccess
	}
	admission = slides.SelectedAdmission{HumanUserID: user.ID, AgentID: receipt.AgentID, ChatID: chat, AudienceJSON: mustArtifactReceiptJSON(receipt)}
	token := extractToken(r)
	if IsCLIToken(token) {
		admission.CredentialKind = "cli"
		if strings.HasPrefix(token, cliTokenAdminPrefix) {
			key, keyErr := adminHMACKey()
			if keyErr != nil {
				return admission, errSlideAccess
			}
			admission.CredentialHash = hashAdmin(token, key)
		} else {
			admission.CredentialHash = hashStandard(token)
		}
		if h.pages.db.QueryRowContext(r.Context(), `SELECT id FROM cli_tokens WHERE token_hash=? AND user_id=?`, admission.CredentialHash, user.ID).Scan(&admission.CredentialID) != nil {
			return admission, errSlideAccess
		}
	} else {
		admission.CredentialKind, admission.CredentialID = "session", user.SessionID
	}
	return admission, nil
}

func selectedCredentialLive(ctx context.Context, tx *sql.Tx, admission slides.SelectedAdmission) error {
	var expiry, revoked, scopes sql.NullString
	switch admission.CredentialKind {
	case "session":
		if admission.CredentialHash != "" || tx.QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM user_sessions WHERE id=? AND user_id=?`, admission.CredentialID, admission.HumanUserID).Scan(&expiry, &revoked) != nil || !expiry.Valid {
			return errSlideAccess
		}
	case "cli":
		var tier string
		if admission.CredentialHash == "" || tx.QueryRowContext(ctx, `SELECT tier,expires_at,revoked_at,scopes FROM cli_tokens WHERE id=? AND user_id=? AND token_hash=?`, admission.CredentialID, admission.HumanUserID, admission.CredentialHash).Scan(&tier, &expiry, &revoked, &scopes) != nil || (tier != "ADMIN" && tier != "STANDARD") {
			return errSlideAccess
		}
		if scopes.Valid {
			set := parseScopes(scopes.String)
			if set != nil && !canScope(context.WithValue(ctx, ctxTokenScopes, set), "pages:write") {
				return errSlideAccess
			}
		}
	default:
		return errSlideAccess
	}
	if revoked.Valid {
		return errSlideAccess
	}
	if expiry.Valid {
		limit, err := time.Parse(time.RFC3339Nano, expiry.String)
		if err != nil || !time.Now().Before(limit) {
			return errSlideAccess
		}
	}
	return nil
}

// selectedAdmissionLive rechecks the original credential, human+agent grants
// and exact private ChatAudience epoch under the caller's writer transaction.
// A new read cannot refresh any part of the admitted authority.
func (h *slideHTTP) selectedAdmissionLive(ctx context.Context, tx *sql.Tx, key slides.Key, admission slides.SelectedAdmission) (*chataudience.Receipt, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if err := selectedCredentialLive(ctx, tx, admission); err != nil {
		return nil, err
	}
	role, err := h.pages.liveContentHumanRole(ctx, key.WorkspaceID, admission.HumanUserID)
	if err != nil {
		return nil, errSlideAccess
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	var slug string
	if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE workspace_id=? AND id=?`, key.WorkspaceID, key.PageID).Scan(&slug) != nil {
		return nil, errSlideAccess
	}
	rec, err := h.pages.loadPage(ctx, key.WorkspaceID, slug)
	if err != nil || !rec.HasSlides || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || rec.HasArtifact || rec.HasDocument || rec.HasBoard || rec.HasProject || rec.HasApplication || !h.pages.mayEditSpec(ctx, key.WorkspaceID, admission.HumanUserID, role, rec) || !h.pages.agentMayEditSpec(ctx, key.WorkspaceID, rec, admission.AgentID) || !h.pages.artifactRequestChatPrivate(ctx, admission.ChatID, admission.HumanUserID) {
		return nil, errSlideAccess
	}
	var occupied bool
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_panels WHERE page_id=?) OR EXISTS(SELECT 1 FROM page_project_live WHERE page_id=?)`, key.PageID, key.PageID).Scan(&occupied) != nil || occupied {
		return nil, errSlideAccess
	}
	var original chataudience.Receipt
	if json.Unmarshal([]byte(admission.AudienceJSON), &original) != nil || mustArtifactReceiptJSON(&original) != admission.AudienceJSON || original.UserID != admission.HumanUserID || original.AgentID != admission.AgentID || original.ChatID != admission.ChatID || original.WorkspaceID != key.WorkspaceID {
		return nil, errSlideAccess
	}
	receipt, err := chataudience.CaptureTrustedQuery(ctx, tx, admission.ChatID, admission.HumanUserID, &original)
	if err != nil || receipt == nil {
		return nil, errSlideAccess
	}
	return receipt, nil
}

func (h *slideHTTP) selectedHumanAuthority(r *http.Request, key slides.Key, write bool, admission *slides.SelectedAdmission) slides.Authorize {
	base := h.authorize(r, key, write, false)
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		if _, ok := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); ok {
			return "", errSlideAccess
		}
		actor, err := base(ctx, tx)
		if err != nil {
			return "", err
		}
		if admission != nil {
			if actor != "user:"+admission.HumanUserID {
				return "", errSlideAccess
			}
			if _, err = h.selectedAdmissionLive(ctx, tx, key, *admission); err != nil {
				return "", err
			}
		}
		return actor, nil
	}
}
