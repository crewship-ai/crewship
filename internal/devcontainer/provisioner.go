package devcontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// CommitClient is the subset of the Docker API needed for image provisioning.
// A real *client.Client satisfies this interface.
type CommitClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerCommit(ctx context.Context, containerID string, options container.CommitOptions) (container.CommitResponse, error)
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

// Provisioner orchestrates the full devcontainer provisioning flow: create a
// temporary container from the base image, install features, run post-create
// commands, and commit the result as a cached image.
type Provisioner struct {
	docker     CommitClient
	installer  *Installer
	downloader *FeatureDownloader
	logger     *slog.Logger
}

// ProvisionResult contains the output of a successful provisioning run.
type ProvisionResult struct {
	CachedImage  string                 // e.g. "crewship-cache:a1b2c3d4e5f6"
	ConfigHash   string                 // full SHA-256 hex digest
	Requirements AggregatedRequirements // runtime requirements bubbled up from features
}

// AggregatedRequirements contains runtime requirements bubbled up from the
// set of features a crew installs. These must reach the runtime HostConfig
// for features like DinD to actually work (privileged + docker.sock mount).
type AggregatedRequirements struct {
	ContainerEnv map[string]string `json:"containerEnv,omitempty"`
	Mounts       []FeatureMount    `json:"mounts,omitempty"`
	Privileged   bool              `json:"privileged,omitempty"`
	Init         bool              `json:"init,omitempty"`
	CapAdd       []string          `json:"capAdd,omitempty"`
	SecurityOpt  []string          `json:"securityOpt,omitempty"`
}

// aggregateFeatureRequirements merges runtime requirements across features.
// Root-level containerEnv takes precedence over feature-declared values.
// Privileged/Init are OR-ed; CapAdd/SecurityOpt are concatenated (callers may
// dedupe when applying to HostConfig).
func (p *Provisioner) aggregateFeatureRequirements(features []*ResolvedFeature, rootEnv map[string]string) AggregatedRequirements {
	req := AggregatedRequirements{ContainerEnv: map[string]string{}}
	for _, f := range features {
		if f == nil {
			continue
		}
		for k, v := range f.Metadata.ContainerEnv {
			if _, exists := req.ContainerEnv[k]; !exists {
				req.ContainerEnv[k] = v
			}
		}
		req.Mounts = append(req.Mounts, f.Metadata.Mounts...)
		if f.Metadata.Privileged {
			req.Privileged = true
		}
		if f.Metadata.Init {
			req.Init = true
		}
		req.CapAdd = append(req.CapAdd, f.Metadata.CapAdd...)
		req.SecurityOpt = append(req.SecurityOpt, f.Metadata.SecurityOpt...)
	}
	// Root-level containerEnv wins over feature-declared values.
	for k, v := range rootEnv {
		req.ContainerEnv[k] = v
	}
	return req
}

// NewProvisioner creates a Provisioner with all required dependencies.
func NewProvisioner(docker CommitClient, installer *Installer, downloader *FeatureDownloader, logger *slog.Logger) *Provisioner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provisioner{
		docker:     docker,
		installer:  installer,
		downloader: downloader,
		logger:     logger,
	}
}

// provisionerSchemaVersion invalidates all cached images when this changes.
// Bump when:
//   - Provisioning logic changes meaningfully (feature install order, env injection, etc.)
//   - Core mount layout changes
//   - Any backward-incompatible runtime change
const provisionerSchemaVersion = "v1"

// cacheImageTag returns the Docker image tag for a given config hash.
func cacheImageTag(configHash string) string {
	short := configHash
	if len(short) > 12 {
		short = short[:12]
	}
	return "crewship-cache:" + short
}

// configHash computes a deterministic SHA-256 hash from the base image,
// devcontainer config, and mise config.
//
// Canonical JSON representation: Config.MarshalJSON emits a map with sorted
// keys (Go's json package sorts map[string]X keys). miseConfig is re-parsed
// and re-marshaled so that whitespace / key-order differences in the stored
// JSON produce the same hash. Unparseable mise config falls back to raw.
//
// Note: changing the canonicalization changes existing hashes once; users
// will re-provision on next run. Document in CHANGELOG when bumped.
func configHash(baseImage string, cfg *Config, miseConfig string) string {
	h := sha256.New()
	h.Write([]byte(provisionerSchemaVersion))
	h.Write([]byte("|"))
	h.Write([]byte(baseImage))
	h.Write([]byte("|"))

	// Canonicalize cfg via MarshalJSON (sorted keys in canonicalMap).
	cfgCanon, _ := json.Marshal(cfg)
	h.Write(cfgCanon)
	h.Write([]byte("|"))

	// Canonicalize miseConfig by parsing + re-marshaling. Falls back to raw
	// bytes if the config is not valid JSON.
	if miseConfig != "" {
		var miseData any
		if err := json.Unmarshal([]byte(miseConfig), &miseData); err == nil {
			sortedMise, _ := json.Marshal(miseData)
			h.Write(sortedMise)
		} else {
			h.Write([]byte(miseConfig))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// IsCached reports whether a cached image for the given config hash exists.
func (p *Provisioner) IsCached(ctx context.Context, hash string) (bool, error) {
	tag := cacheImageTag(hash)
	return p.imageExists(ctx, tag)
}

// imageExists checks whether a locally available image matches the given
// reference (e.g. "crewship-cache:a1b2c3d4e5f6").
func (p *Provisioner) imageExists(ctx context.Context, ref string) (bool, error) {
	imgs, err := p.docker.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("listing images: %w", err)
	}
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if tag == ref {
				return true, nil
			}
		}
	}
	return false, nil
}

// Provision builds a cached image by installing devcontainer features and
// running post-create commands in a temporary container. If a cached image
// already exists, it returns immediately.
func (p *Provisioner) Provision(ctx context.Context, baseImage string, cfg *Config, miseConfig string) (*ProvisionResult, error) {
	hash := configHash(baseImage, cfg, miseConfig)
	tag := cacheImageTag(hash)

	// 1. Check cache.
	exists, err := p.imageExists(ctx, tag)
	if err != nil {
		return nil, err
	}
	if exists {
		p.logger.Info("using cached image", "tag", tag)
		return &ProvisionResult{CachedImage: tag, ConfigHash: hash}, nil
	}

	// Skip provisioning if no features, no postCreateCommand, no containerEnv, and no mise config.
	if len(cfg.Features) == 0 && cfg.PostCreateCommand == nil && len(cfg.ContainerEnv) == 0 && miseConfig == "" {
		p.logger.Debug("skipping provisioning - config has no customizations")
		return &ProvisionResult{CachedImage: "", ConfigHash: hash}, nil
	}

	p.logger.Info("provisioning devcontainer image", "base", baseImage, "tag", tag)

	// 2. Create temporary container.
	containerID, err := p.createTempContainer(ctx, baseImage)
	if err != nil {
		return nil, fmt.Errorf("creating temp container: %w", err)
	}

	// Ensure cleanup on any exit path.
	defer func() {
		cleanupCtx := context.Background()
		_ = p.docker.ContainerStop(cleanupCtx, containerID, container.StopOptions{})
		_ = p.docker.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true})
	}()

	// 3. Start the container.
	if err := p.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("starting temp container: %w", err)
	}

	// 4. Download and sort features. Both the agent user (via common-utils
	// feature with username/userUid options) and the Claude Code CLI (via
	// devcontainers-extra/claude-code feature) come from the devcontainer
	// configuration — no custom Go installers needed.
	resolvedFeatures, err := p.installFeatures(ctx, containerID, cfg)
	if err != nil {
		return nil, err
	}

	// 5. Handle mise configuration.
	if miseConfig != "" {
		if err := p.installMise(ctx, containerID, miseConfig); err != nil {
			return nil, fmt.Errorf("mise provisioning: %w", err)
		}
	}

	// 6a. Run feature-level postCreateCommand hooks. Baked into cached image.
	if err := p.runFeatureLifecycleCommands(ctx, containerID, resolvedFeatures); err != nil {
		return nil, err
	}

	// 6b. Run root-level postCreateCommand as agent user (1001:1001).
	if err := p.runPostCreateCommands(ctx, containerID, cfg); err != nil {
		return nil, err
	}

	// 7. Write containerEnv (aggregated from features + root-level) to
	// /etc/environment. Root-level wins on key conflict.
	requirements := p.aggregateFeatureRequirements(resolvedFeatures, cfg.ContainerEnv)
	if err := p.writeAggregatedContainerEnv(ctx, containerID, requirements.ContainerEnv); err != nil {
		return nil, err
	}

	// 8. Clean up caches inside the container.
	if err := p.cleanupCaches(ctx, containerID); err != nil {
		p.logger.Warn("cache cleanup failed", "error", err)
	}

	// 9. Commit the container as a cached image.
	_, commitErr := p.docker.ContainerCommit(ctx, containerID, container.CommitOptions{
		Reference: tag,
	})
	if commitErr != nil {
		return nil, fmt.Errorf("committing container: %w", commitErr)
	}

	p.logger.Info("provisioned cached image",
		"tag", tag,
		"privileged", requirements.Privileged,
		"mounts", len(requirements.Mounts),
		"cap_add", len(requirements.CapAdd),
	)
	return &ProvisionResult{
		CachedImage:  tag,
		ConfigHash:   hash,
		Requirements: requirements,
	}, nil
}

// createTempContainer creates a temporary container from the base image,
// configured for provisioning (root user, writable filesystem). If the image
// is not present locally, it is pulled first.
func (p *Provisioner) createTempContainer(ctx context.Context, baseImage string) (string, error) {
	if err := p.ensureImage(ctx, baseImage); err != nil {
		return "", fmt.Errorf("pull base image %q: %w", baseImage, err)
	}
	// Note: do NOT mount /tmp as tmpfs — Docker's CopyToContainer has issues
	// finding paths created via exec inside tmpfs mounts. The container's
	// normal /tmp (union filesystem layer) works correctly with both exec
	// and CopyToContainer.
	resp, err := p.docker.ContainerCreate(ctx,
		&container.Config{
			Image: baseImage,
			Cmd:   []string{"sleep", "infinity"},
			User:  "0:0",
		},
		&container.HostConfig{},
		nil, // networkingConfig
		nil, // platform
		"",  // no name (Docker assigns one)
	)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}


// ensureImage pulls the given image if it is not already present locally.
// Uses ImageList to check existence (matches docker.go pattern of avoiding
// filters that Docker Desktop sometimes blocks).
func (p *Provisioner) ensureImage(ctx context.Context, ref string) error {
	imgs, err := p.docker.ImageList(ctx, image.ListOptions{})
	if err == nil {
		for _, img := range imgs {
			for _, tag := range img.RepoTags {
				if tag == ref {
					return nil
				}
			}
		}
	}
	p.logger.Info("pulling base image", "ref", ref)
	rc, err := p.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	// Drain the pull output stream (Docker requires the stream to be fully
	// read for the pull to complete).
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("read pull stream: %w", err)
	}
	return nil
}

// installFeatures downloads, sorts, and installs all features from the config.
// Returns the sorted slice of resolved features so callers can inspect
// metadata (containerEnv, mounts, lifecycle hooks, privileged, etc.) after
// installation.
func (p *Provisioner) installFeatures(ctx context.Context, containerID string, cfg *Config) ([]*ResolvedFeature, error) {
	if len(cfg.Features) == 0 {
		return nil, nil
	}

	// Sort feature refs for deterministic download order.
	refs := make([]string, 0, len(cfg.Features))
	for ref := range cfg.Features {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	// Download all features.
	var resolved []*ResolvedFeature
	optionsByRef := make(map[string]map[string]any, len(cfg.Features))
	for _, ref := range refs {
		opts := cfg.Features[ref]
		feature, err := p.downloader.Download(ctx, ref, opts)
		if err != nil {
			return nil, fmt.Errorf("downloading feature %s: %w", ref, err)
		}
		resolved = append(resolved, feature)
		optionsByRef[ref] = opts
	}

	// Sort by dependency order.
	sorted := SortFeatures(resolved)

	// Install each feature.
	for _, feature := range sorted {
		opts := optionsByRef[feature.Ref]
		if err := p.installer.InstallFeature(ctx, containerID, feature, opts); err != nil {
			return nil, fmt.Errorf("installing feature %s: %w", feature.Metadata.ID, err)
		}
	}

	return sorted, nil
}

// runFeatureLifecycleCommands executes feature-level postCreateCommand hooks
// in install order. Effects are baked into the cached image. Runs as the
// agent user (1001:1001) to match the final runtime environment.
func (p *Provisioner) runFeatureLifecycleCommands(ctx context.Context, containerID string, features []*ResolvedFeature) error {
	for _, feature := range features {
		if feature == nil || feature.Metadata.PostCreateCommand == nil {
			continue
		}
		cmds := NormalizeCommand(feature.Metadata.PostCreateCommand)
		for _, cmd := range cmds {
			p.logger.Info("running feature postCreateCommand",
				"feature", feature.Metadata.ID, "cmd", cmd)
			output, exitCode, err := p.installer.execInContainerAsUser(ctx, containerID,
				[]string{"bash", "-c", cmd},
				"1001:1001",
				[]string{"HOME=/home/agent", "USER=agent"},
			)
			if err != nil {
				return fmt.Errorf("feature %s postCreateCommand: %w", feature.Metadata.ID, err)
			}
			if exitCode != 0 {
				return fmt.Errorf("feature %s postCreateCommand exit %d: %s",
					feature.Metadata.ID, exitCode, output)
			}
			if output != "" {
				p.logger.Debug("feature postCreateCommand output",
					"feature", feature.Metadata.ID, "output", output)
			}
		}
	}
	return nil
}

// installMise parses the mise config, installs the mise binary, and installs
// the configured tools inside the container.
func (p *Provisioner) installMise(ctx context.Context, containerID string, miseConfig string) error {
	cfg, err := ParseMiseConfig(miseConfig)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.IsEmpty() {
		p.logger.Debug("mise config has no tools, skipping")
		return nil
	}

	// Build an ExecFunc adapter from the installer's docker client.
	execFn := p.installer.execInContainerAsUser

	p.logger.Info("installing mise binary", "container", containerID)
	if err := InstallMise(ctx, containerID, execFn); err != nil {
		return err
	}

	p.logger.Info("installing mise tools", "container", containerID, "tools", len(cfg.Tools))
	if err := InstallMiseTools(ctx, containerID, cfg, execFn); err != nil {
		return err
	}

	return nil
}

// runPostCreateCommands executes postCreateCommand entries as the agent user.
func (p *Provisioner) runPostCreateCommands(ctx context.Context, containerID string, cfg *Config) error {
	cmds := cfg.NormalizedPostCreateCommands()
	if len(cmds) == 0 {
		return nil
	}

	for _, cmd := range cmds {
		output, exitCode, err := p.installer.execInContainerAsUser(ctx, containerID,
			[]string{"bash", "-c", cmd},
			"1001:1001",
			[]string{"USER=agent", "HOME=/home/agent"},
		)
		if err != nil {
			return fmt.Errorf("postCreateCommand %q: %w", cmd, err)
		}
		if output != "" {
			p.logger.Debug("postCreateCommand output", "cmd", cmd, "output", output)
		}
		if exitCode != 0 {
			return fmt.Errorf("postCreateCommand %q exited with code %d: %s", cmd, exitCode, output)
		}
	}

	return nil
}

// writeContainerEnv writes containerEnv entries to /etc/environment so they
// are available to all login shells.
func (p *Provisioner) writeContainerEnv(ctx context.Context, containerID string, cfg *Config) error {
	return p.writeAggregatedContainerEnv(ctx, containerID, cfg.ContainerEnv)
}

// writeAggregatedContainerEnv writes the merged (feature + root-level)
// containerEnv map to /etc/environment. Iteration order is deterministic.
func (p *Provisioner) writeAggregatedContainerEnv(ctx context.Context, containerID string, env map[string]string) error {
	if len(env) == 0 {
		return nil
	}

	// Deterministic order so the committed image is reproducible.
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var lines []string
	for _, key := range keys {
		lines = append(lines, key+"="+env[key])
	}
	content := strings.Join(lines, "\n") + "\n"

	// Append to /etc/environment (preserves existing content).
	cmd := []string{"bash", "-c", fmt.Sprintf("printf '%%s' %q >> /etc/environment", content)}
	_, exitCode, err := p.installer.execInContainer(ctx, containerID, cmd, nil)
	if err != nil {
		return fmt.Errorf("writing containerEnv: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("writing containerEnv: exit code %d", exitCode)
	}

	return nil
}

// cleanupCaches removes package manager caches and temp files to reduce the
// committed image size.
func (p *Provisioner) cleanupCaches(ctx context.Context, containerID string) error {
	cmd := []string{"bash", "-c", "rm -rf /var/cache/apt /var/lib/apt/lists /tmp/*"}
	_, exitCode, err := p.installer.execInContainer(ctx, containerID, cmd, nil)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("cache cleanup exited with code %d", exitCode)
	}
	return nil
}
