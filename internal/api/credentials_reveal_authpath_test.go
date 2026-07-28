package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// T-R7 (L9) — agents never reveal.
//
// The threat this closes is specific: a compromised agent already holds a
// credential (that is how it does its job) and the sidecar already speaks to
// the API on its behalf. If reveal were reachable from that plane, one
// compromised container would drain the whole vault, and every other layer in
// §2.6 — classification, workspace switch, capability, reason — would still
// have been satisfied, because the agent inherits a human's identity.
//
// So the gate is on the CREDENTIAL SHAPE, not the identity: only a live,
// revocable user session counts. A bearer token that survives logout does
// not, no matter whose name is on it.
//
// Every case below is otherwise fully authorised — OWNER, capability granted,
// workspace switch on, substantive reason, STANDARD classification — so a 403
// can only be the auth-path gate.

// revealAuthPathRig builds the "everything else passes" fixture the cases share.
func revealAuthPathRig(t *testing.T) *revealRig {
	t.Helper()
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-auth", true)
	r.seedMember(t, "ws-auth", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-auth", "u-owner", "cred-1", "GH_TOKEN", "ghp_authpathvalue", SensitivityStandard)
	return r
}

func TestReveal_APITokenPathDenied(t *testing.T) {
	r := revealAuthPathRig(t)

	// A crewship_cli_… / crewship_admin_… bearer. RequireAuth resolves it to
	// a real user with a real role — the identity is genuine, the shape is
	// not interactive. AuthUser.SessionID is empty because CLI tokens have
	// no user_sessions row.
	req := r.revealReq("cred-1", "ws-auth", "u-owner", "OWNER", validRevealReason)
	ctx := withUser(req.Context(), &AuthUser{ID: "u-owner", Email: "u-owner@example.com"})
	ctx = withWorkspace(ctx, "ws-auth", "OWNER")
	ctx = withAuthKind(ctx, AuthKindCLIToken)
	rec := r.doReveal(req.WithContext(ctx))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 (API token must never reveal)", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "ghp_authpathvalue") {
		t.Fatal("API-token reveal leaked the credential value")
	}
	if len(r.j.all()) != 0 {
		t.Fatalf("refused API-token reveal wrote %d journal entries", len(r.j.all()))
	}
}

// The internal/sidecar plane never runs RequireAuth at all — /api/v1/internal
// routes are wrapped by requireInternal, and the credential adapters
// synthesize an AuthUser with a hard-coded role to reuse the public handlers
// (internal_credentials_mutate.go does exactly this with role "ADMIN"). A
// context built that way has NO auth kind, and the gate must read that
// absence as "deny", not as "unknown, allow".
func TestReveal_InternalAdapterContextDenied(t *testing.T) {
	r := revealAuthPathRig(t)

	// Built from scratch rather than from revealReq, so the context carries
	// EXACTLY what internal_credentials_mutate.go's envelope builds: a
	// synthesized user with the placeholder email, a hard-coded ADMIN role,
	// and no auth kind at all — RequireAuth never ran on this plane.
	body, _ := json.Marshal(map[string]string{"reason": validRevealReason})
	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-1/reveal", strings.NewReader(string(body)))
	ctx := withUser(req.Context(), &AuthUser{ID: "u-owner", Email: "x-internal"})
	ctx = withWorkspace(ctx, "ws-auth", "ADMIN")
	req = req.WithContext(ctx)
	req.SetPathValue("credentialId", "cred-1")
	rec := r.doReveal(req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 (sidecar/internal plane must never reveal)", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "ghp_authpathvalue") {
		t.Fatal("internal-plane reveal leaked the credential value")
	}
}

// Fail-closed on an unset auth kind, stated as its own case. The two above
// both happen to be denied; this one pins the RULE — anything that is not
// explicitly AuthKindSession is refused — so a future auth method added
// without touching this gate is denied by default rather than admitted by
// omission.
func TestReveal_UnknownAuthKindDeniedFailClosed(t *testing.T) {
	r := revealAuthPathRig(t)

	for _, kind := range []string{"", "webhook", "oauth-device", "totally-new-in-2027"} {
		name := kind
		if name == "" {
			name = "(absent)"
		}
		t.Run(name, func(t *testing.T) {
			req := r.revealReq("cred-1", "ws-auth", "u-owner", "OWNER", validRevealReason)
			ctx := withUser(req.Context(), &AuthUser{ID: "u-owner", SessionID: "sess-u-owner"})
			ctx = withWorkspace(ctx, "ws-auth", "OWNER")
			ctx = withAuthKind(ctx, kind)
			rec := r.doReveal(req.WithContext(ctx))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("auth kind %q: status = %d body=%s, want 403", kind, rec.Code, rec.Body.String())
			}
		})
	}
}

// An unauthenticated context (no user at all) must not panic its way to a
// 500 that a caller could distinguish from a deny.
func TestReveal_NoUserDenied(t *testing.T) {
	r := revealAuthPathRig(t)

	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-1/reveal", nil)
	ctx := withWorkspace(req.Context(), "ws-auth", "OWNER")
	ctx = withAuthKind(ctx, AuthKindSession)
	req = req.WithContext(ctx)
	req.SetPathValue("credentialId", "cred-1")

	rec := r.doReveal(req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

// RequireAuth is the only writer of the auth kind, and both of its branches
// must set it. A branch that forgot would make every request from that path
// indistinguishable from the internal plane — which fails closed for reveal,
// but would also silently break any future consumer that trusts the value.
// Asserting the constants are distinct and non-empty is the cheap guard that
// the two branches cannot collapse into one.
func TestAuthKindConstantsAreDistinct(t *testing.T) {
	if AuthKindSession == "" || AuthKindCLIToken == "" {
		t.Fatal("auth kind constants must be non-empty; the empty string means 'never authenticated'")
	}
	if AuthKindSession == AuthKindCLIToken {
		t.Fatal("session and CLI-token auth kinds must be distinguishable")
	}
}
