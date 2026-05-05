package skills

import (
	"fmt"
	"regexp"
	"strings"
)

// LicenseGate determines whether a skill's SPDX license is acceptable
// for ingestion. The allowlist is intentionally narrow — every value on
// it is OSI-approved, permissive, and explicitly allows redistribution
// without copyleft obligations that would propagate to a workspace's
// generated artifacts. Strong-copyleft licenses (GPL-*, AGPL-*) are
// excluded not because they're bad licenses, but because a skill body
// gets injected verbatim into agent system prompts which are then mixed
// with user code — the legal status of "agent output that incorporated
// a GPL skill" is unsettled enough that defaulting to deny is safer.
//
// The freeform `license` field on a skill (which can be anything —
// "Apache-2.0 (anthropics/skills)", "Complete terms in LICENSE.txt",
// custom strings) is normalised through DetectSPDX before consultation.
var allowedSPDX = map[string]bool{
	"MIT":          true,
	"Apache-2.0":   true,
	"BSD-2-Clause": true,
	"BSD-3-Clause": true,
	"ISC":          true,
	"CC0-1.0":      true,
	"MPL-2.0":      true,
	"Unlicense":    true,
	"0BSD":         true,
}

// LicenseAllowed reports whether the SPDX id is on Crewship's import
// allowlist. Empty / unknown ids return false — the caller decides
// whether to surface --unsafe-license override to the user.
func LicenseAllowed(spdx string) bool {
	return allowedSPDX[spdx]
}

// AllowedSPDXLicenses returns a sorted snapshot of the allowlist for
// surfacing in error messages and CLI help text.
func AllowedSPDXLicenses() []string {
	out := make([]string, 0, len(allowedSPDX))
	for k := range allowedSPDX {
		out = append(out, k)
	}
	// sort.Strings would be ideal but pulling sort just for this is
	// noisy — the allowlist is small enough that a trivial ordered
	// insert below would beat std sort, but readability wins here:
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// DetectSPDX best-effort normalises a freeform license string into an
// SPDX identifier. Recognises the common shapes upstream skill
// publishers use: bare SPDX ids ("Apache-2.0"), embedded references
// ("Complete terms in LICENSE.txt", "Apache 2.0 (vendor/repo)"),
// case variants, and the few human aliases ("MIT License" -> "MIT").
//
// Returns empty string when no confident match is available — caller
// MUST treat that as "unknown / outside allowlist" and not silently
// pass through.
func DetectSPDX(raw string) string {
	if raw == "" {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)

	// Direct SPDX id match (case-insensitive — the registry stores the
	// canonical-cased form, so we map back).
	for spdx := range allowedSPDX {
		if strings.EqualFold(trimmed, spdx) {
			return spdx
		}
	}

	// Embedded references — "Apache-2.0 (...)", "MIT (vendor)" etc.
	for spdx := range allowedSPDX {
		if strings.HasPrefix(strings.ToUpper(trimmed), strings.ToUpper(spdx)) {
			return spdx
		}
	}

	// Human aliases. Kept tiny and exact — a fuzzier match would
	// silently approve "GPL-Compatible Apache License" which is not
	// the same legal animal as "Apache-2.0".
	switch {
	case strings.Contains(upper, "APACHE 2"), strings.Contains(upper, "APACHE-2"):
		return "Apache-2.0"
	case upper == "MIT LICENSE", upper == "THE MIT LICENSE":
		return "MIT"
	case upper == "BSD" || strings.HasPrefix(upper, "BSD 3"):
		return "BSD-3-Clause"
	case strings.HasPrefix(upper, "BSD 2"):
		return "BSD-2-Clause"
	case strings.Contains(upper, "MOZILLA PUBLIC LICENSE 2"):
		return "MPL-2.0"
	}
	return ""
}

// LicenseError describes a rejected skill so callers can surface the
// actual SPDX (or absence) and the allowlist alongside.
type LicenseError struct {
	Detected string // canonicalised SPDX, or "" when none could be inferred
	Raw      string // original freeform license string from frontmatter
}

func (e *LicenseError) Error() string {
	if e.Detected == "" {
		return fmt.Sprintf("license %q is not on the SPDX allowlist (no SPDX id detected)", e.Raw)
	}
	return fmt.Sprintf("license %q (SPDX %s) is not on the import allowlist", e.Raw, e.Detected)
}

// === Prompt-injection scanner ===

// injectionPatterns matches the most common prompt-injection markers
// observed in the wild as of May 2026. Sources: Snyk + Vercel
// agent-scan corpus (36% of 22.5k skills sampled flagged something),
// Anthropic threat-model docs, OWASP LLM top-10.
//
// The patterns are intentionally conservative — false positives flag a
// skill but do not block it (status FLAGGED, not BLOCKED). A human
// reviews the row before installing on an agent. Patterns that have
// historically produced too many false positives in benign skills (e.g.
// "system" without context, "previous" alone) are excluded.
var injectionPatterns = []*regexp.Regexp{
	// Direct attempts to override the system prompt. Case-insensitive,
	// allow whitespace between "ignore" and "previous"/"prior".
	regexp.MustCompile(`(?i)\bignore\s+(all\s+)?(previous|prior|earlier)\s+(instructions?|prompts?|messages?)\b`),
	regexp.MustCompile(`(?i)\bdisregard\s+(all\s+)?(previous|prior|earlier)\s+(instructions?|prompts?|messages?)\b`),
	regexp.MustCompile(`(?i)\bforget\s+(everything|your\s+(previous|prior)\s+instructions?)\b`),

	// Pretend-to-be-system / role hijack. Open and closed angle bracket
	// variants used by both XML-style and pseudo-tag injections.
	regexp.MustCompile(`(?i)<\s*system\s*>`),
	regexp.MustCompile(`(?i)<\s*\|im_start\|\s*>`),
	regexp.MustCompile(`(?i)\bnew\s+instructions?\s*:\s*\bignore\b`),

	// Common obfuscation: instruction asks the model to reveal its
	// system prompt, jailbreak guidelines, or training. These can be
	// legitimate in a meta-skill (skill-creator), so the scanner only
	// flags — never blocks.
	regexp.MustCompile(`(?i)\breveal\s+(your\s+)?system\s+prompt\b`),
	regexp.MustCompile(`(?i)\bDAN\b.{0,40}\bjailbreak\b`),
}

// detectLargeBase64Run scans for a contiguous >=1024-char run of
// base64-alphabet characters. RE2 caps regex repeat counts at 1000 so
// we hand-roll the scan rather than fight the engine — also slightly
// faster on long bodies (one O(n) pass, no NFA backtracking).
//
// Real skills rarely embed encoded payloads of this size, but smuggling
// instructions / binaries through a base64 blob is a documented
// injection technique.
const largeBase64Threshold = 1024

func detectLargeBase64Run(s string) (start, length int, found bool) {
	run := 0
	runStart := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isBase64Char(c) {
			if run == 0 {
				runStart = i
			}
			run++
			if run >= largeBase64Threshold {
				// Continue scanning to capture the full run length so the
				// reason can report an honest size, not the threshold.
				for j := i + 1; j < len(s) && isBase64Char(s[j]); j++ {
					run++
				}
				return runStart, run, true
			}
		} else {
			run = 0
		}
	}
	return 0, 0, false
}

func isBase64Char(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '+' || c == '/' || c == '='
}

// ScanResult is the outcome of [ScanContent] — the scan_status enum
// value plus a human-readable reason ready to record in the row's
// description_quality / scan_log column.
type ScanResult struct {
	Status string // "CLEAN" | "FLAGGED"
	Reason string // empty when CLEAN; one short sentence when FLAGGED
}

// ScanContent runs the built-in heuristic scanner against a SKILL.md
// body. Always-on by default for the import / generate paths. Returns
// CLEAN when no pattern matches; FLAGGED otherwise. The scanner does
// not BLOCK — that gate is reserved for the optional shell-out to
// snyk-agent-scan, which has the threat-model fidelity to justify
// rejecting a skill outright.
//
// Patterns are checked in order; the first match decides the reason
// string. We don't aggregate across all matches because the row only
// has one description_quality column, and a long concatenated reason
// is harder to act on than the first hit.
func ScanContent(content string) ScanResult {
	for _, re := range injectionPatterns {
		if loc := re.FindStringIndex(content); loc != nil {
			snippet := content[loc[0]:loc[1]]
			return ScanResult{
				Status: "FLAGGED",
				Reason: fmt.Sprintf("possible prompt-injection marker: %q", clipSnippet(snippet)),
			}
		}
	}
	if _, length, ok := detectLargeBase64Run(content); ok {
		return ScanResult{
			Status: "FLAGGED",
			Reason: fmt.Sprintf("large base64 blob (%d chars) — common injection vector", length),
		}
	}
	return ScanResult{Status: "CLEAN"}
}

func clipSnippet(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
