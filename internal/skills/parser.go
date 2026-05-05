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

// validCategories enumerates the SkillCategory values accepted both by the
// frontmatter parser and the v65 SQL schema. Kept in sync with the
// SkillCategory enum in prisma/schema.prisma — adding a value here without
// the matching schema change will fail at INSERT.
var validCategories = map[string]bool{
	"CODING":     true,
	"AUTOMATION": true,
	"DATA":       true,
	"DEVOPS":     true,
	"SUPPORT":    true,
	"SALES":      true,
	"WRITING":    true,
	"RESEARCH":   true,
	"PM":         true,
	"DESIGN":     true,
	"SECURITY":   true,
	"FINANCE":    true,
	"OPS":        true,
	"CUSTOM":     true,
}

// validRuntimes mirrors the SkillRuntime enum.
var validRuntimes = map[string]bool{
	"INSTRUCTIONS": true,
	"SCRIPT":       true,
	"MCP":          true,
	"HYBRID":       true,
}

// validMaturities mirrors the SkillMaturity enum.
var validMaturities = map[string]bool{
	"OFFICIAL":     true,
	"CURATED":      true,
	"COMMUNITY":    true,
	"EXPERIMENTAL": true,
}

// ValidCategory returns true if the category is in the allowed set.
func ValidCategory(cat string) bool {
	return validCategories[cat]
}

// ValidRuntime returns true if the runtime is in the allowed set.
func ValidRuntime(rt string) bool {
	return validRuntimes[rt]
}

// ValidMaturity returns true if the maturity is in the allowed set.
func ValidMaturity(m string) bool {
	return validMaturities[m]
}

// SkillMeta holds parsed YAML frontmatter from a SKILL.md file.
//
// Vendor namespaces the skill within the registry (e.g. "anthropic",
// "vercel", "community"). When absent in frontmatter, callers default to
// the import source ("community" for user-imported skills, the embedder's
// vendor for bundled skills).
//
// Runtime / Maturity are optional — Crewship infers safe defaults
// (INSTRUCTIONS / COMMUNITY) when frontmatter omits them. License is the
// SPDX identifier; we accept the freeform field name `license` for
// compatibility with anthropics/skills (license: Apache-2.0 etc.) and
// surface it via the spdx_license column once SPDX-validated downstream.
type SkillMeta struct {
	Name                   string   `yaml:"name"`
	DisplayName            string   `yaml:"display_name"`
	Version                string   `yaml:"version"`
	Author                 string   `yaml:"author"`
	Vendor                 string   `yaml:"vendor"`
	Homepage               string   `yaml:"homepage"`
	License                string   `yaml:"license"`
	Description            string   `yaml:"description"`
	Category               string   `yaml:"category"`
	Runtime                string   `yaml:"runtime"`
	Maturity               string   `yaml:"maturity"`
	Icon                   string   `yaml:"icon"`
	CredentialRequirements []string `yaml:"credential_requirements"`
	Tags                   []string `yaml:"tags"`
}

// ParsedSkill contains the parsed metadata and markdown body of a SKILL.md file.
//
// DescriptionQuality is the result of [LintDescription] applied to Meta —
// "" means the description passes the v0.1 linter; otherwise a one-line
// reason. Importers store this on the row so the UI can surface it
// without re-running the linter.
type ParsedSkill struct {
	Meta               SkillMeta
	Content            string
	DescriptionQuality string
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

	// Normalize and validate category (if provided). Unknown values fall
	// back to CUSTOM rather than rejecting the whole skill — third-party
	// registries use a long tail of category strings ("Tools", "MCP",
	// language slugs) and a hard reject would block ~30% of imports we
	// surveyed in the May 2026 ecosystem research.
	if meta.Category != "" {
		meta.Category = strings.ToUpper(meta.Category)
		if !ValidCategory(meta.Category) {
			meta.Category = "CUSTOM"
		}
	}

	if meta.Runtime != "" {
		meta.Runtime = strings.ToUpper(meta.Runtime)
		if !ValidRuntime(meta.Runtime) {
			meta.Runtime = "INSTRUCTIONS"
		}
	}

	if meta.Maturity != "" {
		meta.Maturity = strings.ToUpper(meta.Maturity)
		if !ValidMaturity(meta.Maturity) {
			meta.Maturity = "COMMUNITY"
		}
	}

	// Auto-slugify name
	meta.Name = Slugify(meta.Name)

	// Strip dynamic-context backtick syntax (e.g. !`git diff HEAD`) — Anthropic
	// Claude Code executes these at load time on the host, which is wrong for a
	// multi-tenant runtime. v0.2 will reintroduce with a per-skill allow-list;
	// for now we silently remove so the rest of the body stays useful.
	content = stripDynamicContext(content)

	descQuality := LintDescription(meta.Description)

	return &ParsedSkill{
		Meta:               meta,
		Content:            content,
		DescriptionQuality: descQuality,
	}, nil
}

// dynamicContextRe matches !`...` blocks (single-line and multi-line bodies),
// the exact syntax Claude Code expands by shelling out before sending the
// skill body to the model. Stripped on import; see [ParseSKILLMD].
var dynamicContextRe = regexp.MustCompile("(?s)!`[^`]*`")

func stripDynamicContext(s string) string {
	return dynamicContextRe.ReplaceAllString(s, "")
}

// triggerPhraseRe matches the phrasings that make a skill description
// usable as an LLM trigger. The skill-creator workflow at
// github.com/anthropics/skills/skills/skill-creator emphasises that
// description is THE field the router matches on; without an explicit
// trigger phrase the skill silently never activates. We accept the common
// surfaces: "use when ...", "use this when ...", "use this skill when ...",
// "useful for ...", "useful when ...", "for ...ing ..." (gerund), "to ...".
var triggerPhraseRe = regexp.MustCompile(`(?i)\b(use\s+(this\s+)?(skill\s+)?when|useful\s+(for|when)|to\s+\w+|for\s+\w+ing)\b`)

// LintDescription returns "" if the description passes the v0.1 linter,
// otherwise a one-line reason explaining why it likely won't trigger
// reliably. Linter is intentionally lenient — anything stricter blocks
// real upstream skills (anthropics/skills uses short imperatives in some
// SKILL.md frontmatter). Callers should record the reason on the row but
// not refuse the import.
func LintDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "missing description"
	}
	if len(desc) < 30 {
		return "description too short (<30 chars) — LLM router may not match it"
	}
	if !triggerPhraseRe.MatchString(desc) {
		return "description has no trigger phrase (\"use when ...\", \"useful for ...\") — LLM router may not match it"
	}
	return ""
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
