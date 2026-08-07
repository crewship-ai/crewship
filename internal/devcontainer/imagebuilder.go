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
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// buildLogScrubber redacts secret-shaped tokens (KEY=secret, bearer tokens,
// API keys, etc.) from BuildKit log tails before they are emitted as provision
// events. The tail is arbitrary build output that can echo credentials baked
// into a feature/Dockerfile; without this, those secrets would land in live WS
// payloads and journal rows. Created once (pattern compilation is not free).
var buildLogScrubber = scrubber.New()

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
	// host pins the build to one daemon: the endpoint the container provider
	// resolved, rather than whatever `docker context` happens to point at
	// (#1705). Empty = unpinned, the pre-#1705 behaviour, kept only for the
	// hand-built case where no provider client is available.
	host string
}

// daemonHoster is the one thing this builder needs from the provider's Docker
// SDK client: the endpoint it dialled. *client.Client satisfies it.
//
// Type-asserted rather than added to CommitClient because CommitClient is the
// *image* API surface and several tests implement it with fakes that have no
// daemon at all; a builder that cannot learn the endpoint simply stays
// unpinned instead of forcing every fake to grow a method.
type daemonHoster interface{ DaemonHost() string }

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

// NewDockerBuildKitBuilderFor is NewDockerBuildKitBuilder pinned to the daemon
// that `docker` (the SDK client the provisioner runs and cache-checks against)
// is connected to.
//
// It exists because the two halves of provisioning select their daemon by
// different mechanisms and could disagree in silence (#1705). The build shells
// out to the CLI, which resolves its endpoint from `docker context`; the "is it
// already built?" check and the container create go through the provider's own
// probed socket. `colima start` and a Rancher Desktop launch both make
// themselves the current context without touching /var/run/docker.sock, so on a
// machine with more than one runtime the default state is *split*: the image
// builds into daemon A and the create looks for the local-only tag on daemon B,
// fails to find it, tries to pull it from a registry, and reports
// `pull access denied ... may require 'docker login'` — an authentication error
// for a problem that has nothing to do with authentication.
//
// Pinning rather than detecting-and-refusing: a divergence that cannot happen
// needs no error message, and the operator's `docker context` is their own
// business — Crewship has no business failing because of where the human's
// shell points, only because of where its own daemon is.
//
// docker is the provisioner's SDK client (a CommitClient); anything that can
// report its endpoint pins the build, anything else leaves it unpinned.
func NewDockerBuildKitBuilderFor(logger *slog.Logger, docker any) *DockerBuildKitBuilder {
	b := NewDockerBuildKitBuilder(logger)
	if h, ok := docker.(daemonHoster); ok {
		b.host = strings.TrimSpace(h.DaemonHost())
	}
	return b
}

// Available reports whether a usable docker CLI was found.
func (b *DockerBuildKitBuilder) Available() bool {
	return b != nil && b.bin != ""
}

// buildEnv is the environment handed to `docker build`: the parent's, with
// BuildKit on and — when host is set — the daemon pinned.
//
// Both DOCKER_HOST and DOCKER_CONTEXT are stripped from the inherited
// environment first, and the order matters. Measured against Docker CLI 29.x on
// macOS: DOCKER_CONTEXT alone selects that context's endpoint; DOCKER_HOST
// alone overrides the *stored current context*; and with both set DOCKER_HOST
// wins. Stripping DOCKER_CONTEXT rather than relying on that precedence keeps
// the guarantee from depending on a CLI behaviour that has no compatibility
// promise — the child sees exactly one endpoint selector, the one we set.
//
// Unpinned (host == "") the inherited values are left in place: that is the
// pre-#1705 behaviour, and the only callers who get it are ones with no
// provider client to pin to.
func buildEnv(parent []string, host string) []string {
	env := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		if host != "" && (strings.HasPrefix(kv, "DOCKER_HOST=") || strings.HasPrefix(kv, "DOCKER_CONTEXT=")) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "DOCKER_BUILDKIT=1")
	if host != "" {
		env = append(env, "DOCKER_HOST="+host)
	}
	return env
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
	cmd.Env = buildEnv(os.Environ(), b.host)
	if b.host != "" {
		b.logger.Debug("docker build pinned to the provider's daemon", "docker_host", b.host, "tag", tag)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("build stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // BuildKit writes progress to stderr; merge streams

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting docker build: %w", err)
	}

	// Stream log lines so the UI can show live per-layer progress, and keep a
	// bounded tail of the same stream so a failure can say WHY (#1730). The two
	// existing sinks are both usually absent at the moment of failure — onLog is
	// nil for any caller that did not pass WithProgress, and Debug is below the
	// default level — while the returned error is the only thing that reaches
	// the operator, CI and a test log.
	tail := newBoundedLog(buildErrTailLineCap, buildErrTailByteCap)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		tail.add(line)
		if onLog != nil {
			onLog(line)
		}
		b.logger.Debug("docker build", "line", line)
	}
	// A token-too-long stream stops the loop early and would otherwise leave
	// the tail silently short — the same class of bug as the one above.
	if scanErr := scanner.Err(); scanErr != nil {
		tail.add(fmt.Sprintf("[crewship: build output truncated: %v]", scanErr))
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker build failed for %s: %w%s", tag, err, buildOutputSuffix(tail))
	}
	return nil
}

// buildOutputSuffix renders the retained build-output window for attachment to
// a build error.
//
// Scrubbed on the way out: the window is arbitrary build output, and a
// feature's `RUN` can echo a build-arg, an env var or a `curl -H` header. This
// error travels to terminals, CI logs, journal rows and WS payloads, so it goes
// through the same scrubber the durable build_failed event already uses rather
// than becoming a second, unfiltered path for the same secrets.
//
// Empty output gets a sentence instead of an empty block: a `docker` that died
// without printing anything is itself the finding (the CLI never reached a
// daemon), and an empty block reads like the output was cut.
func buildOutputSuffix(tail *boundedLog) string {
	scrubbed := buildLogScrubber.Scrub(tail.tail())
	if strings.TrimSpace(scrubbed) == "" {
		return " — the build produced no output, which usually means the docker CLI never reached a daemon"
	}
	return "\ndocker build output (tail):\n" + scrubbed
}

// buildFeatureImage stages a build context for the resolved features and builds
// an intermediate image (tagged crewship-feat:{hash}) on top of baseImage using
// BuildKit. The returned tag is used as the base for the temp container that
// then runs mise/postCreate/env. emit reports plan progress (pull + per-feature)
// so the UI checklist advances identically to the container-commit path.
func (p *Provisioner) buildFeatureImage(ctx context.Context, baseImage string, features []*ResolvedFeature, optionsByRef map[string]map[string]any, rootEnv map[string]string, emit func(string), sink ProvisionSink) (string, error) {
	emit(pullStepLabel(baseImage))

	contextDir, _, featTag, err := stageBuildContext(baseImage, features, optionsByRef, rootEnv)
	if err != nil {
		emitProvision(sink, ProvisionEvent{Step: ProvStepFailed, Status: ProvStatusFailed, Detail: ProvStepImageBuildStart, Error: err.Error()})
		return "", err
	}
	defer func() { _ = os.RemoveAll(contextDir) }()

	buildStart := time.Now()
	emitProvision(sink, ProvisionEvent{
		Step:   ProvStepImageBuildStart,
		Status: ProvStatusStarted,
		Tag:    featTag,
		Detail: featureRefsSummary(features),
	})

	// Exact-tag hit: skip invoking the builder entirely. BuildKit also caches
	// layers, but this avoids the process spawn when nothing changed.
	if exists, _ := p.imageExists(ctx, featTag); exists {
		for _, f := range features {
			emit(featureStepLabel(f.Metadata.ID))
			emitProvision(sink, ProvisionEvent{Step: ProvStepFeatureInstall, Feature: f.Metadata.ID, Status: ProvStatusCompleted, Detail: "cached"})
		}
		p.logger.Info("reusing cached feature image", "tag", featTag)
		emitProvision(sink, ProvisionEvent{Step: ProvStepImageBuildDone, Status: ProvStatusCompleted, Tag: featTag, Detail: "cache_hit", DurationMs: elapsedMs(buildStart)})
		return featTag, nil
	}

	// BuildKit bakes every feature in one invocation, so we can't observe a
	// per-feature completion mid-build. Emit `started` for each up front (so the
	// audit shows exactly which features the build covers), then `completed`
	// once the whole build lands.
	for _, f := range features {
		emit(featureStepLabel(f.Metadata.ID))
		emitProvision(sink, ProvisionEvent{Step: ProvStepFeatureInstall, Feature: f.Metadata.ID, Status: ProvStatusStarted})
	}
	p.logger.Info("building feature image via BuildKit", "tag", featTag, "features", featureRefsSummary(features))

	// Capture a bounded tail of the BuildKit log so a build failure carries the
	// output of the failing step without flooding the journal/WS on success.
	logTail := newBoundedLog(buildLogTailLineCap, buildLogTailByteCap)
	if err := p.builder.Build(ctx, contextDir, featTag, func(line string) {
		logTail.add(line)
		p.logger.Debug("buildkit", "line", line)
	}); err != nil {
		emitProvision(sink, ProvisionEvent{
			Step:       ProvStepFailed,
			Status:     ProvStatusFailed,
			Detail:     ProvStepImageBuildStart,
			Tag:        featTag,
			Error:      err.Error(),
			DurationMs: elapsedMs(buildStart),
		})
		// Persist the failing-step output as a dedicated build-failed event
		// carrying BOTH the error and the scrubbed tail. The sink routes it to
		// a durable provisioning.build_failed journal row so `crew provision
		// status` can show WHY the build failed after the live stream is gone
		// (#829) — not just the (potentially truncated) top-level Error.
		if tail := buildLogScrubber.Scrub(logTail.tail()); tail != "" {
			emitProvision(sink, ProvisionEvent{
				Step:   ProvStepBuildFailed,
				Status: ProvStatusFailed,
				Tag:    featTag,
				Error:  err.Error(),
				Detail: tail,
			})
		}
		return "", fmt.Errorf("building feature image: %w", err)
	}
	p.invalidateImageListCache()
	for _, f := range features {
		emitProvision(sink, ProvisionEvent{Step: ProvStepFeatureInstall, Feature: f.Metadata.ID, Status: ProvStatusCompleted})
	}
	emitProvision(sink, ProvisionEvent{Step: ProvStepImageBuildDone, Status: ProvStatusCompleted, Tag: featTag, DurationMs: elapsedMs(buildStart)})
	return featTag, nil
}

// buildLogTailLineCap / buildLogTailByteCap bound the trailing BuildKit log we
// retain for a failure event — enough to see the failing RUN's output, capped
// on BOTH lines and bytes so a verbose build (or a few pathologically long
// lines) never balloons memory or the durable failure payload (#829).
const (
	buildLogTailLineCap = 100
	buildLogTailByteCap = 8 * 1024
)

// buildErrTailLineCap / buildErrTailByteCap bound the window attached to the
// RETURNED error of a failed build (#1730). Deliberately tighter than the
// durable-event caps above, because the two windows are read differently: the
// event payload is a stored record someone opens on purpose, while the error
// string is printed inline into a terminal, a CI log or a `go test` failure,
// where an 8 KB wall of layer progress buries the sentence it was added to
// show. A BuildKit failure block — the failing `RUN`'s output, its
// `#N ERROR:` line, the Dockerfile excerpt and the `failed to solve` summary —
// runs about 25-30 lines, so 40 covers it with room to spare.
const (
	buildErrTailLineCap = 40
	buildErrTailByteCap = 4 * 1024
)

// boundedLog keeps only the last `lineCap` lines appended to it — a fixed-size
// tail buffer for capturing the end of a build log cheaply. tail() additionally
// clamps the joined result to byteCap bytes.
type boundedLog struct {
	lineCap int
	byteCap int
	lines   []string
}

func newBoundedLog(lineCap, byteCap int) *boundedLog {
	return &boundedLog{lineCap: lineCap, byteCap: byteCap}
}

func (b *boundedLog) add(line string) {
	b.lines = append(b.lines, line)
	if len(b.lines) > b.lineCap {
		// Re-slice to the last lineCap lines; copy to release the head storage.
		tail := make([]string, b.lineCap)
		copy(tail, b.lines[len(b.lines)-b.lineCap:])
		b.lines = tail
	}
}

// tail joins the retained lines and, if the result exceeds byteCap, keeps only
// the trailing byteCap bytes — the failing step's output is at the END — cut
// forward to the next line boundary so we never emit a partial leading line.
func (b *boundedLog) tail() string {
	joined := strings.Join(b.lines, "\n")
	if b.byteCap > 0 && len(joined) > b.byteCap {
		joined = joined[len(joined)-b.byteCap:]
		if nl := strings.IndexByte(joined, '\n'); nl >= 0 && nl+1 <= len(joined) {
			joined = joined[nl+1:]
		}
	}
	return joined
}

// stageBuildContext materializes a build context directory for the given
// features: it writes the generated Dockerfile and copies each feature's
// extracted directory to <context>/features/{id}. Returns the context dir and
// the feature-image tag derived from the Dockerfile content AND the staged
// feature file contents (so any change to base image, features, options, or a
// feature's own files yields a new tag and a clean rebuild). The caller is
// responsible for removing contextDir.
func stageBuildContext(baseImage string, features []*ResolvedFeature, optionsByRef map[string]map[string]any, rootEnv map[string]string) (contextDir, dockerfile, tag string, err error) {
	dockerfile, err = GenerateDockerfile(DockerfileBuild{
		BaseImage:    baseImage,
		Features:     features,
		OptionsByRef: optionsByRef,
		RootEnv:      rootEnv,
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

	// Derive the tag from the Dockerfile AND the staged feature contents. The
	// Dockerfile alone only captures base image + refs + options; hashing the
	// actual feature files means a feature that republishes new content under
	// the same mutable ref (e.g. ":2") yields a new tag and a clean rebuild,
	// instead of the exact-tag fast path silently reusing a stale image.
	contentHash, err := hashBuildInputs(dockerfile, features)
	if err != nil {
		cleanup()
		return "", "", "", fmt.Errorf("hashing build inputs: %w", err)
	}
	tag = FeatureImageTagPrefix + contentHash[:12]
	return contextDir, dockerfile, tag, nil
}

// hashBuildInputs returns a hex content hash over the generated Dockerfile and
// the full contents of every feature directory (path, mode, and bytes), walked
// in lexical order for determinism. Folding feature file bytes into the tag is
// what makes the cache key honor content changes, not just the Dockerfile.
func hashBuildInputs(dockerfile string, features []*ResolvedFeature) (string, error) {
	h := sha256.New()
	h.Write([]byte(dockerfile))
	for _, f := range features {
		if f == nil {
			continue
		}
		// Domain separator keyed by feature id so reordering or renaming a
		// feature changes the hash even with identical file bytes.
		fmt.Fprintf(h, "\x00feat:%s\x00", f.Metadata.ID)
		walkErr := filepath.Walk(f.Dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil // dirs/symlinks/etc. — only regular file bytes matter
			}
			rel, err := filepath.Rel(f.Dir, path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%s\x00%o\x00", rel, info.Mode().Perm())
			data, err := os.ReadFile(path) // #nosec G304 — feature-cache path we extracted
			if err != nil {
				return err
			}
			h.Write(data)
			return nil
		})
		if walkErr != nil {
			return "", walkErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
