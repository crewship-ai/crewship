package devcontainer

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The build-only provisioning path (#1779).
//
// The commit path creates a temp container, runs every provisioning step inside
// it, and commits the result. Apple Containers has no `commit` — `container
// commit` reports `Plugin 'container-commit' not found` — so on a Mac that path
// has nothing to end with, and provisioning was simply unavailable.
//
// This path produces the same image with one `build` instead. The trick that
// keeps the two from drifting is dockerfileRecorder: the provisioning steps are
// not reimplemented as Dockerfile text, they are the SAME functions, handed an
// ExecFunc that writes RUN layers instead of executing. Add a step to the commit
// path and it appears here; change one and both change.
//
// The one step that cannot come along is captureLoginPath, which reads a login
// shell's PATH out of the finished container. A build has no container to read
// from. It is best-effort by contract — the runtime falls back to prepending the
// well-known devcontainer bin dirs — so it degrades rather than blocks.

// readFileBestEffort is a tiny helper shared with the tests.
func readFileBestEffort(path string) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 — internally constructed temp path
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ImageProber is an optional ImageBuilder capability: report whether a tag is
// already in the local store. Optional rather than part of ImageBuilder because
// only engines that can wedge mid-success need it, and every existing fake
// would otherwise have to grow the method.
type ImageProber interface {
	ImageExists(ctx context.Context, tag string) (bool, error)
}

// dockerfileRecorder implements ExecFunc by recording each command as a
// Dockerfile RUN layer rather than running it.
//
// Commands are emitted in JSON exec form. Shell form would send the script
// through /bin/sh a second time, so a postCreateCommand containing a quote or a
// `$VAR` would either break the build or expand at the wrong moment — the
// commit path passes these strings to exec untouched, and so must this one.
type dockerfileRecorder struct {
	groups []recordedGroup
}

// recordedGroup is a run of consecutive commands sharing one user — one
// Dockerfile layer.
type recordedGroup struct {
	user string
	cmds []string // already shell-quoted, one command per line
}

// shellQuote wraps s in single quotes so the shell treats it as one literal
// argument, escaping embedded single quotes the only way sh allows: close the
// string, emit an escaped quote, reopen. Everything else — $, ", \, spaces,
// newlines — is inert inside single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// exec satisfies ExecFunc. It always reports success: there is nothing to run
// yet, and a step that fails does so during the build, where BuildKit stops on
// the first non-zero RUN exactly as the commit path stops on the first non-zero
// exec.
func (r *dockerfileRecorder) exec(_ context.Context, _ string, cmd []string, user string, env []string) (string, int, error) {
	if len(cmd) == 0 {
		return "", 0, nil
	}
	if user == "" || user == "root" {
		user = "0:0"
	}

	parts := make([]string, 0, len(env)+len(cmd)+1)
	if len(env) > 0 {
		// `env K=V … cmd` rather than Dockerfile ENV: these values are
		// step-scoped in the commit path, and ENV would persist them into every
		// later layer and into the runtime environment.
		parts = append(parts, "env")
		for _, kv := range env {
			parts = append(parts, shellQuote(kv))
		}
	}
	for _, a := range cmd {
		parts = append(parts, shellQuote(a))
	}
	line := strings.Join(parts, " ")

	// Extend the open group when the user matches; otherwise start a new one.
	if n := len(r.groups); n > 0 && r.groups[n-1].user == user {
		r.groups[n-1].cmds = append(r.groups[n-1].cmds, line)
		return "", 0, nil
	}
	r.groups = append(r.groups, recordedGroup{user: user, cmds: []string{line}})
	return "", 0, nil
}

// steps renders the recorded groups as Dockerfile directives.
//
// One layer per group, not per command. Every RUN layer costs ~30-60s of fixed
// overhead on Apple's builder — measured on a real run, where `printf >>
// /etc/environment` took 136.7s and a bare `mise --version` took 50.5s — so a
// layer per exec call turned a handful of seconds of actual work into a twenty
// minute build. Grouping keeps the same commands in the same order and pays the
// overhead a few times instead of fifteen.
//
// `set -e` preserves the commit path's semantics: it stops at the first
// failure, and BuildKit then fails the build on the non-zero RUN.
func (r *dockerfileRecorder) steps() []string {
	out := make([]string, 0, len(r.groups)*3)
	for _, g := range r.groups {
		script := "set -e\n" + strings.Join(g.cmds, "\n")
		encoded, err := json.Marshal([]string{"sh", "-c", script})
		if err != nil {
			// argv is []string; Marshal cannot fail.
			continue
		}
		if g.user == "0:0" {
			out = append(out, "RUN "+string(encoded))
			continue
		}
		out = append(out, "USER "+g.user, "RUN "+string(encoded), "USER root")
	}
	return out
}

// NewBuildOnlyProvisioner returns a Provisioner that can only build, for
// container runtimes with no commit. It holds no Docker client and no
// Installer, so any attempt to use the commit path would panic rather than
// silently half-provision — Provision must not be called on it.
func NewBuildOnlyProvisioner(builder ImageBuilder, downloader *FeatureDownloader, logger *slog.Logger) *Provisioner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provisioner{
		builder:    builder,
		downloader: downloader,
		logger:     logger,
	}
}

// ProvisionByBuild produces the provisioned image with a single image build.
//
// Steps mirror Provision in order: features, agent-home ownership, mise,
// feature lifecycle hooks, root postCreate, aggregated containerEnv, cache
// cleanup. Features come from GenerateDockerfile's own COPY+RUN layers (so they
// stay one cached layer each); everything after them is recorded through
// dockerfileRecorder.
func (p *Provisioner) ProvisionByBuild(ctx context.Context, baseImage string, cfg *Config, miseConfig string, opts ...ProvisionOption) (*ProvisionResult, error) {
	o := &provisionOpts{}
	for _, fn := range opts {
		fn(o)
	}
	return p.provisionByBuild(ctx, baseImage, cfg, miseConfig, o, time.Now())
}

func (p *Provisioner) provisionByBuild(ctx context.Context, baseImage string, cfg *Config, miseConfig string, o *provisionOpts, runStart time.Time) (*ProvisionResult, error) {
	emitEvt := func(ev ProvisionEvent) { emitProvision(o.onProvision, ev) }
	fail := func(step string, err error) (*ProvisionResult, error) {
		emitEvt(ProvisionEvent{Step: ProvStepFailed, Status: ProvStatusFailed, Detail: step, Error: err.Error()})
		return nil, err
	}

	if p.builder == nil || !p.builder.Available() {
		return fail(ProvStepStart, fmt.Errorf("devcontainer: no image builder available for build-only provisioning"))
	}

	resolvedFeatures, optionsByRef, err := p.resolveFeatures(ctx, cfg)
	if err != nil {
		return fail(ProvStepResolveFeatures, fmt.Errorf("resolving features: %w", err))
	}

	// Same hash the commit path computes, so an image built here is found by
	// the same cache lookup and a machine that later gains Docker does not
	// rebuild what it already has.
	hash := configHash(baseImage, cfg, miseConfig, dockerfileGenFingerprint(baseImage, cfg))
	tag := cacheImageTag(hash)

	requirements := p.aggregateFeatureRequirements(resolvedFeatures, cfg.ContainerEnv)
	requirements.PostStartCommands = append(
		requirements.PostStartCommands,
		cfg.NormalizedPostStartCommands()...,
	)

	rec := &dockerfileRecorder{}
	if err := p.recordProvisionSteps(ctx, rec, resolvedFeatures, cfg, miseConfig, requirements.ContainerEnv); err != nil {
		return fail(ProvStepImageBuildStart, err)
	}

	contextDir, err := stageBuildContextWithSteps(baseImage, resolvedFeatures, optionsByRef, cfg.ContainerEnv, rec.steps(), tag)
	if err != nil {
		return fail(ProvStepImageBuildStart, err)
	}
	defer func() { _ = os.RemoveAll(contextDir) }()

	buildStart := time.Now()
	emitEvt(ProvisionEvent{
		Step:   ProvStepImageBuildStart,
		Status: ProvStatusStarted,
		Tag:    tag,
		Detail: featureRefsSummary(resolvedFeatures),
	})
	if err := p.builder.Build(ctx, contextDir, tag, func(line string) {
		p.logger.Debug("build-only provisioning", "line", line)
	}); err != nil {
		// The builder may have produced the image and then wedged (Apple's
		// `container build` does — see AppleContainerBuilder.Build). The tag is
		// the authority on whether the work got done, so ask before failing a
		// build that actually succeeded.
		if prober, ok := p.builder.(ImageProber); ok {
			if exists, probeErr := prober.ImageExists(ctx, tag); probeErr == nil && exists {
				p.logger.Warn("image build did not exit cleanly, but its image is present — continuing",
					"tag", tag, "build_error", err)
			} else {
				return fail(ProvStepImageBuildStart, err)
			}
		} else {
			return fail(ProvStepImageBuildStart, err)
		}
	}
	emitEvt(ProvisionEvent{Step: ProvStepImageBuildDone, Status: ProvStatusCompleted, Tag: tag, DurationMs: elapsedMs(buildStart)})
	p.invalidateImageListCache()

	// A build that reports success and leaves no image is not a success.
	// Observed for real: BuildKit exported and tagged, this logged "provisioned
	// image by build", and the tag was gone from the store minutes later — the
	// runtime's image state had wedged and every write after it was lost. The
	// crew was recorded as provisioned against an image nothing could start.
	//
	// The tag is already consulted when Build FAILS, because a wedged CLI may
	// still have produced its image. This is the mirror, and it is the case
	// that lies in the dangerous direction: it marks a crew ready.
	//
	// Corroboration, not a gate: a builder that cannot answer is not a reason
	// to fail, or no non-probing builder could ever provision.
	if prober, ok := p.builder.(ImageProber); ok {
		if !p.waitForImage(ctx, prober, tag) {
			return fail(ProvStepImageBuildDone,
				fmt.Errorf("build reported success but %s never appeared in the image store", tag))
		}
	}

	p.logger.Info("provisioned image by build",
		"tag", tag,
		"features", len(resolvedFeatures),
		"privileged", requirements.Privileged,
	)
	emitEvt(ProvisionEvent{Step: ProvStepReady, Status: ProvStatusCompleted, Tag: tag, DurationMs: elapsedMs(runStart)})
	return &ProvisionResult{
		CachedImage:  tag,
		ConfigHash:   hash,
		Requirements: requirements,
		Features:     featureRecords(resolvedFeatures),
	}, nil
}

// recordProvisionSteps replays the post-feature provisioning sequence into the
// recorder. Deliberately the same functions the commit path calls.
func (p *Provisioner) recordProvisionSteps(
	ctx context.Context,
	rec *dockerfileRecorder,
	features []*ResolvedFeature,
	cfg *Config,
	miseConfig string,
	containerEnv map[string]string,
) error {
	const noContainer = "" // there is no container yet; the recorder ignores it

	if err := EnsureAgentHomeOwnership(ctx, noContainer, rec.exec); err != nil {
		return fmt.Errorf("agent home: %w", err)
	}
	if miseConfig != "" {
		if err := p.installMise(ctx, noContainer, miseConfig, rec.exec); err != nil {
			return fmt.Errorf("mise provisioning: %w", err)
		}
	}
	if err := p.runFeatureLifecycleCommands(ctx, noContainer, features, rec.exec); err != nil {
		return fmt.Errorf("feature postCreate: %w", err)
	}
	if err := p.runPostCreateCommands(ctx, noContainer, cfg, rec.exec); err != nil {
		return fmt.Errorf("postCreate: %w", err)
	}
	if err := p.writeAggregatedContainerEnv(ctx, noContainer, containerEnv, rec.exec); err != nil {
		return fmt.Errorf("containerEnv: %w", err)
	}
	if err := p.cleanupCaches(ctx, noContainer, rec.exec); err != nil {
		return fmt.Errorf("cache cleanup: %w", err)
	}
	return nil
}

// stageBuildContextWithSteps is stageBuildContext plus the recorded steps. The
// tag is passed only so a staging failure can name what it was building.
func stageBuildContextWithSteps(
	baseImage string,
	features []*ResolvedFeature,
	optionsByRef map[string]map[string]any,
	rootEnv map[string]string,
	extraSteps []string,
	tag string,
) (string, error) {
	dockerfile, err := GenerateDockerfile(DockerfileBuild{
		BaseImage:    baseImage,
		Features:     features,
		OptionsByRef: optionsByRef,
		RootEnv:      rootEnv,
		ExtraSteps:   extraSteps,
		// Directory COPY is broken on Apple's builder — see the field comment.
		FeatureArchives: true,
	})
	if err != nil {
		return "", fmt.Errorf("generating Dockerfile for %s: %w", tag, err)
	}

	contextDir, err := os.MkdirTemp("", "crewship-build-*")
	if err != nil {
		return "", fmt.Errorf("creating build context: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(contextDir) }

	if err := os.WriteFile(joinPath(contextDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		cleanup()
		return "", fmt.Errorf("writing Dockerfile: %w", err)
	}
	if err := os.MkdirAll(joinPath(contextDir, featureContextDir), 0o750); err != nil {
		cleanup()
		return "", fmt.Errorf("creating feature context dir: %w", err)
	}
	for _, f := range features {
		if f == nil {
			continue
		}
		dst := joinPath(contextDir, featureContextDir, f.Metadata.ID+".tar")
		if err := tarTree(f.Dir, dst); err != nil {
			cleanup()
			return "", fmt.Errorf("staging feature %s: %w", f.Metadata.ID, err)
		}
	}
	return contextDir, nil
}

// tarTree writes src's contents into an uncompressed tar at dst, rooted at the
// archive root so `ADD <archive> <dest>/` lands them straight in <dest>.
//
// Only regular files and directories travel; sockets, devices and symlinks are
// skipped, matching copyTree on the commit path. Modes are preserved, which
// matters because install.sh has to stay executable.
func tarTree(src, dst string) (err error) {
	out, err := os.Create(dst) // #nosec G304 — dst is an internally built temp path
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	tw := tar.NewWriter(out)
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if werr := tw.WriteHeader(hdr); werr != nil {
			return werr
		}
		if info.IsDir() {
			return nil
		}
		in, oerr := os.Open(path) // #nosec G304 — path comes from walking src
		if oerr != nil {
			return oerr
		}
		defer func() { _ = in.Close() }()
		_, cerr := io.Copy(tw, in)
		return cerr
	})
	if walkErr != nil {
		return walkErr
	}
	return tw.Close()
}

func joinPath(parts ...string) string { return strings.Join(parts, "/") }

// Default bounds for waitForImage. Overridable in tests.
const (
	defaultImageWaitTimeout  = 30 * time.Second
	defaultImageWaitInterval = 500 * time.Millisecond
)

// waitForImage reports whether tag shows up in the store within the bound.
//
// Sampling once does not work, and the reason is a conflict between two things
// this file already does. The build watchdog kills the CLI the moment BuildKit
// reports its export DONE — but the runtime is still committing the image at
// that point, so an immediate probe finds nothing. Live, that failed three
// crews whose images were all present seconds later.
//
// Bounded rather than patient: an image that never lands is a real failure, and
// waiting indefinitely would restore the hang this whole path exists to end. A
// probe that errors is treated as "cannot tell" and does not consume the
// verdict — the check corroborates, it does not gate.
func (p *Provisioner) waitForImage(ctx context.Context, prober ImageProber, tag string) bool {
	timeout := p.imageWaitTimeout
	if timeout <= 0 {
		timeout = defaultImageWaitTimeout
	}
	interval := p.imageWaitInterval
	if interval <= 0 {
		interval = defaultImageWaitInterval
	}

	deadline := time.Now().Add(timeout)
	for {
		exists, err := prober.ImageExists(ctx, tag)
		if err != nil {
			return true // cannot tell; do not fail a build on a broken probe
		}
		if exists {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return true // shutting down; not the build's fault
		case <-time.After(interval):
		}
	}
}
