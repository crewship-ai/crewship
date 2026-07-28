package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// T-R20 (L0) — classification changes are asymmetric on purpose.
//
// RAISING a classification only ever removes reach: after it, strictly fewer
// people can reveal strictly less. Ceremony there buys nothing and costs
// something real — if tightening is annoying, people stop tightening.
//
// LOWERING hands out a key that did not exist a second earlier. It is the
// move an attacker who has taken an admin session makes first, because it is
// cheaper than defeating SEALED head-on. So lowering is journaled on the
// tamper-evident chain, with the same fail-closed rule as the reveal itself.

// sensitivityReq builds a PUT /credentials/{id}/sensitivity request.
func (r *revealRig) sensitivityReq(credID, wsID, userID, role, want string) *http.Request {
	body, _ := json.Marshal(map[string]string{"sensitivity": want})
	req := httptest.NewRequest("PUT", "/api/v1/credentials/"+credID+"/sensitivity", strings.NewReader(string(body)))
	ctx := withUser(req.Context(), &AuthUser{ID: userID, SessionID: "sess-" + userID})
	ctx = withWorkspace(ctx, wsID, role)
	ctx = withAuthKind(ctx, AuthKindSession)
	req = req.WithContext(ctx)
	req.SetPathValue("credentialId", credID)
	req.RemoteAddr = "203.0.113.7:52344"
	return req
}

func (r *revealRig) doSensitivity(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.h.SetSensitivity(rec, req)
	return rec
}

func (r *revealRig) sensitivityOf(t *testing.T, credID string) string {
	t.Helper()
	var s string
	if err := r.db.QueryRow(`SELECT sensitivity FROM credentials WHERE id = ?`, credID).Scan(&s); err != nil {
		t.Fatalf("read sensitivity: %v", err)
	}
	return s
}

func TestSensitivity_RaisingIsNotJournaled(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-raise", true)
	r.seedMember(t, "ws-raise", "u-mgr", "MANAGER", nil)
	r.seedCredential(t, "ws-raise", "u-mgr", "cred-1", "GH_TOKEN", "ghp_v", SensitivityStandard)

	for _, to := range []string{SensitivityRestricted, SensitivitySealed} {
		rec := r.doSensitivity(r.sensitivityReq("cred-1", "ws-raise", "u-mgr", "MANAGER", to))
		if rec.Code != http.StatusOK {
			t.Fatalf("raise to %s: status = %d body=%s, want 200", to, rec.Code, rec.Body.String())
		}
		if got := r.sensitivityOf(t, "cred-1"); got != to {
			t.Fatalf("sensitivity = %q, want %q", got, to)
		}
	}
	if n := len(r.j.ofType(journal.EntryCredentialSensitivityLowered)); n != 0 {
		t.Fatalf("raising wrote %d sensitivity_lowered entries, want 0", n)
	}
}

func TestSensitivity_LoweringIsJournaled(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-lower", true)
	r.seedMember(t, "ws-lower", "u-owner", "OWNER", nil)
	r.seedCredential(t, "ws-lower", "u-owner", "cred-1", "PROD_DB_DSN", "postgres://x", SensitivitySealed)

	rec := r.doSensitivity(r.sensitivityReq("cred-1", "ws-lower", "u-owner", "OWNER", SensitivityStandard))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := r.sensitivityOf(t, "cred-1"); got != SensitivityStandard {
		t.Fatalf("sensitivity = %q, want %q", got, SensitivityStandard)
	}

	entries := r.j.ofType(journal.EntryCredentialSensitivityLowered)
	if len(entries) != 1 {
		t.Fatalf("got %d sensitivity_lowered entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Payload["from"] != SensitivitySealed || e.Payload["to"] != SensitivityStandard {
		t.Fatalf("payload from/to = %v/%v, want SEALED/STANDARD", e.Payload["from"], e.Payload["to"])
	}
	if e.ActorID != "u-owner" {
		t.Errorf("actor_id = %q, want u-owner", e.ActorID)
	}
}

// Same fail-closed rule as the reveal: if the chained record cannot be
// written, the classification does not drop. Otherwise the cheapest way to
// unseal a credential without a trace would be to wedge the journal first.
func TestSensitivity_LoweringFailsClosedWithoutAudit(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-lowerfail", true)
	r.seedMember(t, "ws-lowerfail", "u-owner", "OWNER", nil)
	r.seedCredential(t, "ws-lowerfail", "u-owner", "cred-1", "PROD_DB_DSN", "postgres://x", SensitivitySealed)
	r.j.failWith = errJournalTestWedged

	rec := r.doSensitivity(r.sensitivityReq("cred-1", "ws-lowerfail", "u-owner", "OWNER", SensitivityStandard))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rec.Code, rec.Body.String())
	}
	if got := r.sensitivityOf(t, "cred-1"); got != SensitivitySealed {
		t.Fatalf("sensitivity = %q after a failed audit, want it unchanged at SEALED", got)
	}
}

// Lowering is an OWNER/ADMIN action; a MANAGER may tighten but not loosen.
// Splitting the two directions at different role floors is what makes
// "raise freely" safe to offer.
func TestSensitivity_ManagerCannotLower(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-lowerrole", true)
	r.seedMember(t, "ws-lowerrole", "u-mgr", "MANAGER", nil)
	r.seedCredential(t, "ws-lowerrole", "u-mgr", "cred-1", "PROD_DB_DSN", "postgres://x", SensitivitySealed)

	rec := r.doSensitivity(r.sensitivityReq("cred-1", "ws-lowerrole", "u-mgr", "MANAGER", SensitivityStandard))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if got := r.sensitivityOf(t, "cred-1"); got != SensitivitySealed {
		t.Fatalf("sensitivity = %q, want it unchanged at SEALED", got)
	}
}

// An unknown class is rejected before it reaches SQL. The column's CHECK
// would also reject it, but a 400 naming the allowed set is the difference
// between a fixable error and a 500.
func TestSensitivity_RejectsUnknownClass(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-badclass", true)
	r.seedMember(t, "ws-badclass", "u-owner", "OWNER", nil)
	r.seedCredential(t, "ws-badclass", "u-owner", "cred-1", "GH_TOKEN", "ghp_v", SensitivityStandard)

	for _, bad := range []string{"", "standard", "TOP_SECRET", "SEALED "} {
		t.Run("class="+bad, func(t *testing.T) {
			rec := r.doSensitivity(r.sensitivityReq("cred-1", "ws-badclass", "u-owner", "OWNER", bad))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
	if got := r.sensitivityOf(t, "cred-1"); got != SensitivityStandard {
		t.Fatalf("sensitivity = %q, want it unchanged", got)
	}
}

// Cross-tenant is 404 here for the same reason it is on reveal: a 403 would
// confirm the id exists.
func TestSensitivity_CrossTenantIsNotFound(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-s1", true)
	r.seedWorkspace(t, "ws-s2", true)
	r.seedMember(t, "ws-s1", "u-1", "OWNER", nil)
	r.seedMember(t, "ws-s2", "u-2", "OWNER", nil)
	r.seedCredential(t, "ws-s2", "u-2", "cred-2", "OTHER", "v", SensitivityStandard)

	rec := r.doSensitivity(r.sensitivityReq("cred-2", "ws-s1", "u-1", "OWNER", SensitivitySealed))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if got := r.sensitivityOf(t, "cred-2"); got != SensitivityStandard {
		t.Fatalf("cross-tenant write took effect: sensitivity = %q", got)
	}
}

// The Go classification vocabulary and the column's CHECK constraint have to
// stay identical: a value Go accepts but SQLite rejects is a 500, and a value
// SQLite accepts but Go does not recognise would fall through the SEALED
// comparison in the reveal gate — the one failure mode that silently unseals
// a credential.
func TestSensitivity_GoVocabularyMatchesTheColumnCheck(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-vocab", true)
	r.seedMember(t, "ws-vocab", "u-owner", "OWNER", nil)

	for i, class := range AllSensitivities() {
		credID := "cred-vocab-" + class
		r.seedCredential(t, "ws-vocab", "u-owner", credID, "K"+string(rune('A'+i)), "v", class)
		if got := r.sensitivityOf(t, credID); got != class {
			t.Fatalf("stored %q, read back %q", class, got)
		}
	}

	if _, err := r.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status,
			sensitivity, created_by, created_at, updated_at)
		VALUES ('cred-bogus', 'ws-vocab', 'BOGUS', 'x', 'SECRET', 'GITHUB', 'WORKSPACE', 'ACTIVE',
			'NOT_A_CLASS', 'u-owner', datetime('now'), datetime('now'))`); err == nil {
		t.Fatal("the column accepted an unknown classification; the CHECK constraint is missing")
	}
}
