package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// L1 — the workspace reveal switch. Turning it on is the moment a tenant
// stops being default-deny, so it is an event on the tamper-evident chain in
// its own right, not a settings diff someone can flip and unflip quietly.

// errJournalTestWedged stands in for "the chained write cannot commit". Shared
// with the sensitivity tests so both fail-closed paths use the same stimulus.
var errJournalTestWedged = errors.New("journal: chain write unavailable")

func (r *revealRig) policyReq(method, wsID, userID, role string, body any) *http.Request {
	var rdr *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, "/api/v1/credentials/reveal-policy", rdr)
	ctx := withUser(req.Context(), &AuthUser{ID: userID, SessionID: "sess-" + userID})
	ctx = withWorkspace(ctx, wsID, role)
	ctx = withAuthKind(ctx, AuthKindSession)
	return req.WithContext(ctx)
}

func (r *revealRig) policyEnabled(t *testing.T, wsID string) bool {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT credential_reveal_enabled FROM workspaces WHERE id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("read policy: %v", err)
	}
	return n == 1
}

// A workspace created through the ordinary path — no mention of reveal
// anywhere — comes out with reveal off. The column default is the enforcement;
// this test is what stops a later migration or seed from "helpfully"
// defaulting it to 1.
func TestRevealPolicy_DefaultsOffForNewWorkspaces(t *testing.T) {
	r := newRevealRig(t)
	if _, err := r.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-fresh', 'Fresh', 'fresh')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if r.policyEnabled(t, "ws-fresh") {
		t.Fatal("a freshly created workspace has reveal enabled; L1 default-deny is gone")
	}
}

func TestRevealPolicy_OwnerCanEnableAndItIsJournaled(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-pol", false)
	r.seedMember(t, "ws-pol", "u-owner", "OWNER", nil)

	rec := httptest.NewRecorder()
	r.h.SetPolicy(rec, r.policyReq("PUT", "ws-pol", "u-owner", "OWNER", map[string]bool{"enabled": true}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !r.policyEnabled(t, "ws-pol") {
		t.Fatal("switch did not persist")
	}
	entries := r.j.ofType(journal.EntryCredentialRevealPolicy)
	if len(entries) != 1 {
		t.Fatalf("got %d reveal_policy_changed entries, want 1", len(entries))
	}
	if entries[0].Payload["enabled"] != true || entries[0].Payload["previous"] != false {
		t.Fatalf("payload = %v, want enabled=true previous=false", entries[0].Payload)
	}
}

// Same fail-closed rule as everywhere else in §2.6: if the chained record
// cannot be written, the switch does not move. An attacker who can wedge the
// journal must not be able to open the tenant's reveal surface unobserved.
func TestRevealPolicy_EnableFailsClosedWithoutAudit(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-polfail", false)
	r.seedMember(t, "ws-polfail", "u-owner", "OWNER", nil)
	r.j.failWith = errJournalTestWedged

	rec := httptest.NewRecorder()
	r.h.SetPolicy(rec, r.policyReq("PUT", "ws-polfail", "u-owner", "OWNER", map[string]bool{"enabled": true}))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rec.Code, rec.Body.String())
	}
	if r.policyEnabled(t, "ws-polfail") {
		t.Fatal("switch moved despite the audit write failing")
	}
}

// Only OWNER flips the switch. §2.6 L1 says "until an OWNER turns it on";
// the same section's Settings table lists ADMIN as an editor of that screen.
// Read the narrow one: this is the control that decides whether the tenant
// has a reveal surface at all, and an ADMIN who needs it can be made an OWNER
// deliberately. Widening later is a one-line change; narrowing after someone
// has relied on it is not.
func TestRevealPolicy_AdminAndBelowCannotFlipIt(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-polrole", false)

	for _, role := range []string{"ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		t.Run(role, func(t *testing.T) {
			userID := "u-pol-" + role
			r.seedMember(t, "ws-polrole", userID, role, []string{CapabilityChat, CapabilityCredentialReveal})

			rec := httptest.NewRecorder()
			r.h.SetPolicy(rec, r.policyReq("PUT", "ws-polrole", userID, role, map[string]bool{"enabled": true}))

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body.String())
			}
			if r.policyEnabled(t, "ws-polrole") {
				t.Fatalf("%s flipped the workspace reveal switch", role)
			}
		})
	}
}

// MANAGER reads the policy without being able to change it — they have to
// know the rules they work under (§2.6 "Kde se to konfiguruje"). MEMBER and
// VIEWER see nothing: the mere fact that a workspace has reveal enabled is
// information an attacker can use to pick a target.
func TestRevealPolicy_ReadVisibility(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-polread", true)

	cases := map[string]int{
		"OWNER":   http.StatusOK,
		"ADMIN":   http.StatusOK,
		"MANAGER": http.StatusOK,
		"MEMBER":  http.StatusForbidden,
		"VIEWER":  http.StatusForbidden,
	}
	for role, want := range cases {
		t.Run(role, func(t *testing.T) {
			userID := "u-polread-" + role
			r.seedMember(t, "ws-polread", userID, role, nil)

			rec := httptest.NewRecorder()
			r.h.GetPolicy(rec, r.policyReq("GET", "ws-polread", userID, role, nil))

			if rec.Code != want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), want)
			}
			if want == http.StatusOK {
				var out struct {
					Enabled bool `json:"enabled"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if !out.Enabled {
					t.Fatal("enabled = false, want true")
				}
			}
		})
	}
}

// Disabling is journaled too. "Off" is the safe state, so it is not gated any
// harder than "on" — but an attacker flipping it off to hide a spike, or an
// operator wondering why reveal stopped working, both need the record.
func TestRevealPolicy_DisableIsJournaled(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-poloff", true)
	r.seedMember(t, "ws-poloff", "u-owner", "OWNER", nil)

	rec := httptest.NewRecorder()
	r.h.SetPolicy(rec, r.policyReq("PUT", "ws-poloff", "u-owner", "OWNER", map[string]bool{"enabled": false}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if r.policyEnabled(t, "ws-poloff") {
		t.Fatal("switch did not persist")
	}
	entries := r.j.ofType(journal.EntryCredentialRevealPolicy)
	if len(entries) != 1 {
		t.Fatalf("got %d reveal_policy_changed entries, want 1", len(entries))
	}
	if entries[0].Payload["enabled"] != false || entries[0].Payload["previous"] != true {
		t.Fatalf("payload = %v, want enabled=false previous=true", entries[0].Payload)
	}
}
