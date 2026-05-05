package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Scope identifies the granularity of a backup bundle.
type Scope string

const (
	ScopeCrew      Scope = "crew"
	ScopeWorkspace Scope = "workspace"
	ScopeInstance  Scope = "instance"
)

// Valid reports whether s is a known scope value.
func (s Scope) Valid() bool {
	switch s {
	case ScopeCrew, ScopeWorkspace, ScopeInstance:
		return true
	default:
		return false
	}
}

// ScopeLevel selects which of the per-crew filesystem sections the
// collector pulls into the bundle. Three presets — Quick / Standard
// / Full — keep the admin UX manageable while still giving operators
// real choice between snapshot speed (~1 MiB) and full preservation
// of installed services (~50+ MiB depending on /var/lib content).
//
// Restore is preset-agnostic: missing sections are silent skips, so
// a Quick bundle can be restored into the same target as a Full one
// without flag wrangling.
type ScopeLevel string

const (
	// ScopeLevelQuick captures only /workspace + /output (agent
	// memory). Use when you need a fast snapshot of "what the agent
	// is currently working on" without the home dir / tools / system
	// data tail.
	ScopeLevelQuick ScopeLevel = "quick"
	// ScopeLevelStandard adds /home/agent + /opt/crew-tools to the
	// Quick set. This is the default and matches what every release
	// up to and including the /var/lib expansion was producing.
	ScopeLevelStandard ScopeLevel = "standard"
	// ScopeLevelFull adds /var/lib on top of Standard so any service
	// the agent installed and started inside the container (redis,
	// postgresql, mysql, ...) round-trips through the bundle.
	ScopeLevelFull ScopeLevel = "full"
)

// Valid reports whether l is a known scope level.
func (l ScopeLevel) Valid() bool {
	switch l {
	case ScopeLevelQuick, ScopeLevelStandard, ScopeLevelFull:
		return true
	default:
		return false
	}
}

// DefaultScopeLevel is what an empty / unspecified preset resolves
// to. Standard matches the historical (pre-preset) collector
// behaviour exactly, so existing call sites don't change shape.
const DefaultScopeLevel = ScopeLevelStandard

// Target describes where a bundle can be restored.
//   - "same-instance": crew-scope bundles; cannot be restored to a
//     different Crewship instance in MVP due to FK / ID remapping gaps.
//   - "any-instance": workspace-scope and instance-scope bundles are
//     self-contained and portable across instances.
type Target string

const (
	TargetSameInstance Target = "same-instance"
	TargetAnyInstance  Target = "any-instance"
)

// Manifest is the strongly-typed representation of MANIFEST.json inside
// a bundle. It is always stored in plaintext (never inside the AGE
// payload) so that `crewship backup inspect` works without a passphrase.
//
// Field additions are backward-compatible: readers ignore unknown fields
// and writers always include the current set. Removals require a bump
// of FormatVersion.
type Manifest struct {
	FormatVersion           int    `json:"format_version"`
	CrewshipVersionAtBackup string `json:"crewship_version_at_backup"`
	SchemaMigrationVersions []int  `json:"schema_migration_versions"`
	Scope                   Scope  `json:"scope"`
	// ScopeLevel records which preset (quick/standard/full) the
	// admin chose at create time so the UI can render a coherent
	// badge without re-deriving from CrewSummary.*Included flags.
	// Older bundles that pre-date the preset feature omit this
	// field entirely; the catalog migration backfills 'standard'.
	ScopeLevel        ScopeLevel `json:"scope_level,omitempty"`
	CompatibleTargets []Target   `json:"compatible_targets"`
	CreatedAt         time.Time  `json:"created_at"`
	CreatedBy         Actor      `json:"created_by"`
	SourceInstance    Instance   `json:"source_instance"`
	Contents          Contents   `json:"contents"`
	Encryption        Encryption `json:"encryption"`
	Checksums         Checksums  `json:"checksums"`
}

// Actor describes the user who created or restored the bundle.
type Actor struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// Instance describes the Crewship installation that produced the bundle.
type Instance struct {
	Hostname      string `json:"hostname"`
	Platform      string `json:"platform"`
	DockerVersion string `json:"docker_version,omitempty"`
}

// Contents enumerates what sections of the bundle are populated. This
// is primarily informational; the authoritative listing lives inside
// the payload tar.
type Contents struct {
	Workspace *WorkspaceSummary `json:"workspace,omitempty"`
	Crews     []CrewSummary     `json:"crews,omitempty"`

	// Instance-scope only (V1.5, CRE-129). Nil for MVP bundles.
	CredstoreIncluded      bool `json:"credstore_included,omitempty"`
	AuthKeysIncluded       bool `json:"auth_keys_included,omitempty"`
	InstanceConfigIncluded bool `json:"instance_config_included,omitempty"`
}

// WorkspaceSummary carries workspace-level identity fields.
type WorkspaceSummary struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// CrewSummary carries per-crew provenance. Image digests are pinned so
// that V2 restore can rebuild the exact devcontainer image even if
// `crewship/agent-runtime:latest` has moved.
type CrewSummary struct {
	ID                         string       `json:"id"`
	Slug                       string       `json:"slug"`
	Name                       string       `json:"name"`
	RuntimeImage               string       `json:"runtime_image,omitempty"`
	BaseImageDigest            string       `json:"base_image_digest,omitempty"`
	CachedImageDigest          string       `json:"cached_image_digest,omitempty"`
	ConfigHash                 string       `json:"config_hash,omitempty"`
	DevcontainerConfigIncluded bool         `json:"devcontainer_config_included"`
	MiseConfigIncluded         bool         `json:"mise_config_included"`
	Features                   []FeaturePin `json:"features,omitempty"`
	WorkspaceIncluded          bool         `json:"workspace_included"`
	VolumesIncluded            []string     `json:"volumes_included,omitempty"`
	MemoryIncluded             bool         `json:"memory_included"`
	// SystemIncluded is set when the bundle carries the /var/lib
	// section (any service data the agent populated at runtime —
	// redis dump.rdb, postgresql data dir, etc.). Bundles produced
	// before this section was added omit the field entirely; the
	// restore preflight treats absent as "no system data to land".
	SystemIncluded   bool  `json:"system_included,omitempty"`
	AgentCount       int   `json:"agent_count"`
	PayloadSizeBytes int64 `json:"payload_size_bytes,omitempty"`
}

// FeaturePin pins a devcontainer feature OCI reference to a digest so
// that rebuild on restore is reproducible.
type FeaturePin struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// Encryption records how the payload is sealed. When Enabled is false,
// the payload sits in the outer tar as plaintext and must carry a
// warning to the user.
type Encryption struct {
	Enabled       bool     `json:"enabled"`
	Algorithm     string   `json:"algorithm,omitempty"`
	KeyDerivation string   `json:"key_derivation,omitempty"`
	Recipients    []string `json:"recipients,omitempty"`
}

// Checksums records integrity hashes. PayloadSHA256 covers the bytes of
// the (possibly encrypted) payload blob inside the bundle; it does not
// cover MANIFEST.json itself.
type Checksums struct {
	PayloadSHA256 string `json:"payload_sha256"`
}

// Validate performs structural validation on the manifest. It does not
// verify checksums or touch disk.
func (m *Manifest) Validate() error {
	if m.FormatVersion <= 0 {
		return fmt.Errorf("%w: format_version must be positive", ErrInvalidManifest)
	}
	if !m.Scope.Valid() {
		return fmt.Errorf("%w: scope %q not in {crew, workspace, instance}", ErrInvalidScope, m.Scope)
	}
	// scope_level was added after FormatVersion=1 shipped, so an
	// empty value means "legacy bundle from before presets". Only
	// reject NON-empty values that aren't a known preset — that
	// catches a tampered or future-version manifest leaking an
	// unknown preset into restore / UI badge logic.
	if m.ScopeLevel != "" && !m.ScopeLevel.Valid() {
		return fmt.Errorf("%w: scope_level %q not in {quick, standard, full}", ErrInvalidManifest, m.ScopeLevel)
	}
	if len(m.CompatibleTargets) == 0 {
		return fmt.Errorf("%w: compatible_targets must not be empty", ErrInvalidManifest)
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at must be set", ErrInvalidManifest)
	}
	if m.CreatedBy.UserID == "" {
		return fmt.Errorf("%w: created_by.user_id must be set", ErrInvalidManifest)
	}
	if m.Checksums.PayloadSHA256 == "" {
		return fmt.Errorf("%w: checksums.payload_sha256 must be set", ErrInvalidManifest)
	}
	return nil
}

// WriteTo serializes m as pretty-printed JSON to w. A trailing newline
// is emitted so the file is POSIX text-file compliant.
func (m *Manifest) WriteTo(w io.Writer) (int64, error) {
	// Count bytes via a wrapper; json.Encoder does not expose counts.
	cw := &countingWriter{w: w}
	enc := json.NewEncoder(cw)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return cw.n, err
	}
	return cw.n, nil
}

// ReadManifest parses MANIFEST.json bytes into a Manifest and validates
// it structurally. Unknown fields are tolerated (forward-compat rule).
func ReadManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// countingWriter wraps an io.Writer to count how many bytes have passed
// through it. Used internally for manifest serialization where the
// *json.Encoder does not expose a byte counter.
type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}
