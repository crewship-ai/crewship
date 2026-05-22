package memory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ScanHit describes a single positive scan match. Category is one of
// {prompt_injection, exfiltration, persistence, invisible_unicode,
// base64_obfuscation}; Pattern is the rule identifier so operators
// looking at quarantine metadata can map the alert back to its rule.
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
// and directional isolates. Numeric literals are used instead of the
// glyphs themselves so the source file itself stays free of invisible
// codepoints — without this, gofmt or a stray BOM in the source
// breaks the build (and the file's own contents would trip
// every editor's "weird character" warning).
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
	// --- PR-F4 Scanner v2: URL exfiltration ---
	// The existing curl_with_token rule covers `curl ... $TOKEN` but
	// misses the generic "browser-style exfil URL" shape that an
	// indirect-injection payload could ask the agent to GET via any
	// HTTP client (axios, fetch, wget, python requests, ...). Two
	// shapes worth catching:
	//
	//   1) Query-string exfil: https://attacker/path?x=$TOKEN
	//      where the value placeholder is one of the canonical
	//      secret-env names. `[^\s]` clamps the URL to whitespace
	//      boundaries — multi-line payloads still match per line.
	//
	//   2) Path exfil: https://attacker/$TOKEN/foo
	//      same secret names, embedded directly in the URL path
	//      after a `/`. The `?` query separator is excluded from
	//      the path component so the two rules don't overlap.
	{
		category: "exfiltration",
		name:     "url_exfil_query_token",
		// https://host[/path]?...=${TOKEN|...} OR ?...=$TOKEN
		re: regexp.MustCompile(`https?://[^\s?]+\?[^\s=]+=\$\{?(?i:TOKEN|API_KEY|SECRET|PASSWORD|CREDENTIAL|AUTH|ENV)\b`),
	},
	{
		category: "exfiltration",
		name:     "url_exfil_path_token",
		// https://host/$TOKEN or https://host/path/${API_KEY}
		// Anchored after a slash so a literal "$TOKEN" in prose
		// without a URL doesn't match.
		re: regexp.MustCompile(`https?://[^\s?]+/\$\{?(?i:TOKEN|API_KEY|SECRET|PASSWORD|CREDENTIAL|AUTH|ENV)\b`),
	},
}

// base64Block is a permissive base64 block detector — chunks of 60+
// continuous base64-alphabet chars terminated by optional padding.
// The 60-char floor keeps low-entropy ASCII (CSS hex colours, base64
// snippets inside markdown like image data URIs <60 chars) from
// matching. We only attempt decode for blocks above this floor.
var base64Block = regexp.MustCompile(`[A-Za-z0-9+/]{60,}={0,2}`)

// homoglyphFolds maps Cyrillic + Greek look-alike codepoints to their
// Latin equivalents. The intended attack vector is bypassing
// case-folded ASCII regexes by substituting a single visually-identical
// non-Latin letter — e.g. Cyrillic small "i" (U+0456) inside the word
// "ignore" so `(?i)\bignore\b` skips it. The set is intentionally
// narrow (small letters that overlap with ASCII identifiers commonly
// used in injection literature): a wider table risks folding legit
// non-Latin content. NFKD normalisation handles compatibility forms
// (full-width Latin, ligatures); the fold map handles the look-alikes
// NFKD leaves alone because they're separate scripts.
//
// Source: Unicode Confusables list (relevant subset for ASCII
// look-alikes). Only the lowercase forms are mapped because the
// existing rules use (?i) — case-insensitive — and we apply this fold
// after the rules' own case folding.
var homoglyphFolds = map[rune]rune{
	// Cyrillic
	0x0430: 'a', // а CYRILLIC SMALL LETTER A
	0x0435: 'e', // е CYRILLIC SMALL LETTER IE
	0x0438: 'u', // и CYRILLIC SMALL LETTER I (looks like n/u)
	0x0456: 'i', // і CYRILLIC SMALL LETTER BYELORUSSIAN-UKRAINIAN I
	0x043E: 'o', // о CYRILLIC SMALL LETTER O
	0x0440: 'p', // р CYRILLIC SMALL LETTER ER
	0x0441: 'c', // с CYRILLIC SMALL LETTER ES
	0x0445: 'x', // х CYRILLIC SMALL LETTER HA
	0x0443: 'y', // у CYRILLIC SMALL LETTER U
	0x0455: 's', // ѕ CYRILLIC SMALL LETTER DZE
	// Greek
	0x03B1: 'a', // α GREEK SMALL LETTER ALPHA
	0x03BF: 'o', // ο GREEK SMALL LETTER OMICRON
	0x03C1: 'p', // ρ GREEK SMALL LETTER RHO
	0x03C5: 'u', // υ GREEK SMALL LETTER UPSILON
	0x03BD: 'v', // ν GREEK SMALL LETTER NU (looks like v)
	0x03C7: 'x', // χ GREEK SMALL LETTER CHI
}

// ScanContent runs every rule against body and returns the first hit
// (or nil for clean content). First-hit semantics: the first signal
// is enough to quarantine; running the rest is wasted work.
//
// Scan order (cheapest, highest-signal first):
//  1. Invisible unicode — single pass, rune compare, instant reject.
//  2. Raw rules — the curated regex set against the original body.
//  3. Homoglyph-folded rules — NFKD + Cyrillic/Greek fold, re-run
//     the same rule set. Catches `іgnore` (Cyrillic i) and similar
//     look-alike bypasses. Reported as `<original_name>_homoglyph`
//     so operators can tell the two paths apart.
//  4. Base64 deobfuscation — extract long base64 blocks, decode,
//     re-run the original rule set against the decoded text and
//     flag with category=`base64_obfuscation` if any sub-rule
//     matches. Bounded by base64Block's 60-char floor to limit
//     decode cost (we expect O(1) blocks per memory file).
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
	// PR-F4: homoglyph-folded second pass. Catches attacks that
	// substitute a single Cyrillic/Greek look-alike inside a known
	// injection phrase (`іgnore previous instructions` etc.).
	if folded := foldHomoglyphs(body); folded != body {
		for _, r := range scannerRules {
			if r.re.MatchString(folded) {
				return &ScanHit{
					Category: r.category,
					Pattern:  r.name + "_homoglyph",
				}
			}
		}
	}
	// PR-F4: base64-obfuscated payload detection. The encoded form
	// is short and innocuous; the decoded form is the real payload.
	// We only decode blocks >= 60 chars to bound cost and avoid
	// false-positives on incidental base64-shaped substrings (UUIDs,
	// hashes, css minified chunks etc.).
	if hit := scanBase64Obfuscation(body); hit != nil {
		return hit
	}
	return nil
}

func scanInvisibleUnicode(body string) *ScanHit {
	for _, ch := range body {
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

// foldHomoglyphs normalises body via NFKD (handles full-width,
// ligatures, compatibility forms) and substitutes the Cyrillic + Greek
// look-alikes from homoglyphFolds. The result is intentionally NOT a
// faithful Unicode-aware lowercase; it's a fold designed specifically
// to expose injection bypasses.
//
// Returning body unchanged (same string identity) when no fold is
// needed keeps the common-case path zero-allocation — ScanContent
// uses `folded != body` to skip the second regex pass entirely on
// pure-ASCII input.
func foldHomoglyphs(body string) string {
	// Fast path: pure ASCII without any of the look-alike runes is
	// the common case. A single pass to detect "needs folding" is
	// cheaper than building the transformed string speculatively.
	needsFold := false
	for _, r := range body {
		if r > unicode.MaxASCII {
			needsFold = true
			break
		}
	}
	if !needsFold {
		return body
	}
	// NFKD normalisation collapses compatibility forms (e.g. full-
	// width Latin "ｉ" → "i"). Cyrillic/Greek look-alikes survive
	// NFKD intact — they're distinct scripts, not compatibility
	// forms — so we apply the explicit fold map afterwards.
	decomposed := norm.NFKD.String(body)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if mapped, ok := homoglyphFolds[r]; ok {
			b.WriteRune(mapped)
			continue
		}
		// Drop NFKD combining marks (Mn / Mc / Me) so accented
		// letters fold to their base form. Avoids missing
		// "ÍGNORE" → after NFKD → "I" + U+0301 → after this drop → "I".
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// scanBase64Obfuscation extracts base64 blocks, attempts to decode
// them, and re-runs the curated rule set against the decoded text.
// A positive match is reported as category=`base64_obfuscation` with
// the underlying rule name appended so triage knows what the encoded
// payload was hiding.
//
// Why this matters: the existing rules are anchored to literal
// strings ("curl", "ignore previous instructions") that an attacker
// trivially bypasses by base64-encoding the payload and asking the
// agent to "decode this and execute". The decoded inspection here
// closes that gap while staying conservative — we only flag when the
// DECODED text matches an existing rule, so we don't randomly
// quarantine every PEM key or JWT.
// base64ScanMaxBlocks caps how many base64-like runs the scanner
// will attempt to decode per body. Without it, a body containing
// thousands of long base64-shaped tokens (e.g. an embedded keystore
// dump, a fixture file pasted into a memory write, or a malicious
// agent intentionally trying to exhaust the scanner) forces O(N)
// decode work where N is unbounded. 256 blocks comfortably covers
// any realistic legitimate payload (a long PR description rarely
// contains more than a handful of base64 strings) and bounds the
// worst-case scanner CPU at ~256 × ~64KB decode = ~16MB peak —
// well below the scanner's existing body-size cap.
// CodeRabbit round-11 catch.
const base64ScanMaxBlocks = 256

func scanBase64Obfuscation(body string) *ScanHit {
	matches := base64Block.FindAllString(body, -1)
	if len(matches) > base64ScanMaxBlocks {
		matches = matches[:base64ScanMaxBlocks]
	}
	for _, match := range matches {
		// Strict StdEncoding; ignore decode errors (most random
		// long base64-shaped strings won't decode cleanly).
		decoded, err := base64.StdEncoding.DecodeString(match)
		if err != nil {
			continue
		}
		// Decoded payload must look like text (>50% printable ASCII)
		// — binary blobs are out of scope and likelier to cause
		// false positives via random regex coincidences.
		if !looksLikeText(decoded) {
			continue
		}
		decodedStr := string(decoded)
		for _, r := range scannerRules {
			if r.re.MatchString(decodedStr) {
				return &ScanHit{
					Category: "base64_obfuscation",
					Pattern:  r.name + "_base64",
				}
			}
		}
		// Also check for invisible-unicode in the decoded payload —
		// double obfuscation is a thing.
		if hit := scanInvisibleUnicode(decodedStr); hit != nil {
			return &ScanHit{
				Category: "base64_obfuscation",
				Pattern:  hit.Pattern + "_base64",
			}
		}
	}
	return nil
}

// looksLikeText returns true when the byte slice is overwhelmingly
// printable / whitespace ASCII. The threshold is intentionally lax
// (50%) so UTF-8 text with multi-byte glyphs (which have high-bit
// bytes that aren't printable in the ASCII sense) still qualifies as
// "decoded text worth re-scanning".
func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	printable := 0
	for _, c := range b {
		// printable ASCII: space..~ plus tab/newline/CR
		if (c >= 0x20 && c <= 0x7E) || c == '\t' || c == '\n' || c == '\r' {
			printable++
		}
	}
	return printable*2 >= len(b)
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
