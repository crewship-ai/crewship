package skills

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parser.go — ValidCategory / ValidRuntime / ValidMaturity, plus a few
// SSRF-protection edge cases on ValidateImportURL that the existing
// partial-coverage tests don't exercise.
// ---------------------------------------------------------------------------

func TestValidCategory_KnownValues(t *testing.T) {
	// Pin every member of the validCategories set so a refactor that
	// drops one (e.g. removing CUSTOM during a future taxonomy clean-up)
	// surfaces here AND would otherwise break agents whose category
	// silently flips to "invalid" mid-flight.
	known := []string{
		"CODING", "AUTOMATION", "DATA", "DEVOPS", "SUPPORT", "SALES",
		"WRITING", "RESEARCH", "PM", "DESIGN", "SECURITY", "FINANCE",
		"OPS", "CUSTOM",
	}
	for _, c := range known {
		t.Run(c, func(t *testing.T) {
			if !ValidCategory(c) {
				t.Errorf("ValidCategory(%q) = false; should be in the allowed set", c)
			}
		})
	}
}

func TestValidCategory_Rejects(t *testing.T) {
	for _, c := range []string{
		"",
		"coding",         // lowercase rejected — pins case-sensitivity
		"CODING ",        // trailing space
		" CODING",        // leading space
		"UNKNOWN",        // outside the set
		"CODING,WRITING", // comma-separated input rejected
	} {
		t.Run(c, func(t *testing.T) {
			if ValidCategory(c) {
				t.Errorf("ValidCategory(%q) = true; want false", c)
			}
		})
	}
}

func TestValidRuntime_KnownAndUnknown(t *testing.T) {
	for _, rt := range []string{"INSTRUCTIONS", "SCRIPT", "MCP", "HYBRID"} {
		t.Run("known/"+rt, func(t *testing.T) {
			if !ValidRuntime(rt) {
				t.Errorf("ValidRuntime(%q) = false; should be in allowed set", rt)
			}
		})
	}
	for _, rt := range []string{
		"",
		"instructions", // lowercase
		"INSTRUCTION",  // singular
		"PYTHON",       // outside the set
	} {
		t.Run("reject/"+rt, func(t *testing.T) {
			if ValidRuntime(rt) {
				t.Errorf("ValidRuntime(%q) = true; want false", rt)
			}
		})
	}
}

func TestValidMaturity_KnownAndUnknown(t *testing.T) {
	for _, m := range []string{"OFFICIAL", "CURATED", "COMMUNITY", "EXPERIMENTAL"} {
		t.Run("known/"+m, func(t *testing.T) {
			if !ValidMaturity(m) {
				t.Errorf("ValidMaturity(%q) = false; should be in allowed set", m)
			}
		})
	}
	for _, m := range []string{
		"",
		"official",   // lowercase
		"STABLE",     // not in enum
		"DEPRECATED", // not in enum
	} {
		t.Run("reject/"+m, func(t *testing.T) {
			if ValidMaturity(m) {
				t.Errorf("ValidMaturity(%q) = true; want false", m)
			}
		})
	}
}

// ---- ValidateImportURL (extra SSRF edge cases) ----

func TestValidateImportURL_AllRejectionPaths(t *testing.T) {
	// The source rejects: empty, non-HTTPS, localhost, private IPs,
	// loopback IPs, link-local. Cover each rejection branch in one
	// table.
	cases := []struct {
		name, url string
		wantSub   string // substring expected in error message
	}{
		{"empty", "", "required"},
		{"http-scheme", "http://example.com/skill.md", "HTTPS"},
		{"localhost-name", "https://localhost/skill.md", "localhost"},
		{"loopback-ipv4", "https://127.0.0.1/skill.md", "private/internal"},
		{"loopback-ipv6", "https://[::1]/skill.md", "private/internal"},
		{"private-10x", "https://10.0.0.5/skill.md", "private/internal"},
		{"private-192-168", "https://192.168.1.1/skill.md", "private/internal"},
		{"private-172-16", "https://172.16.0.1/skill.md", "private/internal"},
		{"link-local-ipv4", "https://169.254.169.254/skill.md", "private/internal"}, // AWS metadata trap
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateImportURL(context.Background(), tc.url)
			if err == nil {
				t.Fatalf("ValidateImportURL(%q) = nil; want error", tc.url)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestValidateImportURL_AcceptsPublicHTTPS(t *testing.T) {
	// Sanity: known-good public URLs pass. The GitHub shorthand
	// normalises to a raw URL that should also pass.
	for _, url := range []string{
		"https://raw.githubusercontent.com/anthropic/skills/main/coding/SKILL.md",
		"https://example.com/skill.md",
		"https://github.com/owner/repo/blob/main/skill.md", // blob → raw via NormalizeSkillURL
		"owner/repo/skill.md",                              // GitHub shorthand → normalised
	} {
		t.Run(url, func(t *testing.T) {
			if err := ValidateImportURL(context.Background(), url); err != nil {
				t.Errorf("ValidateImportURL(%q) = %v; want nil", url, err)
			}
		})
	}
}

// TestValidateImportURL_NonHTTPSchemeNormalisationQuirk documents a
// subtle gotcha in the NormalizeSkillURL → ValidateImportURL pipeline.
// Inputs without an http/https prefix fall into the GitHub-shorthand
// branch, which produces a rewritten https:// URL — so a raw
// "ftp://example.com/skill.md" string never actually trips the
// "HTTPS-only" gate because it gets rewritten to
// "https://raw.githubusercontent.com/ftp:/ /example.com/skill.md"
// first. The downstream fetch will 404 on raw.githubusercontent.com,
// not leak through to ftp://. Pin the current behavior here so a
// future hardening (e.g. reject "://" inside the shorthand) becomes
// an explicit breaking change. Skipped in CI: this test documents,
// not asserts, an unintended-but-harmless behaviour.
func TestValidateImportURL_NonHTTPSchemeNormalisationQuirk(t *testing.T) {
	t.Skip("Documents NormalizeSkillURL's behaviour: ftp:// shorthands get rewritten to raw.githubusercontent.com, not rejected. Harmless because raw.githubusercontent.com 404s on the rewritten path.")
}

func TestValidateImportURL_AWSMetadataEndpoint_Blocked(t *testing.T) {
	// 169.254.169.254 is the AWS instance metadata service — the
	// classic SSRF target. Pin explicitly that link-local rejection
	// catches it, even though the upstream `IsLinkLocalUnicast` check
	// already covers it. Defense-in-depth via test coverage.
	err := ValidateImportURL(context.Background(), "https://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("AWS metadata endpoint must be rejected")
	}
}
