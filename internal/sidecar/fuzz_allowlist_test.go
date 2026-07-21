package sidecar

import "testing"

// FuzzDomainAllowlistIsAllowed fuzzes the egress-allowlist host check. The
// sidecar proxy calls IsAllowed with whatever Host the CONNECT/HTTP request
// inside the agent container claims — attacker-adjacent input, since a
// compromised or malicious agent process controls that string directly.
// stripPort's IPv6-bracket handling is hand-rolled (net.SplitHostPort isn't
// used on the fast path), which is exactly the kind of string-surgery code
// fuzzing is good at breaking.
func FuzzDomainAllowlistIsAllowed(f *testing.F) {
	seeds := []string{
		"",
		"api.anthropic.com",
		"api.anthropic.com:443",
		"[::1]:443",
		"[::1]",
		"[",
		"]",
		"[]",
		":",
		"host:",
		":8080",
		"a:b:c:d:e",
		"API.ANTHROPIC.COM",
		"api.anthropic.com.",
		"[::ffff:127.0.0.1]:443",
		"evil.com\x00api.anthropic.com",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	al := NewDomainAllowlist(DefaultAllowedDomains)
	f.Fuzz(func(t *testing.T, host string) {
		_ = al.IsAllowed(host)
		_ = stripPort(host)
		_ = providerForHost(host)
	})
}
