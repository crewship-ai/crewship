package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

type enrolledIdentityVerifier struct{ reject bool }

func (v *enrolledIdentityVerifier) Verify(_ context.Context, id, access, account string) (providerlogin.CodexIdentity, error) {
	if v.reject || id == "" || access == "" || account == "" {
		return providerlogin.CodexIdentity{}, providerlogin.ErrUnverifiedCodexIdentity
	}
	return providerlogin.CodexIdentity{Issuer: "https://auth.openai.com", ClientID: providerlogin.OpenAIClientID, Subject: "subject-own", AccountID: account, UserID: "user-own", Plan: "plus", AccessExpires: time.Now().Add(time.Hour).UTC().Truncate(time.Second), IDExpires: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}, nil
}
func TestProviderLoginRefreshEnrolledIdentityGuard(t *testing.T) {
	r := newPLRig(t)
	credential := r.seedCodexLogin(t, "verified-login", time.Hour)
	verifier := new(enrolledIdentityVerifier)
	store := providerlogin.NewCodexProofStore(r.db, verifier)
	r.rf.identityProof = store
	if e := store.Enroll(t.Context(), r.wsID, credential); e != nil {
		t.Fatal(e)
	}
	oldAccess := plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credentials WHERE id=?`, credential)
	oldRefresh := plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credential_fields WHERE credential_id=? AND key='refresh_token'`, credential)
	r.tokens.next = providerlogin.RefreshResult{AccessToken: "access-rotated", RefreshToken: "refresh-rotated", IDToken: "id-rotated", ExpiresAt: time.Now().Add(24 * time.Hour)}
	verifier.reject = true
	if _, e := r.rf.Refresh(t.Context(), credential, true); !errors.Is(e, providerlogin.ErrUnverifiedCodexIdentity) {
		t.Fatalf("foreign identity refresh=%v", e)
	}
	if plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credentials WHERE id=?`, credential) != oldAccess || plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credential_fields WHERE credential_id=? AND key='refresh_token'`, credential) != oldRefresh {
		t.Fatal("unverified rotation changed stored tokens")
	}
	verifier.reject = false
	if _, e := store.Current(t.Context(), r.wsID, credential); e == nil {
		t.Fatal("identity mismatch left proof enabled")
	}
	if e := store.Enroll(t.Context(), r.wsID, credential); e != nil {
		t.Fatal(e)
	}
	if _, e := r.rf.Refresh(t.Context(), credential, true); e != nil {
		t.Fatal(e)
	}
	proof, e := store.Current(t.Context(), r.wsID, credential)
	if e != nil {
		t.Fatal(e)
	}
	if plDecryptColumn(t, r.db, `SELECT encrypted_value FROM credentials WHERE id=?`, credential) != "access-rotated" {
		t.Fatal("verified rotation not committed")
	}
	var expiry string
	if e = r.db.QueryRow(`SELECT token_expires_at FROM credentials WHERE id=?`, credential).Scan(&expiry); e != nil {
		t.Fatal(e)
	}
	if expiry != proof.AccessExpires.Format(time.RFC3339) {
		t.Fatal("endpoint expiry overrode signed expiry")
	}
}
