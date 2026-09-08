package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRevealDefaultCapabilities(t *testing.T) {
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER", "unknown"} {
		for _, raw := range []sql.NullString{{}, {Valid: true, String: `[]`}, {Valid: true, String: `["chat"]`}, {Valid: true, String: `invalid`}} {
			t.Run(role+"/"+raw.String, func(t *testing.T) {
				got := HasCapability(resolveCapabilitiesFromRow(raw, role), CapabilityCredentialReveal)
				want := !raw.Valid && (role == "OWNER" || role == "ADMIN")
				if got != want {
					t.Fatalf("reveal = %v, want %v", got, want)
				}
			})
		}
	}
}

func TestReveal_DefaultAdminAndOwnerAllowed(t *testing.T) {
	for _, role := range []string{"OWNER", "ADMIN"} {
		t.Run(role, func(t *testing.T) {
			r := newRevealRig(t)
			r.seedWorkspace(t, "ws-default", true)
			r.seedMember(t, "ws-default", "u-admin", role, nil)
			r.seedCredential(t, "ws-default", "u-admin", "cred-default", "DEMO", "dummy-only", SensitivityStandard)
			rec := r.doReveal(r.revealReq("cred-default", "ws-default", "u-admin", role, validRevealReason))
			if rec.Code != http.StatusOK || revealValue(t, rec) != "dummy-only" {
				t.Fatalf("default admin reveal failed: status %d", rec.Code)
			}
			if len(r.j.all()) == 0 {
				t.Fatal("default grant bypassed audit")
			}
		})
	}
}

func TestWorkspaceList_AdminDefaultReveal(t *testing.T) {
	h, f := newWsCapsHandler(t)
	f.seedMember(t, h, "ADMIN", "")
	for _, capability := range listWorkspaceCaps(t, h, f) {
		if capability == CapabilityCredentialReveal {
			return
		}
	}
	t.Fatal("workspace API did not expose the default reveal capability to the UI")
}

func TestReveal_DefaultAdminCanBeRevokedThroughAPI(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-revoke-default", true)
	r.seedMember(t, "ws-revoke-default", "u-owner", "OWNER", nil)
	r.seedMember(t, "ws-revoke-default", "u-admin", "ADMIN", nil)
	r.seedCredential(t, "ws-revoke-default", "u-owner", "cred-revoke", "DEMO", "dummy-only", SensitivityStandard)
	before := r.doReveal(r.revealReq("cred-revoke", "ws-revoke-default", "u-admin", "ADMIN", validRevealReason))
	if before.Code != http.StatusOK {
		t.Fatalf("default reveal status = %d", before.Code)
	}
	h := NewWorkspaceHandler(r.db, revealTestLogger())
	req := httptest.NewRequest("PATCH", "/capabilities", strings.NewReader(`{"revoke":["credentials:reveal"]}`))
	req.SetPathValue("memberId", "u-admin")
	req = withWorkspaceUser(req, "u-owner", "ws-revoke-default", "OWNER")
	rec := httptest.NewRecorder()
	h.PatchMemberCapabilities(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d", rec.Code)
	}
	after := r.doReveal(r.revealReq("cred-revoke", "ws-revoke-default", "u-admin", "ADMIN", validRevealReason))
	if after.Code != http.StatusForbidden {
		t.Fatalf("revoked reveal status = %d, want 403", after.Code)
	}
}
