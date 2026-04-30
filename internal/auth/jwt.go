package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/crypto/hkdf"
)

var (
	ErrTokenExpired = errors.New("token expired")
	ErrInvalidToken = errors.New("invalid token")
	ErrWrongKind    = errors.New("token kind mismatch")
)

// Token kinds. See package doc on JWTValidator for the lifecycle.
const (
	KindAccess  = "access"
	KindRefresh = "refresh"
	KindWS      = "ws"
)

// HKDF salt strings — different per kind so a key compromise on one
// derivation can't be replayed on another. Salt strings double as the
// cookie name for the access/refresh tokens (the WS ticket is returned
// in JSON, not as a cookie).
const (
	saltAccess  = "authjs.session-token"
	saltRefresh = "authjs.refresh-token"
	saltWS      = "authjs.ws-ticket"
)

// Default TTLs — short-lived access tokens force the client through the
// refresh path, where revocation actually bites; the long refresh window
// is the user-facing "stay logged in" lifetime.
const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour
	WSTicketTTL     = 15 * time.Minute
)

// Claims represents the decoded payload of an Auth.js v5 JWE token. The
// shape is preserved across access / refresh / ws tickets — the `Kind`
// claim disambiguates and Validate*() refuses cross-use.
//
// Sid joins to user_sessions.id (migration v63). Without a matching row
// (or with revoked_at != NULL) the auth middleware rejects the token,
// so a stolen access token is killable in <= AccessTokenTTL once the
// user signs out / changes password / an admin force-revokes.
type Claims struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Sid   string `json:"sid,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Jti   string `json:"jti"`
}

// JWTValidator issues and decodes Auth.js v5 JWE tokens. Holds three
// HKDF-derived encryption keys (access, refresh, ws ticket) so that
// revoking or rotating one kind doesn't interfere with the others.
//
// The constructor derives all three from the same NEXTAUTH_SECRET; the
// per-kind salt makes the keys independent.
type JWTValidator struct {
	accessKey  []byte
	refreshKey []byte
	wsKey      []byte

	// nowFn is the time source for issuing claims (iat/exp). Tests
	// override it via SetClock to mint already-expired tokens without
	// sleeping; production never reassigns. Validation always uses
	// time.Now directly because the validator's clock and the world
	// clock are the same in production, and we want validation to
	// reflect real wall time even in tests that issue forged tokens.
	nowFn func() time.Time
}

// NewJWTValidator constructs a validator that can issue and verify all
// three token kinds. Returns an error if NEXTAUTH_SECRET is empty —
// without it the entire auth path becomes unsigned, which we'd rather
// fail loudly at startup than silently accept.
//
// The legacy second argument (cookie-name salt) is gone: kinds always
// derive their own salts. Callers from before this change should pass
// just the secret. (router.go and server.go updated in same change.)
func NewJWTValidator(secret string) (*JWTValidator, error) {
	if secret == "" {
		return nil, errors.New("jwt secret is required")
	}
	a, err := deriveEncryptionKey(secret, saltAccess, 64)
	if err != nil {
		return nil, fmt.Errorf("derive access key: %w", err)
	}
	r, err := deriveEncryptionKey(secret, saltRefresh, 64)
	if err != nil {
		return nil, fmt.Errorf("derive refresh key: %w", err)
	}
	w, err := deriveEncryptionKey(secret, saltWS, 64)
	if err != nil {
		return nil, fmt.Errorf("derive ws key: %w", err)
	}
	return &JWTValidator{accessKey: a, refreshKey: r, wsKey: w, nowFn: time.Now}, nil
}

// SetClock overrides the issue-side clock. Tests use this to mint
// tokens with iat/exp in the past or future without sleeping.
// Production code must never call this.
func (v *JWTValidator) SetClock(fn func() time.Time) {
	if fn == nil {
		v.nowFn = time.Now
		return
	}
	v.nowFn = fn
}

// IssueAccessToken creates a short-lived API/cookie token bound to a
// session row. Caller must have already inserted the user_sessions row
// and has its id at hand.
func (v *JWTValidator) IssueAccessToken(userID, sessionID, name, email string) (string, error) {
	return v.issue(v.accessKey, KindAccess, AccessTokenTTL, userID, sessionID, name, email)
}

// IssueRefreshToken creates the long-lived rotation token. The cookie
// for this MUST be Path-scoped to /api/auth/token/refresh so it never
// leaks to other endpoints — the refresh endpoint is the single place
// that ever sees it.
func (v *JWTValidator) IssueRefreshToken(userID, sessionID string) (string, error) {
	return v.issue(v.refreshKey, KindRefresh, RefreshTokenTTL, userID, sessionID, "", "")
}

// IssueWSTicket creates the JSON-returned ticket the browser embeds in
// the /ws upgrade URL. Validated server-side via ValidateWS at upgrade
// time and again on the periodic revoke check tick.
func (v *JWTValidator) IssueWSTicket(userID, sessionID, name, email string) (string, error) {
	return v.issue(v.wsKey, KindWS, WSTicketTTL, userID, sessionID, name, email)
}

// ValidateAccess decrypts an access token cookie and refuses anything
// that isn't kind=access (so a refresh-cookie smuggled into the wrong
// header gets rejected as ErrWrongKind, not silently honored).
func (v *JWTValidator) ValidateAccess(tokenStr string) (*Claims, error) {
	return v.validate(v.accessKey, KindAccess, tokenStr)
}

// ValidateRefresh is the converse — only the refresh handler should
// accept refresh tokens.
func (v *JWTValidator) ValidateRefresh(tokenStr string) (*Claims, error) {
	return v.validate(v.refreshKey, KindRefresh, tokenStr)
}

// ValidateWS validates the WS ticket. Note: WS upgrades historically
// also accepted full access tokens for convenience; we keep that door
// shut now — tickets are issued specifically for /ws via IssueWSTicket
// so there's no reason to broaden the validator.
func (v *JWTValidator) ValidateWS(tokenStr string) (*Claims, error) {
	return v.validate(v.wsKey, KindWS, tokenStr)
}

func (v *JWTValidator) validate(key []byte, expectedKind, tokenStr string) (*Claims, error) {
	jwe, err := jose.ParseEncrypted(tokenStr, []jose.KeyAlgorithm{jose.DIRECT}, []jose.ContentEncryption{jose.A256CBC_HS512})
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidToken, err)
	}
	plaintext, err := jwe.Decrypt(key)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt: %v", ErrInvalidToken, err)
	}
	var claims Claims
	if err := json.Unmarshal(plaintext, &claims); err != nil {
		return nil, fmt.Errorf("%w: unmarshal: %v", ErrInvalidToken, err)
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp+5 {
		return nil, ErrTokenExpired
	}
	if claims.ID == "" {
		return nil, fmt.Errorf("%w: missing user id", ErrInvalidToken)
	}
	if claims.Kind != expectedKind {
		return nil, fmt.Errorf("%w: got %q want %q", ErrWrongKind, claims.Kind, expectedKind)
	}
	return &claims, nil
}

func (v *JWTValidator) issue(key []byte, kind string, ttl time.Duration, userID, sessionID, name, email string) (string, error) {
	if userID == "" {
		return "", errors.New("user id required")
	}
	// Access and refresh tokens MUST carry a session id — that's what
	// makes them revocable. WS tickets issued from CLI-token auth are
	// the deliberate exception (CLI tokens have their own revocation
	// table); the WS hub validator only consults sessions when sid is
	// non-empty.
	if sessionID == "" && kind != KindWS {
		return "", errors.New("session id required")
	}
	now := v.nowFn()
	jti, err := randomJti()
	if err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}
	claims := Claims{
		ID:    userID,
		Name:  name,
		Email: email,
		Sid:   sessionID,
		Kind:  kind,
		Iat:   now.Unix(),
		Exp:   now.Add(ttl).Unix(),
		Jti:   jti,
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	encrypter, err := jose.NewEncrypter(
		jose.A256CBC_HS512,
		jose.Recipient{Algorithm: jose.DIRECT, Key: key},
		(&jose.EncrypterOptions{}).WithContentType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("create encrypter: %w", err)
	}
	jwe, err := encrypter.Encrypt(payload)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	return jwe.CompactSerialize()
}

// randomJti generates 128 bits of entropy as a base64url string. That's
// enough to make collision-by-accident astronomically unlikely (the
// rotation blacklist keys on it) and enough to deny an attacker any
// useful guess against a leaked partial.
func randomJti() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// deriveEncryptionKey replicates Auth.js v5's getDerivedEncryptionKey:
// HKDF-SHA256 with info = "Auth.js Generated Encryption Key ({salt})".
// Length is 64 bytes (256-bit AES + 256-bit HMAC for A256CBC_HS512).
func deriveEncryptionKey(secret, salt string, length int) ([]byte, error) {
	info := fmt.Sprintf("Auth.js Generated Encryption Key (%s)", salt)
	hkdfReader := hkdf.New(sha256.New, []byte(secret), []byte(salt), []byte(info))
	key := make([]byte, length)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, err
	}
	return key, nil
}
