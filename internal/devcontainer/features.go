package devcontainer

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// FeatureRef represents a parsed OCI feature reference.
type FeatureRef struct {
	Registry string
	Repo     string
	Tag      string
}

// ResolvedFeature represents a downloaded and extracted feature ready for use.
type ResolvedFeature struct {
	Ref      string
	Dir      string // local path to extracted feature
	Metadata FeatureMetadata
}

// FeatureMetadata mirrors the devcontainer-feature.json schema.
type FeatureMetadata struct {
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	Options       map[string]any    `json:"options,omitempty"`
	InstallsAfter []string          `json:"installsAfter,omitempty"`
	ContainerEnv  map[string]string `json:"containerEnv,omitempty"`

	// Mounts declares bind/volume mounts the feature needs at runtime (e.g. DinD
	// needs /var/run/docker.sock bound from the host).
	Mounts []FeatureMount `json:"mounts,omitempty"`

	// Docker security requirements bubbled up into the runtime HostConfig.
	Privileged  bool     `json:"privileged,omitempty"`
	Init        bool     `json:"init,omitempty"`
	CapAdd      []string `json:"capAdd,omitempty"`
	SecurityOpt []string `json:"securityOpt,omitempty"`

	// Feature-level lifecycle hooks. These run while the feature is being
	// installed into the provisioning container — their effects are baked
	// into the cached image. Use `any` because devcontainer spec allows
	// string, []string, or map[string]string.
	OnCreateCommand      any `json:"onCreateCommand,omitempty"`
	PostCreateCommand    any `json:"postCreateCommand,omitempty"`
	PostStartCommand     any `json:"postStartCommand,omitempty"`
	PostAttachCommand    any `json:"postAttachCommand,omitempty"`
	UpdateContentCommand any `json:"updateContentCommand,omitempty"`
}

// FeatureMount describes a bind or volume mount declared by a feature.
type FeatureMount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type,omitempty"` // "bind" or "volume"
}

// InstallsAfter is retained for backward compatibility — the canonical
// type is now []string (per devcontainer spec). Struct-form arrays in the
// wild are normalized into string IDs at unmarshal time via
// UnmarshalJSON on FeatureMetadata (see below) if we encounter them.
type InstallsAfter struct {
	ID string `json:"id"`
}

// UnmarshalJSON allows installsAfter to be either []string (spec) or
// []{id: string} (occasionally seen in the wild).
func (m *FeatureMetadata) UnmarshalJSON(data []byte) error {
	type raw struct {
		ID            string            `json:"id"`
		Version       string            `json:"version"`
		Name          string            `json:"name"`
		Description   string            `json:"description,omitempty"`
		Options       map[string]any    `json:"options,omitempty"`
		InstallsAfter json.RawMessage   `json:"installsAfter,omitempty"`
		ContainerEnv  map[string]string `json:"containerEnv,omitempty"`

		Mounts      []FeatureMount `json:"mounts,omitempty"`
		Privileged  bool           `json:"privileged,omitempty"`
		Init        bool           `json:"init,omitempty"`
		CapAdd      []string       `json:"capAdd,omitempty"`
		SecurityOpt []string       `json:"securityOpt,omitempty"`

		OnCreateCommand      any `json:"onCreateCommand,omitempty"`
		PostCreateCommand    any `json:"postCreateCommand,omitempty"`
		PostStartCommand     any `json:"postStartCommand,omitempty"`
		PostAttachCommand    any `json:"postAttachCommand,omitempty"`
		UpdateContentCommand any `json:"updateContentCommand,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.ID = r.ID
	m.Version = r.Version
	m.Name = r.Name
	m.Description = r.Description
	m.Options = r.Options
	m.ContainerEnv = r.ContainerEnv
	m.Mounts = r.Mounts
	m.Privileged = r.Privileged
	m.Init = r.Init
	m.CapAdd = r.CapAdd
	m.SecurityOpt = r.SecurityOpt
	m.OnCreateCommand = r.OnCreateCommand
	m.PostCreateCommand = r.PostCreateCommand
	m.PostStartCommand = r.PostStartCommand
	m.PostAttachCommand = r.PostAttachCommand
	m.UpdateContentCommand = r.UpdateContentCommand
	if len(r.InstallsAfter) > 0 && string(r.InstallsAfter) != "null" {
		// Try []string first.
		var strs []string
		if err := json.Unmarshal(r.InstallsAfter, &strs); err == nil {
			m.InstallsAfter = strs
		} else {
			// Fall back to []{id: string}.
			var objs []InstallsAfter
			if err := json.Unmarshal(r.InstallsAfter, &objs); err == nil {
				m.InstallsAfter = make([]string, 0, len(objs))
				for _, o := range objs {
					if o.ID != "" {
						m.InstallsAfter = append(m.InstallsAfter, o.ID)
					}
				}
			}
			// If neither form works, silently skip (non-critical field).
		}
	}
	return nil
}

// FeatureDownloader fetches devcontainer Features from OCI registries and
// caches them locally.
type FeatureDownloader struct {
	cacheDir string
	logger   *slog.Logger
}

// NewFeatureDownloader creates a downloader that stores extracted features
// under cacheDir.
func NewFeatureDownloader(cacheDir string, logger *slog.Logger) *FeatureDownloader {
	return &FeatureDownloader{
		cacheDir: cacheDir,
		logger:   logger,
	}
}

// ToFeatureRef converts the 4-return-value ParseFeatureRef into a FeatureRef
// struct for convenience.
func ToFeatureRef(ref string) (FeatureRef, error) {
	registry, repo, tag, err := ParseFeatureRef(ref)
	if err != nil {
		return FeatureRef{}, err
	}
	return FeatureRef{
		Registry: registry,
		Repo:     repo,
		Tag:      tag,
	}, nil
}

// cacheKey returns a truncated SHA-256 hex digest used as the cache directory
// name for the given reference string.
func cacheKey(ref string) string {
	h := sha256.Sum256([]byte(ref))
	return fmt.Sprintf("%x", h[:8]) // 16 hex chars
}

// cachePathFor returns the absolute path to the cache directory for ref.
func (d *FeatureDownloader) cachePathFor(ref string) string {
	return filepath.Join(d.cacheDir, cacheKey(ref))
}

// IsCached reports whether a usable cached copy of the feature exists.
func (d *FeatureDownloader) IsCached(ref string) bool {
	dir := d.cachePathFor(ref)
	_, err := os.Stat(filepath.Join(dir, "install.sh"))
	return err == nil
}

// ClearCache removes all cached features.
func (d *FeatureDownloader) ClearCache() error {
	return os.RemoveAll(d.cacheDir)
}

// Download fetches the OCI artifact for the given feature reference, extracts
// it into the local cache, and returns a ResolvedFeature. If a cached copy
// already exists it is returned without a network call. When a network error
// occurs but a cached copy is present, the cached version is used (offline
// resilience).
func (d *FeatureDownloader) Download(ctx context.Context, ref string, options map[string]any) (*ResolvedFeature, error) {
	dir := d.cachePathFor(ref)

	// Fast path: cache hit.
	if d.IsCached(ref) {
		d.logger.Debug("feature cache hit", "ref", ref, "dir", dir)
		return d.resolveFromCache(ref, dir)
	}

	// Pull from OCI registry.
	if err := d.pull(ctx, ref, dir); err != nil {
		// Offline resilience: if the cache directory already has content
		// from a previous partial download, try using it.
		if d.IsCached(ref) {
			d.logger.Warn("OCI pull failed, using cached copy", "ref", ref, "error", err)
			return d.resolveFromCache(ref, dir)
		}
		return nil, fmt.Errorf("downloading feature %s: %w", ref, err)
	}

	return d.resolveFromCache(ref, dir)
}

// resolveFromCache reads metadata from an already-extracted cache directory.
func (d *FeatureDownloader) resolveFromCache(ref, dir string) (*ResolvedFeature, error) {
	meta, err := readMetadata(dir)
	if err != nil {
		return nil, fmt.Errorf("reading cached metadata for %s: %w", ref, err)
	}
	return &ResolvedFeature{
		Ref:      ref,
		Dir:      dir,
		Metadata: meta,
	}, nil
}

// pull fetches the OCI image for ref using go-containerregistry and extracts
// its first layer (the feature tarball) into destDir. Extraction is atomic:
// content is written to a temporary directory first, then renamed into place.
func (d *FeatureDownloader) pull(ctx context.Context, ref, destDir string) error {
	tag, err := name.NewTag(ref, name.StrictValidation)
	if err != nil {
		return fmt.Errorf("parsing OCI tag %q: %w", ref, err)
	}

	d.logger.Info("pulling feature", "ref", ref)

	img, err := remote.Image(tag, remote.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("fetching image %q: %w", ref, err)
	}

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("listing layers for %q: %w", ref, err)
	}
	if len(layers) == 0 {
		return fmt.Errorf("image %q has no layers", ref)
	}

	// Devcontainer features are OCI artifacts with media type
	// "application/vnd.devcontainers.layer.v1+tar" (uncompressed tar).
	// Use Uncompressed() which handles both gzipped image layers and
	// raw-tar artifact layers transparently.
	rc, err := layers[0].Uncompressed()
	if err != nil {
		return fmt.Errorf("reading layer for %q: %w", ref, err)
	}
	defer rc.Close()

	// Extract into a temporary directory first, then rename atomically.
	tempDir := destDir + ".tmp-" + fmt.Sprintf("%x", sha256.Sum256([]byte(ref+fmt.Sprint(os.Getpid()))))[:12]
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return fmt.Errorf("creating temp cache dir: %w", err)
	}
	// Ensure cleanup on failure.
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tempDir)
		}
	}()

	if err := extractTarGz(rc, tempDir); err != nil {
		return fmt.Errorf("extracting layer for %q: %w", ref, err)
	}

	// Validate that required files exist before committing.
	if _, err := os.Stat(filepath.Join(tempDir, "install.sh")); err != nil {
		return fmt.Errorf("extracted feature %q missing install.sh", ref)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "devcontainer-feature.json")); err != nil {
		return fmt.Errorf("extracted feature %q missing devcontainer-feature.json", ref)
	}

	// Remove any existing destination before atomic rename.
	_ = os.RemoveAll(destDir)
	if err := os.Rename(tempDir, destDir); err != nil {
		return fmt.Errorf("atomically placing cache dir: %w", err)
	}
	success = true

	return nil
}

// extractTarGz reads a tar stream (already uncompressed — go-containerregistry's
// Uncompressed() handles any gzip layer transparently, and devcontainer feature
// artifacts use raw tar layers) and writes entries into destDir. It protects
// against path traversal by rejecting entries that resolve outside destDir.
func extractTarGz(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		// Sanitize: strip leading "./" or "/" and reject path traversal.
		cleanName := filepath.Clean(hdr.Name)
		if strings.HasPrefix(cleanName, "..") {
			continue // skip entries that try to escape
		}
		target := filepath.Join(destDir, cleanName)

		// Verify the resolved path is inside destDir.
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) && target != filepath.Clean(destDir) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			// Limit extraction to 50 MB per file as a safety measure.
			const maxFileSize = 50 << 20
			if hdr.Size > maxFileSize {
				return fmt.Errorf("tar entry %q exceeds max size (%d > %d)", hdr.Name, hdr.Size, maxFileSize)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode)
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, maxFileSize)); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// readMetadata parses devcontainer-feature.json from the given directory.
func readMetadata(dir string) (FeatureMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "devcontainer-feature.json"))
	if err != nil {
		return FeatureMetadata{}, fmt.Errorf("reading devcontainer-feature.json: %w", err)
	}
	var meta FeatureMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return FeatureMetadata{}, fmt.Errorf("parsing devcontainer-feature.json: %w", err)
	}
	return meta, nil
}

// SortFeatures performs a topological sort of features based on their
// installsAfter dependencies. Features with no dependencies or whose
// dependencies are not in the input set come first. The sort is stable:
// features at the same dependency depth retain their original order.
func SortFeatures(features []*ResolvedFeature) []*ResolvedFeature {
	if len(features) <= 1 {
		return features
	}

	// Build an index from feature ID to position.
	idIndex := make(map[string]int, len(features))
	for i, f := range features {
		idIndex[f.Metadata.ID] = i
	}

	// Kahn's algorithm for topological sort.
	n := len(features)
	inDegree := make([]int, n)
	adjList := make([][]int, n)

	for i, f := range features {
		for _, depID := range f.Metadata.InstallsAfter {
			if j, ok := idIndex[depID]; ok {
				adjList[j] = append(adjList[j], i)
				inDegree[i]++
			}
		}
	}

	// Seed with zero-degree nodes in original order (stable).
	var queue []int
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}

	result := make([]*ResolvedFeature, 0, n)
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		result = append(result, features[idx])

		// Sort neighbours to maintain stability.
		neighbours := adjList[idx]
		sort.Ints(neighbours)
		for _, next := range neighbours {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	// If there's a cycle, append remaining features in original order.
	if len(result) < n {
		seen := make(map[int]bool, len(result))
		for _, rf := range result {
			for i, orig := range features {
				if orig == rf {
					seen[i] = true
					break
				}
			}
		}
		for i, f := range features {
			if !seen[i] {
				result = append(result, f)
			}
		}
	}

	return result
}
