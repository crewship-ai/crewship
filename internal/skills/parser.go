package skills

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// validCategories defines the allowed skill categories.
var validCategories = map[string]bool{
	"CODING":        true,
	"RESEARCH":      true,
	"DEVELOPMENT":   true,
	"DEVOPS":        true,
	"COMMUNICATION": true,
	"CUSTOM":        true,
}

// ValidCategory returns true if the category is in the allowed set.
func ValidCategory(cat string) bool {
	return validCategories[cat]
}

// SkillMeta holds parsed YAML frontmatter from a SKILL.md file.
type SkillMeta struct {
	Name                   string   `yaml:"name"`
	DisplayName            string   `yaml:"display_name"`
	Version                string   `yaml:"version"`
	Author                 string   `yaml:"author"`
	Description            string   `yaml:"description"`
	Category               string   `yaml:"category"`
	Icon                   string   `yaml:"icon"`
	CredentialRequirements []string `yaml:"credential_requirements"`
	Tags                   []string `yaml:"tags"`
}

// ParsedSkill contains the parsed metadata and markdown body of a SKILL.md file.
type ParsedSkill struct {
	Meta    SkillMeta
	Content string
}

// ParseSKILLMD parses a SKILL.md file string and returns a ParsedSkill.
// The file must have YAML frontmatter delimited by --- lines.
func ParseSKILLMD(input string) (*ParsedSkill, error) {
	lines := strings.Split(input, "\n")

	// Find opening ---
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, "\r") == "---" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("no YAML frontmatter: file must start with ---")
	}

	// Find closing ---
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("no YAML frontmatter: closing --- not found")
	}

	yamlStr := strings.Join(lines[start+1:end], "\n")

	// Trim leading blank lines from content
	contentLines := lines[end+1:]
	for len(contentLines) > 0 && strings.TrimRight(contentLines[0], "\r ") == "" {
		contentLines = contentLines[1:]
	}
	content := strings.Join(contentLines, "\n")

	var meta SkillMeta
	if err := yaml.Unmarshal([]byte(yamlStr), &meta); err != nil {
		return nil, fmt.Errorf("parse YAML frontmatter: %w", err)
	}

	if meta.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	// Normalize and validate category (if provided)
	if meta.Category != "" {
		meta.Category = strings.ToUpper(meta.Category)
		if !ValidCategory(meta.Category) {
			return nil, fmt.Errorf("invalid category %q: must be one of CODING, RESEARCH, DEVELOPMENT, DEVOPS, COMMUNICATION, CUSTOM", meta.Category)
		}
	}

	// Auto-slugify name
	meta.Name = Slugify(meta.Name)

	return &ParsedSkill{
		Meta:    meta,
		Content: content,
	}, nil
}

var multiHyphenRe = regexp.MustCompile(`-{2,}`)

// Slugify converts a string to a URL-safe slug (lowercase, hyphens for spaces).
func Slugify(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '-'
	}, s)
	s = multiHyphenRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// NormalizeSkillURL converts various GitHub URL formats to raw content URLs.
// Supports:
//   - "owner/repo/path.md" → raw.githubusercontent.com URL (main branch)
//   - "https://github.com/owner/repo/blob/branch/path" → raw.githubusercontent.com URL
//   - Raw or other HTTPS URLs → unchanged
func NormalizeSkillURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}

	// GitHub blob URL: https://github.com/owner/repo/blob/branch/path
	if strings.HasPrefix(rawURL, "https://github.com/") {
		path := strings.TrimPrefix(rawURL, "https://github.com/")
		parts := strings.SplitN(path, "/blob/", 2)
		if len(parts) == 2 {
			return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1], nil
		}
	}

	// GitHub shorthand: "owner/repo/path.md" (no scheme prefix)
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		parts := strings.SplitN(rawURL, "/", 3)
		if len(parts) == 3 {
			owner, repo, path := parts[0], parts[1], parts[2]
			return "https://raw.githubusercontent.com/" + owner + "/" + repo + "/main/" + path, nil
		}
	}

	// Already a raw URL or other arbitrary URL — pass through unchanged
	return rawURL, nil
}

// ValidateImportURL checks that a skill URL is safe to fetch (SSRF protection).
// Blocks HTTP, localhost, private IPs, and link-local addresses.
//
// Note: this validates the hostname at call time. A DNS rebinding attack could
// resolve the hostname to a private IP during the actual fetch. For MVP this
// protection level is acceptable; future hardening should resolve the hostname
// at fetch time and re-check the IP.
func ValidateImportURL(_ context.Context, rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("url is required")
	}

	// Normalize first (GitHub shortcuts → HTTPS raw URLs)
	normalized, err := NormalizeSkillURL(rawURL)
	if err != nil {
		return err
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsed.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed for skill import")
	}

	host := parsed.Hostname()

	// Block localhost
	if host == "localhost" {
		return fmt.Errorf("localhost URLs are not allowed")
	}

	// Block private/internal IP ranges
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("private/internal IP addresses are not allowed")
		}
	}

	return nil
}
