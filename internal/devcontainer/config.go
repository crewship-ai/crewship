package devcontainer

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Typed errors for validation failures.
var (
	ErrEmptyImage        = errors.New("devcontainer: image is required")
	ErrInvalidImage      = errors.New("devcontainer: invalid image reference")
	ErrInvalidFeatureRef = errors.New("devcontainer: invalid feature reference")
	ErrUnsupportedField  = errors.New("devcontainer: unsupported field")

	// Security-control validation errors (#1380). The save path (crews
	// create/update) maps ErrPrivilegedNotAllowed to 403 and the cap/mount
	// errors to 400 so a client learns *why* a privileged/cap/mount config
	// was refused rather than seeing it silently accepted-and-discarded.
	ErrPrivilegedNotAllowed = errors.New("devcontainer: privileged mode requires the workspace allow_privileged_credentials flag")
	ErrCapabilityNotAllowed = errors.New("devcontainer: capability not allowed")
	ErrMountNotAllowed      = errors.New("devcontainer: mount source not allowed")
)

// Config represents the subset of devcontainer.json that Crewship supports.
// Unsupported fields (build, dockerfile, forwardPorts, extensions, etc.) are
// silently ignored during parsing.
//
// Lifecycle commands are polymorphic (string | []string | map[string]string)
// per the devcontainer spec. Supported semantics:
//
//   - PostCreateCommand: baked into the cached image during provisioning.
//     Runs once per image hash, as UID 1001.
//   - PostStartCommand: runs on every container start / restart, as UID 1001.
//     Intentionally excluded from ConfigHash — it does not affect image
//     content, only runtime behaviour. Template authors use this for
//     "start my local DB" or "mount secrets from Vault" style init.
type Config struct {
	Image             string                    `json:"image"`
	Features          map[string]map[string]any `json:"features,omitempty"`
	PostCreateCommand any                       `json:"postCreateCommand,omitempty"`
	PostStartCommand  any                       `json:"postStartCommand,omitempty"`
	ContainerEnv      map[string]string         `json:"containerEnv,omitempty"`
	RemoteUser        string                    `json:"remoteUser,omitempty"`

	// Top-level container-privilege controls (#1380). These are the
	// operator-declared runtime escape hatches the Security-tab UI writes.
	// Unlike feature-declared requirements (which come from arbitrary OCI
	// registries and are force-stripped in features.go), these are an
	// explicit first-party operator opt-in — so the runtime HONORS them,
	// but only after the save path has validated them: privileged requires
	// the workspace allow_privileged_credentials flag, capAdd is bounded to
	// the same allowlist as the feature path, and mount sources must pass
	// IsAllowedMountSource. They are runtime-only (HostConfig, not image
	// content), so they persist in canonicalMap but are excluded from the
	// provisioning ConfigHash (see hashRelevantMap).
	Privileged bool           `json:"privileged,omitempty"`
	Init       bool           `json:"init,omitempty"`
	CapAdd     []string       `json:"capAdd,omitempty"`
	Mounts     []FeatureMount `json:"mounts,omitempty"`
}

// Parse reads a devcontainer.json from r and returns a validated Config.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("devcontainer: reading input: %w", err)
	}
	return ParseBytes(data)
}

// ParseBytes parses a devcontainer.json from raw bytes.
func ParseBytes(data []byte) (*Config, error) {
	var raw struct {
		Image             string                    `json:"image"`
		Features          map[string]map[string]any `json:"features"`
		PostCreateCommand json.RawMessage           `json:"postCreateCommand"`
		PostStartCommand  json.RawMessage           `json:"postStartCommand"`
		ContainerEnv      map[string]string         `json:"containerEnv"`
		RemoteUser        string                    `json:"remoteUser"`
		Privileged        bool                      `json:"privileged"`
		Init              bool                      `json:"init"`
		CapAdd            []string                  `json:"capAdd"`
		Mounts            []FeatureMount            `json:"mounts"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("devcontainer: invalid JSON: %w", err)
	}

	c := &Config{
		Image:        raw.Image,
		Features:     raw.Features,
		ContainerEnv: raw.ContainerEnv,
		RemoteUser:   raw.RemoteUser,
		Privileged:   raw.Privileged,
		Init:         raw.Init,
		CapAdd:       raw.CapAdd,
		Mounts:       raw.Mounts,
	}

	// Parse polymorphic lifecycle commands.
	if len(raw.PostCreateCommand) > 0 && string(raw.PostCreateCommand) != "null" {
		pcc, err := parsePolymorphicCommand(raw.PostCreateCommand, "postCreateCommand")
		if err != nil {
			return nil, err
		}
		c.PostCreateCommand = pcc
	}
	if len(raw.PostStartCommand) > 0 && string(raw.PostStartCommand) != "null" {
		psc, err := parsePolymorphicCommand(raw.PostStartCommand, "postStartCommand")
		if err != nil {
			return nil, err
		}
		c.PostStartCommand = psc
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}

	return c, nil
}

// parsePolymorphicCommand handles devcontainer.json lifecycle fields which
// may be string, []string, or map[string]string. field is the field name
// (for error messages).
func parsePolymorphicCommand(data json.RawMessage, field string) (any, error) {
	// Try string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return s, nil
	}

	// Try []string.
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		return arr, nil
	}

	// Try map[string]string.
	var m map[string]string
	if err := json.Unmarshal(data, &m); err == nil {
		return m, nil
	}

	return nil, fmt.Errorf("devcontainer: %s must be string, []string, or map[string]string", field)
}

// commonUtilsFeatureRef is the canonical devcontainer common-utils
// feature, version-pinned so behaviour stays stable when upstream
// publishes a new tag. Bumping requires re-verifying that the
// `username` / `uid` / `gid` install options are still respected.
const commonUtilsFeatureRef = "ghcr.io/devcontainers/features/common-utils:2"

// commonUtilsRefPrefix is the registry prefix used to detect ANY
// pinned version of the feature (e.g. `:1`, `:2`, sha digests). We
// match on prefix so an operator who pinned `:1` for compatibility
// reasons still counts as having opted in — we don't second-guess
// their version pick.
//
// The trailing separator is required: a bare prefix would also match
// future or third-party features like `common-utils-extra:1`, which
// don't create the `agent` user, so auto-inject would be wrongly
// suppressed and runtime would fail with "user 'agent' does not
// exist". The set of legal separators after the feature name is small
// and stable (`:` for tag, `@` for digest), so an exhaustive check is
// safer than a regex.
const commonUtilsRefPrefix = "ghcr.io/devcontainers/features/common-utils"

// isCommonUtilsRef returns true iff ref is the common-utils feature
// at any tag or digest. Rejects sibling refs whose names extend the
// prefix (e.g. common-utils-extra).
//
// Registry refs are case-insensitive at the resolver layer (Docker /
// OCI both fold names to lower case before lookup), so an operator
// who pasted `GHCR.IO/devcontainers/features/common-utils:1` from a
// release-notes page is semantically declaring the same feature as
// the canonical lowercased ref. We normalise before comparing so the
// idempotency check holds either way and EnsureAgentUser doesn't
// double-inject under a case variant.
func isCommonUtilsRef(ref string) bool {
	lower := strings.ToLower(ref)
	if !strings.HasPrefix(lower, commonUtilsRefPrefix) {
		return false
	}
	suffix := lower[len(commonUtilsRefPrefix):]
	if suffix == "" {
		return true // unversioned reference
	}
	return suffix[0] == ':' || suffix[0] == '@'
}

// EnsureAgentUser injects the devcontainer common-utils feature with
// username=agent + UID/GID 1001 when the manifest / API caller didn't
// declare any flavour of common-utils themselves. Crewship's container
// runtime hard-codes UID 1001 (see Validate above), but without
// common-utils there's no `agent` user inside the image to map onto —
// so an operator who forgot the feature would hit "user 'agent' does
// not exist" at exec time. Auto-injecting the same feature the docs
// historically asked authors to type by hand eliminates that footgun.
//
// Idempotent: if any common-utils variant (any tag, any options) is
// already declared, the function leaves Features untouched so an
// operator who picked a different version / options keeps full
// control. Returns true when something was injected, false when no
// change was needed — callers wanting to log "auto-injected default
// agent user" key on the bool.
func (c *Config) EnsureAgentUser() bool {
	if c == nil {
		return false
	}
	if c.Features == nil {
		c.Features = make(map[string]map[string]any)
	}
	for ref := range c.Features {
		if isCommonUtilsRef(ref) {
			return false // operator opted in; respect their config
		}
	}
	c.Features[commonUtilsFeatureRef] = map[string]any{
		"username":                   "agent",
		"uid":                        "1001",
		"gid":                        "1001",
		"installZsh":                 "false", // keep image lean; agents bash
		"upgradePackages":            "false", // determinism > freshness for cached images
		"configureZshAsDefaultShell": "false",
	}
	return true
}

// Validate checks that the Config is well-formed.
func (c *Config) Validate() error {
	if c.Image == "" {
		return ErrEmptyImage
	}

	// Basic image format check: must not contain spaces or control characters.
	if strings.ContainsAny(c.Image, " \t\n\r") {
		return fmt.Errorf("%w: %q", ErrInvalidImage, c.Image)
	}

	for ref := range c.Features {
		if err := ValidateFeatureRef(ref); err != nil {
			return err
		}
	}

	// Crewship enforces UID 1001 (agent) at runtime. Explicit overrides are
	// rejected rather than silently ignored — templates that assume a
	// different user would fail in confusing ways.
	if c.RemoteUser != "" && c.RemoteUser != "agent" && c.RemoteUser != "1001" {
		return fmt.Errorf("%w: remoteUser %q unsupported — Crewship runs containers as 'agent' (UID 1001); remove remoteUser or set to 'agent'", ErrUnsupportedField, c.RemoteUser)
	}

	return nil
}

// NormalizedPostCreateCommands returns the postCreateCommand as a flat []string
// regardless of its original form.
//
//   - string → []string{s}
//   - []string → returned as-is
//   - map[string]string → values sorted by key, returned as []string
//   - nil → nil
func (c *Config) NormalizedPostCreateCommands() []string {
	return NormalizeCommand(c.PostCreateCommand)
}

// NormalizedPostStartCommands returns postStartCommand as a flat []string
// using the same normalization rules as NormalizedPostCreateCommands.
func (c *Config) NormalizedPostStartCommands() []string {
	return NormalizeCommand(c.PostStartCommand)
}

// NormalizeCommand converts a devcontainer command field (string | []string |
// map[string]string | []any | map[string]any as parsed from JSON) to a flat
// []string. Returns nil for nil input or unknown types.
func NormalizeCommand(raw any) []string {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case map[string]string:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cmds := make([]string, 0, len(v))
		for _, k := range keys {
			cmds = append(cmds, v[k])
		}
		return cmds
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		cmds := make([]string, 0, len(v))
		for _, k := range keys {
			if s, ok := v[k].(string); ok {
				cmds = append(cmds, s)
			}
		}
		return cmds
	default:
		return nil
	}
}

// Hash returns a deterministic SHA-256 hex digest of the config's canonical
// JSON representation. Keys are sorted to ensure stability. PostStartCommand
// is excluded (see HashRelevantMap) since it only affects runtime behaviour.
func (c *Config) Hash() string {
	canonical := c.hashRelevantMap()
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// hashRelevantMap is canonicalMap minus runtime-only fields. Used by
// ConfigHash so that "I tweaked postStartCommand" does not invalidate the
// cached image — the image content is identical, only start-time behaviour
// differs.
//
// privileged/init/capAdd/mounts are runtime HostConfig knobs, not image
// content, so they are excluded too — flipping privileged must not force a
// full rebuild (the runtime honours them straight from devcontainer_config).
func (c *Config) hashRelevantMap() map[string]any {
	m := c.canonicalMap()
	delete(m, "postStartCommand")
	delete(m, "privileged")
	delete(m, "init")
	delete(m, "capAdd")
	delete(m, "mounts")
	return m
}

// canonicalMap builds a map with sorted keys suitable for deterministic JSON.
func (c *Config) canonicalMap() map[string]any {
	m := map[string]any{
		"image": c.Image,
	}

	if c.RemoteUser != "" {
		m["remoteUser"] = c.RemoteUser
	}

	if len(c.ContainerEnv) > 0 {
		// json.Marshal already sorts map[string]string keys.
		m["containerEnv"] = c.ContainerEnv
	}

	if len(c.Features) > 0 {
		// Build sorted features map. json.Marshal sorts string keys in
		// map[string]any, so we just need to ensure the inner maps use
		// sortable key types (they do — map[string]any).
		m["features"] = c.Features
	}

	if c.PostCreateCommand != nil {
		m["postCreateCommand"] = c.NormalizedPostCreateCommands()
	}

	// PostStartCommand is runtime-only — included in canonical form for
	// storage/export but intentionally excluded from ConfigHash so that
	// changing a start hook does not invalidate the cached image.
	if c.PostStartCommand != nil {
		m["postStartCommand"] = c.NormalizedPostStartCommands()
	}

	// Container-privilege controls (#1380). Persisted so an EnsureAgentUser
	// re-marshal (or a config export) doesn't silently drop an operator's
	// privileged/capAdd/mounts declaration — the pre-#1380 bug where the UI
	// wrote keys the backend parsed-and-discarded. Runtime-only, hence
	// excluded from hashRelevantMap.
	if c.Privileged {
		m["privileged"] = true
	}
	if c.Init {
		m["init"] = true
	}
	if len(c.CapAdd) > 0 {
		m["capAdd"] = c.CapAdd
	}
	if len(c.Mounts) > 0 {
		m["mounts"] = c.Mounts
	}

	return m
}

// NormalizeCapability upper-cases a capability name and strips a leading
// "CAP_" so "cap_net_bind_service", "NET_BIND_SERVICE" and "CAP_NET_BIND_SERVICE"
// all resolve to the canonical Docker cap name ("NET_BIND_SERVICE").
func NormalizeCapability(raw string) string {
	up := strings.ToUpper(strings.TrimSpace(raw))
	return strings.TrimPrefix(up, "CAP_")
}

// IsAllowedCapAdd reports whether cap (in any CAP_/case form) is within the
// capability allowlist a first-party operator may grant without going fully
// privileged. It is deliberately the SAME allowlist the feature-metadata path
// enforces (allowedFeatureCapAdd — NET_BIND_SERVICE today): anything broader is
// an escalation that must go through the privileged flag (workspace-gated),
// not a per-cap grant.
func IsAllowedCapAdd(cap string) bool {
	_, ok := allowedFeatureCapAdd[NormalizeCapability(cap)]
	return ok
}

// ValidateSecurity checks the operator-declared top-level privilege controls
// against Crewship policy. allowPrivileged is the workspace's
// allow_privileged_credentials opt-in. Returns a typed sentinel error the API
// layer maps to a status code (privileged → 403, cap/mount → 400). Called at
// SAVE time so a disallowed config is refused server-side rather than stored
// and silently discarded.
func (c *Config) ValidateSecurity(allowPrivileged bool) error {
	if c == nil {
		return nil
	}
	if c.Privileged && !allowPrivileged {
		return ErrPrivilegedNotAllowed
	}
	for _, cap := range c.CapAdd {
		if !IsAllowedCapAdd(cap) {
			return fmt.Errorf("%w: %q (only NET_BIND_SERVICE may be granted directly; use privileged mode for broader access)", ErrCapabilityNotAllowed, cap)
		}
	}
	for _, m := range c.Mounts {
		if !IsAllowedMountSource(m.Source) {
			return fmt.Errorf("%w: %q", ErrMountNotAllowed, m.Source)
		}
	}
	return nil
}

// SecurityRequirements returns the operator-declared top-level privilege
// controls as an AggregatedRequirements fragment, with caps/mounts filtered
// through the runtime allowlists (defense in depth: even a tampered stored
// config can't smuggle an unlisted cap/mount into the HostConfig). Privileged
// and Init pass through as declared — the save path is responsible for gating
// privileged on the workspace flag. The runtime merges this fragment into the
// feature-derived cached_requirements so the Security-tab toggles actually
// reach the container.
func (c *Config) SecurityRequirements() AggregatedRequirements {
	if c == nil {
		return AggregatedRequirements{}
	}
	req := AggregatedRequirements{
		Privileged: c.Privileged,
		Init:       c.Init,
	}
	for _, cap := range c.CapAdd {
		if IsAllowedCapAdd(cap) {
			req.CapAdd = append(req.CapAdd, NormalizeCapability(cap))
		}
	}
	for _, m := range c.Mounts {
		if IsAllowedMountSource(m.Source) {
			req.Mounts = append(req.Mounts, m)
		}
	}
	return req
}

// ParseConfigSecurity decodes ONLY the top-level privilege controls
// (privileged/init/capAdd/mounts) from a stored devcontainer_config JSON string
// and returns them as a filtered AggregatedRequirements fragment. A malformed
// or empty blob yields the zero value (no extra privilege). Used by the runtime
// (resolver + credential gate) to honour the Security-tab controls, which live
// at the devcontainer.json top level rather than in the feature-aggregated
// cached_requirements. It intentionally does NOT run full Config.Validate — a
// previously-stored config is trusted to be well-formed, and we never want the
// privilege read to fail open on an unrelated validation nit.
func ParseConfigSecurity(devcontainerJSON string) AggregatedRequirements {
	if strings.TrimSpace(devcontainerJSON) == "" {
		return AggregatedRequirements{}
	}
	var raw struct {
		Privileged bool           `json:"privileged"`
		Init       bool           `json:"init"`
		CapAdd     []string       `json:"capAdd"`
		Mounts     []FeatureMount `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(devcontainerJSON), &raw); err != nil {
		return AggregatedRequirements{}
	}
	c := &Config{Privileged: raw.Privileged, Init: raw.Init, CapAdd: raw.CapAdd, Mounts: raw.Mounts}
	return c.SecurityRequirements()
}

// MarshalJSON implements json.Marshaler for deterministic DB storage.
func (c *Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.canonicalMap())
}

// ValidateFeatureRef validates that a feature reference is well-formed in
// either tag form ({registry}/{repo}:{tag}) or digest form
// ({registry}/{repo}@sha256:{64 hex}).
func ValidateFeatureRef(ref string) error {
	_, _, _, _, err := ParseFeatureRef(ref)
	return err
}

// sha256HexRe matches the digest body after "sha256:" (exactly 64 hex chars).
var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseFeatureRef parses an OCI feature reference into its components.
//
// Accepts two forms:
//
//   - Tag form:    {registry}/{repo}:{tag}     → digest = ""
//     Example:     ghcr.io/devcontainers/features/python:1
//
//   - Digest form: {registry}/{repo}@sha256:{64 hex chars}   → tag = ""
//     Example:     ghcr.io/devcontainers/features/python@sha256:abc...def
//
// The registry component is normalized to lowercase (OCI registry names are
// case-insensitive per distribution spec). The repo and tag/digest parts are
// preserved case-sensitive.
//
// Exactly one of `tag` and `digest` is non-empty for a valid ref.
func ParseFeatureRef(ref string) (registry, repo, tag, digest string, err error) {
	// Digest form takes precedence — look for "@sha256:" anchor first.
	if atIdx := strings.LastIndex(ref, "@"); atIdx >= 0 {
		path := ref[:atIdx]
		dig := ref[atIdx+1:]
		if !strings.HasPrefix(dig, "sha256:") {
			return "", "", "", "", fmt.Errorf("%w: only sha256 digest form is supported: %q", ErrInvalidFeatureRef, ref)
		}
		hex := strings.TrimPrefix(dig, "sha256:")
		if !sha256HexRe.MatchString(hex) {
			return "", "", "", "", fmt.Errorf("%w: malformed sha256 digest in %q", ErrInvalidFeatureRef, ref)
		}
		registry, repo, err = splitRegistryRepo(path, ref)
		if err != nil {
			return "", "", "", "", err
		}
		return strings.ToLower(registry), repo, "", dig, nil
	}

	// Tag form.
	colonIdx := strings.LastIndex(ref, ":")
	if colonIdx < 0 {
		return "", "", "", "", fmt.Errorf("%w: missing tag in %q", ErrInvalidFeatureRef, ref)
	}
	path := ref[:colonIdx]
	tag = ref[colonIdx+1:]
	if tag == "" {
		return "", "", "", "", fmt.Errorf("%w: empty tag in %q", ErrInvalidFeatureRef, ref)
	}

	registry, repo, err = splitRegistryRepo(path, ref)
	if err != nil {
		return "", "", "", "", err
	}
	return strings.ToLower(registry), repo, tag, "", nil
}

// splitRegistryRepo splits "<registry>/<repo>" honouring the first slash.
func splitRegistryRepo(path, fullRef string) (registry, repo string, err error) {
	slashIdx := strings.Index(path, "/")
	if slashIdx < 0 {
		return "", "", fmt.Errorf("%w: missing registry in %q", ErrInvalidFeatureRef, fullRef)
	}
	registry = path[:slashIdx]
	repo = path[slashIdx+1:]
	if registry == "" {
		return "", "", fmt.Errorf("%w: empty registry in %q", ErrInvalidFeatureRef, fullRef)
	}
	if repo == "" {
		return "", "", fmt.Errorf("%w: empty repo in %q", ErrInvalidFeatureRef, fullRef)
	}
	return registry, repo, nil
}
