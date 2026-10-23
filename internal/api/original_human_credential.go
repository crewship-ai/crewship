package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
)

type originalCredentialSnapshot struct {
	reference       auth.OriginalCredential
	scopes          sql.NullString
	tokenHash, tier string
}

// Credential liveness, user identity and the global revocation counter are read
// in one SQL snapshot. A separate later user-counter lookup could refresh an
// already queued action after credential/account revocation and restoration.
func loadOriginalCredential(ctx context.Context, q pageAuthQueryer, user, kind, id string) (originalCredentialSnapshot, error) {
	var result originalCredentialSnapshot
	var created string
	var expires, revoked sql.NullString
	result.reference = auth.OriginalCredential{UserID: user, Kind: kind, ID: id}
	var err error
	switch kind {
	case AuthKindSession:
		err = q.QueryRowContext(ctx, `SELECT s.created_at,s.expires_at,s.revoked_at,CAST(u.authority_revision AS TEXT) FROM user_sessions s JOIN users u ON u.id=s.user_id AND u.suspended_at IS NULL WHERE s.id=? AND s.user_id=?`, id, user).Scan(&created, &expires, &revoked, &result.reference.UserAuthorityRevision)
	case AuthKindCLIToken:
		err = q.QueryRowContext(ctx, `SELECT c.created_at,c.expires_at,c.revoked_at,c.token_hash,c.tier,c.scopes,CAST(u.authority_revision AS TEXT) FROM cli_tokens c JOIN users u ON u.id=c.user_id AND u.suspended_at IS NULL WHERE c.id=? AND c.user_id=?`, id, user).Scan(&created, &expires, &revoked, &result.tokenHash, &result.tier, &result.scopes, &result.reference.UserAuthorityRevision)
	default:
		return result, errContentCredentialNotCurrent
	}
	if err != nil {
		return result, err
	}
	if revoked.Valid || created == "" || (kind == AuthKindSession && !expires.Valid) || (kind == AuthKindCLIToken && (result.tokenHash == "" || (result.tier != "STANDARD" && result.tier != "ADMIN"))) {
		return result, errContentCredentialNotCurrent
	}
	if expires.Valid {
		expiry, err := time.Parse(time.RFC3339Nano, expires.String)
		if err != nil || !time.Now().Before(expiry) {
			return result, errContentCredentialNotCurrent
		}
		result.reference.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	}
	raw, _ := json.Marshal([]string{"crewship.original-credential.v1", kind, id, user, created, result.tier, result.tokenHash})
	digest := sha256.Sum256(raw)
	result.reference.Binding = "sha256:" + hex.EncodeToString(digest[:])
	if !result.reference.Valid() {
		return result, errContentCredentialNotCurrent
	}
	return result, nil
}

// Called only after signature/bearer authentication, with its original accepted
// horizon. The stored row must still match that exact authenticated credential.
func captureOriginalHumanCredential(ctx context.Context, q pageAuthQueryer, user *AuthUser, kind, bearer string) (*auth.OriginalCredential, error) {
	if user == nil {
		return nil, errContentCredentialNotCurrent
	}
	id := user.SessionID
	if kind == AuthKindCLIToken {
		id = user.TokenID
	}
	snapshot, err := loadOriginalCredential(ctx, q, user.ID, kind, id)
	if err != nil {
		return nil, err
	}
	if kind == AuthKindCLIToken {
		hash, tier, err := contentCLITokenIdentity(bearer)
		if err != nil || hash != snapshot.tokenHash || tier != snapshot.tier {
			return nil, errContentCredentialNotCurrent
		}
	}
	expiry, _ := snapshot.reference.Expiry()
	expiry = earlierExpiry(expiry, user.CredentialExpiresAt)
	if kind == AuthKindSession && user.CredentialExpiresAt.IsZero() {
		return nil, errContentCredentialNotCurrent
	}
	if !expiry.IsZero() {
		if !time.Now().Before(expiry) {
			return nil, errContentCredentialNotCurrent
		}
		snapshot.reference.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	}
	result := snapshot.reference
	return &result, nil
}

// A reference is provenance, never public authentication. Callers first bind it
// to the authenticated human or immutable host run admission, then use this
// function inside the protected writer transaction. Scope is CURRENT policy.
func validateOriginalHumanCredential(ctx context.Context, q pageAuthQueryer, original auth.OriginalCredential, requiredScope string) (time.Time, error) {
	if !original.Valid() || requiredScope == "" {
		return time.Time{}, errContentCredentialNotCurrent
	}
	current, err := loadOriginalCredential(ctx, q, original.UserID, original.Kind, original.ID)
	if err != nil {
		return time.Time{}, err
	}
	if current.reference.UserAuthorityRevision != original.UserAuthorityRevision || current.reference.Binding != original.Binding {
		return time.Time{}, errContentCredentialNotCurrent
	}
	if original.Kind == AuthKindCLIToken && requiredScope != scopeSelf && current.scopes.Valid {
		set := parseScopes(current.scopes.String)
		if set != nil && !canScope(context.WithValue(ctx, ctxTokenScopes, set), requiredScope) {
			return time.Time{}, errContentCredentialScope
		}
	}
	originalExpiry, _ := original.Expiry()
	currentExpiry, _ := current.reference.Expiry()
	expiry := earlierExpiry(originalExpiry, currentExpiry)
	if !expiry.IsZero() && !time.Now().Before(expiry) {
		return time.Time{}, errContentCredentialNotCurrent
	}
	return expiry, nil
}

func originalCredentialAdmissionError(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, errContentCredentialNotCurrent) || errors.Is(err, errInvalidCLIToken)
}

// Only the host-authenticated resolve-human IPC calls this. The supplied source
// was signed at the original HTTP/socket authentication; it is never inferred
// from a user id, current run token or a later resume request.
func (h *InternalHandler) originalHumanChatReceipt(ctx context.Context, chat, user string, expected *chataudience.Receipt, source *auth.OriginalCredential) (*chataudience.Receipt, error) {
	if h.db == nil || (expected != nil && source != nil) {
		return nil, errContentCredentialNotCurrent
	}
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ctx = auth.WithOriginalCredential(ctx, auth.OriginalCredential{})
	if expected != nil && expected.SourceCredentialKind != "" {
		original := expected.OriginalCredential()
		source = &original
	}
	if source != nil {
		if source.UserID != user {
			return nil, nil
		}
		if _, err := validateOriginalHumanCredential(ctx, tx, *source, scopeSelf); err != nil {
			if originalCredentialAdmissionError(err) {
				return nil, nil
			}
			return nil, err
		}
		ctx = auth.WithOriginalCredential(ctx, *source)
	}
	receipt, err := chataudience.CaptureTrustedQuery(ctx, tx, chat, user, expected)
	if err != nil || receipt == nil {
		return receipt, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return receipt, nil
}
