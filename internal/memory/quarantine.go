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
	"unicode"
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

// extraInvisibleRunes catches codepoints that defeat regex word
// boundaries but are NOT in Unicode's Cf (Format) category — these
// can't be caught by the unicode.Is(unicode.Cf, ch) class check in
// scanInvisibleUnicode below. All three are Hangul fillers (Lo —
// Letter, Other) that render as empty glyphs.
var extraInvisibleRunes = map[rune]bool{
	0x115F: true, // HANGUL CHOSEONG FILLER
	0x1160: true, // HANGUL JUNGSEONG FILLER
	0x3164: true, // HANGUL FILLER
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
		// Class-based detection: Unicode Cf (Format) covers every
		// invisible-format codepoint that exists today and any added
		// in future Unicode revisions -- zero-width spaces, BIDI
		// overrides, directional isolates, math-invisible operators,
		// the TAG block (U+E0001 + U+E0020-U+E007F), the deprecated
		// Mongolian vowel separator, ARABIC LETTER MARK, SOFT HYPHEN,
		// and the dozens of CodeRabbit/external review specifically
		// called out: U+00AD, U+061C, U+E0001-U+E007F. Trade-off:
		// this also catches legitimate Cf chars in benign Arabic /
		// multilingual content (U+061C as a directionality hint,
		// U+200E as an LRM in RTL passages). Memory content in this
		// system is primarily English code / docs, so the false-
		// positive risk is acceptable; the alternative is a curated
		// list that perpetually lags new Unicode additions.
		if unicode.Is(unicode.Cf, ch) {
			return &ScanHit{
				Category: "invisible_unicode",
				Pattern:  fmt.Sprintf("U+%04X", ch),
			}
		}
		// Hangul fillers render as empty glyphs but are Lo (Letter,
		// Other), not Cf -- they need an explicit allowlist.
		if extraInvisibleRunes[ch] {
			return &ScanHit{
				Category: "invisible_unicode",
				Pattern:  fmt.Sprintf("U+%04X", ch),
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
