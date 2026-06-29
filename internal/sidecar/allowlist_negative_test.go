package sidecar

import (
	"testing"
)

// These tests cover finding T1.12 from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): the sidecar domain allowlist
// (internal/sidecar/allowlist.go) is an exact-match, lowercased,
// deny-by-default matcher.
//
// Unlike the tripwire-style tests for unfixed findings, T1.12 is ALREADY
// secure: there is no suffix/substring/wildcard matching, so the obvious
// allowlist-bypass tricks (typo-squat, trailing dot, parent-domain prefix,
// userinfo "@", IPv6 literal) are all rejected. These are REGRESSION GUARDS —
// they pass today and must keep passing. If any starts to fail, the matcher
// has loosened (e.g. someone swapped exact-map lookup for HasSuffix) and
// reopened the allowlist bypass class.

// TestAllowlist_NegativeMatch_T1_12 asserts deny-by-default against a battery
// of host strings that an attacker would use to slip past a naive matcher,
// plus a positive control proving an allowlisted host is still permitted.
func TestAllowlist_NegativeMatch_T1_12(t *testing.T) {
	// Use the production default set so the guard tracks the real allowlist,
	// not a hand-picked subset.
	al := NewDomainAllowlist(DefaultAllowedDomains)

	tests := []struct {
		name    string
		host    string
		allowed bool
		why     string
	}{
		// Positive control: a genuine allowlisted host must be permitted,
		// otherwise the negative results below are meaningless.
		{"allowed_exact", "api.anthropic.com", true,
			"baseline: an allowlisted host must pass exact match"},
		{"allowed_with_port", "api.anthropic.com:443", true,
			"port is stripped before matching"},

		// Typo-squat / lookalike domain — not an exact entry.
		{"typosquat", "evil-github.com", false,
			"lookalike domain is not on the allowlist"},

		// Trailing dot: "api.anthropic.com." is the FQDN-rooted form. DNS
		// treats it as equivalent, but the exact-match map does not, so it
		// must be denied (a HasSuffix-style matcher would also reject the
		// reverse, but the danger is a matcher that normalizes the dot and
		// then suffix-matches).
		{"trailing_dot", "api.anthropic.com.", false,
			"FQDN trailing-dot variant is not an exact map key"},

		// Parent-domain prefix attack: attacker controls evil.com and points
		// a subdomain that embeds an allowlisted host as a label prefix.
		{"parent_suffix_attack", "api.anthropic.com.evil.com", false,
			"allowlisted host as a left-label of an attacker domain must be denied"},

		// Substring of an allowed host is NOT a registrable parent and must
		// be denied (anthropic.com is not on the list; api.anthropic.com is).
		{"substring_of_allowed", "anthropic.com", false,
			"a substring/parent of an allowed host is not itself allowed"},
		{"label_substring", "pi.anthropic.com", false,
			"a host that is a substring of an allowed one must be denied"},

		// Mixed / upper case must normalize to the same deny/allow result.
		// (Mixed-case of an allowed host is allowed; mixed-case of an evil
		// host stays denied.)
		{"mixedcase_evil", "Api.Anthropic.Com.Evil.Com", false,
			"case folding must not turn an evil host into an allowed one"},
		{"uppercase_allowed", "API.ANTHROPIC.COM", true,
			"case folding makes an allowed host match regardless of case"},

		// IPv6 loopback literal — not on the default allowlist, so denied.
		// (The bracketed and bracket+port forms exercise stripPort's IPv6 path.)
		{"ipv6_literal_bare", "::1", false,
			"loopback IPv6 literal is not on the default allowlist"},
		{"ipv6_literal_bracketed", "[::1]", false,
			"bracketed IPv6 literal is not on the default allowlist"},
		{"ipv6_literal_bracket_port", "[::1]:443", false,
			"bracketed IPv6 literal with port is not on the default allowlist"},

		// Userinfo "@" trick: a URL like https://user@api.anthropic.com routes
		// to api.anthropic.com, but if a caller mistakenly passes the raw
		// authority (with userinfo) as the host, the matcher must NOT treat
		// "evil.com@api.anthropic.com" (whose real host is evil.com under URL
		// rules) or "user@api.anthropic.com" as the allowed host.
		{"userinfo_allowed_as_user", "evil.com@api.anthropic.com", false,
			"authority with userinfo must not match the bare allowed host"},
		{"userinfo_prefix", "user@api.anthropic.com", false,
			"userinfo-prefixed authority is not an exact allowlist key"},
		{"userinfo_evil_host", "api.anthropic.com@evil.com", false,
			"allowlisted string as userinfo of an attacker host must be denied"},

		// Empty / whitespace.
		{"empty", "", false, "empty host is denied by default"},
		{"whitespace", "  api.anthropic.com  ", false,
			"untrimmed whitespace is not an exact key (no implicit trimming)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := al.IsAllowed(tt.host)
			if got != tt.allowed {
				t.Errorf("IsAllowed(%q) = %v, want %v — %s", tt.host, got, tt.allowed, tt.why)
			}
		})
	}
}

// TestAllowlist_DenyByDefault_T1_12 documents the core invariant: a freshly
// constructed allowlist with no entries denies everything, and only the exact
// strings added are permitted. This is the property the negative-match suite
// above relies on.
func TestAllowlist_DenyByDefault_T1_12(t *testing.T) {
	empty := NewDomainAllowlist(nil)
	for _, h := range []string{"api.anthropic.com", "anything.com", "", "localhost"} {
		if empty.IsAllowed(h) {
			t.Errorf("empty allowlist must deny %q (deny-by-default invariant)", h)
		}
	}

	// Adding one host permits ONLY that host, not its parents or children.
	one := NewDomainAllowlist([]string{"api.anthropic.com"})
	if !one.IsAllowed("api.anthropic.com") {
		t.Fatal("explicitly added host must be allowed")
	}
	for _, h := range []string{
		"anthropic.com",              // parent
		"sub.api.anthropic.com",      // child
		"api.anthropic.com.evil.com", // suffix attack
		"xapi.anthropic.com",         // left-extension
	} {
		if one.IsAllowed(h) {
			t.Errorf("single-entry allowlist must deny related host %q (exact match only)", h)
		}
	}
}
