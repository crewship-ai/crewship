package api

import (
	"net/http/httptest"
	"testing"
)

// TestCallerUserIDFromRequest_PrefersContext: when AuthMiddleware
// has put a user on the context, that wins over any incoming
// X-Caller-User-Id header. Prevents a malicious client from spoofing
// another user's id on a JWT-authed call by also setting the header.
func TestCallerUserIDFromRequest_PrefersContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-Caller-User-Id", "spoofed-user-id")
	r = r.WithContext(withUser(r.Context(), &AuthUser{ID: "real-user-id"}))

	if got := CallerUserIDFromRequest(r); got != "real-user-id" {
		t.Errorf("got %q, want context to win over header", got)
	}
}

// TestCallerUserIDFromRequest_HeaderFallback: when no context user
// is set (sidecar internal-token path), the header is the source of
// truth. Without it the function returns empty so the dual-path
// handler delegates to the autonomy gate.
func TestCallerUserIDFromRequest_HeaderFallback(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-Caller-User-Id", "ludmila-id")
	if got := CallerUserIDFromRequest(r); got != "ludmila-id" {
		t.Errorf("got %q, want ludmila-id", got)
	}
}

// TestCallerUserIDFromRequest_EmptyWhenAutonomous: no context user,
// no header. The handler reads empty and routes to the autonomy
// check path. Asserting this explicitly because a future helper
// regression could silently break the dual-path semantics.
func TestCallerUserIDFromRequest_EmptyWhenAutonomous(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	if got := CallerUserIDFromRequest(r); got != "" {
		t.Errorf("got %q, want empty for autonomous-agent call", got)
	}
}

// TestCallerUserIDFromRequest_EmptyContextUserFallsToHeader: a
// context with an *AuthUser whose ID is empty (e.g. partially
// initialized state) shouldn't override a valid header. The
// header is the lower-priority but explicit signal; defense against
// the AuthUser-but-no-id edge.
func TestCallerUserIDFromRequest_EmptyContextUserFallsToHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("X-Caller-User-Id", "header-id")
	r = r.WithContext(withUser(r.Context(), &AuthUser{ID: ""}))

	if got := CallerUserIDFromRequest(r); got != "header-id" {
		t.Errorf("got %q, want header to win when context user has empty ID", got)
	}
}

// TestCallerSourceFromRequest_PassthroughString: header value is
// returned verbatim. No validation against a closed enum — the
// field is intentionally open so a new surface can land without
// coordinated rollout.
func TestCallerSourceFromRequest_PassthroughString(t *testing.T) {
	cases := []string{
		CallerSourceChatUI,
		CallerSourceCLIRepl,
		"mobile-app", // future surface
		"",           // empty for autonomous / direct JWT
	}
	for _, want := range cases {
		t.Run(want, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/x", nil)
			if want != "" {
				r.Header.Set("X-Caller-Source", want)
			}
			if got := CallerSourceFromRequest(r); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}
