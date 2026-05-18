package auth

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// ---------------------------------------------------------------------------
// jwt.go — SetClock (test-only clock injection).
//
// SetClock overrides the iat/exp source used by issue(). Production never
// touches it; tests use it to mint tokens with timestamps in the past or
// future without sleeping. The source comment explicitly notes:
//
//   "Validation always uses time.Now directly because the validator's
//    clock and the world clock are the same in production, and we want
//    validation to reflect real wall time even in tests that issue
//    forged tokens."
//
// That asymmetry — issuance honors the injected clock, validation doesn't
// — is exactly what these tests pin.
// ---------------------------------------------------------------------------

func TestSetClock_InstallsCustomNowAndIatExpReflectIt(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	// Pick a fixed point well in the past so the assertion isn't a
	// race with real wall-time.
	fixed := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	v.SetClock(func() time.Time { return fixed })

	tok, err := v.IssueAccessToken("u1", "s1", "Name", "n@x")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}

	// Real validation: this token is from 2020, far past now. Must be
	// rejected as ErrTokenExpired — proves issue() used the injected
	// clock for iat/exp (not time.Now) AND that validate() uses real
	// wall time (not the injected clock).
	if _, err := v.ValidateAccess(tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("ValidateAccess on 2020-issued token = %v, want ErrTokenExpired", err)
	}

	// Decrypt the claims directly to inspect iat/exp. The exp test above
	// already proves "old enough to be expired", but we want to pin the
	// exact iat = fixed.Unix() so a regression that shifts the clock-use
	// (e.g. nowFn called only for exp, not iat) is caught.
	claims := decryptClaimsForTest(t, v, tok, v.accessKey)
	if claims.Iat != fixed.Unix() {
		t.Errorf("iat = %d, want %d (fixed clock)", claims.Iat, fixed.Unix())
	}
	if claims.Exp != fixed.Add(AccessTokenTTL).Unix() {
		t.Errorf("exp = %d, want %d (fixed clock + AccessTokenTTL)", claims.Exp, fixed.Add(AccessTokenTTL).Unix())
	}
}

func TestSetClock_NilRestoresTimeNow(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	// Install a deterministic past clock, then nil-reset, then mint —
	// the mint must use the real wall clock again, so validation passes.
	v.SetClock(func() time.Time { return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) })
	v.SetClock(nil)

	// Bracket the issuance with before/after wall-clock samples so the
	// assertion stays accurate even under slow CI scheduling. A fixed
	// 5-second delta was flaky on a loaded runner.
	before := time.Now().Unix()
	tok, err := v.IssueAccessToken("u1", "s1", "Name", "n@x")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	after := time.Now().Unix()
	if _, err := v.ValidateAccess(tok); err != nil {
		t.Fatalf("ValidateAccess after SetClock(nil) = %v, want nil (real clock restored)", err)
	}

	// iat must fall in the wall-clock window we captured — if SetClock(nil)
	// silently kept the stale fn, iat would be the 2020 Unix timestamp
	// which is far below `before`.
	claims := decryptClaimsForTest(t, v, tok, v.accessKey)
	if claims.Iat < before || claims.Iat > after {
		t.Errorf("iat = %d outside issuance window [%d, %d]; SetClock(nil) did not restore time.Now", claims.Iat, before, after)
	}
}

func TestSetClock_FutureClockProducesValidToken(t *testing.T) {
	// Clock 10 minutes in the future → iat is in the future → exp is
	// 15+10 minutes in the future. Validation uses real time.Now so this
	// token has plenty of life left. (If someone "fixes" validation to
	// also use nowFn, the 5-second skew tolerance in validate would
	// surface that — the iat being in the future is acceptable to the
	// validator because it only checks exp.)
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	future := time.Now().Add(10 * time.Minute)
	v.SetClock(func() time.Time { return future })

	tok, err := v.IssueAccessToken("u1", "s1", "Name", "n@x")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	if _, err := v.ValidateAccess(tok); err != nil {
		t.Errorf("ValidateAccess on future-issued token = %v, want nil (exp far in future)", err)
	}
}

func TestSetClock_AffectsAllThreeKinds(t *testing.T) {
	// SetClock must affect Access, Refresh, and WS issuance uniformly —
	// they all flow through the same issue() that reads v.nowFn.
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	past := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	v.SetClock(func() time.Time { return past })

	access, err := v.IssueAccessToken("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	refresh, err := v.IssueRefreshToken("u1", "s1")
	if err != nil {
		t.Fatalf("IssueRefreshToken: %v", err)
	}
	ws, err := v.IssueWSTicket("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("IssueWSTicket: %v", err)
	}

	// All three must report iat at the fixed past — proves nowFn is read
	// inside issue() and not the per-kind wrapper.
	for _, tc := range []struct {
		name, tok string
		key       []byte
	}{
		{"access", access, v.accessKey},
		{"refresh", refresh, v.refreshKey},
		{"ws", ws, v.wsKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := decryptClaimsForTest(t, v, tc.tok, tc.key)
			if claims.Iat != past.Unix() {
				t.Errorf("%s iat = %d, want %d (fixed clock)", tc.name, claims.Iat, past.Unix())
			}
		})
	}
}

func TestSetClock_OverridingFnReplacesPriorOne(t *testing.T) {
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}

	first := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2021, 6, 15, 0, 0, 0, 0, time.UTC)
	v.SetClock(func() time.Time { return first })
	v.SetClock(func() time.Time { return second })

	tok, err := v.IssueAccessToken("u1", "s1", "", "")
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	claims := decryptClaimsForTest(t, v, tok, v.accessKey)
	if claims.Iat != second.Unix() {
		t.Errorf("iat = %d (want %d from second clock); SetClock must replace, not stack",
			claims.Iat, second.Unix())
	}
	if claims.Iat == first.Unix() {
		t.Errorf("iat = %d matches FIRST clock; SetClock was not replaced", claims.Iat)
	}
}

// decryptClaimsForTest peels a JWE open with the supplied key so the
// test can inspect iat/exp without going through validate() (which
// would reject the past-clock fixtures on the wall-time freshness
// check). Mirrors the validate() decrypt-and-unmarshal pair.
func decryptClaimsForTest(t *testing.T, _ *JWTValidator, tok string, key []byte) Claims {
	t.Helper()
	jwe, err := jose.ParseEncrypted(tok,
		[]jose.KeyAlgorithm{jose.DIRECT},
		[]jose.ContentEncryption{jose.A256CBC_HS512})
	if err != nil {
		t.Fatalf("parse jwe: %v", err)
	}
	plain, err := jwe.Decrypt(key)
	if err != nil {
		t.Fatalf("decrypt jwe: %v", err)
	}
	var c Claims
	if err := json.Unmarshal(plain, &c); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return c
}
