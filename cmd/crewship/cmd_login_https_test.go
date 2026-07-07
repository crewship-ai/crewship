package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// These tests exercise exchangeCredentialsForSession — the CSRF+credentials
// leg of interactive `crewship login`. They target that helper (not
// loginInteractive) because the interactive path reads the password straight
// off the TTY via term.ReadPassword, which is untestable.
//
// NOTE: the token (`--token`) and pairing (`--code`) login paths are pure
// bearer flows — they send an Authorization header and never touch NextAuth
// CSRF cookies — so they are unaffected by this fix. Do not "fix" the cookie
// name in those paths; there is no cookie there.

// csrfLoginServer mirrors internal/api/nextauth.go closely enough to catch the
// real bug: it names the CSRF cookie `cookieName` (which is
// __Host-authjs.csrf-token over HTTPS) and, on the credentials callback, looks
// the cookie up under that *exact* name — exactly what the production server
// does via csrfCookieName(r). A client that re-sends the cookie under a
// different name gets a 403, just like against dev3.
func csrfLoginServer(t *testing.T, cookieName, wantPassword, sessionCookieName string) http.Handler {
	t.Helper()
	const csrfSecret = "csrf-secret-abc123"
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/csrf", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: csrfSecret, Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"csrfToken": csrfSecret})
	})

	mux.HandleFunc("/api/auth/callback/credentials", func(w http.ResponseWriter, r *http.Request) {
		// Server looks the cookie up by its TLS-derived name. A mismatch is
		// indistinguishable from "no cookie sent" → Missing CSRF token.
		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" {
			replyJSON(w, http.StatusForbidden, map[string]string{"error": "Missing CSRF token"})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var in struct {
			Password  string `json:"password"`
			CSRFToken string `json:"csrfToken"`
		}
		_ = json.Unmarshal(body, &in)
		if in.CSRFToken != c.Value {
			replyJSON(w, http.StatusForbidden, map[string]string{"error": "Invalid CSRF token"})
			return
		}
		if in.Password != wantPassword {
			// Generic credentials error, HTTP 200 — matches respondCredentialsError.
			replyJSON(w, http.StatusOK, map[string]interface{}{
				"ok":    false,
				"error": "CredentialsSignin",
				"url":   "/api/auth/error?error=CredentialsSignin",
			})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "session-token-xyz", Path: "/"})
		replyJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "url": "/", "status": 200})
	})

	return mux
}

func replyJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// clientFor builds a cli.Client pointed at srv and, crucially, reuses srv's
// own http.Client so the httptest TLS server's self-signed cert is trusted.
func clientFor(srv *httptest.Server) *cli.Client {
	c := cli.NewClient(srv.URL, "", "")
	c.HTTPClient = srv.Client()
	return c
}

// The core regression: over HTTPS the server names the cookie
// __Host-authjs.csrf-token. The CLI must re-send it under that name, not a
// hardcoded "authjs.csrf-token". On the pre-fix code this fails with a CSRF
// 403 masked as "invalid credentials".
func TestExchangeCredentials_HTTPS_CookieNamePassthrough(t *testing.T) {
	srv := httptest.NewTLSServer(csrfLoginServer(t,
		"__Host-authjs.csrf-token", "correct-password", "__Secure-authjs.session-token"))
	defer srv.Close()

	tok, err := exchangeCredentialsForSession(clientFor(srv), srv.URL, "demo@crewship.ai", "correct-password")
	if err != nil {
		t.Fatalf("HTTPS login should succeed with the correct password, got error: %v", err)
	}
	if tok != "session-token-xyz" {
		t.Fatalf("expected session token from cookie, got %q", tok)
	}
}

// A CSRF/server rejection must NOT be reported as a bad password. A user with
// the correct password who hits the __Host- mismatch must be told what really
// failed. Pre-fix, this returned "invalid credentials".
func TestExchangeCredentials_CSRFRejectionNotMaskedAsBadPassword(t *testing.T) {
	// Server that always rejects CSRF (client cookie will never match the
	// name it looks up), regardless of password.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/csrf") {
			http.SetCookie(w, &http.Cookie{Name: "__Host-authjs.csrf-token", Value: "x", Path: "/"})
			replyJSON(w, http.StatusOK, map[string]string{"csrfToken": "x"})
			return
		}
		replyJSON(w, http.StatusForbidden, map[string]string{"error": "Invalid CSRF token"})
	}))
	defer srv.Close()

	_, err := exchangeCredentialsForSession(clientFor(srv), srv.URL, "demo@crewship.ai", "correct-password")
	if err == nil {
		t.Fatal("expected an error on CSRF rejection")
	}
	if errors.Is(err, errInvalidCredentials) {
		t.Fatalf("CSRF failure must not be reported as invalid credentials, got: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "csrf") {
		t.Fatalf("error should name the real cause (csrf); got: %v", err)
	}
}

// A genuinely wrong password must still surface as errInvalidCredentials — the
// fix must not turn every failure into a "server error".
func TestExchangeCredentials_WrongPassword(t *testing.T) {
	srv := httptest.NewTLSServer(csrfLoginServer(t,
		"__Host-authjs.csrf-token", "correct-password", "__Secure-authjs.session-token"))
	defer srv.Close()

	_, err := exchangeCredentialsForSession(clientFor(srv), srv.URL, "demo@crewship.ai", "wrong-password")
	if !errors.Is(err, errInvalidCredentials) {
		t.Fatalf("wrong password should be errInvalidCredentials, got: %v", err)
	}
}

// Plain HTTP (localhost / LAN IP) names the cookie authjs.csrf-token and must
// keep working — this is the path that was never broken.
func TestExchangeCredentials_PlainHTTP(t *testing.T) {
	srv := httptest.NewServer(csrfLoginServer(t,
		"authjs.csrf-token", "correct-password", "authjs.session-token"))
	defer srv.Close()

	tok, err := exchangeCredentialsForSession(clientFor(srv), srv.URL, "demo@crewship.ai", "correct-password")
	if err != nil {
		t.Fatalf("plain-HTTP login should succeed, got error: %v", err)
	}
	if tok != "session-token-xyz" {
		t.Fatalf("expected session token, got %q", tok)
	}
}
