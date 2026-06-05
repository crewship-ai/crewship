package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecErrMask_* assert that the 500-path handlers in
// user_peer_privacy.go and agent_persona.go do NOT leak raw SQLite
// error text or host filesystem paths into the HTTP response body
// (LOW-severity information disclosure). Each test forces a real 500
// by corrupting the DB (drop a table so the query errors) and checks
// the body is generic while the status stays 500.

// leakyBody fails the test if the response body contains raw SQL
// error fragments or filesystem path leakage. Centralized so every
// site asserts the same denylist.
func assertNoErrLeak(t *testing.T, body string) {
	t.Helper()
	forbidden := []string{"/output", "sql:", "no such table", "SQL", "permission denied", "/crews/", ".memory"}
	for _, frag := range forbidden {
		if strings.Contains(body, frag) {
			t.Errorf("response body leaks internal detail %q: %s", frag, body)
		}
	}
}

func TestSecErrMask_PeerPrivacy_GetConsent_500(t *testing.T) {
	r := peerTestSetup(t)
	// Drop the table the query reads so QueryRowContext errors with a
	// non-ErrNoRows DB error (e.g. "no such table: user_peer_consent").
	if _, err := r.db.Exec(`DROP TABLE user_peer_consent`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	rec := httptest.NewRecorder()
	r.privacy.GetConsent(rec, r.req(t, http.MethodGet, "", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertNoErrLeak(t, rec.Body.String())
}

func TestSecErrMask_PeerPrivacy_PutConsent_500(t *testing.T) {
	r := peerTestSetup(t)
	if _, err := r.db.Exec(`DROP TABLE user_peer_consent`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	rec := httptest.NewRecorder()
	r.privacy.PutConsent(rec, r.req(t, http.MethodPut, `{"opted_out":true}`, nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertNoErrLeak(t, rec.Body.String())
}

func TestSecErrMask_PeerPrivacy_GetMyCards_500(t *testing.T) {
	r := peerTestSetup(t)
	if _, err := r.db.Exec(`DROP TABLE peer_cards`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	rec := httptest.NewRecorder()
	r.privacy.GetMyCards(rec, r.req(t, http.MethodGet, "", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertNoErrLeak(t, rec.Body.String())
}

func TestSecErrMask_Persona_AgentLookup_500(t *testing.T) {
	r := newPersonaTestRig(t)
	// Drop agents so resolveAgentPaths' QueryRowContext errors with a
	// non-ErrNoRows DB error → replyAgentLookup hits the 500 branch.
	if _, err := r.h.db.Exec(`DROP TABLE agents`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertNoErrLeak(t, rec.Body.String())
}

func TestSecErrMask_Persona_CrewResolve_500(t *testing.T) {
	r := newPersonaTestRig(t)
	// Drop crews so resolveCrewPaths errors with a non-ErrNoRows DB
	// error → GetCrewPersona hits the raw-err 500 branch.
	if _, err := r.h.db.Exec(`DROP TABLE crews`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.GetCrewPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertNoErrLeak(t, rec.Body.String())
}
