// Package manifest is the declarative workspace-bundle layer for
// Crewship. A manifest is a YAML (or JSON, since JSON is a YAML 1.2
// subset) document that captures a crew or workspace as data:
//
//	apiVersion: crewship/v1
//	kind: Crew | Workspace
//	metadata: ...
//	spec:
//	  agents: [...]
//	  skills: [...]
//	  credentials: [...]   # slots only — values never travel in the file
//	  mcp_servers: [...]
//
// Callers parse a manifest with Load, validate it with Validate, then
// drive create/update calls through Apply. The flow is intentionally
// client-side: every mutation goes through the same REST endpoints the
// UI uses, so RBAC, audit logging, and WebSocket notifications fire
// the way they would for an interactive user. No direct DB writes.
//
// Idempotency: every resource is keyed by slug within its workspace.
// Re-applying the same manifest converges existing rows toward the
// declared state. The default upsert mode is the friendliest for
// iterative editing; --strict and --replace are explicit knobs for
// the cases where create-only or destructive replace is the desired
// behaviour.
package manifest

// APIVersion is the version string the parser recognises. Future
// breaking changes bump this to v2; v1 stays supported indefinitely
// (the server accepts every past version it has ever shipped).
const APIVersion = "crewship/v1"

// Kind enumerates the top-level shapes the parser accepts. Workspace
// is a multi-crew bundle that nests Crew specs inside it; Crew is the
// single-crew bundle that lives on its own. Both share the same Spec
// types under the hood — Workspace just adds outer wrapping.
//
// SPEC-2 added 14 additional kinds for declarative deployment of
// projects, labels, milestones, routines, workflow templates, triage
// rules, recurring issues, saved views, feature flags, instance
// settings, recipes, crew templates, connectors, and hooks. Their
// document types live under internal/manifest/kinds.
const (
	KindCrew             = "Crew"
	KindAgent            = "Agent"
	KindIntegration      = "Integration"
	KindWorkspace        = "Workspace"
	KindProject          = "Project"
	KindLabel            = "Label"
	KindMilestone        = "Milestone"
	KindWorkflowTemplate = "WorkflowTemplate"
	KindTriageRule       = "TriageRule"
	KindRecurringIssue   = "RecurringIssue"
	KindSavedView        = "SavedView"
	KindRoutine          = "Routine"
	KindFeatureFlag      = "FeatureFlag"
	KindInstanceSetting  = "InstanceSetting"
	KindRecipe           = "Recipe"
	KindCrewTemplate     = "CrewTemplate"
	KindConnector        = "Connector"
	KindHook             = "Hook"
	KindSkill            = "Skill"
	KindIssue            = "Issue"
)

// Document is the discriminated top-level shape. apiVersion + kind
// drive which branch of Spec is populated. The raw YAML may have one
// or many Document blocks separated by `---`; Load returns the list.
type Document struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind"       json:"kind"`
	Metadata   Metadata `yaml:"metadata"   json:"metadata"`

	// Spec for kind=Crew. Populated when Kind == "Crew".
	Spec *CrewSpec `yaml:"spec,omitempty" json:"spec,omitempty"`
}

// WorkspaceDocument is the variant the parser unmarshals into when
// Kind == "Workspace". The two structs intentionally don't share the
// `spec:` key because YAML can't switch a single field's type by
// sibling discriminator — we tee the raw bytes into the right shape
// inside Load instead.
type WorkspaceDocument struct {
	APIVersion string        `yaml:"apiVersion" json:"apiVersion"`
	Kind       string        `yaml:"kind"       json:"kind"`
	Metadata   Metadata      `yaml:"metadata"   json:"metadata"`
	Spec       WorkspaceSpec `yaml:"spec"       json:"spec"`
}

// Metadata is the descriptive header common to every kind. Slug is
// the only field load-bearing for apply (it's the idempotency key);
// the rest exists for humans browsing the file or a future "shared
// manifest registry" view.
type Metadata struct {
	Name              string            `yaml:"name"               json:"name"`
	Slug              string            `yaml:"slug"               json:"slug"`
	Description       string            `yaml:"description,omitempty"        json:"description,omitempty"`
	Icon              string            `yaml:"icon,omitempty"               json:"icon,omitempty"`
	Color             string            `yaml:"color,omitempty"              json:"color,omitempty"`
	Author            string            `yaml:"author,omitempty"             json:"author,omitempty"`
	Version           string            `yaml:"version,omitempty"            json:"version,omitempty"`
	License           string            `yaml:"license,omitempty"            json:"license,omitempty"`
	PreferredLanguage string            `yaml:"preferred_language,omitempty" json:"preferred_language,omitempty"`
	Labels            map[string]string `yaml:"labels,omitempty"             json:"labels,omitempty"`
}

// WorkspaceSpec is the shape under `spec:` for a kind=Workspace
// document. Credentials and Skills declared at workspace scope are
// shared across every nested crew; per-crew overrides go in the
// nested CrewSpec.
type WorkspaceSpec struct {
	Credentials []Credential `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	Skills      []Skill      `yaml:"skills,omitempty"      json:"skills,omitempty"`
	Crews       []CrewSpec   `yaml:"crews"                 json:"crews"`
}

// CrewSpec is the shape under `spec:` for a kind=Crew document, and
// also the shape of each entry under spec.crews of a Workspace.
// Devcontainer is optional — leaving it nil ships a crew with no
// runtime container (a less common but valid mode for purely
// orchestration-only crews).
type CrewSpec struct {
	// SlugOverride lets a workspace-nested crew specify a slug
	// different from a sibling metadata block. For standalone Crew
	// documents the metadata.slug field is the source of truth and
	// this field is ignored.
	SlugOverride string `yaml:"slug,omitempty" json:"slug,omitempty"`

	// Name override, same rationale as SlugOverride.
	Name        string `yaml:"name,omitempty"        json:"name,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Icon        string `yaml:"icon,omitempty"        json:"icon,omitempty"`
	Color       string `yaml:"color,omitempty"       json:"color,omitempty"`

	Devcontainer *Devcontainer `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
	Credentials  []Credential  `yaml:"credentials,omitempty"  json:"credentials,omitempty"`
	MCPServers   []MCPServer   `yaml:"mcp_servers,omitempty"  json:"mcp_servers,omitempty"`
	Skills       []Skill       `yaml:"skills,omitempty"       json:"skills,omitempty"`
	Agents       []Agent       `yaml:"agents"                 json:"agents"`

	// Services describes sidecar containers (Postgres, Redis,
	// MySQL, etc.) that run alongside the agent container on the
	// crew's bridge network. The docker provider starts each
	// service before the agent runtime and gates the agent's
	// start on the sidecar's HEALTHCHECK. Agents reach services
	// by Name (e.g. `redis:6379`, `postgres:5432`).
	//
	// Volumes are crew-private named volumes — declared
	// volume names are namespaced internally to the crew, so
	// two crews can both declare `pg-data` and get isolated
	// stores.
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
}

// Service is one sidecar container the crew needs. Each entry maps
// 1-to-1 to a `docker run` invocation against a crew-scoped bridge
// network at provision time (provisioner support pending — see
// CrewSpec.Services for the deferred-implementation note).
type Service struct {
	// Name is the network alias inside the crew bridge. Agents
	// reach the service via this name (e.g. "redis:6379"). Must be
	// a valid DNS label and unique within the crew.
	Name string `yaml:"name" json:"name"`

	// Image is the container image — anything `docker pull` can
	// fetch. Pinning a digest is encouraged for reproducibility.
	Image string `yaml:"image" json:"image"`

	// Command overrides the image ENTRYPOINT+CMD. Rarely needed
	// for stock DB images but useful for utility containers.
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`

	// Env injects literal environment variables. For secrets use
	// EnvRefs instead so the value comes from the workspace
	// credential vault.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// EnvRefs resolves credential env names from the surrounding
	// scope and injects them. The credential's value (or PENDING
	// sentinel) lands in the service container's environment.
	EnvRefs []string `yaml:"env_refs,omitempty" json:"env_refs,omitempty"`

	// Ports exposes container ports inside the crew bridge. Format
	// is "PORT" or "PORT/proto" (e.g. "5432", "6379/tcp"). The
	// agent reaches these via Name:Port; we don't publish to the
	// host by default — sidecars are crew-private.
	Ports []string `yaml:"ports,omitempty" json:"ports,omitempty"`

	// Volumes attaches persistent storage. Use named volumes
	// (volume:/mount/point) for per-crew persistence; bind mounts
	// are intentionally unsupported because manifests are meant
	// to be portable across hosts.
	Volumes []ServiceVolume `yaml:"volumes,omitempty" json:"volumes,omitempty"`

	// Healthcheck mirrors the docker healthcheck shape. The
	// provisioner waits for HEALTHY before starting the agent
	// when this is set, so agents don't race ahead of a DB that
	// hasn't finished its first migration.
	Healthcheck *ServiceHealthcheck `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`

	// AutoCredentials declares secrets that Crewship should
	// generate and manage on the operator's behalf. Each entry
	// produces an AUTO_MANAGED credential row at apply time
	// (attributed to the crew's lead agent), is injected as an
	// env var into this sidecar's runtime, and is automatically
	// appended to every agent's env_refs in the same crew so the
	// agent can reach the sidecar with the right value.
	//
	// Operators rarely fill this slice by hand: for well-known
	// images (postgres:*, mysql:*, mongo:*, etc.) the parser
	// merges in a sugar default — see DefaultAutoCredentialsForImage.
	// Explicit entries win over sugar defaults with the same Name.
	//
	// When a service publishes a port to the host
	// (sidecar leaves the crew bridge), AUTO_MANAGED is unsafe:
	// the external attack surface deserves an operator-chosen
	// credential, so validate.go refuses auto_credentials in that
	// configuration.
	AutoCredentials []AutoCredential `yaml:"auto_credentials,omitempty" json:"auto_credentials,omitempty"`
}

// AutoCredential is one auto-managed secret declaration on a Service.
// All fields except Name have sane defaults; minimal authoring is:
//
//	auto_credentials:
//	  - { name: POSTGRES_PASSWORD }
//
// Length defaults to 32 random bytes (64 hex chars). InjectAsEnv
// defaults to Name. InjectToAgents defaults to true.
type AutoCredential struct {
	// Name is both the credential's workspace-unique name AND the
	// default env-var name on the sidecar + on every agent in the
	// crew. Use SCREAMING_SNAKE_CASE; same constraints as the
	// existing Credential.EnvVar field.
	Name string `yaml:"name" json:"name"`

	// InjectAsEnv overrides the env-var name the sidecar receives.
	// Some images want POSTGRES_PASSWORD literally; others
	// (e.g. mariadb) want MARIADB_ROOT_PASSWORD. Empty = use Name.
	InjectAsEnv string `yaml:"inject_as_env,omitempty" json:"inject_as_env,omitempty"`

	// InjectToAgents controls whether crew agents pick the
	// credential up automatically. Nil pointer = true; set false
	// when the sidecar uses the secret internally but no agent
	// should ever see it (rare).
	InjectToAgents *bool `yaml:"inject_to_agents,omitempty" json:"inject_to_agents,omitempty"`

	// Length is the number of random bytes Crewship generates
	// before hex-encoding. Default 32 bytes (64 hex chars).
	// Minimum 16 bytes; below that the validator refuses the row.
	Length int `yaml:"length,omitempty" json:"length,omitempty"`

	// Description is shown in the UI when the operator hovers the
	// "Created by <agent>" row. Optional; the sugar layer fills
	// this in with a human sentence for known images.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// EffectiveInjectAsEnv returns the env-var name the sidecar should
// receive — InjectAsEnv when set, else Name. Centralised so the
// dispatch and the validator agree on the same fallback rule.
func (a *AutoCredential) EffectiveInjectAsEnv() string {
	if a.InjectAsEnv != "" {
		return a.InjectAsEnv
	}
	return a.Name
}

// EffectiveInjectToAgents returns the bool with default-true
// resolution. Same centralisation rationale as EffectiveInjectAsEnv.
func (a *AutoCredential) EffectiveInjectToAgents() bool {
	if a.InjectToAgents == nil {
		return true
	}
	return *a.InjectToAgents
}

// EffectiveLength returns the resolved byte count, applying the
// 32-byte default when Length is zero. Caller is responsible for
// rejecting positive values below 16 (validate.go does that).
func (a *AutoCredential) EffectiveLength() int {
	if a.Length <= 0 {
		return 32
	}
	return a.Length
}

// ServiceVolume is a named-volume → mount-path binding.
type ServiceVolume struct {
	Name  string `yaml:"name"  json:"name"`
	Mount string `yaml:"mount" json:"mount"`
}

// ServiceHealthcheck is a small projection of Docker's healthcheck
// config. Mirrors compose's healthcheck shape so authors can copy
// snippets between crewship.yaml and docker-compose.yml.
type ServiceHealthcheck struct {
	Test        []string `yaml:"test"             json:"test"`
	Interval    string   `yaml:"interval,omitempty"     json:"interval,omitempty"` // e.g. "5s"
	Timeout     string   `yaml:"timeout,omitempty"      json:"timeout,omitempty"`  // e.g. "3s"
	Retries     int      `yaml:"retries,omitempty"      json:"retries,omitempty"`
	StartPeriod string   `yaml:"start_period,omitempty" json:"start_period,omitempty"` // e.g. "10s"
}

// Devcontainer mirrors the relevant subset of devcontainer.json used
// by Crewship's provisioner. Anything not listed here can still be
// expressed by hand-writing the JSON in the Raw field; the apply
// path merges Raw onto the structured fields, with structured fields
// taking precedence (so `image:` in YAML overrides Raw["image"]).
type Devcontainer struct {
	Image          string            `yaml:"image,omitempty"            json:"image,omitempty"`
	Features       map[string]any    `yaml:"features,omitempty"         json:"features,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"              json:"env,omitempty"`
	Mise           string            `yaml:"mise,omitempty"             json:"mise,omitempty"`
	RuntimeImage   string            `yaml:"runtime_image,omitempty"    json:"runtime_image,omitempty"`
	MemoryMB       *int              `yaml:"memory_mb,omitempty"        json:"memory_mb,omitempty"`
	CPUs           *float64          `yaml:"cpus,omitempty"             json:"cpus,omitempty"`
	TTLHours       *int              `yaml:"ttl_hours,omitempty"        json:"ttl_hours,omitempty"`
	NetworkMode    string            `yaml:"network_mode,omitempty"     json:"network_mode,omitempty"`
	AllowedDomains []string          `yaml:"allowed_domains,omitempty"  json:"allowed_domains,omitempty"`
	Raw            map[string]any    `yaml:"raw,omitempty"              json:"raw,omitempty"`
}

// Credential is a slot declaration — never a value carrier. The
// manifest format intentionally has no `value:` field so a manifest
// is always safe to commit to git and copy-paste between teams. The
// CLI's apply path either accepts values via flags (--from-env,
// --secrets-file) or prompts the user; placeholders that aren't
// supplied are created as status=PENDING on the server so they show
// up in the UI as "needs value" with a Set Value CTA.
type Credential struct {
	// EnvVar is the environment variable name agents bind against —
	// doubles as the credential's workspace-unique name. Mirrors
	// recipes.RecipeCredential.EnvVarName.
	EnvVar      string `yaml:"env"                    json:"env"`
	Provider    string `yaml:"provider"               json:"provider"`
	Type        string `yaml:"type"                   json:"type"`
	Label       string `yaml:"label,omitempty"        json:"label,omitempty"`
	HelpURL     string `yaml:"help_url,omitempty"     json:"help_url,omitempty"`
	Description string `yaml:"description,omitempty"  json:"description,omitempty"`

	// Required defaults to true; setting false lets a crew declare
	// "this credential is nice-to-have, agents that need it gate on
	// presence at runtime." The current resolver treats every
	// agent_credentials row as required, so the flag is metadata
	// for UI hints today and load-bearing once the resolver grows a
	// "soft requirement" mode.
	Required *bool `yaml:"required,omitempty" json:"required,omitempty"`
}

// MCPServer is a per-crew MCP integration. Shape mirrors what
// crew_integrations endpoints accept. EnvMapping wires the server's
// expected env vars to credential env names from this manifest.
type MCPServer struct {
	Name        string            `yaml:"name"                    json:"name"`
	DisplayName string            `yaml:"display_name,omitempty"  json:"display_name,omitempty"`
	Transport   string            `yaml:"transport"               json:"transport"`
	Command     string            `yaml:"command,omitempty"       json:"command,omitempty"`
	Args        []string          `yaml:"args,omitempty"          json:"args,omitempty"`
	Endpoint    string            `yaml:"endpoint,omitempty"      json:"endpoint,omitempty"`
	EnvMapping  map[string]string `yaml:"env_mapping,omitempty"   json:"env_mapping,omitempty"`
	Icon        string            `yaml:"icon,omitempty"          json:"icon,omitempty"`
	Enabled     *bool             `yaml:"enabled,omitempty"       json:"enabled,omitempty"`
}

// Skill is a declaration with at most one source of content. The
// resolver fills in the actual SKILL.md body during Load (for
// inline / path) or during Apply (for url). Slug is the idempotency
// key; the SKILL.md frontmatter `name:` should match.
type Skill struct {
	Slug   string `yaml:"slug"             json:"slug"`
	Path   string `yaml:"path,omitempty"   json:"path,omitempty"`   // ./skills/X/SKILL.md
	Source string `yaml:"source,omitempty" json:"source,omitempty"` // https://github.com/...
	Ref    string `yaml:"ref,omitempty"    json:"ref,omitempty"`    // git ref/tag/sha
	Digest string `yaml:"digest,omitempty" json:"digest,omitempty"` // sha256:...
	Inline string `yaml:"inline,omitempty" json:"inline,omitempty"`

	// AllowUnsafeLicense bypasses the SPDX allowlist gate when the
	// importer would otherwise refuse. Per-skill flag; the manifest
	// CLI maps this to the existing /skills/import body field.
	AllowUnsafeLicense bool `yaml:"allow_unsafe_license,omitempty" json:"allow_unsafe_license,omitempty"`

	// resolved holds the SKILL.md body after Load. Not exported via
	// YAML — manifests authored by hand never set this; export
	// always extracts inline content back into path/inline form
	// depending on the export mode.
	resolved string `yaml:"-" json:"-"`
}

// Resolved returns the fetched SKILL.md body. Populated by Load for
// path/inline sources and by Apply for url sources (deferred so the
// CLI doesn't need network access at validate-time). Empty before
// resolution.
func (s *Skill) Resolved() string { return s.resolved }

// SetResolved overrides the cached SKILL.md body. Used by the resolver
// and by tests; never call it from manifest authors.
func (s *Skill) SetResolved(content string) { s.resolved = content }

// Agent is one agent definition inside a CrewSpec. Field shapes match
// internal/api.createAgentRequest so the apply path can populate the
// POST body directly without a translation layer.
type Agent struct {
	Slug           string   `yaml:"slug"                       json:"slug"`
	Name           string   `yaml:"name"                       json:"name"`
	Description    string   `yaml:"description,omitempty"      json:"description,omitempty"`
	RoleTitle      string   `yaml:"role_title,omitempty"       json:"role_title,omitempty"`
	AgentRole      string   `yaml:"agent_role,omitempty"       json:"agent_role,omitempty"` // AGENT | LEAD
	LeadMode       string   `yaml:"lead_mode,omitempty"        json:"lead_mode,omitempty"`  // active | passive
	CLIAdapter     string   `yaml:"cli_adapter,omitempty"      json:"cli_adapter,omitempty"`
	LLM            AgentLLM `yaml:"llm,omitempty"            json:"llm,omitempty"`
	ToolProfile    string   `yaml:"tool_profile,omitempty"     json:"tool_profile,omitempty"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty"  json:"timeout_seconds,omitempty"`
	MemoryEnabled  bool     `yaml:"memory_enabled,omitempty"   json:"memory_enabled,omitempty"`

	// Prompt is the system prompt body. Authors can use Prompt or
	// PromptFile but not both — PromptFile reads relative to the
	// manifest file's directory and is preferred for prompts longer
	// than a screenful.
	Prompt     string `yaml:"prompt,omitempty"      json:"prompt,omitempty"`
	PromptFile string `yaml:"prompt_file,omitempty" json:"prompt_file,omitempty"`

	// Skills and EnvRefs are slug-level references to entries in
	// the surrounding CrewSpec/WorkspaceSpec. Apply resolves the
	// slugs to IDs after the underlying objects are created.
	Skills  []string `yaml:"skills,omitempty"   json:"skills,omitempty"`
	EnvRefs []string `yaml:"env_refs,omitempty" json:"env_refs,omitempty"`
}

// AgentLLM is the LLM provider/model pair. Either field may be empty
// — the server defaults to the workspace setting when unspecified.
type AgentLLM struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string `yaml:"model,omitempty"    json:"model,omitempty"`
}

// EffectiveSlug returns the metadata slug for a standalone Crew
// document or the SlugOverride for a workspace-nested entry. Used by
// validate.go and apply.go as the single source of truth so a
// reviewer doesn't have to remember the fallback order.
func (s *CrewSpec) EffectiveSlug(meta Metadata) string {
	if s.SlugOverride != "" {
		return s.SlugOverride
	}
	return meta.Slug
}

// EffectiveName mirrors EffectiveSlug for the display name.
func (s *CrewSpec) EffectiveName(meta Metadata) string {
	if s.Name != "" {
		return s.Name
	}
	return meta.Name
}
