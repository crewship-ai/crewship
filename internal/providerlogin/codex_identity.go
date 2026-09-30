package providerlogin

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/httpsafe"
)

const openAIIssuer = "https://auth.openai.com"
const openAIJWKS = openAIIssuer + "/.well-known/jwks.json"

var ErrUnverifiedCodexIdentity = errors.New("OpenAI login identity verification failed")

// CodexIdentity records signed identity, not model entitlement or a spend limit.
// Callers must separately prove account-specific model availability and budget.
type CodexIdentity struct {
	Issuer, ClientID, Subject, AccountID, UserID, Plan string
	AccessExpires, IDExpires                           time.Time
}

// CodexIdentityVerifier owns a fixed public-key endpoint; task inputs cannot
// select an issuer, redirect, transport, key, account, or OAuth audience.
type CodexIdentityVerifier struct {
	mu               sync.Mutex
	client           *http.Client
	now              func() time.Time
	keys             map[string]*rsa.PublicKey
	expires, fetched time.Time
}

func NewCodexIdentityVerifier() *CodexIdentityVerifier {
	transport := httpsafe.SafeTransport()
	transport.Proxy = nil
	return &CodexIdentityVerifier{
		client: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:    time.Now,
	}
}

// Verify validates a legacy Codex login's exact signed subject/account against
// both tokens. It does not convert that login into SIWC enrollment. A separate
// application enrollment must validate its saved OIDC nonce and granted scopes.
func (v *CodexIdentityVerifier) Verify(ctx context.Context, idToken, accessToken, account string) (CodexIdentity, error) {
	deny := func() (CodexIdentity, error) { return CodexIdentity{}, ErrUnverifiedCodexIdentity }
	if v == nil || v.client == nil || v.now == nil || !identityString(account, 256) {
		return deny()
	}
	id, err := v.claims(ctx, idToken)
	if err != nil || !audienceContains(id["aud"], OpenAIClientID) {
		return deny()
	}
	access, err := v.claims(ctx, accessToken)
	if err != nil || !audienceContains(access["aud"], "https://api.openai.com/v1") {
		return deny()
	}
	idSub, ok := id["sub"].(string)
	if !ok || !identityString(idSub, 256) || access["sub"] != idSub {
		return deny()
	}
	idMeta, ok := id["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return deny()
	}
	accessMeta, ok := access["https://api.openai.com/auth"].(map[string]any)
	if !ok || idMeta["chatgpt_account_id"] != account || accessMeta["chatgpt_account_id"] != account {
		return deny()
	}
	user := signedUser(idMeta)
	if !identityString(user, 256) || signedUser(accessMeta) != user {
		return deny()
	}
	plan, ok := idMeta["chatgpt_plan_type"].(string)
	if !ok || !identityString(plan, 64) || accessMeta["chatgpt_plan_type"] != plan {
		return deny()
	}
	idExp, _ := claimUnix(id["exp"])
	accessExp, _ := claimUnix(access["exp"])
	return CodexIdentity{Issuer: openAIIssuer, ClientID: OpenAIClientID, Subject: idSub, AccountID: account, UserID: user, Plan: plan, IDExpires: time.Unix(idExp, 0).UTC(), AccessExpires: time.Unix(accessExp, 0).UTC()}, nil
}

func signedUser(m map[string]any) string {
	a, _ := m["chatgpt_user_id"].(string)
	b, _ := m["user_id"].(string)
	if a != "" && b != "" && a != b {
		return ""
	}
	if a != "" {
		return a
	}
	return b
}
func identityString(s string, max int) bool {
	if len(s) == 0 || len(s) > max || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 33 || r == 127 {
			return false
		}
	}
	return true
}
func audienceContains(value any, want string) bool {
	if audience, ok := value.(string); ok {
		return audience == want
	}
	values, ok := value.([]any)
	if !ok || len(values) == 0 || len(values) > 8 {
		return false
	}
	seen := map[string]bool{}
	found := false
	for _, value := range values {
		a, ok := value.(string)
		if !ok || !identityString(a, 256) || seen[a] {
			return false
		}
		seen[a] = true
		found = found || a == want
	}
	return found
}
func claimUnix(value any) (int64, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	x, e := n.Int64()
	return x, e == nil && x > 0
}

func (v *CodexIdentityVerifier) claims(ctx context.Context, token string) (map[string]any, error) {
	if len(token) == 0 || len(token) > 64<<10 {
		return nil, ErrUnverifiedCodexIdentity
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrUnverifiedCodexIdentity
	}
	headerBytes, e := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	header, e := identityObject(headerBytes)
	if e != nil || header["alg"] != "RS256" {
		return nil, ErrUnverifiedCodexIdentity
	}
	for key := range header {
		if key != "alg" && key != "kid" && key != "typ" {
			return nil, ErrUnverifiedCodexIdentity
		}
	}
	if typ, exists := header["typ"]; exists && typ != "JWT" && typ != "at+jwt" {
		return nil, ErrUnverifiedCodexIdentity
	}
	kid, ok := header["kid"].(string)
	if !ok || !identityString(kid, 128) {
		return nil, ErrUnverifiedCodexIdentity
	}
	key, e := v.key(ctx, kid)
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	signature, e := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature) != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	payload, e := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	claims, e := identityObject(payload)
	if e != nil || claims["iss"] != openAIIssuer {
		return nil, ErrUnverifiedCodexIdentity
	}
	now := v.now().Unix()
	expiry, ok := claimUnix(claims["exp"])
	if !ok || expiry <= now || expiry > now+8*24*60*60 {
		return nil, ErrUnverifiedCodexIdentity
	}
	issued, ok := claimUnix(claims["iat"])
	if !ok || issued > now+30 || issued >= expiry || issued < now-8*24*60*60 || expiry-issued > 8*24*60*60 {
		return nil, ErrUnverifiedCodexIdentity
	}
	if value, exists := claims["nbf"]; exists {
		notBefore, ok := claimUnix(value)
		if !ok || notBefore > now+30 || notBefore >= expiry {
			return nil, ErrUnverifiedCodexIdentity
		}
	}
	return claims, nil
}

func (v *CodexIdentityVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if now.Before(v.expires) {
		if key := v.keys[kid]; key != nil {
			return key, nil
		}
	}
	// One shared refresh per 30s bounds unknown-kid and failed-fetch amplification.
	if !v.fetched.IsZero() && now.Before(v.fetched.Add(30*time.Second)) {
		return nil, ErrUnverifiedCodexIdentity
	}
	v.fetched = now
	fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(fetchCtx, http.MethodGet, openAIJWKS, nil)
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	req.Header.Set("Accept", "application/json")
	response, e := v.client.Do(req)
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength > 256<<10 || response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		return nil, ErrUnverifiedCodexIdentity
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if e != nil || len(raw) > 256<<10 {
		return nil, ErrUnverifiedCodexIdentity
	}
	document, e := identityObject(raw)
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	entries, ok := document["keys"].([]any)
	if !ok || len(entries) == 0 || len(entries) > 32 {
		return nil, ErrUnverifiedCodexIdentity
	}
	keys := map[string]*rsa.PublicKey{}
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, ErrUnverifiedCodexIdentity
		}
		id, ok := m["kid"].(string)
		if !ok || !identityString(id, 128) || keys[id] != nil || m["kty"] != "RSA" {
			return nil, ErrUnverifiedCodexIdentity
		}
		if alg, exists := m["alg"]; exists && alg != "RS256" {
			return nil, ErrUnverifiedCodexIdentity
		}
		if use, exists := m["use"]; exists && use != "sig" {
			return nil, ErrUnverifiedCodexIdentity
		}
		if operations, exists := m["key_ops"]; exists {
			values, ok := operations.([]any)
			if !ok || len(values) != 1 || values[0] != "verify" {
				return nil, ErrUnverifiedCodexIdentity
			}
		}
		n, ok := m["n"].(string)
		if !ok || len(n) > 1024 {
			return nil, ErrUnverifiedCodexIdentity
		}
		exponent, ok := m["e"].(string)
		if !ok || len(exponent) > 16 {
			return nil, ErrUnverifiedCodexIdentity
		}
		nb, e := base64.RawURLEncoding.Strict().DecodeString(n)
		if e != nil {
			return nil, ErrUnverifiedCodexIdentity
		}
		eb, e := base64.RawURLEncoding.Strict().DecodeString(exponent)
		if e != nil || len(eb) > 4 {
			return nil, ErrUnverifiedCodexIdentity
		}
		modulus := new(big.Int).SetBytes(nb)
		if modulus.BitLen() < 2048 || modulus.BitLen() > 4096 {
			return nil, ErrUnverifiedCodexIdentity
		}
		ei := 0
		for _, b := range eb {
			ei = ei*256 + int(b)
		}
		if ei < 3 || ei > 65537 || ei%2 == 0 {
			return nil, ErrUnverifiedCodexIdentity
		}
		keys[id] = &rsa.PublicKey{N: modulus, E: ei}
	}
	v.keys = keys
	v.expires = now.Add(5 * time.Minute)
	if key := keys[kid]; key != nil {
		return key, nil
	}
	return nil, ErrUnverifiedCodexIdentity
}

func identityObject(raw []byte) (map[string]any, error) {
	if len(raw) > 256<<10 || !utf8.Valid(raw) {
		return nil, ErrUnverifiedCodexIdentity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, e := identityJSON(decoder, 0)
	if e != nil {
		return nil, ErrUnverifiedCodexIdentity
	}
	if _, e = decoder.Token(); e != io.EOF {
		return nil, ErrUnverifiedCodexIdentity
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrUnverifiedCodexIdentity
	}
	return object, nil
}
func identityJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, ErrUnverifiedCodexIdentity
	}
	token, e := d.Token()
	if e != nil {
		return nil, e
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, e
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrUnverifiedCodexIdentity
			}
			if _, exists := object[name]; exists {
				return nil, ErrUnverifiedCodexIdentity
			}
			value, e := identityJSON(d, depth+1)
			if e != nil {
				return nil, e
			}
			object[name] = value
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, ErrUnverifiedCodexIdentity
		}
		return object, nil
	case '[':
		var array []any
		for d.More() {
			value, e := identityJSON(d, depth+1)
			if e != nil {
				return nil, e
			}
			array = append(array, value)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, ErrUnverifiedCodexIdentity
		}
		return array, nil
	default:
		return nil, ErrUnverifiedCodexIdentity
	}
}
