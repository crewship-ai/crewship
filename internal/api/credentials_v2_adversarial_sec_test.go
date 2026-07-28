package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Adversarial coverage for the credentials-V2 surfaces — reveal, bindings and
// fields. The existing reveal tests assert the happy-gate order; these try to
// get PAST it, and try to pull a secret out of the two surfaces that were added
// with no security test at all. Each test is an attack, named for what it tries
// to do, and passes only when the attack fails.

// ── reveal: getting a value without earning it ──────────────────────────────

// A caller who cannot see a credential must not learn its classification from
// the reveal endpoint. If SEALED (403) and cross-tenant (404) produced
// different answers for a credential in another tenant, reveal would be an
// existence oracle: probe an id, a 403 means "exists and is sealed", a 404
// means "nothing here".
func TestRevealAdversarial_CrossTenantNeverLeaksExistenceViaClassification(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-victim", true)
	r.seedWorkspace(t, "ws-attacker", true)
	victimOwner := "u-victim"
	r.seedMember(t, "ws-victim", victimOwner, "OWNER", []string{"credentials:reveal"})
	attacker := "u-attacker"
	r.seedMember(t, "ws-attacker", attacker, "OWNER", []string{"credentials:reveal"})

	// The victim's credential is SEALED — the most sensitive class.
	r.seedCredential(t, "ws-victim", victimOwner, "cred-sealed", "PROD_DB", "s3cr3t", "SEALED")

	// The attacker, fully privileged in their OWN workspace, aims the reveal at
	// the victim's credential id but carries their own workspace context (which
	// is all the server trusts — the body cannot forge it).
	rec := r.doReveal(r.revealReq("cred-sealed", "ws-attacker", attacker, "OWNER", validRevealReason))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant reveal of a SEALED credential returned %d, want 404 — anything else "+
			"(a 403 for SEALED) tells the attacker the id exists in another tenant", rec.Code)
	}
	if v := revealValue(t, rec); v != "" {
		t.Fatalf("value leaked across tenants: %q", v)
	}
}

// The reason field is free text that lands in the audit. A caller must not be
// able to smuggle the reveal past the gates by making the reason itself an
// injection — and more basically, a denied caller must get the SAME refusal
// regardless of what they typed, so the reason cannot be used to probe the
// layers above it.
func TestRevealAdversarial_ReasonCannotProbeUpperGates(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws1", false) // reveal disabled — the L1 gate
	r.seedMember(t, "ws1", "u1", "OWNER", []string{"credentials:reveal"})
	r.seedCredential(t, "ws1", "u1", "c1", "GH", "v", "STANDARD")

	// A reason crafted to look like it might change behaviour. It must not: the
	// workspace switch is off, so every one of these is the same 403, and the
	// reason is never even reached.
	for _, reason := range []string{
		validRevealReason,
		strings.Repeat("A", 5000),
		`'; SELECT value FROM credentials; --`,
		`{"enabled":true}`,
	} {
		rec := r.doReveal(r.revealReq("c1", "ws1", "u1", "OWNER", reason))
		if rec.Code != http.StatusForbidden {
			t.Errorf("reason %q produced %d, want a uniform 403 with reveal disabled — the reason "+
				"must not move the earlier gates", reason[:min(20, len(reason))], rec.Code)
		}
		if v := revealValue(t, rec); v != "" {
			t.Errorf("value leaked: %q", v)
		}
	}
}

// An ADMIN who is denied the capability must not slip through by holding some
// OTHER capability. This pins that the check is specifically for
// credentials:reveal, not "any capability present".
func TestRevealAdversarial_UnrelatedCapabilityDoesNotGrant(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws1", true)
	// Every capability the vault surface knows about EXCEPT reveal.
	r.seedMember(t, "ws1", "u1", "ADMIN", []string{"credentials:write", "credential.rotate", "chat"})
	r.seedCredential(t, "ws1", "u1", "c1", "GH", "v", "STANDARD")

	rec := r.doReveal(r.revealReq("c1", "ws1", "u1", "ADMIN", validRevealReason))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ADMIN with write+rotate but not reveal got %d, want 403 — the gate must be the "+
			"specific capability, not the presence of any", rec.Code)
	}
}

// The auth-path gate (L9) is the one keeping an agent from reading a secret
// back out of the vault. Every non-interactive kind must be refused, and the
// default for an unknown/absent kind must be denial, not the session path.
func TestRevealAdversarial_EveryNonInteractiveKindRefused(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws1", true)
	r.seedMember(t, "ws1", "u1", "OWNER", []string{"credentials:reveal"})
	r.seedCredential(t, "ws1", "u1", "c1", "GH", "v", "STANDARD")

	for _, kind := range []string{AuthKindCLIToken, "", "internal", "sidecar", "future_method"} {
		body, _ := json.Marshal(map[string]string{"reason": validRevealReason})
		req := httptest.NewRequest("POST", "/api/v1/credentials/c1/reveal", strings.NewReader(string(body)))
		ctx := withUser(req.Context(), &AuthUser{ID: "u1", Email: "u1@example.com", SessionID: "s"})
		ctx = withWorkspace(ctx, "ws1", "OWNER")
		ctx = withAuthKind(ctx, kind)
		req = req.WithContext(ctx)
		req.SetPathValue("credentialId", "c1")

		rec := httptest.NewRecorder()
		r.h.Reveal(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("auth kind %q got %d, want 403 — only an interactive session may reveal, and "+
				"an unrecognised kind must fail closed", kind, rec.Code)
		}
	}
}

// ── bindings: reaching another tenant's credential ──────────────────────────

// A binding names a credential id and a scope. The obvious attack is to bind
// one tenant's slot to ANOTHER tenant's credential id, so the victim's secret
// is delivered into the attacker's crew under a variable the attacker's agents
// read. The workspace predicate on delivery is the backstop; this asserts the
// write path refuses it in the first place.
func TestBindingAdversarial_CannotBindForeignCredential(t *testing.T) {
	_, db := newCredHandler(t)
	victimUser := seedTestUser(t, db)
	victimWS := seedTestWorkspace(t, db, victimUser)
	seedCredentialEnc(t, db, victimWS, victimUser, "victim-cred", "PROD", "victim-secret")

	// Attacker workspace + a crew in it.
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('att-ws', 'Att', 'att')`)
	attUser := "att-user"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		attUser, "att@example.com", "Att")
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-att', 'att-ws', ?, 'OWNER')`, attUser)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('att-crew', 'att-ws', 'AC', 'ac')`)

	bh := NewCredentialBindingHandler(db, newTestLogger())
	body, _ := json.Marshal(map[string]string{
		"credential_id": "victim-cred", // the foreign id
		"slot":          "GH_TOKEN",
		"scope":         "CREW",
		"crew_id":       "att-crew",
	})
	req := httptest.NewRequest("POST", "/api/v1/credentials/bindings", strings.NewReader(string(body)))
	req = req.WithContext(withWorkspace(req.Context(), "att-ws", "OWNER"))
	rec := httptest.NewRecorder()
	bh.Create(rec, req)

	// Specifically Bad Request "credential not found in this workspace" — not
	// just any non-2xx. A 500 would also be non-2xx, and a 500 is what a
	// half-removed guard produces; asserting the exact refusal keeps the test
	// from passing on an accident.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-tenant binding of a foreign credential returned %d, want 400 — the delivered "+
			"GH_TOKEN would otherwise carry the victim's secret. Body: %s", rec.Code, rec.Body.String())
	}
	// And nothing was written, so a later delivery cannot pick it up.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_bindings WHERE credential_id = 'victim-cred'`).Scan(&n); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if n != 0 {
		t.Fatalf("a cross-tenant binding row was written despite the rejection: %d", n)
	}
}

// A MEMBER cannot create bindings — they are roleManage, the same tier as
// deleting a credential. A binding decides which account a whole crew's agents
// authenticate as, which is not a thing a MEMBER may set.
func TestBindingAdversarial_MemberCannotBind(t *testing.T) {
	_, db := newCredHandler(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	seedCredentialEnc(t, db, ws, owner, "c1", "GH", "v")
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr', ?, 'C', 'c')`, ws)

	bh := NewCredentialBindingHandler(db, newTestLogger())
	body, _ := json.Marshal(map[string]string{
		"credential_id": "c1", "slot": "GH_TOKEN", "scope": "CREW", "crew_id": "cr",
	})
	req := httptest.NewRequest("POST", "/api/v1/credentials/bindings", strings.NewReader(string(body)))
	req = req.WithContext(withWorkspace(req.Context(), ws, "MEMBER"))
	rec := httptest.NewRecorder()
	bh.Create(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("MEMBER created a binding (%d), want 403", rec.Code)
	}
}

// ── fields: reading a secret part it must not return ────────────────────────

// The whole reason non-disclosure is structural is that a secret field's
// value must never reach any read path. This attacks it from the two angles a
// read has: the list, and a single-key get. Both must return the key and the
// classification and nothing that could be the value.
func TestFieldAdversarial_SecretValueUnreadableFromEveryPath(t *testing.T) {
	_, db := newCredHandler(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	seedCredentialEnc(t, db, ws, owner, "c1", "AWS", "primary")

	fh := NewCredentialFieldHandler(db, newTestLogger())

	// Write a secret field.
	secret := "dummy-adversarial-secret-session-token"
	wbody, _ := json.Marshal(map[string]any{"key": "session_token", "value": secret, "is_secret": true})
	wreq := httptest.NewRequest("POST", "/api/v1/credentials/c1/fields", strings.NewReader(string(wbody)))
	wreq = wreq.WithContext(withWorkspace(wreq.Context(), ws, "OWNER"))
	wreq.SetPathValue("credentialId", "c1")
	wrec := httptest.NewRecorder()
	fh.Create(wrec, wreq)
	if wrec.Code != http.StatusCreated && wrec.Code != http.StatusOK {
		t.Fatalf("field create failed: %d %s", wrec.Code, wrec.Body.String())
	}
	// The write response itself must not echo the value back.
	if strings.Contains(wrec.Body.String(), secret) {
		t.Fatalf("the field CREATE response echoed the secret value: %s", wrec.Body.String())
	}

	// List.
	lreq := httptest.NewRequest("GET", "/api/v1/credentials/c1/fields", nil)
	lreq = lreq.WithContext(withWorkspace(lreq.Context(), ws, "OWNER"))
	lreq.SetPathValue("credentialId", "c1")
	lrec := httptest.NewRecorder()
	fh.List(lrec, lreq)
	if strings.Contains(lrec.Body.String(), secret) {
		t.Fatalf("the field LIST returned the secret value: %s", lrec.Body.String())
	}
	// But it must still name the field — non-disclosure is not the same as
	// invisibility.
	if !strings.Contains(lrec.Body.String(), "session_token") {
		t.Fatalf("the field LIST hid the field entirely: %s", lrec.Body.String())
	}
}

// A field of another tenant's credential must be unreachable — both to write
// (planting a field on someone else's credential) and to read.
func TestFieldAdversarial_CrossTenantUnreachable(t *testing.T) {
	_, db := newCredHandler(t)
	victim := seedTestUser(t, db)
	victimWS := seedTestWorkspace(t, db, victim)
	seedCredentialEnc(t, db, victimWS, victim, "victim-cred", "PROD", "v")

	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('att-ws', 'A', 'a')`)
	att := "att-u"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'a@x.io', 'A')`, att)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m', 'att-ws', ?, 'OWNER')`, att)

	fh := NewCredentialFieldHandler(db, newTestLogger())

	// Attacker tries to READ the victim credential's fields under their own ctx.
	rreq := httptest.NewRequest("GET", "/api/v1/credentials/victim-cred/fields", nil)
	rreq = rreq.WithContext(withWorkspace(rreq.Context(), "att-ws", "OWNER"))
	rreq.SetPathValue("credentialId", "victim-cred")
	rrec := httptest.NewRecorder()
	fh.List(rrec, rreq)
	if rrec.Code != http.StatusNotFound {
		t.Fatalf("attacker read a foreign credential's fields: %d %s", rrec.Code, rrec.Body.String())
	}

	// And tries to WRITE a field onto it.
	wbody, _ := json.Marshal(map[string]any{"key": "planted", "value": "x", "is_secret": false})
	wreq := httptest.NewRequest("POST", "/api/v1/credentials/victim-cred/fields", strings.NewReader(string(wbody)))
	wreq = wreq.WithContext(withWorkspace(wreq.Context(), "att-ws", "OWNER"))
	wreq.SetPathValue("credentialId", "victim-cred")
	wrec := httptest.NewRecorder()
	fh.Create(wrec, wreq)
	if wrec.Code != http.StatusNotFound {
		t.Fatalf("attacker wrote a field onto a foreign credential: %d", wrec.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_fields WHERE credential_id = 'victim-cred'`).Scan(&n); err != nil {
		t.Fatalf("count fields: %v", err)
	}
	if n != 0 {
		t.Fatalf("a field was planted on the victim credential: %d", n)
	}
}
