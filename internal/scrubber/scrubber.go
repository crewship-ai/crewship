package scrubber

import (
	"fmt"
	"regexp"
	"sync"
)

type pattern struct {
	name string
	re   *regexp.Regexp
}

// Scrubber detects and redacts credential patterns from text.
// It is safe for concurrent use.
type Scrubber struct {
	mu       sync.RWMutex
	patterns []pattern
}

// New creates a Scrubber with built-in patterns for common credential types.
func New() *Scrubber {
	s := &Scrubber{}

	// Order matters: more specific patterns first, generic last.

	// SSH/RSA/EC private keys (multiline)
	s.patterns = append(s.patterns, pattern{
		name: "ssh_private_key",
		re:   regexp.MustCompile(`-----BEGIN OPENSSH PRIVATE KEY-----[\s\S]*?-----END OPENSSH PRIVATE KEY-----`),
	})
	s.patterns = append(s.patterns, pattern{
		name: "private_key",
		re:   regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |ED25519 )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |DSA |ED25519 )?PRIVATE KEY-----`),
	})

	// Anthropic API keys: sk-ant-* (also covers sk-ant-oat for OAuth tokens)
	s.patterns = append(s.patterns, pattern{
		name: "anthropic_key",
		re:   regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{10,}`),
	})

	// OpenAI API keys: sk-proj-*, sk-svcacct-*, sk-{48+ chars}
	s.patterns = append(s.patterns, pattern{
		name: "openai_key",
		re:   regexp.MustCompile(`sk-(?:proj|svcacct)-[a-zA-Z0-9_-]{10,}|sk-[a-zA-Z0-9]{20,}`),
	})

	// Google API keys: AIzaSy...
	s.patterns = append(s.patterns, pattern{
		name: "google_key",
		re:   regexp.MustCompile(`AIzaSy[a-zA-Z0-9_-]{33}`),
	})

	// Cursor API keys: cur_* (added with the multi-CLI wave). Pre-fix
	// scrubber missed these — Cursor key leaked in agent stdout would
	// flow unscrubbed into chat UI + journal entries.
	s.patterns = append(s.patterns, pattern{
		name: "cursor_key",
		re:   regexp.MustCompile(`cur_[a-zA-Z0-9_-]{20,}`),
	})

	// Factory Droid API keys: fact_* / factory_* (per Factory CLI docs).
	s.patterns = append(s.patterns, pattern{
		name: "factory_key",
		re:   regexp.MustCompile(`fact(?:ory)?_[a-zA-Z0-9_-]{20,}`),
	})

	// OpenRouter (used by OpenCode multi-provider routing): sk-or-*
	s.patterns = append(s.patterns, pattern{
		name: "openrouter_key",
		re:   regexp.MustCompile(`sk-or-[a-zA-Z0-9_-]{20,}`),
	})

	// xAI / Grok keys: xai-*
	s.patterns = append(s.patterns, pattern{
		name: "xai_key",
		re:   regexp.MustCompile(`xai-[a-zA-Z0-9]{20,}`),
	})

	// Groq keys: gsk_*
	s.patterns = append(s.patterns, pattern{
		name: "groq_key",
		re:   regexp.MustCompile(`gsk_[a-zA-Z0-9]{20,}`),
	})

	// GitHub tokens: ghp_, gho_, ghs_, ghr_, github_pat_
	s.patterns = append(s.patterns, pattern{
		name: "github_token",
		re:   regexp.MustCompile(`(?:ghp_|gho_|ghs_|ghr_|github_pat_)[a-zA-Z0-9]{10,}`),
	})

	// GitLab personal access tokens: glpat-*
	s.patterns = append(s.patterns, pattern{
		name: "gitlab_token",
		re:   regexp.MustCompile(`glpat-[a-zA-Z0-9_-]{20,}`),
	})

	// Slack tokens: xoxb-, xoxp-, xoxa-, xoxr-
	s.patterns = append(s.patterns, pattern{
		name: "slack_token",
		re:   regexp.MustCompile(`xox[bpar]-[a-zA-Z0-9-]+`),
	})

	// AWS access key IDs: AKIA*
	s.patterns = append(s.patterns, pattern{
		name: "aws_key",
		re:   regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	})

	// JWT Bearer tokens in Authorization headers
	s.patterns = append(s.patterns, pattern{
		name: "bearer_token",
		re:   regexp.MustCompile(`Bearer\s+eyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
	})

	// Generic password/secret patterns in JSON or env var format
	s.patterns = append(s.patterns, pattern{
		name: "",
		re:   regexp.MustCompile(`"(?:password|secret|token|api_key|apikey|secret_key)":\s*"[^"]+"`),
	})
	s.patterns = append(s.patterns, pattern{
		name: "",
		re:   regexp.MustCompile(`(?:PASSWORD|SECRET|SECRET_KEY|API_KEY|APIKEY)=[^\s]{6,}`),
	})

	return s
}

// AddPattern registers a custom credential pattern.
// Returns an error if the regex is invalid instead of panicking.
func (s *Scrubber) AddPattern(name, regex string) error {
	re, err := regexp.Compile(regex)
	if err != nil {
		return fmt.Errorf("invalid scrubber pattern %q: %w", name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patterns = append(s.patterns, pattern{
		name: name,
		re:   re,
	})
	return nil
}

// Scrub replaces all detected credential patterns with [REDACTED] markers.
//
// Input is normalised before pattern matching to defeat trivial obfuscation
// — zero-width characters (U+200B/C/D/FEFF, BOM) inside an otherwise-valid
// key form would otherwise let an attacker print "sk-ant-​abcd..." and slip
// past the regex char class. The normalised string is what's pattern-
// matched; the *output* is the normalised string with REDACTED markers
// substituted so we don't accidentally re-emit the embedded zero-width
// character downstream and re-create the bypass for the next sink.
//
// This is intentionally narrower than full Unicode normalisation —
// zero-width-only is enough to close the documented bypass without risk
// of mutating legitimate non-ASCII text in agent output.
func (s *Scrubber) Scrub(input string) string {
	if input == "" {
		return ""
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := stripZeroWidth(input)
	for _, p := range s.patterns {
		replacement := "[REDACTED]"
		if p.name != "" {
			replacement = "[REDACTED:" + p.name + "]"
		}

		if p.name == "" {
			// Generic patterns need special handling to preserve the key name
			result = s.scrubGeneric(result, p.re)
		} else {
			result = p.re.ReplaceAllString(result, replacement)
		}
	}
	return result
}

// zeroWidthRunes is the small, fixed set of invisible characters we strip
// before pattern matching. These are documented-as-zero-width in Unicode
// and have no semantic meaning in key/token contexts — anything else
// (BiDi marks, NBSP, etc.) is left alone to avoid unintended behaviour.
var zeroWidthRunes = map[rune]struct{}{
	'\u200B': {}, // ZERO WIDTH SPACE
	'\u200C': {}, // ZERO WIDTH NON-JOINER
	'\u200D': {}, // ZERO WIDTH JOINER
	'\u2060': {}, // WORD JOINER
	'\uFEFF': {}, // ZERO WIDTH NO-BREAK SPACE / BOM
}

func stripZeroWidth(s string) string {
	// Fast path: nothing to strip.
	hasZW := false
	for _, r := range s {
		if _, ok := zeroWidthRunes[r]; ok {
			hasZW = true
			break
		}
	}
	if !hasZW {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if _, skip := zeroWidthRunes[r]; skip {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

var (
	jsonKVRe = regexp.MustCompile(`^("(?:password|secret|token|api_key|apikey|secret_key)":\s*)"[^"]+"$`)
	envKVRe  = regexp.MustCompile(`^((?:PASSWORD|SECRET|SECRET_KEY|API_KEY|APIKEY)=)`)
)

// scrubGeneric handles patterns like "password": "value" -> "password": "[REDACTED]"
// and PASSWORD=value -> PASSWORD=[REDACTED].
//
// NOTE: The `re` parameter is used for match detection, but the replacement logic
// uses the package-level jsonKVRe/envKVRe regexes for submatch extraction. These
// must stay in sync with the generic patterns registered in New(). This coupling
// is intentional: the detection patterns are broad, while the replacement patterns
// capture the key name to preserve it in the redacted output.
func (s *Scrubber) scrubGeneric(input string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(input, func(match string) string {
		if m := jsonKVRe.FindStringSubmatch(match); len(m) > 1 {
			return m[1] + `"[REDACTED]"`
		}
		if m := envKVRe.FindStringSubmatch(match); len(m) > 1 {
			return m[1] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
}

// ContainsSecret returns true if the input contains any known credential pattern.
// Like Scrub, this matches against the zero-width-stripped form so the
// detector and the redactor agree on whether a string is sensitive.
func (s *Scrubber) ContainsSecret(input string) bool {
	if input == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	normalized := stripZeroWidth(input)
	for _, p := range s.patterns {
		if p.re.MatchString(normalized) {
			return true
		}
	}
	return false
}
