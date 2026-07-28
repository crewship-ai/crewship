package api

import (
	"net/http"
	"testing"
)

// Reveal gate matrix — PRD-CREDENTIALS-V2-2026 §2.6, tests T-R1…T-R6 and
// T-R9. Each test isolates ONE layer by making every other layer pass, so a
// green suite means each layer denies on its own rather than the first one
// masking the rest. That property matters more than usual here: §2.6's whole
// argument is that reveal survives the compromise of any single condition.

// T-R1 (L1) — a workspace that has never turned reveal on denies it, even to
// an OWNER holding the capability for a STANDARD credential. Default deny is
// the point: a freshly created tenant has no reveal at all, and enabling it
// is a decision someone made, not a state they inherited.
func TestReveal_WorkspaceDefaultDeny(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-deny", false)
	r.seedMember(t, "ws-deny", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-deny", "u-owner", "cred-1", "GH_TOKEN", "ghp_supersecretvalue", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-1", "ws-deny", "u-owner", "OWNER", validRevealReason))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 (reveal disabled for workspace)", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "ghp_supersecretvalue") {
		t.Fatal("denied reveal leaked the credential value into the response body")
	}
	if len(r.j.all()) != 0 {
		t.Fatalf("denied reveal wrote %d journal entries; a refusal is not a reveal", len(r.j.all()))
	}
}

// T-R1b — the same workspace, switch on, is the control: without it the test
// above would pass against a handler that denies everything.
func TestReveal_EnabledWorkspaceAllowsOwnerWithCapability(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-ok", true)
	r.seedMember(t, "ws-ok", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-ok", "u-owner", "cred-1", "GH_TOKEN", "ghp_supersecretvalue", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-1", "ws-ok", "u-owner", "OWNER", validRevealReason))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := revealValue(t, rec); got != "ghp_supersecretvalue" {
		t.Fatalf("value = %q, want the decrypted secret", got)
	}
	// L3.6 — the value must never be cached by a proxy or a browser.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// T-R2 (L2) — an ADMIN whose membership carries no explicit capability set.
// This is the shape EVERY pre-existing row has: capabilities IS NULL, so
// resolveCapabilitiesFromRow falls back to the role bundle. If
// credentials:reveal ever slips into BundleAdmin this test goes green for the
// wrong reason and L2 is gone, which is why it asserts on the NULL-caps row
// specifically rather than on an explicitly-drained one.
func TestReveal_AdminWithoutCapabilityDenied(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-cap", true)
	r.seedMember(t, "ws-cap", "u-admin", "ADMIN", nil) // NULL capabilities → role fallback
	r.seedCredential(t, "ws-cap", "u-admin", "cred-1", "GH_TOKEN", "ghp_adminshouldnotsee", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-1", "ws-cap", "u-admin", "ADMIN", validRevealReason))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403 (ADMIN role alone is not sufficient)", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "ghp_adminshouldnotsee") {
		t.Fatal("denied reveal leaked the credential value")
	}
}

// T-R2b — the same ADMIN, granted the capability explicitly, passes. Proves
// the deny above is about the capability and not about the role.
func TestReveal_AdminWithCapabilityAllowed(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-cap2", true)
	r.seedMember(t, "ws-cap2", "u-admin", "ADMIN", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-cap2", "u-admin", "cred-1", "GH_TOKEN", "ghp_value", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-1", "ws-cap2", "u-admin", "ADMIN", validRevealReason))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

// T-R3 — MEMBER and VIEWER are below the role floor and stay below it even
// when the capability is granted by mistake. The credential here is
// WORKSPACE-scoped, i.e. one they can genuinely SEE in the list: seeing a
// secret's name and revealing its value are different privileges, and the
// capability must not be usable to ladder from one to the other.
func TestReveal_MemberAndViewerDeniedEvenWithCapability(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-low", true)
	r.seedMember(t, "ws-low", "u-owner", "OWNER", nil)
	r.seedCredential(t, "ws-low", "u-owner", "cred-1", "GH_TOKEN", "ghp_lowrolevalue", SensitivityStandard)

	for _, role := range []string{"MEMBER", "VIEWER"} {
		t.Run(role, func(t *testing.T) {
			userID := "u-" + role
			r.seedMember(t, "ws-low", userID, role, []string{CapabilityChat, CapabilityCredentialReveal})

			rec := r.doReveal(r.revealReq("cred-1", "ws-low", userID, role, validRevealReason))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body.String())
			}
			if bodyMentions(rec, "ghp_lowrolevalue") {
				t.Fatal("denied reveal leaked the credential value")
			}
		})
	}
}

// T-R4 — a MANAGER with the capability is confined to their crew scope.
// This is the one place reveal deliberately diverges from
// credentialVisibilityFilter: that filter gives MANAGER+ the whole
// workspace (they own the lifecycle — rotate, revoke, audit), which is
// correct for METADATA and wrong for plaintext. §7 decision #3 grants
// MANAGER reveal "only in their scope", so the reveal gate re-derives the
// crew-scoped branch of the filter for anyone below OWNER/ADMIN.
//
// In-workspace-but-out-of-scope answers 403, not 404: the MANAGER can already
// see this credential in the list, so hiding its existence would be theatre.
// Contrast with the cross-tenant case below.
func TestReveal_ManagerOutsideCrewScopeDenied(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-mgr", true)
	r.seedMember(t, "ws-mgr", "u-owner", "OWNER", nil)
	r.seedMember(t, "ws-mgr", "u-mgr", "MANAGER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-mgr", "u-owner", "cred-crew", "PROD_KEY", "prod_value_secret", SensitivityStandard)
	r.scopeToCrew(t, "ws-mgr", "cred-crew", "crew-payments")

	t.Run("outside the crew → 403", func(t *testing.T) {
		rec := r.doReveal(r.revealReq("cred-crew", "ws-mgr", "u-mgr", "MANAGER", validRevealReason))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body.String())
		}
		if bodyMentions(rec, "prod_value_secret") {
			t.Fatal("out-of-scope reveal leaked the credential value")
		}
	})

	// Allow path. Without it a handler that denies every MANAGER would pass
	// the deny case and quietly delete §7 decision #3.
	t.Run("inside the crew → 200", func(t *testing.T) {
		r.joinCrew(t, "crew-payments", "u-mgr")
		rec := r.doReveal(r.revealReq("cred-crew", "ws-mgr", "u-mgr", "MANAGER", validRevealReason))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
		}
		if got := revealValue(t, rec); got != "prod_value_secret" {
			t.Fatalf("value = %q, want the decrypted secret", got)
		}
	})
}

// T-R5 — a credential in ANOTHER tenant answers 404, never 403. A 403 would
// confirm the id exists somewhere in the fleet, which is enough to turn a
// leaked id (a screenshot, a support ticket, a log line) into a tenancy
// oracle. The caller here is a fully privileged OWNER of their own workspace
// with the capability and reveal switched on, so the 404 can only come from
// the tenancy scope.
func TestReveal_CrossTenantIsNotFound(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-a", true)
	r.seedWorkspace(t, "ws-b", true)
	r.seedMember(t, "ws-a", "u-a", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedMember(t, "ws-b", "u-b", "OWNER", nil)
	r.seedCredential(t, "ws-b", "u-b", "cred-b", "OTHER_TENANT_KEY", "other_tenant_secret", SensitivityStandard)

	rec := r.doReveal(r.revealReq("cred-b", "ws-a", "u-a", "OWNER", validRevealReason))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404 (must not disclose that the id exists)", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "other_tenant_secret") || bodyMentions(rec, "OTHER_TENANT_KEY") {
		t.Fatal("cross-tenant reveal leaked value or name")
	}
}

// T-R6 (L0) — SEALED has no escape hatch. Every role, including OWNER with
// the capability in a reveal-enabled workspace, is refused. If this ever
// goes green for only some roles, SEALED has become "RESTRICTED with extra
// steps" and the classification stops meaning anything.
func TestReveal_SealedDeniedForEveryRole(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-sealed", true)
	r.seedMember(t, "ws-sealed", "u-seed", "OWNER", nil)
	r.seedCredential(t, "ws-sealed", "u-seed", "cred-sealed", "PROD_DB_DSN", "postgres://sealedsecret", SensitivitySealed)

	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		t.Run(role, func(t *testing.T) {
			userID := "u-sealed-" + role
			r.seedMember(t, "ws-sealed", userID, role, []string{CapabilityChat, CapabilityCredentialReveal})

			rec := r.doReveal(r.revealReq("cred-sealed", "ws-sealed", userID, role, validRevealReason))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s, want 403 — SEALED is never revealable", rec.Code, rec.Body.String())
			}
			if bodyMentions(rec, "postgres://sealedsecret") {
				t.Fatal("SEALED reveal leaked the credential value")
			}
		})
	}
	if len(r.j.all()) != 0 {
		t.Fatalf("SEALED denials wrote %d journal entries; nothing was revealed", len(r.j.all()))
	}
}

// T-R9 (L3.3) — the reason is mandatory and has to say something. An empty
// or one-word reason makes the audit trail unreadable six months later,
// which is when it is actually needed; §2.6 calls out "test" by name.
//
// The caller is fully authorised, so a 400 here can only come from the
// reason check.
func TestReveal_ReasonMustBeSubstantive(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-reason", true)
	r.seedMember(t, "ws-reason", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-reason", "u-owner", "cred-1", "GH_TOKEN", "ghp_reasonvalue", SensitivityStandard)

	cases := []struct {
		name   string
		reason string
	}{
		{"empty", ""},
		{"whitespace only", "   \t\n  "},
		{"too short", "need it"},
		{"generic: test", "test"},
		{"generic: testing (padded to length)", "testing testing testing"},
		{"generic: n/a", "n/a n/a n/a n/a n/a n/a"},
		{"single repeated word", "debug debug debug debug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := r.doReveal(r.revealReq("cred-1", "ws-reason", "u-owner", "OWNER", tc.reason))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400 for reason %q", rec.Code, rec.Body.String(), tc.reason)
			}
			if bodyMentions(rec, "ghp_reasonvalue") {
				t.Fatal("rejected reveal leaked the credential value")
			}
		})
	}
	if len(r.j.all()) != 0 {
		t.Fatalf("rejected reasons wrote %d journal entries", len(r.j.all()))
	}
}

// T-R6b — a RESTRICTED credential is revealable by the core gate. Four-eyes
// (§2.6 L3.4) is explicitly deferred, so RESTRICTED behaves like STANDARD
// today. Pinning that here means the day four-eyes lands, this test fails and
// forces a deliberate update rather than silently describing old behaviour.
func TestReveal_RestrictedBehavesLikeStandardUntilFourEyes(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-restricted", true)
	r.seedMember(t, "ws-restricted", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-restricted", "u-owner", "cred-1", "DEPLOY_KEY", "restricted_value", SensitivityRestricted)

	rec := r.doReveal(r.revealReq("cred-1", "ws-restricted", "u-owner", "OWNER", validRevealReason))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

// A soft-deleted credential is gone as far as reveal is concerned. Without
// this, revoking a leaked secret would leave its plaintext reachable through
// an endpoint whose whole purpose is disclosure.
func TestReveal_SoftDeletedCredentialIsNotFound(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-del", true)
	r.seedMember(t, "ws-del", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-del", "u-owner", "cred-1", "GH_TOKEN", "ghp_deletedvalue", SensitivityStandard)
	if _, err := r.db.Exec(`UPDATE credentials SET deleted_at = datetime('now') WHERE id = 'cred-1'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	rec := r.doReveal(r.revealReq("cred-1", "ws-del", "u-owner", "OWNER", validRevealReason))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if bodyMentions(rec, "ghp_deletedvalue") {
		t.Fatal("reveal of a soft-deleted credential leaked the value")
	}
}
