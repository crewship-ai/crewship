package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// image_validate.go — ValidateImageExists (parse-error branch) +
// isAuthError exhaustively.
//
// isAuthError is the "fall-open" gate that decides whether a registry
// HEAD failure should block a user PATCH to runtime_image, or be
// treated as "private registry, no creds — let it through and discover
// at provisioning time". A regression that mis-detects an auth error
// would either:
//   - falsely block a real private image (UX regression: user can't
//     set runtime_image they own)
//   - falsely admit a real DNS/transport failure (silent corruption:
//     user persists a nonexistent image, crew provisioning fails later
//     with a much worse error message).
//
// The helper does substring matching on uppercased error text plus a
// transport.Error-shaped interface check. Pin every branch.
// ---------------------------------------------------------------------------

// ---- isAuthError ----

func TestIsAuthError_NilIsFalse(t *testing.T) {
	// Defensive nil-guard. Avoids a "should never happen" panic if a
	// future caller forgets to check err != nil before invoking.
	if isAuthError(nil) {
		t.Errorf("isAuthError(nil) = true; nil must never count as auth")
	}
}

func TestIsAuthError_RecognizedStringSubstrings(t *testing.T) {
	// Each string the source switches on. Mix case to verify the
	// strings.ToUpper normalization works — without it, a registry that
	// returns "Unauthorized" (mixed case) would slip past.
	cases := []struct {
		name string
		msg  string
	}{
		{"uppercase-unauthorized", "UNAUTHORIZED: anonymous access"},
		{"mixed-case-unauthorized", "Unauthorized: token expired"},
		{"lowercase-unauthorized", "unauthorized"},
		{"status-401", "GET https://registry/v2/foo/bar/manifests/latest: 401"},
		{"denied", "DENIED: requested access to the resource is denied"},
		{"mixed-case-denied", "Denied"},
		{"forbidden", "FORBIDDEN: ip address not in allowlist"},
		{"mixed-case-forbidden", "Forbidden"},
		{"status-403", "GET https://registry: HTTP 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isAuthError(errors.New(tc.msg)) {
				t.Errorf("isAuthError(%q) = false; want true (every recognized substring should fall-open)", tc.msg)
			}
		})
	}
}

func TestIsAuthError_NonAuthErrorsAreFalse(t *testing.T) {
	// The non-auth cases must NOT be classified as auth — these are
	// real "image doesn't exist / unreachable" errors that we want to
	// surface to the user as a 400, not silently admit.
	cases := []struct {
		name string
		msg  string
	}{
		{"manifest-not-found", "MANIFEST_UNKNOWN: manifest unknown"},
		{"status-404", "GET https://registry: HTTP 404"},
		{"status-500", "internal server error: 500"},
		{"dns-failure", "no such host: registry.invalid"},
		{"connection-refused", "dial tcp: connect: connection refused"},
		{"timeout", "context deadline exceeded"},
		{"plain-text", "some random error string"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isAuthError(errors.New(tc.msg)) {
				t.Errorf("isAuthError(%q) = true; this MUST surface as a real validation error, not fall-open", tc.msg)
			}
		})
	}
}

// statusErr is a tiny implementation of the
// `interface{ StatusCode() int }` shape that isAuthError matches via
// errors.As. This lets us pin the transport.Error path without pulling
// the full go-containerregistry transport package.
type statusErr struct {
	code int
	msg  string
}

func (e *statusErr) Error() string  { return e.msg }
func (e *statusErr) StatusCode() int { return e.code }

func TestIsAuthError_TransportStatusCode_401_403_AreAuth(t *testing.T) {
	// Source: `errors.As(err, &te)` then `code == 401 || code == 403`.
	// Even if the .Error() text is opaque (e.g. an upstream that
	// doesn't include the status in the string), the interface-asserted
	// status code must still trigger the auth fall-open.
	for _, code := range []int{401, 403} {
		err := &statusErr{code: code, msg: "opaque transport error"}
		if !isAuthError(err) {
			t.Errorf("isAuthError on StatusCode()=%d returned false; transport.Error 401/403 must fall-open", code)
		}
	}
}

func TestIsAuthError_TransportStatusCode_Other_NotAuth(t *testing.T) {
	// All non-401/403 status codes must NOT trigger auth fall-open.
	// 404 (image missing) and 500 (registry down) are exactly the cases
	// where we want the user-facing error to fire so they fix the input.
	for _, code := range []int{200, 400, 404, 405, 429, 500, 502, 503} {
		err := &statusErr{code: code, msg: "opaque transport error"}
		if isAuthError(err) {
			t.Errorf("isAuthError on StatusCode()=%d returned true; only 401/403 should fall-open", code)
		}
	}
}

func TestIsAuthError_TransportStatusCode_Wrapped(t *testing.T) {
	// errors.As walks the unwrap chain. A transport error wrapped in
	// fmt.Errorf("validate: %w", ...) must still be detected — that's
	// exactly the shape ValidateImageExists itself produces internally.
	inner := &statusErr{code: 401, msg: "401"}
	wrapped := fmt.Errorf("validate image %q: %w", "private/img", inner)
	if !isAuthError(wrapped) {
		t.Errorf("wrapped transport error not detected; errors.As walk must reach inner StatusCode()")
	}
}

func TestIsAuthError_TransportStatusCode_TextMatchTakesPrecedence(t *testing.T) {
	// If BOTH the text match AND the StatusCode interface fire, the
	// text-match short-circuit returns first. Pin that we don't break
	// that ordering — the switch above the errors.As block is faster
	// for the common case (string already contains the status).
	err := &statusErr{code: 401, msg: "UNAUTHORIZED: matches text first"}
	if !isAuthError(err) {
		t.Errorf("auth error with both text match AND status code not detected")
	}
}

// ---- ValidateImageExists (parse-error branch only) ----

func TestValidateImageExists_RejectsInvalidReference(t *testing.T) {
	// Parse-error branch is testable WITHOUT a registry. Verifies the
	// "parse image ref" error prefix is in place — operators triaging
	// a 400 on PATCH runtime_image can grep for it.
	err := ValidateImageExists(context.Background(), "not a valid:image:ref!@#$")
	if err == nil {
		t.Fatal("expected parse error on malformed ref")
	}
	if !strings.Contains(err.Error(), "parse image ref") {
		t.Errorf("err = %v, want \"parse image ref\" prefix", err)
	}
}

func TestValidateImageExists_RejectsEmptyReference(t *testing.T) {
	// Empty ref is a common upstream slip (e.g. PATCH with body that
	// has runtime_image absent). The reference parser rejects it.
	err := ValidateImageExists(context.Background(), "")
	if err == nil {
		t.Fatal("expected parse error on empty ref")
	}
}
