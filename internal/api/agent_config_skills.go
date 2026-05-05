package api

import (
	"fmt"
	"net/http"
	"strings"
)

// installedSkillResponse is the per-skill payload that ships in the
// resolveAgentConfig response so the bridge can hand it to the
// orchestrator's per-CLI skill writer. Content is the reconstructed
// SKILL.md (frontmatter + body) — anthropic and other upstream skills
// don't store frontmatter as a discrete field, so we synthesise it from
// the columns we have.
type installedSkillResponse struct {
	Slug    string `json:"slug"`
	Vendor  string `json:"vendor,omitempty"`
	Content string `json:"content"`
}

// resolveInstalledSkills returns the agent's installed skills as ready-
// to-write SKILL.md blobs. Skills with empty bodies are skipped (the
// orchestrator writer would skip them anyway, but filtering server-side
// keeps the payload smaller).
//
// The query is intentionally narrow — name + description + body + a few
// fields used to reconstruct frontmatter. resolveSkillsBlock has its own
// query that selects credential_requirements; we don't need those here
// because per-CLI files don't carry credential status the way the
// system-prompt block does.
func (h *InternalHandler) resolveInstalledSkills(r *http.Request, agentID string) ([]installedSkillResponse, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT s.slug, COALESCE(s.vendor, ''), s.display_name, COALESCE(s.description, ''), s.content
		FROM agent_skills as2
		JOIN skills s ON s.id = as2.skill_id
		WHERE as2.agent_id = ? AND as2.enabled = 1 AND s.content IS NOT NULL AND s.content != ''
		ORDER BY s.slug
	`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query agent skills: %w", err)
	}
	defer rows.Close()

	var out []installedSkillResponse
	for rows.Next() {
		var slug, vendor, displayName, description, content string
		if err := rows.Scan(&slug, &vendor, &displayName, &description, &content); err != nil {
			return nil, fmt.Errorf("scan agent skill: %w", err)
		}
		out = append(out, installedSkillResponse{
			Slug:    slug,
			Vendor:  vendor,
			Content: reconstructSKILLMD(slug, vendor, displayName, description, content),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent skills: %w", err)
	}
	return out, nil
}

// reconstructSKILLMD synthesises a SKILL.md file from DB columns. The
// body already contains markdown; we prepend a minimal frontmatter so
// CLIs that parse the file (Claude Code, Cursor, OpenCode) get the
// metadata they expect. If the content already starts with a `---`
// frontmatter delimiter we trust it as-is — bundled anthropic skills
// arrive verbatim with their original frontmatter intact.
//
// Every scalar gets quoted via [yamlQuote] so embedded colons,
// quotes, hashes, and other YAML metacharacters can't break the
// frontmatter parse on the consuming CLI. display_name was previously
// dropped on the floor — restored, since some CLIs expose it as the
// human-readable label distinct from the slug.
func reconstructSKILLMD(slug, vendor, displayName, description, body string) string {
	trimmed := strings.TrimLeft(body, " \t\r\n")
	if strings.HasPrefix(trimmed, "---") {
		return body
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", yamlQuote(slug))
	if displayName != "" && displayName != slug {
		fmt.Fprintf(&sb, "display_name: %s\n", yamlQuote(displayName))
	}
	if description != "" {
		// SKILL.md spec caps description at 1024 chars and forbids
		// newlines, so we collapse any stray CR/LF to spaces and trust
		// the upstream cap (no truncation here — the parser already
		// validated the field length on import).
		oneLine := strings.ReplaceAll(strings.ReplaceAll(description, "\r", " "), "\n", " ")
		fmt.Fprintf(&sb, "description: %s\n", yamlQuote(oneLine))
	}
	if vendor != "" {
		fmt.Fprintf(&sb, "vendor: %s\n", yamlQuote(vendor))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	return sb.String()
}

// yamlQuote serialises a string as a YAML 1.2 double-quoted scalar.
// Always quotes — the un-quoted plain scalar form has too many
// pitfalls (colons, hashes, leading dashes, "true"/"false"/"null"
// alias values) for an automated writer. Escapes \" and \\ per the
// double-quoted spec; control chars are passed through (the SKILL.md
// fields we serialise here come from a column that the parser
// already cleaned).
func yamlQuote(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
