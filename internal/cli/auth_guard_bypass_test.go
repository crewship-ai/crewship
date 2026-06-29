package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests lock in the fix for finding CLI1 (HIGH) from the 2026-06 security
// audit (.claude/context/SECURITY-AUDIT-2026-06.md): the issue #571 token-host
// guard used to live ONLY in Client.applyAuth (client.go), while several call
// sites built their own *http.Request against client.BaseURL and set the bearer
// by hand, bypassing the host check entirely:
//
//   - internal/cli/sse.go            (StreamSSE — exercised below)
//   - cmd/crewship/cmd_chat.go       (postMultipart — different package)
//   - cmd/crewship/cmd_crew_files.go (putBytes — different package)
//
// As a result, `crewship --server http://attacker.com …` (or a CREWSHIP_SERVER
// pointed at a mismatched host) leaked the operator's token to whatever host the
// SSE / chat / file-upload paths talked to, even when the guard would otherwise
// refuse it.
//
// The fix routes all of those sites through Client.NewRequest, which applies the
// bearer via applyAuth — so the #571 host guard runs for every request. The
// tests below assert that SECURE behaviour: a host-mismatched token is never
// written to the wire, while the matched-host happy path still authenticates.

// TestStreamSSE_HostMismatch_NoToken drives the real StreamSSE against an
// httptest server whose host (127.0.0.1) differs from the client's TokenHost.
// applyAuth refuses this (ServerMismatchError, no header), so the secret must
// never ride the request to the wrong host.
func TestStreamSSE_HostMismatch_NoToken(t *testing.T) {
	const secret = "super-secret-operator-token"

	var mu sync.Mutex
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: hello\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	srvHost := mustHost(t, srv.URL)

	c := NewClient(srv.URL, secret, "") // empty workspace → no resolve preflight
	// Token was minted for a DIFFERENT host than the one we're about to hit.
	c.TokenHost = "victim.example.com"
	if strings.EqualFold(c.TokenHost, srvHost) {
		t.Fatalf("test setup invalid: TokenHost %q must differ from server host %q", c.TokenHost, srvHost)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// errStop (defined in sse_test.go) makes onEvent return after the first event.
	err := c.StreamSSE(ctx, "/api/v1/stream", "", func(e SSEEvent) error {
		return errStop
	})

	// StreamSSE must refuse: NewRequest returns a *ServerMismatchError before
	// the connection is ever opened.
	var mismatch *ServerMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("StreamSSE to a mismatched host must return *ServerMismatchError, got %v", err)
	}

	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "" {
		t.Fatalf("token leaked to mismatched host: Authorization=%q (TokenHost=%q, server=%q)", auth, c.TokenHost, srvHost)
	}
}

// TestStreamSSE_HostMismatch_SecureTarget is the regression test that StreamSSE
// routes its auth through the #571 host check, so a host-mismatched token is
// never written to the wire.
func TestStreamSSE_HostMismatch_SecureTarget(t *testing.T) {
	const secret = "super-secret-operator-token"
	var mu sync.Mutex
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: hello\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, secret, "")
	c.TokenHost = "victim.example.com"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.StreamSSE(ctx, "/api/v1/stream", "", func(e SSEEvent) error { return errStop })

	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "" {
		t.Fatalf("token leaked to mismatched host: Authorization=%q", auth)
	}
}

// TestStreamSSE_HostMatch_StillSendsToken is a plain regression guard: when the
// request host DOES match TokenHost, StreamSSE must still authenticate (the fix
// for CLI1 must not break the happy path).
func TestStreamSSE_HostMatch_StillSendsToken(t *testing.T) {
	const secret = "matched-host-token"

	var mu sync.Mutex
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: hi\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, secret, "")
	c.TokenHost = mustHost(t, srv.URL) // matches → must be allowed

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.StreamSSE(ctx, "/api/v1/stream", "", func(e SSEEvent) error { return errStop })

	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "Bearer "+secret {
		t.Fatalf("matched-host request must carry the bearer token, got Authorization=%q", auth)
	}
}

// TestSourceGuard_NoHandRolledAuthOutsideClient scans the package source for
// hand-rolled `req.Header.Set("Authorization", ...)` calls living outside
// client.go (the one place the #571 guard is enforced). Every such site is a
// potential CLI1-style bypass; after the fix there must be none.
func TestSourceGuard_NoHandRolledAuthOutsideClient(t *testing.T) {
	if offenders := authHeaderSetSites(t); len(offenders) != 0 {
		t.Fatalf("hand-rolled Authorization writes outside client.go (bypass the #571 host guard): %v", offenders)
	}
}

// TestSourceGuard_NoHandRolledAuthOutsideClient_SecureTarget is the regression
// guard that all auth in internal/cli flows through applyAuth.
func TestSourceGuard_NoHandRolledAuthOutsideClient_SecureTarget(t *testing.T) {
	if offenders := authHeaderSetSites(t); len(offenders) != 0 {
		t.Fatalf("hand-rolled Authorization writes outside client.go (bypass the #571 host guard): %v", offenders)
	}
}

// authHeaderSetSites returns the .go files in the package directory (excluding
// _test.go files and client.go) that contain a literal
// `Header.Set("Authorization"` — i.e. set the bearer without going through
// Client.applyAuth.
func authHeaderSetSites(t *testing.T) []string {
	t.Helper()
	const needle = `Header.Set("Authorization"`

	entries, err := os.ReadDir(".") // go test runs with cwd == package dir
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var hits []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") || name == "client.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), needle) {
			hits = append(hits, name)
		}
	}
	return hits
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u.Hostname()
}
