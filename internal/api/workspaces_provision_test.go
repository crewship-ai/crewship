package api

// Adding a person to a workspace was impossible without a mail server.
// CreateInvitation wrote a workspace_invitations row, never called the
// mailer (it does not hold one), and the API's token never reached the UI —
// so the invitee learned nothing and the admin had no link to pass on. The
// button worked; the flow did not exist.
//
// Provision replaces it with one action: create the user if needed, put them
// in the workspace, and hand the admin a setup link they can send however
// they like. No password is ever chosen by the admin or transmitted — the
// invitee sets their own via the reset-password page that already ships.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// provisionRig sets the public origin every setup link is built from. A
// handler that cannot build a link refuses the whole call (see
// TestProvision_RefusesWithoutAPublicURL), so every other case needs it.
func provisionRig(t *testing.T) (*WorkspaceHandler, string, string) {
	t.Helper()
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example.com")
	return membershipRig(t)
}

func provisionReq(t *testing.T, h *WorkspaceHandler, userID, wsID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/members/provision?workspace_id="+wsID, strings.NewReader(body))
	req.SetPathValue("workspaceId", wsID)
	ctx := context.WithValue(req.Context(), ctxUser, &AuthUser{ID: userID})
	ctx = context.WithValue(ctx, ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ProvisionMember(rr, req)
	return rr
}

func decodeProvision(t *testing.T, rr *httptest.ResponseRecorder) provisionResponse {
	t.Helper()
	var out provisionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v — body %s", err, rr.Body.String())
	}
	return out
}

func TestProvision_CreatesTheUserAndHandsBackASetupLink(t *testing.T) {
	h, userID, wsID := provisionRig(t)

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"new@example.com","role":"MEMBER"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rr.Code, rr.Body.String())
	}
	out := decodeProvision(t, rr)

	// The link is the entire point: with no mailer wired, it is the only
	// way the invitee ever learns the account exists.
	if out.SetupURL == "" {
		t.Error("no setup_url returned — the admin has nothing to send")
	}
	if !strings.Contains(out.SetupURL, "token=") {
		t.Errorf("setup_url carries no token: %q", out.SetupURL)
	}
	if !out.CreatedUser {
		t.Error("created_user = false for a brand-new email")
	}
}

func TestProvision_LeavesNoPasswordOnTheNewAccount(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	provisionReq(t, h, userID, wsID, "OWNER", `{"email":"new@example.com","role":"MEMBER"}`)

	var hashed sql.NullString
	if err := h.db.QueryRow(`SELECT hashed_password FROM users WHERE email = ?`, "new@example.com").Scan(&hashed); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	// The admin never picks or sees a password; the invitee sets their own.
	// A pre-set password would be a credential in a Slack message forever.
	if hashed.Valid && hashed.String != "" {
		t.Error("provisioned account has a password set — nobody should have chosen one")
	}
}

func TestProvision_SetupTokenOutlivesAPasswordReset(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	provisionReq(t, h, userID, wsID, "OWNER", `{"email":"new@example.com","role":"MEMBER"}`)

	var expires string
	if err := h.db.QueryRow(
		`SELECT expires FROM verification_tokens WHERE identifier = ? AND purpose = 'account_setup'`,
		"new@example.com").Scan(&expires); err != nil {
		t.Fatalf("no account_setup token stored: %v", err)
	}
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		t.Fatalf("parse expires %q: %v", expires, err)
	}
	// A reset link is 30 minutes because the user asked for it and is
	// waiting. A setup link is sent by someone else, possibly on a Friday
	// afternoon — 30 minutes would strand every real handover.
	if until := time.Until(exp); until < 24*time.Hour {
		t.Errorf("setup token expires in %s; too short to send out of band", until)
	}
}

func TestProvision_ExistingUserIsAddedNotDuplicated(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	seedOtherUser(t, h, "user-existing", "existing@example.com")

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"existing@example.com","role":"MANAGER"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rr.Code, rr.Body.String())
	}
	out := decodeProvision(t, rr)
	if out.CreatedUser {
		t.Error("created_user = true for an email that already had an account")
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "existing@example.com").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("users with that email = %d, want 1 — provisioning must not fork an account", n)
	}
}

func TestProvision_AlreadyAMemberIsRejectedClearly(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	provisionReq(t, h, userID, wsID, "OWNER", `{"email":"dup@example.com","role":"MEMBER"}`)

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"dup@example.com","role":"MEMBER"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for someone already in the workspace", rr.Code)
	}
}

func TestProvision_RequiresAdminTier(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	// Mirrors POST /members, which is roleManage. A MANAGER may change a
	// role but must not mint accounts.
	for _, role := range []string{"MANAGER", "MEMBER", "VIEWER"} {
		rr := provisionReq(t, h, userID, wsID, role, `{"email":"x@example.com","role":"MEMBER"}`)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s got %d, want 403", role, rr.Code)
		}
	}
}

func TestProvision_RejectsAnInvalidRole(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"x@example.com","role":"SUPERUSER"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown role", rr.Code)
	}
}

func TestProvision_RejectsAMissingEmail(t *testing.T) {
	h, userID, wsID := provisionRig(t)
	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"role":"MEMBER"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without an email", rr.Code)
	}
}

func TestProvision_WorksWithNoConfiguredPublicURL(t *testing.T) {
	t.Setenv("CREWSHIP_PUBLIC_URL", "")
	h, userID, wsID := membershipRig(t)

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"new@example.com","role":"MEMBER"}`)
	// Requiring an env var to add a colleague is friction a self-hosted
	// product should not impose. The link is returned to the admin who just
	// made the request, so the host they are already browsing is the right
	// origin — and the only person a forged Host could mislead is themselves.
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rr.Code, rr.Body.String())
	}
	out := decodeProvision(t, rr)
	if !strings.Contains(out.SetupURL, "example.com") {
		t.Errorf("setup_url %q was not derived from the request host", out.SetupURL)
	}
}

func TestProvision_ConfiguredPublicURLWins(t *testing.T) {
	t.Setenv("CREWSHIP_PUBLIC_URL", "https://crewship.example.com")
	h, userID, wsID := membershipRig(t)

	rr := provisionReq(t, h, userID, wsID, "OWNER", `{"email":"new@example.com","role":"MEMBER"}`)
	out := decodeProvision(t, rr)
	// An operator behind a proxy whose Host differs from the public name
	// must be able to pin it; config beats derivation, never the reverse.
	if !strings.HasPrefix(out.SetupURL, "https://crewship.example.com/") {
		t.Errorf("setup_url = %q, want the configured origin", out.SetupURL)
	}
}
