package manifest

import (
	"fmt"
	"regexp"
	"strings"
)

// slugFormat mirrors api.validSlugFormat: lowercase letters, digits,
// hyphens, underscores. Enforced client-side so the user sees a
// useful error before a 400 from the server.
var slugFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,49}$`)

// Validate runs every check that doesn't require network access.
// Returns a *ValidationError that bundles every failure into one
// message so the user can fix all of them in one pass instead of
// playing whack-a-mole.
//
// Categories of check, in order:
//  1. Schema shape (required fields, enum membership, slug format)
//  2. Cross-reference resolution (agent.skills points at a declared
//     skill; agent.env_refs points at a declared credential)
//  3. Authorial consistency (no duplicate slugs within a scope,
//     mcp_servers env_mapping values resolve to credentials)
func (b *Bundle) Validate() error {
	v := &validator{}
	for i := range b.Documents {
		v.checkCrewDoc(&b.Documents[i])
	}
	for i := range b.Workspaces {
		v.checkWorkspaceDoc(&b.Workspaces[i])
	}
	if len(v.errors) == 0 {
		return nil
	}
	return &ValidationError{Messages: v.errors}
}

// ValidationError aggregates problems found by Validate. Implements
// error so callers can `return err`, but exposes Messages for CLI
// rendering (one bullet per failure).
type ValidationError struct {
	Messages []string
}

func (e *ValidationError) Error() string {
	if len(e.Messages) == 1 {
		return e.Messages[0]
	}
	return fmt.Sprintf("%d validation errors:\n  - %s",
		len(e.Messages), strings.Join(e.Messages, "\n  - "))
}

type validator struct {
	errors []string
}

func (v *validator) errf(format string, args ...any) {
	v.errors = append(v.errors, fmt.Sprintf(format, args...))
}

func (v *validator) checkSlug(field, slug string) {
	if slug == "" {
		v.errf("%s: slug is required", field)
		return
	}
	if !slugFormat.MatchString(slug) {
		v.errf("%s: invalid slug %q (lowercase letters, digits, '-', '_'; max 50 chars; must start with letter or digit)", field, slug)
	}
}

func (v *validator) checkCrewDoc(doc *Document) {
	v.checkSlug("crew", doc.Metadata.Slug)
	if doc.Spec == nil {
		v.errf("crew %q: spec is required", doc.Metadata.Slug)
		return
	}
	v.checkCrewSpec(doc.Metadata, doc.Spec, nil, nil)
}

func (v *validator) checkWorkspaceDoc(doc *WorkspaceDocument) {
	v.checkSlug("workspace", doc.Metadata.Slug)
	wsCreds := indexCredentials(doc.Spec.Credentials)
	wsSkills := indexSkills(doc.Spec.Skills)

	v.checkCredentials("workspace "+doc.Metadata.Slug, doc.Spec.Credentials)
	v.checkSkills("workspace "+doc.Metadata.Slug, doc.Spec.Skills)

	seen := map[string]bool{}
	for i := range doc.Spec.Crews {
		crew := &doc.Spec.Crews[i]
		slug := crew.EffectiveSlug(Metadata{Slug: crew.SlugOverride})
		if slug == "" {
			v.errf("workspace %q: crews[%d] needs a slug", doc.Metadata.Slug, i)
			continue
		}
		if seen[slug] {
			v.errf("workspace %q: duplicate crew slug %q", doc.Metadata.Slug, slug)
			continue
		}
		seen[slug] = true
		v.checkCrewSpec(Metadata{Slug: slug, Name: crew.Name}, crew, wsCreds, wsSkills)
	}
}

// checkCrewSpec validates a single crew. wsCreds/wsSkills carry the
// workspace-scope declarations available for cross-reference; pass
// nil when validating a standalone Crew document. Cross-refs from
// agents look in the crew scope first, then fall back to workspace
// scope.
func (v *validator) checkCrewSpec(meta Metadata, spec *CrewSpec, wsCreds map[string]Credential, wsSkills map[string]Skill) {
	crewLabel := "crew " + meta.Slug

	v.checkCredentials(crewLabel, spec.Credentials)
	v.checkSkills(crewLabel, spec.Skills)
	v.checkMCPServers(crewLabel, spec.MCPServers, mergeCredIndex(wsCreds, spec.Credentials))
	v.checkServices(crewLabel, spec.Services, mergeCredIndex(wsCreds, spec.Credentials))

	if len(spec.Agents) == 0 {
		v.errf("%s: at least one agent is required", crewLabel)
	}

	creds := mergeCredIndex(wsCreds, spec.Credentials)
	skills := mergeSkillIndex(wsSkills, spec.Skills)

	seenAgent := map[string]bool{}
	leadSeen := false
	for i := range spec.Agents {
		a := &spec.Agents[i]
		label := fmt.Sprintf("%s agent %q", crewLabel, a.Slug)
		v.checkSlug(label, a.Slug)
		if a.Name == "" {
			v.errf("%s: name is required", label)
		}
		if seenAgent[a.Slug] {
			v.errf("%s: duplicate slug within crew", label)
		}
		seenAgent[a.Slug] = true

		if a.AgentRole != "" && !validAgentRole(a.AgentRole) {
			v.errf("%s: agent_role %q invalid (want AGENT or LEAD)", label, a.AgentRole)
		}
		if a.AgentRole == "LEAD" {
			if leadSeen {
				v.errf("%s crew has more than one LEAD", crewLabel)
			}
			leadSeen = true
		}
		if a.CLIAdapter != "" && !validCLIAdapter(a.CLIAdapter) {
			v.errf("%s: cli_adapter %q invalid", label, a.CLIAdapter)
		}
		if a.ToolProfile != "" && !validToolProfile(a.ToolProfile) {
			v.errf("%s: tool_profile %q invalid (want FULL, CODING, MINIMAL)", label, a.ToolProfile)
		}

		for _, sk := range a.Skills {
			if _, ok := skills[sk]; !ok {
				v.errf("%s references unknown skill %q", label, sk)
			}
		}
		for _, env := range a.EnvRefs {
			if _, ok := creds[env]; !ok {
				v.errf("%s references unknown credential env %q", label, env)
			}
		}
	}
}

func (v *validator) checkCredentials(scope string, creds []Credential) {
	seen := map[string]bool{}
	for i := range creds {
		c := &creds[i]
		if c.EnvVar == "" {
			v.errf("%s: credentials[%d] missing env", scope, i)
			continue
		}
		if seen[c.EnvVar] {
			v.errf("%s: duplicate credential env %q", scope, c.EnvVar)
		}
		seen[c.EnvVar] = true
		if c.Provider == "" {
			v.errf("%s credential %q: provider is required", scope, c.EnvVar)
		}
		if c.Type == "" {
			v.errf("%s credential %q: type is required", scope, c.EnvVar)
		}
	}
}

func (v *validator) checkSkills(scope string, skills []Skill) {
	seen := map[string]bool{}
	for i := range skills {
		s := &skills[i]
		if s.Slug == "" {
			v.errf("%s: skills[%d] missing slug", scope, i)
			continue
		}
		if seen[s.Slug] {
			v.errf("%s: duplicate skill slug %q", scope, s.Slug)
		}
		seen[s.Slug] = true

		// Source-count enforcement is also done in resolveLocalReferences,
		// but mirroring it here lets validate-only runs (e.g. CI in a
		// repo that doesn't have ./skills/ checked out) still surface
		// the misconfig instead of blowing up at resolve time.
		sources := 0
		if s.Path != "" {
			sources++
		}
		if s.Source != "" {
			sources++
		}
		if s.Inline != "" {
			sources++
		}
		if sources == 0 {
			v.errf("%s skill %q: must have one of path, source, or inline", scope, s.Slug)
		}
		if sources > 1 {
			v.errf("%s skill %q: only one of path, source, or inline may be set", scope, s.Slug)
		}
	}
}

// serviceNameRe enforces a DNS-label-safe shape because the name
// becomes the bridge-network alias agents resolve. Allowing arbitrary
// characters would either produce un-resolvable hostnames or, worse,
// silently rewrite them in the runtime. RFC 1035: 1–63 chars,
// lowercase letters/digits/'-', must start with a letter and end
// with letter or digit. The single-char form ("r") is also valid.
var serviceNameRe = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (v *validator) checkServices(scope string, services []Service, creds map[string]Credential) {
	seen := map[string]bool{}
	for i := range services {
		s := &services[i]
		if s.Name == "" {
			v.errf("%s: services[%d] missing name", scope, i)
			continue
		}
		if !serviceNameRe.MatchString(s.Name) {
			v.errf("%s service %q: name must be a DNS label (lowercase letters/digits/'-', start with letter, end with letter or digit)", scope, s.Name)
		}
		if seen[s.Name] {
			v.errf("%s: duplicate service name %q", scope, s.Name)
		}
		seen[s.Name] = true
		if s.Image == "" {
			v.errf("%s service %q: image is required", scope, s.Name)
		}
		for _, envRef := range s.EnvRefs {
			if _, ok := creds[envRef]; !ok {
				v.errf("%s service %q: env_refs[%s] references unknown credential", scope, s.Name, envRef)
			}
		}
		seenVol := map[string]bool{}
		for j, vol := range s.Volumes {
			if vol.Name == "" || vol.Mount == "" {
				v.errf("%s service %q: volumes[%d] needs both name and mount", scope, s.Name, j)
				continue
			}
			if seenVol[vol.Mount] {
				v.errf("%s service %q: duplicate mount %q", scope, s.Name, vol.Mount)
			}
			seenVol[vol.Mount] = true
			// Bind mounts (host paths) are intentionally rejected
			// — manifests are meant to be portable; a "/Users/foo"
			// path will break on every other machine. Named
			// volumes are the supported persistence model.
			if strings.HasPrefix(vol.Name, "/") || strings.HasPrefix(vol.Name, ".") {
				v.errf("%s service %q: volume %q looks like a bind mount; manifests only support named volumes for portability",
					scope, s.Name, vol.Name)
			}
		}
		if s.Healthcheck != nil && len(s.Healthcheck.Test) == 0 {
			v.errf("%s service %q: healthcheck declared without a test command", scope, s.Name)
		}
	}
}

func (v *validator) checkMCPServers(scope string, servers []MCPServer, creds map[string]Credential) {
	seen := map[string]bool{}
	for i := range servers {
		s := &servers[i]
		if s.Name == "" {
			v.errf("%s: mcp_servers[%d] missing name", scope, i)
			continue
		}
		if seen[s.Name] {
			v.errf("%s: duplicate mcp server name %q", scope, s.Name)
		}
		seen[s.Name] = true

		switch s.Transport {
		case "stdio":
			if s.Command == "" {
				v.errf("%s mcp %q: stdio transport requires command", scope, s.Name)
			}
		case "streamable-http", "http", "sse":
			if s.Endpoint == "" {
				v.errf("%s mcp %q: %s transport requires endpoint", scope, s.Name, s.Transport)
			}
		case "":
			v.errf("%s mcp %q: transport is required", scope, s.Name)
		default:
			v.errf("%s mcp %q: unknown transport %q", scope, s.Name, s.Transport)
		}

		for envName, credRef := range s.EnvMapping {
			if _, ok := creds[credRef]; !ok {
				v.errf("%s mcp %q: env_mapping[%s] -> %q references unknown credential", scope, s.Name, envName, credRef)
			}
		}
	}
}

func indexCredentials(creds []Credential) map[string]Credential {
	out := make(map[string]Credential, len(creds))
	for _, c := range creds {
		out[c.EnvVar] = c
	}
	return out
}

func indexSkills(skills []Skill) map[string]Skill {
	out := make(map[string]Skill, len(skills))
	for _, s := range skills {
		out[s.Slug] = s
	}
	return out
}

func mergeCredIndex(base map[string]Credential, override []Credential) map[string]Credential {
	out := make(map[string]Credential, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for _, c := range override {
		out[c.EnvVar] = c
	}
	return out
}

func mergeSkillIndex(base map[string]Skill, override []Skill) map[string]Skill {
	out := make(map[string]Skill, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for _, s := range override {
		out[s.Slug] = s
	}
	return out
}

func validAgentRole(r string) bool {
	// Mirror the server's closed set in internal/api/agents.go. OBSERVER
	// was an early design notion that never landed in the API; admitting
	// it here would let a manifest pass validate-time and then crash at
	// apply-time with a 400 from the server.
	switch r {
	case "AGENT", "LEAD":
		return true
	}
	return false
}

func validCLIAdapter(a string) bool {
	switch a {
	case "CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID":
		return true
	}
	return false
}

func validToolProfile(p string) bool {
	switch p {
	case "FULL", "CODING", "MINIMAL":
		return true
	}
	return false
}
