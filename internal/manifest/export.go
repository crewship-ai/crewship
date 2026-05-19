package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ExportOptions controls how Export renders the manifest. Defaults
// produce a single-file YAML with skills inlined as `inline:` blocks
// — the friendliest shape for copy-paste sharing.
type ExportOptions struct {
	// IncludeCredentials toggles whether credential slots are
	// emitted. Default true; some sharing flows prefer to strip
	// them so consumers must explicitly add their own.
	IncludeCredentials bool
	// IncludeSkillBodies controls whether skill bodies are inlined.
	// When false, only the slug is emitted — useful for a
	// "structure-only" overview manifest.
	IncludeSkillBodies bool
	// Future: multi-file mode where each skill body is written to
	// a sibling SKILL.md and the manifest references it via
	// `path:`. Not implemented; the option used to live on this
	// struct but it was misleading to expose without a code path
	// that read it. Track in a follow-up PR.
}

// DefaultExportOptions are the inline-everything-into-one-file
// defaults used by the CLI's `crewship export` command.
func DefaultExportOptions() ExportOptions {
	return ExportOptions{
		IncludeCredentials: true,
		IncludeSkillBodies: true,
	}
}

// ExportCrew fetches a crew (by slug) and renders it as a kind=Crew
// manifest. Skills attached to any of the crew's agents are pulled
// into a top-level skills: list; per-agent skill links are emitted
// as slug references.
//
// This is the round-trip partner of Apply: apply → export → apply
// should be a no-op on a fresh workspace, modulo computed fields
// (ids, timestamps) that the manifest format intentionally omits.
func ExportCrew(ctx context.Context, c *Client, slug string, opts ExportOptions) (string, error) {
	crew, err := c.FindCrewBySlug(ctx, slug)
	if err != nil {
		return "", fmt.Errorf("look up crew %q: %w", slug, err)
	}
	if crew == nil {
		return "", fmt.Errorf("crew %q not found", slug)
	}
	agents, err := c.ListAgentsByCrew(ctx, crew.ID)
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	servers, err := c.ListCrewIntegrations(ctx, crew.ID)
	if err != nil {
		// MCP listing failure isn't fatal — the rest of the manifest
		// is still useful; note the gap and continue.
		servers = nil
	}

	doc := Document{
		APIVersion: APIVersion,
		Kind:       KindCrew,
		Metadata: Metadata{
			Name:        crew.Name,
			Slug:        crew.Slug,
			Description: deref(crew.Description),
			Color:       deref(crew.Color),
			Icon:        deref(crew.Icon),
		},
	}

	spec := &CrewSpec{}
	// Services round-trip through the services_json column. Decode
	// here so the exported manifest carries the same Service shape
	// the user would have authored.
	if deref(crew.ServicesJSON) != "" {
		var svcs []Service
		if err := json.Unmarshal([]byte(*crew.ServicesJSON), &svcs); err == nil {
			spec.Services = svcs
		}
	}
	if hasDevcontainerFields(crew) {
		spec.Devcontainer = &Devcontainer{
			MemoryMB:       crew.ContainerMemoryMB,
			CPUs:           crew.ContainerCPUs,
			TTLHours:       crew.ContainerTTLHours,
			NetworkMode:    deref(crew.NetworkMode),
			AllowedDomains: crew.AllowedDomains,
			RuntimeImage:   deref(crew.RuntimeImage),
		}
		if deref(crew.MiseConfig) != "" {
			spec.Devcontainer.Mise = *crew.MiseConfig
		}
		// devcontainer_config is JSON; surface it under raw: rather
		// than trying to round-trip it back to image+features+env.
		// A future iteration can parse and split it, but the JSON
		// blob is unambiguous and survives apply unchanged.
		if cfg := deref(crew.DevcontainerConfig); cfg != "" {
			raw := map[string]any{}
			if err := yaml.Unmarshal([]byte(cfg), &raw); err == nil {
				spec.Devcontainer.Raw = raw
			}
		}
	}

	// Agents — sorted alphabetically by slug for diff-stable output.
	// For each agent we also fetch its skill bindings and credential
	// bindings so the export round-trips through apply: without this
	// step, applying an exported crew would create the agent rows
	// but every agent_skills / agent_credentials link would be lost.
	sort.Slice(agents, func(i, j int) bool { return agents[i].Slug < agents[j].Slug })
	skillSlugSet := map[string]bool{}
	credEnvSet := map[string]bool{}
	for i := range agents {
		a := agents[i]
		agentDecl := Agent{
			Slug:           a.Slug,
			Name:           a.Name,
			RoleTitle:      deref(a.RoleTitle),
			AgentRole:      a.AgentRole,
			CLIAdapter:     a.CLIAdapter,
			ToolProfile:    a.ToolProfile,
			TimeoutSeconds: a.TimeoutSeconds,
			MemoryEnabled:  a.MemoryEnabled,
			LLM: AgentLLM{
				Provider: deref(a.LLMProvider),
				Model:    deref(a.LLMModel),
			},
			Prompt: deref(a.SystemPrompt),
		}
		if skillBindings, err := c.ListAgentSkills(ctx, a.ID); err == nil {
			sort.Slice(skillBindings, func(i, j int) bool { return skillBindings[i].Skill.Slug < skillBindings[j].Skill.Slug })
			for _, b := range skillBindings {
				if b.Skill.Slug == "" {
					continue
				}
				agentDecl.Skills = append(agentDecl.Skills, b.Skill.Slug)
				skillSlugSet[b.Skill.Slug] = true
			}
		}
		if credBindings, err := c.ListAgentCredentials(ctx, a.ID); err == nil {
			sort.Slice(credBindings, func(i, j int) bool { return credBindings[i].EnvVarName < credBindings[j].EnvVarName })
			for _, b := range credBindings {
				agentDecl.EnvRefs = append(agentDecl.EnvRefs, b.EnvVarName)
				credEnvSet[b.EnvVarName] = true
			}
		}
		spec.Agents = append(spec.Agents, agentDecl)
	}

	// Populate spec.Skills with declarations for every skill any
	// agent in this crew binds against, so apply can re-link them.
	// The skill body is fetched per slug; missing bodies become
	// `source:` references (since we don't know the original URL,
	// we emit slug-only and let the user fill source: by hand). For
	// inline use cases an export through this path is best-effort.
	if len(skillSlugSet) > 0 {
		allSkills, err := c.ListSkills(ctx)
		if err == nil {
			byslug := map[string]SkillResponse{}
			for _, s := range allSkills {
				byslug[s.Slug] = s
			}
			var skillSlugs []string
			for s := range skillSlugSet {
				skillSlugs = append(skillSlugs, s)
			}
			sort.Strings(skillSlugs)
			for _, slug := range skillSlugs {
				decl := Skill{Slug: slug}
				if opts.IncludeSkillBodies {
					if s, ok := byslug[slug]; ok && s.ID != "" {
						decl.Inline = c.fetchSkillContent(s.ID)
					}
				}
				if decl.Inline == "" && decl.Path == "" && decl.Source == "" {
					// Body unavailable. Emit a sentinel inline so
					// the validator's "one of" check still passes;
					// the user must replace it with path: or
					// source: before re-applying. Documented as a
					// known limitation in examples/manifests/README.md.
					decl.Inline = "---\nname: " + slug + "\ndescription: (exported reference, supply body before re-apply)\n---\n"
				}
				spec.Skills = append(spec.Skills, decl)
			}
		}
	}

	// Populate spec.Credentials with slot declarations for every
	// env_ref any agent binds against, plus the underlying
	// credential's type/provider from the workspace list.
	if len(credEnvSet) > 0 && opts.IncludeCredentials {
		allCreds, err := c.ListCredentials(ctx)
		if err == nil {
			byname := map[string]CredentialResponse{}
			for _, cred := range allCreds {
				byname[cred.Name] = cred
			}
			var envs []string
			for e := range credEnvSet {
				envs = append(envs, e)
			}
			sort.Strings(envs)
			for _, env := range envs {
				cd := Credential{EnvVar: env}
				if cred, ok := byname[env]; ok {
					cd.Type = cred.Type
					cd.Provider = cred.Provider
				}
				spec.Credentials = append(spec.Credentials, cd)
			}
		}
	}

	// MCP servers — sorted for stable output.
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	for i := range servers {
		s := servers[i]
		spec.MCPServers = append(spec.MCPServers, MCPServer{
			Name:        s.Name,
			DisplayName: s.DisplayName,
			Transport:   s.Transport,
			Command:     deref(s.Command),
			Endpoint:    deref(s.Endpoint),
			Enabled:     &s.Enabled,
		})
	}

	doc.Spec = spec
	return MarshalDocument(doc, opts)
}

// ExportWorkspace renders every crew in the workspace as a single
// kind=Workspace bundle. Workspace-level credentials are emitted as
// slot declarations (values never travel in the file).
func ExportWorkspace(ctx context.Context, c *Client, opts ExportOptions) (string, error) {
	crews, err := c.ListCrews(ctx)
	if err != nil {
		return "", fmt.Errorf("list crews: %w", err)
	}
	sort.Slice(crews, func(i, j int) bool { return crews[i].Slug < crews[j].Slug })

	wsDoc := WorkspaceDocument{
		APIVersion: APIVersion,
		Kind:       KindWorkspace,
	}

	// Aggregate skill + credential slots used by ANY agent across
	// ALL crews. That gives the workspace-bundle reader a single
	// place to look at the security surface.
	allSkillSlugs := map[string]bool{}
	allCredEnvs := map[string]string{} // env → provider for prefill

	for i := range crews {
		crewSlug := crews[i].Slug
		// Re-use ExportCrew to get a populated CrewSpec, then strip
		// its kind/metadata wrapper.
		yamlStr, err := ExportCrew(ctx, c, crewSlug, ExportOptions{
			IncludeCredentials: opts.IncludeCredentials,
			IncludeSkillBodies: opts.IncludeSkillBodies,
		})
		if err != nil {
			return "", fmt.Errorf("export crew %q: %w", crewSlug, err)
		}
		// Round-trip back through Load to grab the spec. Since
		// the YAML we're parsing was just produced by ExportCrew
		// in this same call, any Load failure or missing spec
		// indicates a real bug in the serialiser — silently
		// skipping the crew would produce a workspace export
		// missing crews the operator declared.
		b, err := Load([]byte(yamlStr))
		if err != nil {
			return "", fmt.Errorf("reload exported crew %q: %w", crewSlug, err)
		}
		if len(b.Documents) == 0 || b.Documents[0].Spec == nil {
			return "", fmt.Errorf("reload exported crew %q: missing document/spec", crewSlug)
		}
		spec := b.Documents[0].Spec
		// Lift skills + credentials to the workspace level so they
		// dedupe across crews. The per-crew spec keeps the agent
		// refs pointing at the slug/env, which resolves against the
		// merged workspace+crew scope at apply-time.
		for _, sk := range spec.Skills {
			allSkillSlugs[sk.Slug] = true
		}
		for _, cd := range spec.Credentials {
			if _, ok := allCredEnvs[cd.EnvVar]; !ok {
				allCredEnvs[cd.EnvVar] = cd.Provider + "|" + cd.Type
			}
		}
		spec.Skills = nil
		spec.Credentials = nil
		// Tag the crew with its slug/name overrides so the
		// resulting workspace YAML knows which child slug to use.
		spec.SlugOverride = b.Documents[0].Metadata.Slug
		spec.Name = b.Documents[0].Metadata.Name
		spec.Description = b.Documents[0].Metadata.Description
		spec.Icon = b.Documents[0].Metadata.Icon
		spec.Color = b.Documents[0].Metadata.Color
		wsDoc.Spec.Crews = append(wsDoc.Spec.Crews, *spec)
	}

	// Workspace-level skills (slug-only references; consumer has to
	// fetch the body via apply path).
	var skillSlugs []string
	for s := range allSkillSlugs {
		skillSlugs = append(skillSlugs, s)
	}
	sort.Strings(skillSlugs)
	// Hoist ListSkills out of the per-slug loop. The Client caches
	// the result after the first call, so the previous in-loop call
	// only paid an extra map allocation per iteration — but making
	// the O(1) intent obvious in the source beats relying on the
	// cache. Build a slug→response index once and reuse it.
	var skillBySlug map[string]SkillResponse
	if opts.IncludeSkillBodies && len(skillSlugs) > 0 {
		all, _ := c.ListSkills(ctx)
		skillBySlug = make(map[string]SkillResponse, len(all))
		for _, s := range all {
			skillBySlug[s.Slug] = s
		}
	}
	for _, slug := range skillSlugs {
		decl := Skill{Slug: slug}
		if opts.IncludeSkillBodies {
			if s, ok := skillBySlug[slug]; ok && s.ID != "" {
				decl.Inline = c.fetchSkillContent(s.ID)
			}
		}
		if decl.Inline == "" {
			decl.Inline = "---\nname: " + slug + "\ndescription: (exported reference, supply body before re-apply)\n---\n"
		}
		wsDoc.Spec.Skills = append(wsDoc.Spec.Skills, decl)
	}

	// Workspace-level credential slots.
	if opts.IncludeCredentials {
		var envs []string
		for e := range allCredEnvs {
			envs = append(envs, e)
		}
		sort.Strings(envs)
		for _, env := range envs {
			cd := Credential{EnvVar: env}
			parts := strings.SplitN(allCredEnvs[env], "|", 2)
			if len(parts) == 2 {
				cd.Provider = parts[0]
				cd.Type = parts[1]
			}
			wsDoc.Spec.Credentials = append(wsDoc.Spec.Credentials, cd)
		}
	}

	wsDoc.Metadata.Name, wsDoc.Metadata.Slug = workspaceMeta(ctx, c)

	var sb strings.Builder
	sb.WriteString("# yaml-language-server: $schema=https://schemas.crewship.ai/v1/manifest.json\n")
	out, err := yaml.Marshal(wsDoc)
	if err != nil {
		return "", fmt.Errorf("marshal workspace: %w", err)
	}
	sb.Write(out)
	return sb.String(), nil
}

// workspaceMeta fetches the active workspace's display name and
// slug in a single API call. Falls back to empty strings on any
// failure — these fields are informational on the exported manifest
// and the export should still produce a usable file when the API
// is unavailable. Consolidates what used to be two round-trips
// returning one field each.
//
// ctx is threaded through so a cancelled or deadline-bound
// ExportWorkspace call aborts cleanly here instead of issuing a
// blind HTTP request after the caller has already given up.
func workspaceMeta(ctx context.Context, c *Client) (name, slug string) {
	wsID := c.api.GetWorkspaceID()
	if wsID == "" {
		return "", ""
	}
	body, err := c.fetchBodyCtx(ctx, "/api/v1/workspaces/"+wsID)
	if err != nil || len(body) == 0 {
		return "", ""
	}
	var ws struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if jsonErr := json.Unmarshal(body, &ws); jsonErr != nil {
		return "", ""
	}
	return ws.Name, ws.Slug
}

// MarshalDocument serialises a single document to a stable YAML
// representation suitable for committing to a repo. Adds a
// $schema yaml-language-server hint at the top so editors get
// autocomplete without any extra config.
//
// doc is passed by value but its Spec pointer is shared with the
// caller, so we clone the spec (and any slices we mutate) before
// applying export-only filtering. Without the clone, MarshalDocument
// would permanently strip credentials and skill bodies from the
// caller's in-memory manifest — a surprising side effect for a
// "serialiser" name.
func MarshalDocument(doc Document, opts ExportOptions) (string, error) {
	var sb strings.Builder
	sb.WriteString("# yaml-language-server: $schema=https://schemas.crewship.ai/v1/manifest.json\n")
	if doc.Spec != nil && (!opts.IncludeCredentials || !opts.IncludeSkillBodies) {
		clonedSpec := *doc.Spec
		if !opts.IncludeCredentials {
			clonedSpec.Credentials = nil
		}
		if !opts.IncludeSkillBodies {
			clonedSkills := make([]Skill, len(clonedSpec.Skills))
			for i, s := range clonedSpec.Skills {
				s.Inline = ""
				s.Path = ""
				clonedSkills[i] = s
			}
			clonedSpec.Skills = clonedSkills
		}
		doc.Spec = &clonedSpec
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	sb.Write(out)
	return sb.String(), nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func hasDevcontainerFields(c *CrewResponse) bool {
	if c == nil {
		return false
	}
	// NetworkMode counts here because a crew that only flips
	// network_mode to "restricted" (with allowed_domains) needs
	// a devcontainer block on re-apply — without it the export
	// loses the policy and the crew comes back as "free" on the
	// next apply.
	return c.RuntimeImage != nil ||
		c.NetworkMode != nil ||
		c.DevcontainerConfig != nil ||
		c.MiseConfig != nil ||
		c.ContainerMemoryMB != nil ||
		c.ContainerCPUs != nil ||
		c.ContainerTTLHours != nil
}
