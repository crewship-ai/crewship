package scrubber

import (
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

	// Anthropic API keys: sk-ant-*
	s.patterns = append(s.patterns, pattern{
		name: "anthropic_key",
		re:   regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{10,}`),
	})

	// OpenAI API keys: sk-proj-* or sk-{48+ chars}
	s.patterns = append(s.patterns, pattern{
		name: "openai_key",
		re:   regexp.MustCompile(`sk-proj-[a-zA-Z0-9]{10,}|sk-[a-zA-Z0-9]{20,}`),
	})

	// Google API keys: AIzaSy...
	s.patterns = append(s.patterns, pattern{
		name: "google_key",
		re:   regexp.MustCompile(`AIzaSy[a-zA-Z0-9_-]{33}`),
	})

	// GitHub tokens: ghp_, gho_, ghs_, ghr_, github_pat_
	s.patterns = append(s.patterns, pattern{
		name: "github_token",
		re:   regexp.MustCompile(`(?:ghp_|gho_|ghs_|ghr_|github_pat_)[a-zA-Z0-9]{10,}`),
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
func (s *Scrubber) AddPattern(name, regex string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patterns = append(s.patterns, pattern{
		name: name,
		re:   regexp.MustCompile(regex),
	})
}

// Scrub replaces all detected credential patterns with [REDACTED] markers.
func (s *Scrubber) Scrub(input string) string {
	if input == "" {
		return ""
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := input
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

var (
	jsonKVRe = regexp.MustCompile(`^("(?:password|secret|token|api_key|apikey|secret_key)":\s*)"[^"]+"$`)
	envKVRe  = regexp.MustCompile(`^((?:PASSWORD|SECRET|SECRET_KEY|API_KEY|APIKEY)=)`)
)

// scrubGeneric handles patterns like "password": "value" -> "password": "[REDACTED]"
// and PASSWORD=value -> PASSWORD=[REDACTED]
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
func (s *Scrubber) ContainsSecret(input string) bool {
	if input == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, p := range s.patterns {
		if p.re.MatchString(input) {
			return true
		}
	}
	return false
}
