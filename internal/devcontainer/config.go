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
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("devcontainer: invalid JSON: %w", err)
	}

	c := &Config{
		Image:        raw.Image,
		Features:     raw.Features,
		ContainerEnv: raw.ContainerEnv,
		RemoteUser:   raw.RemoteUser,
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
func (c *Config) hashRelevantMap() map[string]any {
	m := c.canonicalMap()
	delete(m, "postStartCommand")
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

	return m
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
