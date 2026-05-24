// Package redact centralises secret-aware string masking for CLI
// output. Every CLI command that prints a value derived from a
// credential, session token, webhook secret, or URL with embedded
// auth should route the value through this package before emitting
// it — both human-readable text and --json output.
//
// The package is deliberately tiny: three functions, no
// configuration, no allocations on the hot path beyond what string
// concatenation already costs. Adding a knob (e.g., "redaction
// level") would invite drift across call sites; the only honest
// answer is "always redact secrets the same way."
package redact

import (
	"net/url"
	"strings"
)

// Secret masks a short secret-bearing identifier — webhook tokens,
// API keys, single-line opaque tokens — preserving the last 4
// characters as a recognition hint. Returns "****" for empty or
// short (≤4 char) inputs so a leak of the redacted form still
// carries no useful prefix.
//
// This is the right primitive for "operator just created this
// secret and we want them to recognise the redacted form in --json
// output piped to a shared log file."
func Secret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "***" + s[len(s)-4:]
}

// Token masks an opaque bearer token (CLI auth tokens, session
// tokens, refresh tokens) by showing the first 4 + last 4
// characters with the middle redacted. Operators recognise the
// token they're holding without exposing enough material to
// reconstruct it.
//
// For tokens under 12 chars, falls back to Secret so that a short
// token doesn't leak more than a quarter of its bytes. A 12-char
// token gives 8 visible chars (66%) which is on the high side —
// callers passing fewer-than-recommended tokens already have a
// quality problem upstream; this just limits the blast radius.
func Token(s string) string {
	if len(s) < 12 {
		return Secret(s)
	}
	return s[:4] + "..." + s[len(s)-4:]
}

// URL strips credential-bearing parts of a URL: userinfo (basic
// auth in user:pass@host form) AND a configurable set of common
// secret-bearing query parameters (token, access_token, api_key,
// signature, sig, etc.) matched case-insensitively.
//
// Returns the input unchanged on parse failure rather than masking
// the whole string — a malformed URL is more likely a printf-
// format-string mismatch than an actual URL, and silently swapping
// it for "****" would hide that bug. Callers that want hard
// redaction on parse failure can wrap: `if u := URL(s); u == s {
// u = Secret(s) }`.
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		// `*` is a URL sub-delim and gets percent-encoded by
		// url.String(); use `x` (unreserved) so the mask stays
		// readable in logs. Different glyph than non-URL output, but
		// the intent — "this slot held a secret" — is preserved.
		u.User = url.UserPassword("xxx", "xxx")
	}
	if q := u.Query(); len(q) > 0 {
		changed := false
		for key, vals := range q {
			if !isRedactedQueryKey(key) || len(vals) == 0 {
				continue
			}
			masked := make([]string, len(vals))
			for i, v := range vals {
				masked[i] = secretURLSafe(v)
			}
			q[key] = masked
			changed = true
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

// secretURLSafe is the URL-safe sibling of Secret: same shape,
// but uses `x` chars so url.Encode doesn't percent-encode the mask
// (which would turn "***lue" into "%2A%2A%2Alue" in the output).
func secretURLSafe(s string) string {
	if len(s) <= 4 {
		return "xxxx"
	}
	return "xxx" + s[len(s)-4:]
}

// redactedQueryParams is the lowercased set of query parameter
// names that commonly carry credentials. Matched case-insensitively
// via isRedactedQueryKey so URL?Token=… is redacted the same as
// url?token=….
var redactedQueryParams = map[string]struct{}{
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"api_key":       {},
	"apikey":        {},
	"signature":     {},
	"sig":           {},
	"password":      {},
	"client_secret": {},
}

func isRedactedQueryKey(k string) bool {
	_, ok := redactedQueryParams[strings.ToLower(k)]
	return ok
}
