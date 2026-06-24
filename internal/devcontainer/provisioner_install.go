package devcontainer

// Container creation + feature install pipeline. The methods here
// are called in order from Provision (provisioner.go); they're the
// 'do the work' half of the file split.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
)

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
		req.PostStartCommands = append(req.PostStartCommands, NormalizeCommand(f.Metadata.PostStartCommand)...)
	}
	// Root-level containerEnv wins over feature-declared values.
	for k, v := range rootEnv {
		req.ContainerEnv[k] = v
	}
	return req
}

// NewProvisioner creates a Provisioner with all required dependencies.

func (p *Provisioner) createTempContainer(ctx context.Context, baseImage string) (string, error) {
	if err := p.ensureImage(ctx, baseImage); err != nil {
		return "", fmt.Errorf("pull base image %q: %w", baseImage, err)
	}
	resp, err := p.docker.ContainerCreate(ctx,
		&container.Config{
			Image: baseImage,
			Cmd:   []string{"sleep", "infinity"},
			User:  "0:0",
			// Label is the canonical marker used by the orphan-temp sweeper.
			// If crewshipd is SIGKILLed between ContainerCreate and the
			// provision flow's cleanup defer, the next start-up sweep removes
			// these by label (see internal/api/crew_provisioning.go).
			Labels: map[string]string{TempContainerLabelKey: TempContainerLabelValue},
		},
		&container.HostConfig{
			// Parity with runtime — some feature install scripts dial the host
			// (local registry mirrors, air-gapped package servers).
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
		},
		nil, // networkingConfig
		nil, // platform
		"",  // no name (Docker assigns one)
	)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// ensureImage makes sure the given image reference is present locally and, if
// reachable, matches the current remote digest. This replaces the previous
// "ImageList + tag match" approach which could silently reuse a stale `:latest`
// tag across hosts with identical configs — breaking reproducibility.
//
// Resolution order:
//  1. HEAD manifest on remote registry (best-effort, via dockerutil.DigestResolver).
//  2. ImageInspect locally for RepoDigests.
//  3. If both succeed and local RepoDigests contain the remote digest → done.
//  4. Otherwise (local missing, stale, or offline): attempt ImagePull. An
//     offline registry with a locally present image is accepted (we trust
//     the presence); a missing image is an error only if pull fails too.

func (p *Provisioner) ensureImage(ctx context.Context, ref string) error {
	remoteDigest := p.digestResolver.Remote(ctx, ref)

	inspect, inspectErr := p.docker.ImageInspect(ctx, ref)
	localPresent := inspectErr == nil
	if localPresent && remoteDigest != "" && dockerutil.RepoDigestsContain(inspect.RepoDigests, remoteDigest) {
		return nil
	}
	if localPresent && remoteDigest == "" {
		// Offline or auth-gated registry; trust local presence.
		p.logger.Debug("image present locally; skipping pull (remote digest unavailable)", "ref", ref)
		return nil
	}

	action := "pulling base image"
	if localPresent {
		action = "local image stale, re-pulling"
	}
	p.logger.Info(action, "ref", ref, "remote_digest", remoteDigest)
	rc, err := p.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		// If pull fails but image exists locally, proceed with stale copy and
		// warn. Otherwise surface the error.
		if localPresent {
			p.logger.Warn("pull failed; proceeding with local (possibly stale) image", "ref", ref, "error", err)
			return nil
		}
		return fmt.Errorf("pull image %q: %w", ref, err)
	}
	defer rc.Close()
	// Docker requires the stream to be fully read for the pull to complete.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("read pull stream: %w", err)
	}
	// New or updated image may now be present locally — drop the cached list.
	p.invalidateImageListCache()
	return nil
}

// installFeatures downloads, sorts, and installs all features from the config.
// Returns the sorted slice of resolved features so callers can inspect
// metadata (containerEnv, mounts, lifecycle hooks, privileged, etc.) after
// installation.
//
// beforeInstall, if non-nil, fires once per feature immediately before its
// install.sh is executed — used by Provision() to drive the progress
// callback. Receives the resolved feature ID (e.g. "common-utils"), not the
// full ref. May be called from the same goroutine as the install itself.

// resolveFeatures downloads and dependency-sorts the features declared in cfg,
// returning the sorted features and the per-ref options map. It performs no
// container work, so both the BuildKit and container-commit provisioning paths
// share it (and the result feeds aggregateFeatureRequirements either way).
func (p *Provisioner) resolveFeatures(ctx context.Context, cfg *Config) ([]*ResolvedFeature, map[string]map[string]any, error) {
	if len(cfg.Features) == 0 {
		return nil, nil, nil
	}

	// Sort feature refs for deterministic download order.
	refs := make([]string, 0, len(cfg.Features))
	for ref := range cfg.Features {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	// Download features concurrently (max 3 at once) — conservative semaphore
	// size balances wall-clock savings against Docker daemon + ghcr.io rate
	// limits. Preserves deterministic ordering in `resolved` via indexed slice
	// + WaitGroup; SortFeatures then re-orders by installsAfter dependencies,
	// so the same input config always produces the same install order.
	resolved := make([]*ResolvedFeature, len(refs))
	optionsByRef := make(map[string]map[string]any, len(cfg.Features))
	for _, ref := range refs {
		optionsByRef[ref] = cfg.Features[ref]
	}
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	var downloadErr error
	var errMu sync.Mutex
	errCtx, errCancel := context.WithCancel(ctx)
	defer errCancel()

	for i, ref := range refs {
		wg.Add(1)
		go func(idx int, r string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-errCtx.Done():
				return
			}
			defer func() { <-sem }()

			opts := cfg.Features[r]
			feature, err := p.downloader.Download(errCtx, r, opts)
			if err != nil {
				errMu.Lock()
				if downloadErr == nil {
					downloadErr = fmt.Errorf("downloading feature %s: %w", r, err)
					errCancel()
				}
				errMu.Unlock()
				return
			}
			resolved[idx] = feature
		}(i, ref)
	}
	wg.Wait()

	if downloadErr != nil {
		return nil, nil, downloadErr
	}

	// Filter out any nil entries (shouldn't happen given error handling above,
	// but defensive in case a goroutine bailed out via errCtx before assigning).
	nonNil := resolved[:0]
	for _, f := range resolved {
		if f != nil {
			nonNil = append(nonNil, f)
		}
	}
	resolved = nonNil

	// Sort by dependency order.
	return SortFeatures(resolved), optionsByRef, nil
}

// installResolvedFeatures runs each feature's install.sh inside containerID in
// dependency order (the container-commit path). beforeInstall fires per feature
// for progress reporting.
func (p *Provisioner) installResolvedFeatures(ctx context.Context, containerID string, sorted []*ResolvedFeature, optionsByRef map[string]map[string]any, beforeInstall func(featureID string)) error {
	for _, feature := range sorted {
		if beforeInstall != nil {
			beforeInstall(feature.Metadata.ID)
		}
		opts := optionsByRef[feature.Ref]
		if err := p.installer.InstallFeature(ctx, containerID, feature, opts); err != nil {
			return fmt.Errorf("installing feature %s: %w", feature.Metadata.ID, err)
		}
	}
	return nil
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
			// strict mode — fail loud on first error, matches install.sh behavior.
			output, exitCode, err := p.installer.execInContainerAsUser(ctx, containerID,
				[]string{"bash", "-c", "set -e\n" + cmd},
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
		// strict mode — fail loud on first error, matches install.sh behavior.
		output, exitCode, err := p.installer.execInContainerAsUser(ctx, containerID,
			[]string{"bash", "-c", "set -e\n" + cmd},
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
// committed image size. Entries intentionally cover the paths commonly touched
// by community feature install.sh scripts: apt, npm, pip, and the two tmp
// directories which many scripts use as scratch space.
//
// Uses `2>/dev/null || true` so missing directories on exotic base images do
// not fail the cleanup step.

func (p *Provisioner) cleanupCaches(ctx context.Context, containerID string) error {
	const script = `set -u
for path in \
  /var/cache/apt \
  /var/lib/apt/lists \
  /var/tmp \
  /tmp \
  /root/.cache \
  /root/.npm \
  /root/.cargo/registry/cache \
  /root/.cargo/registry/src \
  /root/.yarn/cache \
  /home/agent/.cache/pip \
  /home/agent/.npm; do
  rm -rf "$path"/* "$path"/.[!.]* 2>/dev/null || true
done
# Keep mountpoints /tmp and /var/tmp themselves; recreate if the rm wiped them.
mkdir -p /tmp /var/tmp
chmod 1777 /tmp /var/tmp`
	cmd := []string{"bash", "-c", script}
	_, exitCode, err := p.installer.execInContainer(ctx, containerID, cmd, nil)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("cache cleanup exited with code %d", exitCode)
	}
	return nil
}
