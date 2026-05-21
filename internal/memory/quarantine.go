package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ScanHit describes a single positive scan match. Category is one of
// {prompt_injection, exfiltration, persistence, invisible_unicode};
// Pattern is the rule identifier so operators looking at quarantine
// metadata can map the alert back to its rule.
type ScanHit struct {
	Category string
	Pattern  string
}

// rule is the internal scanner rule shape. Match runs the regex in
// case-insensitive mode unless the rule body explicitly anchors.
type rule struct {
	category string
	name     string
	re       *regexp.Regexp
}

// invisible unicode characters used to smuggle hidden text inside
// otherwise-innocent content: zero-width spaces, BIDI overrides,
// directional isolates, math-invisible operators, and deprecated
// formatting tags. Numeric literals are used instead of the glyphs
// themselves so the source file itself stays free of invisible
// codepoints — without this, gofmt or a stray BOM in the source
// breaks the build (and the file's own contents would trip
// every editor's "weird character" warning).
//
// The TAG block (U+E0001 + U+E0020-U+E007F) is matched as a range in
// scanInvisibleUnicode rather than enumerated here; the block is
// deprecated by Unicode 14.0 (the entire ASCII-mirror range has no
// legitimate textual use) and 95+ literal entries would bloat the slice.
var invisibleUnicodeRunes = []rune{
	0x200B, // ZWSP zero-width space
	0x200C, // ZWNJ zero-width non-joiner
	0x200D, // ZWJ zero-width joiner
	0x200E, // LRM left-to-right mark
	0x200F, // RLM right-to-left mark
	0x202A, // LRE left-to-right embedding
	0x202B, // RLE right-to-left embedding
	0x202D, // LRO left-to-right override
	0x202E, // RLO right-to-left override
	0x2066, // LRI left-to-right isolate
	0x2067, // RLI right-to-left isolate
	0x2068, // FSI first strong isolate
	0x2069, // PDI pop directional isolate
	0xFEFF, // BOM byte order mark

	// Math-invisible operators — Cf class, format-only, no glyph.
	// The audit (wave5/a5-1) found U+2060 inserted between letters
	// of `Ignore` bypassed the `\bignore\b` regex because Go's
	// regexp treats Cf as a non-word char *boundary*. Each of these
	// has zero legitimate use in memory content.
	0x2060, // WORD JOINER
	0x2061, // FUNCTION APPLICATION
	0x2062, // INVISIBLE TIMES
	0x2063, // INVISIBLE SEPARATOR
	0x2064, // INVISIBLE PLUS

	// Other zero-width / filler codepoints that defeat word boundaries.
	0x180E, // MONGOLIAN VOWEL SEPARATOR (deprecated by Unicode 6.3)
	0x115F, // HANGUL CHOSEONG FILLER (renders as empty)
	0x1160, // HANGUL JUNGSEONG FILLER (renders as empty)
	0x3164, // HANGUL FILLER (renders as empty)
}

// scannerRules is the curated rule set. Conservative by design:
// every entry must match real attack literature, not casual mentions.
// Keep additions narrow — false positives on benign technical content
// erode operator trust in the alert.
var scannerRules = []rule{
	{
		category: "prompt_injection",
		name:     "ignore_previous_instructions",
		re:       regexp.MustCompile(`(?i)\bignore\s+(?:previous|all|prior)\s+instructions\b`),
	},
	{
		category: "prompt_injection",
		name:     "you_are_now",
		re:       regexp.MustCompile(`(?i)\byou\s+are\s+now\s+(?:DAN|[A-Z][A-Za-z]+,\s*an?\s+(?:AI|assistant|model)\s+without)`),
	},
	{
		category: "prompt_injection",
		name:     "disregard_rules",
		re:       regexp.MustCompile(`(?i)\bdisregard\s+(?:rules|instructions|the\s+(?:above|system|previous))\b`),
	},
	{
		category: "prompt_injection",
		name:     "html_ignore_comment",
		re:       regexp.MustCompile(`(?is)<!--.*?\bignore\b.*?-->`),
	},
	{
		category: "exfiltration",
		name:     "curl_with_token",
		re:       regexp.MustCompile(`curl[^\n]*\$(?:TOKEN|API_KEY|SECRET|PASSWORD|CREDENTIAL|AUTH)`),
	},
	{
		category: "exfiltration",
		name:     "cat_env_pipe_network",
		re:       regexp.MustCompile(`(?i)\bcat\s+\.env\b[^\n]*(?:>|\|)[^\n]*(?:/dev/tcp|nc\b|curl|netcat|http)`),
	},
	{
		category: "exfiltration",
		name:     "aws_s3_exfil_ssh",
		re:       regexp.MustCompile(`(?i)\baws\s+s3\s+cp[^\n]*(?:\.ssh|id_rsa|id_ed25519|\.aws/credentials)\b`),
	},
	{
		category: "persistence",
		name:     "authorized_keys_append",
		re:       regexp.MustCompile(`>>\s*~?/?\.ssh/authorized_keys\b`),
	},
	{
		category: "persistence",
		name:     "crontab_register",
		// Detects "<anything> | crontab -" — the canonical "install a
		// cron job from stdin" gesture. Word boundary after the `-`
		// would never match because `-` ends the typical line, so the
		// rule uses a non-word lookalike: end-of-line or whitespace.
		re: regexp.MustCompile(`\|\s*crontab\s+-(?:\s|$)`),
	},
}

// ScanContent runs every rule against body and returns the first hit
// (or nil for clean content). First-hit semantics: the first signal
// is enough to quarantine; running the rest is wasted work.
func ScanContent(body string) *ScanHit {
	if body == "" {
		return nil
	}
	// Invisible-unicode check is cheap and high-signal — do it first.
	if hit := scanInvisibleUnicode(body); hit != nil {
		return hit
	}
	for _, r := range scannerRules {
		if r.re.MatchString(body) {
			return &ScanHit{Category: r.category, Pattern: r.name}
		}
	}
	return nil
}

func scanInvisibleUnicode(body string) *ScanHit {
	for _, ch := range body {
		// TAG block (U+E0001, U+E0020-U+E007F) is deprecated by
		// Unicode 14.0 — entire block has no legitimate textual use,
		// so a range check is both safer and cheaper than enumerating
		// 95+ entries. Any tag char inside content is evasion intent.
		if ch == 0xE0001 || (ch >= 0xE0020 && ch <= 0xE007F) {
			return &ScanHit{
				Category: "invisible_unicode",
				Pattern:  fmt.Sprintf("U+%04X", ch),
			}
		}
		for _, banned := range invisibleUnicodeRunes {
			if ch == banned {
				return &ScanHit{
					Category: "invisible_unicode",
					Pattern:  fmt.Sprintf("U+%04X", ch),
				}
			}
		}
	}
	return nil
}

// Quarantine writes the original content under {agentMemoryDir}/.
// quarantine/{sha256}.md and returns:
//
//   - placeholder: the safe stand-in to flow into the model in place
//     of the poisoned content. Mentions BLOCKED, the category, the
//     source path, and the sha so an operator can cross-reference.
//   - sha: the sha256 hex digest of the original content (used as the
//     quarantine filename and as the placeholder cross-ref).
//   - err: filesystem errors only; scan/match logic does not return
//     errors.
//
// Idempotent on content: the same body quarantined twice reuses the
// same filename and overwrites in place — the inbound scan runs on
// every read, so without idempotency a poisoned file accumulates
// duplicate quarantine copies.
func Quarantine(agentMemoryDir, sourcePath, original string, hit *ScanHit) (placeholder, sha string, err error) {
	if hit == nil {
		return "", "", fmt.Errorf("quarantine: hit is required")
	}
	digest := sha256.Sum256([]byte(original))
	sha = hex.EncodeToString(digest[:])

	qDir := filepath.Join(agentMemoryDir, ".quarantine")
	if err := os.MkdirAll(qDir, 0o755); err != nil {
		return "", "", fmt.Errorf("quarantine: mkdir %s: %w", qDir, err)
	}
	qPath := filepath.Join(qDir, sha+".md")

	// Wrap original with frontmatter recording category + source so
	// operator triage tooling can route the alert without re-running
	// the scan. Every interpolated field is run through %q so a
	// control character / newline / quote in sourcePath or scanner
	// metadata can't break out of its YAML value and corrupt the
	// frontmatter (and by extension the model-visible placeholder).
	header := fmt.Sprintf(`---
quarantined_at: %q
category: %q
pattern: %q
source: %q
sha256: %q
---

`,
		time.Now().UTC().Format(time.RFC3339),
		hit.Category, hit.Pattern, sourcePath, sha,
	)
	if err := os.WriteFile(qPath, []byte(header+original), 0o600); err != nil {
		return "", "", fmt.Errorf("quarantine: write %s: %w", qPath, err)
	}

	placeholder = fmt.Sprintf(
		"[BLOCKED: %q pattern %q detected in %q. "+
			"Original content quarantined to .quarantine/%s.md for operator review. "+
			"This placeholder is a safe substitute; the poisoned content was never returned to you.]",
		hit.Category, hit.Pattern, sourcePath, sha,
	)
	return placeholder, sha, nil
}

// tierSourceLabel maps an in-flight read's tier+key to the human-
// readable label used in quarantine + placeholder messages. Lets
// operators (and the agent reading the placeholder) tell which file
// the poison came from without resorting to absolute paths.
func tierSourceLabel(tier, key string) string {
	switch tier {
	case "daily":
		return "daily/" + key + ".md"
	case "peers":
		return "peers/" + key + ".md"
	case "AGENT":
		return "AGENT.md"
	case "CREW":
		return "CREW.md"
	case "PERSONA":
		return "PERSONA.md"
	case "pins":
		return "pins.md"
	case "lessons":
		return "lessons.md"
	default:
		return strings.TrimSpace(tier)
	}
}
