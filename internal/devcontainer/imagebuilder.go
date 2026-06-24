package devcontainer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FeatureImageTagPrefix is the Docker repository for intermediate BuildKit
// feature images (crewship-feat:{hash}). These are regenerable and never
// referenced by a crew row, so the orphan GC may prune them freely (subject to
// its age floor) — distinct from the final crewship-cache:* images.
const FeatureImageTagPrefix = "crewship-feat:"

// ImageBuilder builds a container image from a generated Dockerfile plus a
// staged build context. It abstracts the build engine so the Docker (BuildKit)
// implementation here can be joined later by a Kubernetes one (kaniko /
// buildkitd) without touching the provisioner — mirroring the ContainerProvider
// (Docker | K8s) pattern.
type ImageBuilder interface {
	// Available reports whether this builder can run in the current
	// environment. When false the provisioner falls back to the
	// container-commit path so provisioning never hard-fails on the build
	// engine being absent.
	Available() bool
	// Build builds contextDir/Dockerfile and tags the result `tag`. Build log
	// lines are delivered to onLog (may be nil). The build uses BuildKit so the
	// generated Dockerfile's `# syntax` directive and `RUN --mount=type=cache`
	// take effect.
	Build(ctx context.Context, contextDir, tag string, onLog func(string)) error
}

// DockerBuildKitBuilder shells out to the Docker CLI with BuildKit enabled.
// Shelling to `docker build` (not the Docker SDK ImageBuild) is the most
// portable way to get full BuildKit behavior — cache mounts and the dockerfile
// frontend — identically on macOS, Linux and Windows wherever Docker Desktop /
// Engine is installed. Registry cache export (buildx `--cache-to`) is a Phase 2
// concern; local layer cache works with plain `docker build`.
type DockerBuildKitBuilder struct {
	bin    string // resolved docker-compatible CLI on PATH ("" = unavailable)
	logger *slog.Logger
}

// NewDockerBuildKitBuilder probes for a BuildKit-capable CLI on PATH. Phase 1
// restricts itself to the `docker` CLI (Docker Desktop, Colima, OrbStack and
// Rancher all expose it) because BuildKit cache mounts behave consistently
// there; podman/buildah are intentionally not used here and route through the
// container-commit fallback instead.
func NewDockerBuildKitBuilder(logger *slog.Logger) *DockerBuildKitBuilder {
	if logger == nil {
		logger = slog.Default()
	}
	bin := ""
	if p, err := exec.LookPath("docker"); err == nil {
		bin = p
	}
	return &DockerBuildKitBuilder{bin: bin, logger: logger}
}

// Available reports whether a usable docker CLI was found.
func (b *DockerBuildKitBuilder) Available() bool {
	return b != nil && b.bin != ""
}

// Build runs `docker build` with DOCKER_BUILDKIT=1 against contextDir.
func (b *DockerBuildKitBuilder) Build(ctx context.Context, contextDir, tag string, onLog func(string)) error {
	if !b.Available() {
		return fmt.Errorf("devcontainer: no BuildKit-capable docker CLI available")
	}
	// #nosec G204 — bin is a PATH-resolved docker binary; tag/contextDir are
	// internally constructed (cache tag + temp dir), not user-controlled shell.
	cmd := exec.CommandContext(ctx, b.bin, "build",
		"--tag", tag,
		"--file", filepath.Join(contextDir, "Dockerfile"),
		contextDir,
	)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("build stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // BuildKit writes progress to stderr; merge streams

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting docker build: %w", err)
	}

	// Stream log lines so the UI can show live per-layer progress.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if onLog != nil {
			onLog(line)
		}
		b.logger.Debug("docker build", "line", line)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker build failed for %s: %w", tag, err)
	}
	return nil
}

// buildFeatureImage stages a build context for the resolved features and builds
// an intermediate image (tagged crewship-feat:{hash}) on top of baseImage using
// BuildKit. The returned tag is used as the base for the temp container that
// then runs mise/postCreate/env. emit reports plan progress (pull + per-feature)
// so the UI checklist advances identically to the container-commit path.
func (p *Provisioner) buildFeatureImage(ctx context.Context, baseImage string, features []*ResolvedFeature, optionsByRef map[string]map[string]any, emit func(string)) (string, error) {
	emit(pullStepLabel(baseImage))

	contextDir, _, featTag, err := stageBuildContext(baseImage, features, optionsByRef)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(contextDir) }()

	// Exact-tag hit: skip invoking the builder entirely. BuildKit also caches
	// layers, but this avoids the process spawn when nothing changed.
	if exists, _ := p.imageExists(ctx, featTag); exists {
		for _, f := range features {
			emit(featureStepLabel(f.Metadata.ID))
		}
		p.logger.Info("reusing cached feature image", "tag", featTag)
		return featTag, nil
	}

	for _, f := range features {
		emit(featureStepLabel(f.Metadata.ID))
	}
	p.logger.Info("building feature image via BuildKit", "tag", featTag, "features", featureRefsSummary(features))
	if err := p.builder.Build(ctx, contextDir, featTag, func(line string) {
		p.logger.Debug("buildkit", "line", line)
	}); err != nil {
		return "", fmt.Errorf("building feature image: %w", err)
	}
	p.invalidateImageListCache()
	return featTag, nil
}

// stageBuildContext materializes a build context directory for the given
// features: it writes the generated Dockerfile and copies each feature's
// extracted directory to <context>/features/{id}. Returns the context dir and
// the feature-image tag derived from the Dockerfile content (so any change to
// base image, features, or options yields a new tag and a clean rebuild). The
// caller is responsible for removing contextDir.
func stageBuildContext(baseImage string, features []*ResolvedFeature, optionsByRef map[string]map[string]any) (contextDir, dockerfile, tag string, err error) {
	dockerfile, err = GenerateDockerfile(DockerfileBuild{
		BaseImage:    baseImage,
		Features:     features,
		OptionsByRef: optionsByRef,
	})
	if err != nil {
		return "", "", "", err
	}

	contextDir, err = os.MkdirTemp("", "crewship-build-*")
	if err != nil {
		return "", "", "", fmt.Errorf("creating build context: %w", err)
	}
	// Best-effort cleanup on any error after this point.
	cleanup := func() { _ = os.RemoveAll(contextDir) }

	if err = os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		cleanup()
		return "", "", "", fmt.Errorf("writing Dockerfile: %w", err)
	}

	for _, f := range features {
		if f == nil {
			continue
		}
		dst := filepath.Join(contextDir, featureContextDir, f.Metadata.ID)
		if err = copyTree(f.Dir, dst); err != nil {
			cleanup()
			return "", "", "", fmt.Errorf("staging feature %s: %w", f.Metadata.ID, err)
		}
	}

	sum := sha256.Sum256([]byte(dockerfile))
	tag = FeatureImageTagPrefix + hex.EncodeToString(sum[:])[:12]
	return contextDir, dockerfile, tag, nil
}

// copyTree recursively copies the directory at src into dst, creating dst.
// Symlinks are dereferenced; only regular files and directories are copied
// (devcontainer features are plain files — scripts, configs, JSON).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil // skip sockets/devices/etc.
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304 — src is a feature-cache path we extracted
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// featureRefsSummary is a small helper for logs: a compact comma list of leaf
// ids in order. Kept here so both the builder and provisioner can describe a
// build without pulling in the whole feature list.
func featureRefsSummary(features []*ResolvedFeature) string {
	ids := make([]string, 0, len(features))
	for _, f := range features {
		if f != nil {
			ids = append(ids, f.Metadata.ID)
		}
	}
	return strings.Join(ids, ",")
}
