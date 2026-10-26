package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Real signed/encrypted access and refresh cookies, rotation endpoint, ordinary
// authentication/workspace middleware and canonical policy. Runtime and clock
// are fixtures: this does not qualify the Rust actor or a browser connection.
func TestCollaborationHumanRenewedCredentialHTTPRotatedJWE(t *testing.T) {
	f := newContinuityRig(t, true)
	validator, err := auth.NewJWTValidator("renewal-http-fixture-signing-32characters")
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.NewDBStore(f.r.db)
	middleware := NewAuthMiddleware(validator, store, f.r.db, newTestLogger())
	validator.SetClock(func() time.Time { return time.Now().Add(-auth.AccessTokenTTL + time.Minute) })
	access, err := validator.IssueAccessToken(f.r.userID, "admission-source", "", "")
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := validator.IssueRefreshToken(f.r.userID, "admission-source")
	if err != nil {
		t.Fatal(err)
	}
	validator.SetClock(nil)
	var ticket string
	issue := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.ctx = context.WithoutCancel(r.Context())
		ticket, f.admission, err = f.s.issue(r.Context(), f.owner, true)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(204)
	})
	request := func(path, body, token string) *http.Request {
		r := httptest.NewRequest("POST", "https://studio.example"+path+"?workspace_id="+f.r.wsID, strings.NewReader(body))
		r.SetPathValue("slug", "@admission-page")
		r.Header.Set("Origin", "https://studio.example")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "__Secure-authjs.session-token", Value: token})
		}
		return r
	}
	w := httptest.NewRecorder()
	f.s.fence.Wrap(middleware.RequireAuth(middleware.RequireWorkspace(issue))).ServeHTTP(w, request("/admit", "", access))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	joined, err := f.s.join(f.ctx, f.s.runtime, f.owner, ticket, "signed-cookie-join")
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry, _ := f.admission.source.Expiry()
	nextAuth := NewNextAuthHandler(f.r.db, newTestLogger(), validator, store)
	rotation := request("/api/auth/token/refresh", "", access)
	rotation.AddCookie(&http.Cookie{Name: "__Secure-authjs.refresh-token", Value: refresh})
	w = httptest.NewRecorder()
	nextAuth.RefreshToken(w, rotation)
	if w.Code != 200 {
		t.Fatal("refresh endpoint", w.Code, w.Body.String())
	}
	access = ""
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "__Secure-authjs.session-token" {
			access = cookie.Value
		}
	}
	claims, err := validator.ValidateAccess(access)
	if err != nil || claims.Sid != "admission-source" || !time.Unix(claims.Exp, 0).After(originalExpiry) {
		t.Fatal("fresh JWT did not preserve session and extend expiry", err)
	}
	m := &collaborationHTTPManager{pages: f.s.pages, fence: f.s.fence, clock: f.clock, config: MooringHTTPConfig{PublicOrigin: "https://studio.example"}, docs: make(map[mooring.SessionKey]*collaborationHTTPDocument)}
	d := &collaborationHTTPDocument{manager: m, key: f.s.scope, ctx: t.Context(), owner: f.owner, humans: f.s, connections: make(map[string]*collaborationHTTPConnection)}
	c := &collaborationHTTPConnection{document: d, admission: f.admission, joined: joined, ref: "signed-connection", used: true, joinedDelivered: true, auditedOwner: f.r.userID, participantBinding: &mooring.SessionParticipantBinding{}}
	m.docs[f.s.scope] = d
	d.connections[c.ref] = c
	handler := f.s.fence.Wrap(middleware.RequireAuth(middleware.RequireWorkspace(http.HandlerFunc(m.reauthorize))))
	call := func() int {
		w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: f.s.fence}
		handler.ServeHTTP(w, request("/reauthorize", `{"connection_ref":"signed-connection"}`, access))
		return w.Code
	}
	if status := call(); status != 204 {
		t.Fatal("rotated JWE renewal", status)
	}
	f.advance(t, 90*time.Second)
	if f.clock.now <= f.admission.deadline {
		t.Fatal("did not cross original token monotonic cap")
	}
	if err = f.check(t, true); err != nil {
		t.Fatal("rotated JWT did not preserve original admission", err)
	}
	mustExec(t, f.r.db, `UPDATE user_sessions SET revoked_at='2026-10-26T00:00:00Z' WHERE id='admission-source'`)
	if status := call(); status != 401 {
		t.Fatal("middleware accepted revoked session", status)
	}
}
