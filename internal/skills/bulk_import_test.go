package skills

import (
	"strings"
	"testing"
)

// TestValidateGitURL_SSRF locks down the git-clone SSRF hole that an
// earlier revision missed. validateGitURL only checked scheme; a URL
// pointing at 169.254.169.254 (AWS / GCP IMDS) would clone without
// blocking. The fix mirrors ValidateImportURL's IP guard for any
// literal-IP host.
func TestValidateGitURL_SSRF(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
		errSub  string
	}{
		// Public HTTPS — accepted.
		{"github public", "https://github.com/owner/repo", false, ""},
		{"github with .git", "https://github.com/owner/repo.git", false, ""},
		{"gitlab public", "https://gitlab.com/group/project.git", false, ""},

		// Schemes other than https — rejected.
		{"file scheme", "file:///etc/passwd", true, "https"},
		{"ssh shorthand", "git@github.com:owner/repo.git", true, "https"},
		{"http (not https)", "http://github.com/owner/repo", true, "https"},

		// Localhost / loopback — rejected.
		{"localhost host", "https://localhost/repo.git", true, "localhost"},
		{"loopback v4", "https://127.0.0.1/repo.git", true, "private/internal"},
		{"loopback v6", "https://[::1]/repo.git", true, "private/internal"},

		// Private RFC1918 — rejected.
		{"rfc1918 10/8", "https://10.0.0.5/repo.git", true, "private/internal"},
		{"rfc1918 172.16/12", "https://172.16.0.1/repo.git", true, "private/internal"},
		{"rfc1918 192.168/16", "https://192.168.1.1/repo.git", true, "private/internal"},

		// Link-local / metadata — rejected.
		{"aws gcp imds", "https://169.254.169.254/repo.git", true, "private/internal"},
		{"link-local v6", "https://[fe80::1]/repo.git", true, "private/internal"},

		// Multicast / unspecified — rejected.
		{"multicast", "https://224.0.0.1/repo.git", true, "private/internal"},
		{"unspecified", "https://0.0.0.0/repo.git", true, "private/internal"},

		// Credential-bearing URLs — rejected. https://token@host/repo.git
		// is the standard PAT-clone shape and is convenient, but it's
		// also the easiest way to leak a secret into our error/log
		// pipeline. Block at validation; force operators to use a
		// credential helper.
		{"userinfo token", "https://glpat_xxx@gitlab.com/group/project.git", true, "must not embed credentials"},
		{"userinfo userpass", "https://user:pass@github.com/owner/repo.git", true, "must not embed credentials"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateGitURL(c.url)
			if c.wantErr && err == nil {
				t.Fatalf("validateGitURL(%q) = nil, want error containing %q", c.url, c.errSub)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("validateGitURL(%q) = %v, want nil", c.url, err)
			}
			if c.wantErr && c.errSub != "" && !strings.Contains(err.Error(), c.errSub) {
				t.Fatalf("validateGitURL(%q) = %v, want substring %q", c.url, err, c.errSub)
			}
		})
	}
}

// TestRedactGitURL_StripsUserinfo confirms our redaction helper
// does not echo embedded credentials back out — even though
// validateGitURL rejects them, callers from elsewhere in the package
// (logger.Error, source field on the dump) must round-trip cleanly
// for any URL shape we might one day accept.
func TestRedactGitURL_StripsUserinfo(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"https://token@github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"https://user:pass@gitlab.com/g/p.git", "https://gitlab.com/g/p.git"},
		{"not a url", "<redacted>"},
		{"", "<redacted>"},
	}
	for _, c := range cases {
		got := redactGitURL(c.in)
		if got != c.want {
			t.Errorf("redactGitURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "token") || strings.Contains(got, "pass@") {
			t.Errorf("redactGitURL(%q) leaked credential: %q", c.in, got)
		}
	}
}

// TestValidateGitURL_HostnameSurvives confirms we only block literal
// IP addresses; hostnames that happen to be private domains still
// pass at this layer (DNS resolution is git's job at clone time).
// We document the limitation rather than try to pre-resolve, because
// pre-resolution doesn't help against rebinding anyway.
func TestValidateGitURL_HostnameNotResolved(t *testing.T) {
	if err := validateGitURL("https://internal-git.example.com/repo.git"); err != nil {
		t.Fatalf("hostname URL was rejected: %v — only literal IPs should be blocked", err)
	}
}
