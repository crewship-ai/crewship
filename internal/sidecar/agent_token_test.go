package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #812: the shared per-crew sidecar must derive the ACTING agent from the
// per-agent bearer token — never from a caller-supplied `from`/slug that any
// sibling in the same container can spoof.

// escalateRoster is a two-member crew with per-agent tokens wired.
func escalateTokenServer(t *testing.T, backend string) *Server {
	t.Helper()
	return newQueryServer(t, &IPCConfig{
		BaseURL:     backend,
		Token:       "secret-token",
		AgentID:     "agent-nela",
		AgentSlug:   "nela", // boot agent
		AgentToken:  "tok-nela",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
		ChatID:      "chat-1",
	}, []CrewMember{
		{ID: "agent-nela", Slug: "nela", AuthToken: "tok-nela"},
		{ID: "agent-riley", Slug: "riley", AuthToken: "tok-riley"},
	})
}

func mockEscalationBackend(t *testing.T, captured *map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/internal/escalations" && r.Method == http.MethodPost {
			bodyBytes, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(bodyBytes, captured)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"escalation_id":"esc-1","status":"PENDING"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/internal/escalations/") && strings.HasSuffix(r.URL.Path, "/wait") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"RESOLVED","resolution":"ok","action":"approve"}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
}

// TestHandleEscalate_TokenOverridesSpoofedFrom — riley's token but a body
// claiming from=nela must be attributed to riley (the token identity),
// NOT nela. This is the intra-crew impersonation #812 closes: from is
// caller-supplied and a crew member, so #796's membership check alone would
// have let it through.
func TestHandleEscalate_TokenOverridesSpoofedFrom(t *testing.T) {
	var captured map[string]string
	backend := mockEscalationBackend(t, &captured)
	defer backend.Close()

	srv := escalateTokenServer(t, backend.URL)

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"steal attribution"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-riley")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := captured["from_slug"]; got != "riley" {
		t.Fatalf("from_slug = %q, want riley — riley's token must override a spoofed from=nela", got)
	}
}

// TestHandleEscalate_UnknownTokenRejected — a bearer token that matches no
// crew member is a forgery and must be refused, even if `from` names a real
// member.
func TestHandleEscalate_UnknownTokenRejected(t *testing.T) {
	var captured map[string]string
	backend := mockEscalationBackend(t, &captured)
	defer backend.Close()

	srv := escalateTokenServer(t, backend.URL)

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"riley","reason":"r"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-forged")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unrecognized agent token", w.Code)
	}
	if captured != nil {
		t.Fatalf("a forged token must never reach crewshipd; got body %v", captured)
	}
}

// TestHandleEscalate_NoTokenRejectedWhenProvisioned — once per-agent tokens are
// in force for the crew, a request with NO bearer token is a downgrade attempt
// (a sibling omitting the header to reach the spoofable membership check) and is
// refused. This closes the opt-out that would otherwise leave #812 opt-in.
func TestHandleEscalate_NoTokenRejectedWhenProvisioned(t *testing.T) {
	var captured map[string]string
	backend := mockEscalationBackend(t, &captured)
	defer backend.Close()

	srv := escalateTokenServer(t, backend.URL) // boots WITH per-agent tokens

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"riley","reason":"r"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (token-less request must be refused when tokens are provisioned); body: %s", w.Code, w.Body.String())
	}
	if captured != nil {
		t.Fatalf("a token-less spoof must never reach crewshipd; got body %v", captured)
	}
}

// TestHandleEscalate_LegacyNoTokensFallsBack — a genuinely token-less
// (un-upgraded) deployment keeps the pre-#812 membership-validated `from`
// behaviour, so upgrading the server binary doesn't break crews whose agents
// don't yet carry tokens.
func TestHandleEscalate_LegacyNoTokensFallsBack(t *testing.T) {
	var captured map[string]string
	backend := mockEscalationBackend(t, &captured)
	defer backend.Close()

	// No AgentToken, no CrewMember AuthTokens → tokensProvisioned() == false.
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: backend.URL, Token: "secret-token",
		AgentID: "agent-nela", AgentSlug: "nela",
		CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{
		{ID: "agent-nela", Slug: "nela"},
		{ID: "agent-riley", Slug: "riley"},
	})

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"riley","reason":"r"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (legacy fallback); body: %s", w.Code, w.Body.String())
	}
	if got := captured["from_slug"]; got != "riley" {
		t.Fatalf("from_slug = %q, want riley (legacy membership attribution)", got)
	}
}

// TestHandleQuery_TokenOverridesSpoofedFrom mirrors the escalate case for the
// peer-query path.
func TestHandleQuery_TokenOverridesSpoofedFrom(t *testing.T) {
	var captured map[string]interface{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"answer":"ok"}`))
	}))
	defer backend.Close()

	srv := escalateTokenServer(t, backend.URL)

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(`{"target":"nela","question":"q?","from":"nela"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-riley")
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got, _ := captured["from_slug"].(string); got != "riley" {
		t.Fatalf("from_slug = %q, want riley — token identity must override spoofed from", got)
	}
}

// TestHandleQuery_UnknownTokenRejected — a forged token is refused on the
// query path too.
func TestHandleQuery_UnknownTokenRejected(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("forged token reached crewshipd at %s", r.URL.Path)
	}))
	defer backend.Close()

	srv := escalateTokenServer(t, backend.URL)

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(`{"target":"nela","question":"q?","from":"riley"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-forged")
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unrecognized agent token", w.Code)
	}
}

// TestActingIdentity covers the token → identity resolution helper directly.
func TestActingIdentity(t *testing.T) {
	srv := escalateTokenServer(t, "http://unused")

	// No token → not present, falls through to legacy behaviour.
	r := httptest.NewRequest(http.MethodPost, "/escalate", nil)
	if _, _, present, _ := srv.actingIdentity(r); present {
		t.Error("no Authorization header must yield present=false")
	}

	// Boot agent token.
	r = httptest.NewRequest(http.MethodPost, "/escalate", nil)
	r.Header.Set("Authorization", "Bearer tok-nela")
	if id, slug, present, ok := srv.actingIdentity(r); !present || !ok || slug != "nela" || id != "agent-nela" {
		t.Errorf("boot token → (%q,%q,%v,%v), want (agent-nela,nela,true,true)", id, slug, present, ok)
	}

	// Sibling token.
	r = httptest.NewRequest(http.MethodPost, "/escalate", nil)
	r.Header.Set("Authorization", "Bearer tok-riley")
	if id, slug, present, ok := srv.actingIdentity(r); !present || !ok || slug != "riley" || id != "agent-riley" {
		t.Errorf("sibling token → (%q,%q,%v,%v), want (agent-riley,riley,true,true)", id, slug, present, ok)
	}

	// Forged token → present but not ok.
	r = httptest.NewRequest(http.MethodPost, "/escalate", nil)
	r.Header.Set("Authorization", "Bearer nope")
	if _, _, present, ok := srv.actingIdentity(r); !present || ok {
		t.Errorf("forged token must be present=true, ok=false; got present=%v ok=%v", present, ok)
	}
}
