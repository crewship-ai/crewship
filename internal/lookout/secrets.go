package lookout

import (
	"fmt"
	"regexp"
)

// secretRule pairs a compiled regex with the metadata used to redact and
// audit a hit. severity is fixed per rule: API keys and cloud creds are
// critical, generic password fields are high.
type secretRule struct {
	pattern  *regexp.Regexp
	kind     Kind
	severity Severity
	detail   string
	// captureGroup, when > 0, is the regex group index whose content
	// should be replaced (vs the entire match). Used for patterns where
	// the surrounding context (e.g. "password=") should be preserved in
	// the redacted output for grep-ability.
	captureGroup int
}

var secretRules []secretRule

func init() {
	patterns := []struct {
		expr         string
		kind         Kind
		severity     Severity
		detail       string
		captureGroup int
	}{
		// Specific provider patterns first so the generic api-key rule
		// doesn't claim them with a less-precise label.
		{`sk-ant-[A-Za-z0-9_\-]{40,}`, KindSecretAnthropic, SeverityCritical, "Anthropic API key", 0},
		{`sk-[A-Za-z0-9]{20,}`, KindSecretOpenAI, SeverityCritical, "OpenAI API key", 0},
		{`AKIA[A-Z0-9]{16}`, KindSecretAWS, SeverityCritical, "AWS access key ID", 0},
		{`ghp_[A-Za-z0-9]{36}`, KindSecretGitHubPAT, SeverityCritical, "GitHub personal access token", 0},
		{`gho_[A-Za-z0-9]{36}`, KindSecretGitHubOAuth, SeverityCritical, "GitHub OAuth token", 0},
		{`ghs_[A-Za-z0-9]{36}`, KindSecretGitHubApp, SeverityCritical, "GitHub server token", 0},
		{`(?i)bearer\s+[A-Za-z0-9_\-\.]{20,}`, KindSecretBearer, SeverityHigh, "Bearer token", 0},
		// Generic fallbacks last.
		{`(?i)api[_-]?key[=:\s]+["']?[A-Za-z0-9_\-]{16,}["']?`, KindSecretAPIKey, SeverityHigh, "Generic API key assignment", 0},
		{`(?i)password[=:\s]+["'][^"']{8,}["']`, KindSecretPassword, SeverityHigh, "Password assignment", 0},
	}
	secretRules = make([]secretRule, 0, len(patterns))
	for _, p := range patterns {
		secretRules = append(secretRules, secretRule{
			pattern:      regexp.MustCompile(p.expr),
			kind:         p.kind,
			severity:     p.severity,
			detail:       p.detail,
			captureGroup: p.captureGroup,
		})
	}
}

// Redact scans text for secrets and returns a copy where every match is
// replaced by ***REDACTED:{kind}*** plus the slice of findings describing
// what was redacted. The returned findings deliberately do NOT include the
// original matched value: anything downstream (logs, journal payloads)
// would otherwise re-expose the secret. Position is preserved for ordering
// the findings deterministically; callers that need to highlight the
// original location should redact-then-display, not rely on Position
// against the post-redaction string.
//
// Rules are applied in priority order (provider-specific first, generic
// last); once a span is redacted it is excluded from later rules' matches
// because the earlier rule's replacement no longer matches their patterns.
func Redact(text string) (string, []Finding) {
	if text == "" {
		return text, nil
	}
	findings := make([]Finding, 0)
	out := text
	for _, r := range secretRules {
		// Find positions in the current (possibly already-redacted) string.
		locs := r.pattern.FindAllStringIndex(out, -1)
		if len(locs) == 0 {
			continue
		}
		// Replace from the back so earlier offsets stay valid.
		for i := len(locs) - 1; i >= 0; i-- {
			loc := locs[i]
			findings = append(findings, Finding{
				Kind:     r.kind,
				Severity: r.severity,
				Detail:   r.detail,
				// Matched is intentionally only the kind, NEVER the secret
				// itself. See package contract: "secrets redaction must not
				// log the matched secret, even in error messages."
				Matched:  string(r.kind),
				Position: loc[0],
			})
			replacement := fmt.Sprintf("***REDACTED:%s***", r.kind)
			out = out[:loc[0]] + replacement + out[loc[1]:]
		}
	}
	return out, findings
}

// ScanForSecrets returns a ScanResult describing secrets found in text
// without performing the substitution. Used by OutputGuard when the policy
// is "block on secret" rather than "redact on secret". Verdict is Block
// when any secret is found because exposing even a single one is
// unacceptable; the middleware decides whether to redact-and-pass or
// hard-block based on configuration.
func ScanForSecrets(text string) ScanResult {
	_, findings := Redact(text)
	res := ScanResult{Findings: findings, Verdict: VerdictAllow}
	if len(findings) > 0 {
		res.Verdict = VerdictBlock
	}
	return res
}
