package sidecar

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// A bring-your-own endpoint's credential material is not always the token.
// A self-hosted gateway that authenticates with `X-Api-Key: <secret>` instead
// of a bearer puts the whole secret in a HEADER value, and a header value
// matches no built-in shape pattern any more than the token does — that is the
// entire reason registerCredentialLiterals exists.
//
// Registering only c.Token left those unredacted: the sidecar holds the headers
// for exactly the same reason it holds the token, and the phase's own code says
// so ("a custom header on an authenticated endpoint is credential material
// too"), but the scrubber was never told.
func TestRegisterCredentialLiterals_RedactsHeaderValuesToo(t *testing.T) {
	const (
		token       = "byo-endpoint-token-9f2c4a1b6d8e"
		headerValue = "hdr-secret-4d7e2f9a1c3b5e8f"
		shortValue  = "no"
	)

	sc := scrubber.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerCredentialLiterals(sc, []Credential{{
		ID:    "cred-byo",
		Token: token,
		Headers: map[string]string{
			"X-Api-Key":   headerValue,
			"X-Tiny-Hint": shortValue,
		},
	}}, logger)

	got := sc.Scrub("token=" + token + " header=" + headerValue)
	if strings.Contains(got, token) {
		t.Errorf("token survived scrubbing: %q", got)
	}
	if strings.Contains(got, headerValue) {
		t.Errorf("header value survived scrubbing: %q — a header-authenticated endpoint's secret is credential material", got)
	}
}

// A custom-header credential does not have to carry a bearer token. The token
// length guard must therefore govern only the token pattern, not skip the
// independent header patterns that follow it.
func TestRegisterCredentialLiterals_RedactsHeaderOnlyCredential(t *testing.T) {
	const headerValue = "header-only-secret-4d7e2f9a1c3b5e8f"

	sc := scrubber.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerCredentialLiterals(sc, []Credential{{
		ID:      "cred-header-only",
		Headers: map[string]string{"X-Api-Key": headerValue},
	}}, logger)

	if got := sc.Scrub("header=" + headerValue); strings.Contains(got, headerValue) {
		t.Fatalf("header-only secret survived scrubbing: %q", got)
	}
}
