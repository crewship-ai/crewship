// Package kinds — per-kind manifest implementations.
//
// This file implements kind: Crew — the AUTHORING surface for crews.
// Where kind: CrewTemplate is a one-shot DEPLOY of a server-side
// template (no in-place updates, no field-by-field schema), kind: Crew
// is the full-CRUD reference: Create on first apply, Update on drift,
// Unchanged when the declared spec already matches the server.
//
// Relationship to kind: CrewTemplate
// ----------------------------------
//
// Both kinds end up writing rows to the same `crews` table. The
// distinction is provenance + lifecycle:
//
//   - kind: CrewTemplate is "deploy X under name Y" — semantics owned
//     by POST /api/v1/crew-templates/{slug}/deploy. The manifest
//     entry can't author the template body itself.
//   - kind: Crew owns every field of the crew row directly. Operators
//     reach for this when they need to express a crew's runtime
//     image, devcontainer features, mise toolchain, or sidecar
//     services in the same declarative bundle as their projects /
//     labels / routines.
//
// An exported workspace round-trips through `kind: Crew` for crews
// whose slug doesn't match a known template (the common case — most
// operators rename on deploy). `kind: CrewTemplate` only catches the
// heuristic match.
//
// Server endpoints
// ----------------
//
//   - POST   /api/v1/crews              — Create (h.Create in crews_create.go)
//   - PATCH  /api/v1/crews/{crewId}     — Update (h.Update in crews_update.go)
//   - DELETE /api/v1/crews/{crewId}     — Delete (h.Delete in crews_query.go)
//   - GET    /api/v1/crews              — List   (h.List   in crews_query.go)
//
// devcontainer_config column shape
// --------------------------------
//
// The server stores devcontainer config as an OPAQUE JSON string in
// the `devcontainer_config` column. The Create/Update handlers run it
// through devcontainer.ParseBytes (so it must be valid devcontainer.json)
// and auto-inject the common-utils feature on Create when missing —
// but they do NOT enforce any specific top-level schema. The manifest
// layer therefore models the SUBSET of fields operators commonly tweak
// (image, features, env, memory_mb, cpus, post_create_command) as
// first-class YAML fields, and offers a `raw:` escape hatch that
// passes through verbatim.
//
// We assemble the JSON string at Plan time. Round-tripping through
// Export decodes the string back into the typed sub-fields where
// possible and stashes anything else into `raw:` so a future re-apply
// is byte-stable.
//
// mise_config column shape
// ------------------------
//
// Similar story: the server stores `mise_config` as an opaque string
// validated by devcontainer.ParseMiseConfig. The manifest models the
// common `tools: { node: "22" }` map; anything else passes through via
// a passthrough map.
//
// services_json column shape
// --------------------------
//
// Sidecar services serialise to a JSON array of {name, image, env,
// env_refs, ports, volumes, healthcheck} objects. The shape mirrors
// internal/api/crew_services.go's serviceWire — kept in sync because
// the server re-validates it at every write.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Constants & shared regex ──────────────────────────────────────────────

const (
	crewAPIVersion = "crewship/v1"
	crewKind       = "Crew"
)

// crewHexColorRe enforces the 6-digit `#RRGGBB` form. The server
// itself accepts any string (the `color` column is `TEXT` with no
// CHECK), but the UI rendering pipeline assumes hex — so we reject
// anything else here rather than silently shipping a broken render
// to the workspace. Note: the existing seeded crews use bare hex
// (e.g. "#3B82F6"), but the example yamls accept palette names
// like "blue" / "green". We tolerate BOTH: any string that ISN'T a
// hex code is treated as a palette token and passed through verbatim
// (the server-side renderer maps it). The hex regex is only used for
// the "looks like hex but isn't valid hex" rejection.
var crewHexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// crewServiceNameRe mirrors api.serviceNameRe (RFC 1035 DNS label).
// Duplicated here rather than imported to keep the kinds package
// free of a dependency on package api (cycle risk via license /
// orchestrator). The server re-validates on every write so any drift
// between the two regexes is caught at apply time — we just want the
// manifest layer to fail fast on the common typos.
var crewServiceNameRe = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// crewServicePortRe accepts "5432" or "5432/tcp" or "5432/udp".
// Anything else (host:container mappings like "8080:80") is rejected:
// the docker provider runs services on a crew-private bridge network
// and does NOT publish to the host, so a host port number in the
// manifest is almost always a misunderstanding.
var crewServicePortRe = regexp.MustCompile(`^\d{1,5}(?:/(?:tcp|udp))?$`)

// ── Types ────────────────────────────────────────────────────────────────

// CrewSpec is the shape under `spec:` for kind: Crew. Most fields are
// optional; Validate() enforces format + cross-references and Plan()
// emits patches only for fields the user actually declared (so an
// unset Color won't overwrite a color the user picked via the UI).
type CrewSpec struct {
	// Description is mirrored to crews.description. Empty = leave
	// server value alone on update.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Icon is a lucide-react slug (e.g. "terminal", "shield-check").
	// The UI renders by name; the server stores verbatim.
	Icon string `yaml:"icon,omitempty" json:"icon,omitempty"`

	// Color accepts a hex code (`#RRGGBB`) or a palette token
	// ("blue" / "green") for parity with the existing example yamls.
	Color string `yaml:"color,omitempty" json:"color,omitempty"`

	// RuntimeImage maps to crews.runtime_image — the base image the
	// devcontainer build extends. The server validates existence via
	// devcontainer.ValidateImageExists; we don't replicate that check
	// here because it requires network access (Validate is offline).
	RuntimeImage string `yaml:"runtime_image,omitempty" json:"runtime_image,omitempty"`

	// Devcontainer is the manifest's view of the devcontainer.json
	// document. Nil means "no devcontainer overlay" (column stays
	// NULL on Create). Non-nil means "render this to a JSON string
	// and store it in devcontainer_config".
	Devcontainer *Devcontainer `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`

	// Mise is the manifest's view of the .mise.toml document
	// (serialised as JSON in the mise_config column). Nil means "no
	// mise overlay".
	Mise *MiseConfig `yaml:"mise,omitempty" json:"mise,omitempty"`

	// Services is the optional sidecar list. Each entry becomes one
	// element of the services_json array on the crew row. Empty
	// slice = no sidecars (column nulled on update, omitted on create).
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
}

// Devcontainer models the subset of devcontainer.json fields the
// manifest layer surfaces as first-class YAML. The `Raw` field is
// the passthrough escape hatch: any key not modeled here is merged
// verbatim into the output JSON, with the typed fields winning on
// collision. That lets operators set, say, "remoteUser" or
// "containerUser" without us adding a Go field for every devcontainer
// spec option.
type Devcontainer struct {
	// Image is the base devcontainer image. NOTE: the spec-level
	// `runtime_image` is the SAME thing — when both are present we
	// prefer spec.runtime_image (the more visible field) and warn
	// at Validate time. The duplication exists because the example
	// yamls historically nested `image:` under `devcontainer:`,
	// whereas the task spec promotes it to `runtime_image:` at the
	// spec level for parity with the API column name.
	Image string `yaml:"image,omitempty" json:"image,omitempty"`

	// Features maps feature-id → feature-config. The id is the
	// canonical OCI ref (e.g. "ghcr.io/devcontainers/features/common-utils:2").
	// Values are arbitrary JSON; the devcontainer build CLI parses them.
	Features map[string]any `yaml:"features,omitempty" json:"features,omitempty"`

	// Env maps env-var → value, injected into the container's
	// process environment. Distinct from credentials (which are
	// resolved by the Keeper at runtime); this is for static config
	// like PATH overrides or service URLs.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// MemoryMB / CPUs are emitted into the devcontainer JSON as the
	// `hostRequirements` block (`memory: "4096mb"`, `cpus: 2`). The
	// docker provider reads those when sizing the container.
	// Distinct from the spec's `runtime_image` because the column on
	// the crew row stores container_memory_mb / container_cpus
	// SEPARATELY — but the docker provider can also pull from the
	// devcontainer JSON. We model under devcontainer here to match
	// the existing example yamls; future work can promote these to
	// spec-level fields if operators commonly tune them per-crew.
	MemoryMB int     `yaml:"memory_mb,omitempty" json:"memory_mb,omitempty"`
	CPUs     float64 `yaml:"cpus,omitempty"      json:"cpus,omitempty"`

	// PostCreateCommand is the shell snippet executed once after the
	// container is first built. Same semantics as the devcontainer
	// spec's `postCreateCommand`.
	PostCreateCommand string `yaml:"post_create_command,omitempty" json:"post_create_command,omitempty"`

	// Raw is the passthrough escape hatch: any key not modeled
	// above (e.g. "remoteUser", "customizations", "forwardPorts")
	// goes here verbatim. Merged into the output JSON with typed
	// fields WINNING on key collision — so a Raw key named "image"
	// is silently overridden by the typed Image field. We do not
	// document Raw collisions as an error because operators
	// occasionally export-edit-reapply and the round-trip should
	// not produce spurious failures.
	Raw map[string]any `yaml:"raw,omitempty" json:"raw,omitempty"`
}

// MiseConfig models the .mise.toml document. The server stores it as
// a JSON string; mise itself reads TOML, but devcontainer.ParseMiseConfig
// accepts both forms. We emit JSON for round-trip stability.
type MiseConfig struct {
	// Tools maps tool name → version pin (e.g. "node" → "22", "python" → "3.12").
	Tools map[string]string `yaml:"tools,omitempty" json:"tools,omitempty"`

	// Raw is the passthrough for any mise config block we don't
	// model (e.g. `env`, `tasks`). Same merge semantics as
	// Devcontainer.Raw.
	Raw map[string]any `yaml:"raw,omitempty" json:"raw,omitempty"`
}

// Service models one sidecar container on the crew's bridge network.
// The wire shape MUST match api.serviceWire — see internal/api/crew_services.go.
type Service struct {
	Name    string            `yaml:"name"                     json:"name"`
	Image   string            `yaml:"image"                    json:"image"`
	Command []string          `yaml:"command,omitempty"        json:"command,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"            json:"env,omitempty"`
	// EnvRefs is the list of credential env-var names the Keeper
	// should resolve and inject at container start. The credentials
	// themselves live on the workspace (kind: Credential or
	// embedded under kind: Crew's credentials block); this list is
	// purely a reference.
	EnvRefs     []string     `yaml:"env_refs,omitempty"   json:"env_refs,omitempty"`
	Ports       []string     `yaml:"ports,omitempty"      json:"ports,omitempty"`
	Volumes     []Volume     `yaml:"volumes,omitempty"    json:"volumes,omitempty"`
	Healthcheck *Healthcheck `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
}

// Volume is a named docker volume mounted into the sidecar. Bind
// mounts (host paths) are intentionally not supported — the docker
// provider rejects them at runtime, so the manifest fails fast.
type Volume struct {
	Name  string `yaml:"name"  json:"name"`
	Mount string `yaml:"mount" json:"mount"`
}

// Healthcheck mirrors docker's healthcheck shape. Duration fields
// accept Go duration strings ("5s", "1m"); validation runs them
// through time.ParseDuration so typos like "5sec" fail at write time.
type Healthcheck struct {
	Test        []string `yaml:"test"                    json:"test"`
	Interval    string   `yaml:"interval,omitempty"      json:"interval,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty"       json:"timeout,omitempty"`
	Retries     int      `yaml:"retries,omitempty"       json:"retries,omitempty"`
	StartPeriod string   `yaml:"start_period,omitempty"  json:"start_period,omitempty"`
}

// CrewDocument is the YAML envelope produced/consumed by the manifest
// pipeline for kind: Crew.
type CrewDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       CrewSpec             `yaml:"spec"       json:"spec"`
}

// CrewRemote captures the slice of GET /api/v1/crews each row produces.
// Only fields the manifest layer is the source of truth for are kept;
// derived columns (cached_image, config_hash, _count) and per-crew
// runtime state (created_at, updated_at, max_ephemeral_agents) stay
// off the diff to avoid update churn.
//
// All "config string" columns are stored as pointers so we can tell
// "server has no value" from "server has empty value" — the difference
// matters for the diff (a manifest that omits `mise` should not
// overwrite a populated mise_config column).
type CrewRemote struct {
	ID                 string  `json:"id"`
	WorkspaceID        string  `json:"workspace_id"`
	Name               string  `json:"name"`
	Slug               string  `json:"slug"`
	Description        *string `json:"description"`
	Icon               *string `json:"icon"`
	Color              *string `json:"color"`
	RuntimeImage       *string `json:"runtime_image"`
	DevcontainerConfig *string `json:"devcontainer_config"`
	MiseConfig         *string `json:"mise_config"`
	ServicesJSON       *string `json:"services_json"`
	// ContainerMemoryMB / ContainerCPUs mirror the row columns the
	// docker provider reads at run time. Required for diff in
	// updatePatch — without them, every crew with declared
	// devcontainer sizing stayed in ActionUpdate forever and
	// emitted no-op PATCHes after convergence.
	ContainerMemoryMB int     `json:"container_memory_mb"`
	ContainerCPUs     float64 `json:"container_cpus"`
}

// ── Validate ─────────────────────────────────────────────────────────────

// Validate enforces structural rules without any HTTP round-trip:
//
//   - apiVersion must equal "crewship/v1"
//   - kind must equal "Crew"
//   - metadata.name + metadata.slug REQUIRED
//   - slug must be kebab-case (matches server's validSlugFormat)
//   - color, if present and starts with "#", must be valid hex
//   - runtime_image must be present (no sane default exists; the
//     server allows NULL but the docker provider falls back to a
//     hardcoded image that's almost certainly wrong for the operator's
//     intent — better to fail fast than ship a wrong-image crew)
//   - service names must be DNS labels, unique within the crew
//   - service ports must be numeric (no host:container mappings)
//   - service healthcheck durations must parse via time.ParseDuration
//   - service volume names must NOT look like paths (named vols only)
//
// We deliberately do NOT round-trip the assembled devcontainer JSON
// through devcontainer.ParseBytes here because Validate runs offline
// and the parser pulls in heavyweight dependencies. The server
// re-validates on every write so any drift surfaces at Apply time.
func (d *CrewDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != crewAPIVersion {
		return fmt.Errorf("crew %q: apiVersion %q must be %q",
			d.Metadata.Slug, d.APIVersion, crewAPIVersion)
	}
	if d.Kind != "" && d.Kind != crewKind {
		return fmt.Errorf("crew %q: kind %q must be %q",
			d.Metadata.Slug, d.Kind, crewKind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("crew %q: metadata.name is required", d.Metadata.Slug)
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("crew: metadata.slug is required")
	}
	if !crewSlugPattern.MatchString(d.Metadata.Slug) {
		return fmt.Errorf("crew %q: metadata.slug must be kebab-case (lowercase letters, digits, hyphens; no leading/trailing hyphen)",
			d.Metadata.Slug)
	}

	// Hex color: only validate strings that LOOK like hex (start
	// with '#'). Palette tokens ("blue", "green") pass through
	// unchecked — the UI maps them.
	if strings.HasPrefix(d.Spec.Color, "#") && !crewHexColorRe.MatchString(d.Spec.Color) {
		return fmt.Errorf("crew %q: color %q starts with '#' but is not a valid 6-digit hex code (#RRGGBB)",
			d.Metadata.Slug, d.Spec.Color)
	}

	// Runtime image is required. The server tolerates NULL (falls
	// back to a built-in default), but a manifest-authored crew
	// almost certainly wants an explicit image — otherwise the
	// operator can't reason about what's in the container. Fail
	// loudly rather than ship a surprise default.
	if strings.TrimSpace(d.Spec.RuntimeImage) == "" {
		return fmt.Errorf("crew %q: spec.runtime_image is required (no sane default; pick e.g. mcr.microsoft.com/devcontainers/javascript-node:22-bookworm)",
			d.Metadata.Slug)
	}

	// Cross-check the legacy `devcontainer.image` against spec.runtime_image.
	// We don't reject mismatch outright — the spec-level field wins —
	// but a divergent value is almost always a paste mistake.
	if d.Spec.Devcontainer != nil && d.Spec.Devcontainer.Image != "" &&
		d.Spec.Devcontainer.Image != d.Spec.RuntimeImage {
		return fmt.Errorf("crew %q: spec.runtime_image %q does not match spec.devcontainer.image %q (set one only)",
			d.Metadata.Slug, d.Spec.RuntimeImage, d.Spec.Devcontainer.Image)
	}

	if d.Spec.Devcontainer != nil {
		if d.Spec.Devcontainer.MemoryMB < 0 {
			return fmt.Errorf("crew %q: spec.devcontainer.memory_mb must be non-negative", d.Metadata.Slug)
		}
		if d.Spec.Devcontainer.CPUs < 0 {
			return fmt.Errorf("crew %q: spec.devcontainer.cpus must be non-negative", d.Metadata.Slug)
		}
	}

	// Services: shape check + duplicate detection. Loop is shared
	// with the per-service validator helper so the failure messages
	// stay keyed to the service slug, not a numeric index.
	if err := validateCrewServices(d.Metadata.Slug, d.Spec.Services); err != nil {
		return err
	}
	return nil
}

// validateCrewServices runs the per-service shape checks: DNS label
// name, unique names within the crew, image present, numeric ports,
// parseable healthcheck durations, named (not bind) volumes.
//
// Extracted from Validate so the test surface can target it
// independently — the test file builds a CrewDocument and asserts
// on validateCrewServices behaviour without going through the full
// Validate path each time.
func validateCrewServices(crewSlug string, services []Service) error {
	if len(services) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for i, s := range services {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("crew %q: spec.services[%d].name is required", crewSlug, i)
		}
		if !crewServiceNameRe.MatchString(s.Name) {
			return fmt.Errorf("crew %q: spec.services[%q].name must be a DNS label (lowercase letters, digits, hyphens; start with letter, end with letter or digit; ≤63 chars)",
				crewSlug, s.Name)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("crew %q: spec.services[%q] is declared more than once",
				crewSlug, s.Name)
		}
		seen[s.Name] = struct{}{}
		if strings.TrimSpace(s.Image) == "" {
			return fmt.Errorf("crew %q: spec.services[%q].image is required",
				crewSlug, s.Name)
		}
		for j, p := range s.Ports {
			if !crewServicePortRe.MatchString(p) {
				return fmt.Errorf("crew %q: spec.services[%q].ports[%d] %q must be a numeric port (e.g. \"5432\" or \"5432/tcp\"); host:container mappings are not supported on the crew network",
					crewSlug, s.Name, j, p)
			}
		}
		seenMount := map[string]struct{}{}
		for j, v := range s.Volumes {
			if strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Mount) == "" {
				return fmt.Errorf("crew %q: spec.services[%q].volumes[%d] requires both name and mount",
					crewSlug, s.Name, j)
			}
			if strings.HasPrefix(v.Name, "/") || strings.HasPrefix(v.Name, ".") {
				return fmt.Errorf("crew %q: spec.services[%q].volumes[%q] looks like a path; bind mounts are not supported, use a named volume",
					crewSlug, s.Name, v.Name)
			}
			if _, dup := seenMount[v.Mount]; dup {
				return fmt.Errorf("crew %q: spec.services[%q] mounts %q more than once",
					crewSlug, s.Name, v.Mount)
			}
			seenMount[v.Mount] = struct{}{}
		}
		if s.Healthcheck != nil {
			if len(s.Healthcheck.Test) == 0 {
				return fmt.Errorf("crew %q: spec.services[%q].healthcheck declared without test command",
					crewSlug, s.Name)
			}
			// Parse-validate duration strings up front so a typo
			// ("5sec" instead of "5s") fails at write time. The
			// server runs the same check; duplicating here keeps
			// the failure local + clearly attributable.
			for field, value := range map[string]string{
				"interval":     s.Healthcheck.Interval,
				"timeout":      s.Healthcheck.Timeout,
				"start_period": s.Healthcheck.StartPeriod,
			} {
				if value == "" {
					continue
				}
				if _, err := time.ParseDuration(value); err != nil {
					return fmt.Errorf("crew %q: spec.services[%q].healthcheck.%s %q is not a valid Go duration: %w",
						crewSlug, s.Name, field, value, err)
				}
			}
			if s.Healthcheck.Retries < 0 {
				return fmt.Errorf("crew %q: spec.services[%q].healthcheck.retries must be non-negative",
					crewSlug, s.Name)
			}
		}
	}
	return nil
}

// ── Plan ─────────────────────────────────────────────────────────────────

// Plan compares the declared crew against `remote` (nil = not yet on
// server) and returns a single PlanItem: Create, Update, or Unchanged.
//
// Drift detection compares each field the manifest authoritatively
// owns; fields the manifest doesn't declare (empty Devcontainer/Mise/
// Services + empty scalars) are excluded from the diff so the
// manifest never overwrites a value the operator set via the UI.
//
// For Update, we emit the FULL desired body (not a sparse patch).
// The crew PATCH endpoint treats nil fields as "leave alone" and
// non-nil as "overwrite" — matching how we want the manifest to
// behave for fields it declares.
func (d *CrewDocument) Plan(_ context.Context, _ internalapi.Client, remote *CrewRemote) ([]internalapi.PlanItem, error) {
	if remote == nil {
		body, err := d.createBody()
		if err != nil {
			return nil, fmt.Errorf("crew %q: assemble create body: %w", d.Metadata.Slug, err)
		}
		return []internalapi.PlanItem{{
			Kind:        "crew",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create crew %q (image=%s, %d services)", d.Metadata.Slug, d.Spec.RuntimeImage, len(d.Spec.Services)),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Post(ctx, "/api/v1/crews", body)
				if err != nil {
					return fmt.Errorf("POST /api/v1/crews: %w", err)
				}
				return checkStatus(resp, "create crew "+d.Metadata.Slug)
			},
		}}, nil
	}

	patch, err := d.updatePatch(remote)
	if err != nil {
		return nil, fmt.Errorf("crew %q: build update patch: %w", d.Metadata.Slug, err)
	}
	if len(patch) == 0 {
		return []internalapi.PlanItem{{
			Kind:        "crew",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("crew %q already matches manifest", d.Metadata.Slug),
		}}, nil
	}

	crewID := remote.ID
	return []internalapi.PlanItem{{
		Kind:        "crew",
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update crew %q (%d field(s))", d.Metadata.Slug, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			resp, err := c.Patch(ctx, "/api/v1/crews/"+crewID, patch)
			if err != nil {
				return fmt.Errorf("PATCH /api/v1/crews/%s: %w", crewID, err)
			}
			return checkStatus(resp, "update crew "+d.Metadata.Slug)
		},
	}}, nil
}

// ── Body builders ────────────────────────────────────────────────────────

// createBody assembles the JSON body for POST /api/v1/crews. Mirrors
// createCrewRequest in internal/api/crews_create.go field-for-field
// for the columns the manifest authoritatively owns.
//
// The devcontainer / mise / services configs are JSON-serialised
// here (not when the wire body is later marshaled) so a serialisation
// failure surfaces in Plan rather than deep inside the HTTP client.
func (d *CrewDocument) createBody() (map[string]any, error) {
	body := map[string]any{
		"name":          d.Metadata.Name,
		"slug":          d.Metadata.Slug,
		"runtime_image": d.Spec.RuntimeImage,
	}
	if d.Metadata.Description != "" {
		body["description"] = d.Metadata.Description
	} else if d.Spec.Description != "" {
		// Tolerate description in either place; spec.description
		// wins when both are set (metadata.description is the older
		// convention). Either way ends up in the same column.
		body["description"] = d.Spec.Description
	}
	if d.Spec.Color != "" {
		body["color"] = d.Spec.Color
	}
	if d.Spec.Icon != "" {
		body["icon"] = d.Spec.Icon
	}
	if d.Spec.Devcontainer != nil {
		cfg, err := d.devcontainerJSON()
		if err != nil {
			return nil, err
		}
		if cfg != "" {
			body["devcontainer_config"] = cfg
		}
	}
	if d.Spec.Mise != nil {
		cfg, err := d.miseJSON()
		if err != nil {
			return nil, err
		}
		if cfg != "" {
			body["mise_config"] = cfg
		}
	}
	// Services: distinguish nil (omit on create → column NULL) from
	// empty-but-declared (services: [] → store "[]" so the next
	// updatePatch sees a clear empty and doesn't re-emit "[]" as
	// a phantom change). Pre-fix only the non-empty list landed.
	if d.Spec.Services != nil {
		if len(d.Spec.Services) == 0 {
			body["services_json"] = "[]"
		} else {
			cfg, err := d.servicesJSON()
			if err != nil {
				return nil, err
			}
			body["services_json"] = cfg
		}
	}
	// memory_mb / cpus on the devcontainer block ALSO feed the crew
	// row columns (container_memory_mb / container_cpus). The server
	// stores both — the devcontainer JSON for the build, the columns
	// for docker run --memory / --cpus. Forwarding them as siblings
	// keeps both sources in sync.
	if d.Spec.Devcontainer != nil && d.Spec.Devcontainer.MemoryMB > 0 {
		body["container_memory_mb"] = d.Spec.Devcontainer.MemoryMB
	}
	if d.Spec.Devcontainer != nil && d.Spec.Devcontainer.CPUs > 0 {
		body["container_cpus"] = d.Spec.Devcontainer.CPUs
	}
	return body, nil
}

// updatePatch returns ONLY the fields whose declared value differs
// from `remote`. Empty declared fields are skipped — leaving the
// server value alone is the desired behaviour for "manifest doesn't
// mention this field".
//
// Returns an empty map when nothing changed; Plan turns that into an
// Unchanged item.
func (d *CrewDocument) updatePatch(remote *CrewRemote) (map[string]any, error) {
	patch := map[string]any{}

	if d.Metadata.Name != "" && d.Metadata.Name != remote.Name {
		patch["name"] = d.Metadata.Name
	}
	// description: prefer metadata.description, fall back to spec.description.
	desiredDesc := d.Metadata.Description
	if desiredDesc == "" {
		desiredDesc = d.Spec.Description
	}
	if desiredDesc != "" && desiredDesc != deref(remote.Description) {
		patch["description"] = desiredDesc
	}
	if d.Spec.Color != "" && d.Spec.Color != deref(remote.Color) {
		patch["color"] = d.Spec.Color
	}
	if d.Spec.Icon != "" && d.Spec.Icon != deref(remote.Icon) {
		patch["icon"] = d.Spec.Icon
	}
	if d.Spec.RuntimeImage != "" && d.Spec.RuntimeImage != deref(remote.RuntimeImage) {
		patch["runtime_image"] = d.Spec.RuntimeImage
	}

	if d.Spec.Devcontainer != nil {
		want, err := d.devcontainerJSON()
		if err != nil {
			return nil, err
		}
		if want != "" && !jsonStringEqual(want, deref(remote.DevcontainerConfig)) {
			patch["devcontainer_config"] = want
		}
		// Diff container sizing against the remote columns so a
		// converged manifest stops emitting no-op patches. A value
		// of 0 in the manifest means "operator didn't declare a
		// limit" — leave the server value alone. Otherwise patch
		// only when it actually changed.
		if d.Spec.Devcontainer.MemoryMB > 0 && d.Spec.Devcontainer.MemoryMB != remote.ContainerMemoryMB {
			patch["container_memory_mb"] = d.Spec.Devcontainer.MemoryMB
		}
		if d.Spec.Devcontainer.CPUs > 0 && d.Spec.Devcontainer.CPUs != remote.ContainerCPUs {
			patch["container_cpus"] = d.Spec.Devcontainer.CPUs
		}
	}
	if d.Spec.Mise != nil {
		want, err := d.miseJSON()
		if err != nil {
			return nil, err
		}
		if want != "" && !jsonStringEqual(want, deref(remote.MiseConfig)) {
			patch["mise_config"] = want
		}
	}
	// Services: distinguish nil (field absent → leave alone) from
	// empty-but-declared (services: [] → clear remote sidecars).
	// Pre-fix the `len > 0` guard skipped both cases, so a manifest
	// that deleted every service left the old ones running.
	if d.Spec.Services != nil {
		if len(d.Spec.Services) == 0 {
			// Empty array clears the column. server's update handler
			// stores the literal "[]" string back; matching that
			// here avoids re-emitting the patch on the next apply.
			if !jsonStringEqual("[]", deref(remote.ServicesJSON)) {
				patch["services_json"] = "[]"
			}
		} else {
			want, err := d.servicesJSON()
			if err != nil {
				return nil, err
			}
			if !jsonStringEqual(want, deref(remote.ServicesJSON)) {
				patch["services_json"] = want
			}
		}
	}
	return patch, nil
}

// devcontainerJSON renders the typed Devcontainer struct (plus the
// passthrough Raw map) into a single JSON string suitable for the
// devcontainer_config column.
//
// Merge order: start with Raw, then overlay typed fields. Typed
// fields WIN on key collision so a Raw entry can't shadow `image`.
// Returns "" when the resulting object is empty (no meaningful
// content to store).
func (d *CrewDocument) devcontainerJSON() (string, error) {
	if d.Spec.Devcontainer == nil {
		return "", nil
	}
	out := map[string]any{}
	for k, v := range d.Spec.Devcontainer.Raw {
		out[k] = v
	}
	// spec.runtime_image is the canonical image source; if the
	// devcontainer block separately specifies `image`, the spec
	// version still wins (Validate already enforced equality when
	// both are set).
	if d.Spec.RuntimeImage != "" {
		out["image"] = d.Spec.RuntimeImage
	} else if d.Spec.Devcontainer.Image != "" {
		out["image"] = d.Spec.Devcontainer.Image
	}
	if len(d.Spec.Devcontainer.Features) > 0 {
		out["features"] = d.Spec.Devcontainer.Features
	}
	if len(d.Spec.Devcontainer.Env) > 0 {
		// devcontainer.json uses `containerEnv` for the static-env
		// block; mirror that key so the server's devcontainer.ParseBytes
		// accepts the document.
		out["containerEnv"] = d.Spec.Devcontainer.Env
	}
	if d.Spec.Devcontainer.PostCreateCommand != "" {
		out["postCreateCommand"] = d.Spec.Devcontainer.PostCreateCommand
	}
	if d.Spec.Devcontainer.MemoryMB > 0 || d.Spec.Devcontainer.CPUs > 0 {
		hr := map[string]any{}
		if d.Spec.Devcontainer.MemoryMB > 0 {
			// devcontainer spec wants strings like "4096mb".
			hr["memory"] = fmt.Sprintf("%dmb", d.Spec.Devcontainer.MemoryMB)
		}
		if d.Spec.Devcontainer.CPUs > 0 {
			hr["cpus"] = d.Spec.Devcontainer.CPUs
		}
		out["hostRequirements"] = hr
	}
	if len(out) == 0 {
		return "", nil
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal devcontainer config: %w", err)
	}
	return string(buf), nil
}

// miseJSON renders the typed MiseConfig (plus Raw passthrough) into
// a JSON string. devcontainer.ParseMiseConfig on the server accepts
// either TOML or JSON; we always emit JSON for round-trip stability.
func (d *CrewDocument) miseJSON() (string, error) {
	if d.Spec.Mise == nil {
		return "", nil
	}
	out := map[string]any{}
	for k, v := range d.Spec.Mise.Raw {
		out[k] = v
	}
	if len(d.Spec.Mise.Tools) > 0 {
		out["tools"] = d.Spec.Mise.Tools
	}
	if len(out) == 0 {
		return "", nil
	}
	buf, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal mise config: %w", err)
	}
	return string(buf), nil
}

// servicesJSON renders the typed Service slice into the array-of-
// objects shape the services_json column stores. Sorted by service
// name so round-trips are byte-stable (the YAML order is operator-
// chosen but the server's re-emission order is not guaranteed).
func (d *CrewDocument) servicesJSON() (string, error) {
	if len(d.Spec.Services) == 0 {
		return "", nil
	}
	// Copy + sort so we don't mutate the document's services slice.
	cp := make([]Service, len(d.Spec.Services))
	copy(cp, d.Spec.Services)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })
	buf, err := json.Marshal(cp)
	if err != nil {
		return "", fmt.Errorf("marshal services: %w", err)
	}
	return string(buf), nil
}

// ── Remote lookup ────────────────────────────────────────────────────────

// LookupCrewRemoteBySlug fetches the workspace's crew list and
// returns the matching row (or nil when no crew has that slug — Plan
// treats nil as "create").
//
// Cost: one GET /api/v1/crews per call. The list is workspace-scoped
// and small (most workspaces have under a few dozen crews) so we
// don't paginate. Apply callers that look up many crews in a row
// should cache the list themselves — this helper is intentionally
// independent so a kind can call it without coordination.
func LookupCrewRemoteBySlug(ctx context.Context, c internalapi.Client, slug string) (*CrewRemote, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("crew slug is required")
	}
	rows, err := listCrewRemotes(ctx, c)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Slug == slug {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// listCrewRemotes pulls /api/v1/crews and decodes the FULL crew
// shape the manifest cares about. Distinct from crew_template.go's
// listCrews (which only needs id/name/slug) because Plan needs to
// diff every config column.
func listCrewRemotes(ctx context.Context, c internalapi.Client) ([]CrewRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/crews")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/crews: %w", err)
	}
	if err := checkStatus(resp, "list crews"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/crews body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	// Try flat array first; tolerate {crews: [...]} wrapper for
	// forward compatibility with a future paginated list response.
	var flat []CrewRemote
	if err := json.Unmarshal(body, &flat); err == nil {
		return flat, nil
	}
	var wrapped struct {
		Crews []CrewRemote `json:"crews"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode /api/v1/crews: %w", err)
	}
	return wrapped.Crews, nil
}

// ── Export ───────────────────────────────────────────────────────────────

// ExportCrews fetches every crew in the workspace and re-assembles
// each as a CrewDocument suitable for re-applying. The inverse of
// Plan/Create — fields the manifest doesn't model (cached_image,
// config_hash, container_ttl_hours, network_mode, …) are dropped.
//
// Lossy round-trip
// ----------------
//
// The devcontainer JSON is decoded back into the typed Devcontainer
// struct WHERE POSSIBLE and into Raw for unknown keys. Operators who
// stored exotic devcontainer.json shapes will see them round-trip
// via Raw, not via typed YAML fields. That's by design: typed fields
// stay small and obvious; Raw absorbs the long tail.
//
// Sorted by slug so snapshot diffs are stable across runs.
func ExportCrews(ctx context.Context, c internalapi.Client) ([]*CrewDocument, error) {
	rows, err := listCrewRemotes(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export crews: %w", err)
	}
	out := make([]*CrewDocument, 0, len(rows))
	for _, r := range rows {
		doc := &CrewDocument{
			APIVersion: crewAPIVersion,
			Kind:       crewKind,
			Metadata: internalapi.Metadata{
				Name: r.Name,
				Slug: r.Slug,
			},
			Spec: CrewSpec{
				Icon:         deref(r.Icon),
				Color:        deref(r.Color),
				RuntimeImage: deref(r.RuntimeImage),
			},
		}
		if r.Description != nil && *r.Description != "" {
			doc.Metadata.Description = *r.Description
		}
		if r.DevcontainerConfig != nil && *r.DevcontainerConfig != "" {
			if dc, err := parseDevcontainerJSON(*r.DevcontainerConfig); err == nil && dc != nil {
				doc.Spec.Devcontainer = dc
			}
			// Decode error: silently skip — exporting half a config
			// is worse than dropping it. The server's source row is
			// untouched; operators re-export after fixing.
		}
		if r.MiseConfig != nil && *r.MiseConfig != "" {
			if mc, err := parseMiseConfigJSON(*r.MiseConfig); err == nil && mc != nil {
				doc.Spec.Mise = mc
			}
		}
		if r.ServicesJSON != nil && *r.ServicesJSON != "" {
			var svcs []Service
			if err := json.Unmarshal([]byte(*r.ServicesJSON), &svcs); err == nil {
				doc.Spec.Services = svcs
			}
		}
		out = append(out, doc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

// parseDevcontainerJSON decodes the column's JSON string back into
// a Devcontainer. Unknown keys land in Raw so a round-trip is
// byte-stable. Returns (nil, nil) for an empty input.
func parseDevcontainerJSON(s string) (*Devcontainer, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("decode devcontainer_config: %w", err)
	}
	out := &Devcontainer{Raw: map[string]any{}}
	// Pull each known key into the typed field, deleting from raw.
	if v, ok := raw["image"].(string); ok {
		out.Image = v
		delete(raw, "image")
	}
	if v, ok := raw["features"].(map[string]any); ok {
		out.Features = v
		delete(raw, "features")
	}
	if v, ok := raw["containerEnv"].(map[string]any); ok {
		out.Env = map[string]string{}
		for k, val := range v {
			if s, ok := val.(string); ok {
				out.Env[k] = s
			}
		}
		delete(raw, "containerEnv")
	}
	if v, ok := raw["postCreateCommand"].(string); ok {
		out.PostCreateCommand = v
		delete(raw, "postCreateCommand")
	}
	if hr, ok := raw["hostRequirements"].(map[string]any); ok {
		if m, ok := hr["memory"].(string); ok {
			// "4096mb" → 4096. Tolerate trailing "mb" / "MB"; fall
			// back to 0 (omitted) on anything else.
			trimmed := strings.TrimSuffix(strings.TrimSuffix(m, "mb"), "MB")
			var n int
			_, _ = fmt.Sscanf(trimmed, "%d", &n)
			out.MemoryMB = n
		}
		if c, ok := hr["cpus"].(float64); ok {
			out.CPUs = c
		}
		delete(raw, "hostRequirements")
	}
	if len(raw) > 0 {
		out.Raw = raw
	} else {
		out.Raw = nil
	}
	// If nothing meaningful landed in the typed fields and Raw is
	// also empty, return nil rather than an empty struct — keeps
	// the exported YAML uncluttered.
	if out.Image == "" && len(out.Features) == 0 && len(out.Env) == 0 &&
		out.PostCreateCommand == "" && out.MemoryMB == 0 && out.CPUs == 0 &&
		len(out.Raw) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseMiseConfigJSON decodes the mise_config column back into a
// MiseConfig. Same Raw-stash strategy as parseDevcontainerJSON.
func parseMiseConfigJSON(s string) (*MiseConfig, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("decode mise_config: %w", err)
	}
	out := &MiseConfig{Raw: map[string]any{}}
	if v, ok := raw["tools"].(map[string]any); ok {
		out.Tools = map[string]string{}
		for k, val := range v {
			if s, ok := val.(string); ok {
				out.Tools[k] = s
			}
		}
		delete(raw, "tools")
	}
	if len(raw) > 0 {
		out.Raw = raw
	} else {
		out.Raw = nil
	}
	if len(out.Tools) == 0 && len(out.Raw) == 0 {
		return nil, nil
	}
	return out, nil
}

// ── Small helpers ────────────────────────────────────────────────────────

// deref returns the pointee or "" for nil. Used heavily in the
// update-patch path where remote-side fields are pointers (so we
// can distinguish "no value" from "empty value") but the diff only
// cares about the string form.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// jsonStringEqual normalises two JSON strings (parsing + re-marshaling)
// before comparing. Catches whitespace differences and key-order
// drift that would otherwise produce false "drifted" verdicts on
// every Plan against a stable backend.
func jsonStringEqual(a, b string) bool {
	if a == b {
		return true
	}
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		return false
	}
	ab, err := json.Marshal(av)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(bv)
	if err != nil {
		return false
	}
	return string(ab) == string(bb)
}
