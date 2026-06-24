package devcontainer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"

	dockerclient "github.com/docker/docker/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
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
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...dockerclient.ImageInspectOption) (image.InspectResponse, error)
}

// ProgressCallback receives provisioning progress updates. Called synchronously
// from Provision(); implementations must be cheap and non-blocking. Step counts
// from 1 to total inclusive; total is fixed for the duration of one Provision
// call. Message exactly matches the corresponding entry in the step plan
// emitted via WithPlan, so a UI can drive a checklist by string equality.

type ProgressCallback func(step int, total int, message string)

// PlanCallback receives the full ordered list of step labels at the very start
// of a Provision call (before the first ProgressCallback fires). Lets a UI
// render the complete checklist immediately — done/active/pending — instead
// of revealing it one row at a time. Only fires for the actual provisioning
// path; cache hits and skip-path runs don't emit a plan because there's
// nothing meaningful to checklist.

type PlanCallback func(steps []string)

// ProvisionOption configures a single Provision call. Use the With* helpers
// rather than constructing the underlying type directly.

type ProvisionOption func(*provisionOpts)

type provisionOpts struct {
	onProgress ProgressCallback
	onPlan     PlanCallback
}

// WithProgress attaches a progress callback to a Provision call. The callback
// fires on coarse-grained milestones (pull, each feature install, mise install,
// commit) — not on raw BuildKit log lines, which would overwhelm a UI.
func WithProgress(cb ProgressCallback) ProvisionOption {
	return func(o *provisionOpts) { o.onProgress = cb }
}

// WithPlan attaches a one-shot plan callback. Fires once at the start of a
// real provisioning run with the full ordered list of step labels. Each label
// matches verbatim with the message later passed to WithProgress for that
// step — so a UI can map "incoming progress message" → "checklist row" by
// exact string equality.
func WithPlan(cb PlanCallback) ProvisionOption {
	return func(o *provisionOpts) { o.onPlan = cb }
}

// Provisioner orchestrates the full devcontainer provisioning flow: create a
// temporary container from the base image, install features, run post-create

// commands, and commit the result as a cached image.
type Provisioner struct {
	docker     CommitClient
	installer  *Installer
	downloader *FeatureDownloader
	logger     *slog.Logger

	// digestResolver caches remote manifest digests used by ensureImage. Shared
	// helper (see internal/dockerutil) so the runtime Provider uses identical
	// semantics — one source of truth for "is my local copy stale?".
	digestResolver *dockerutil.DigestResolver

	// Cache of ImageList results to avoid an O(n) Docker call on every
	// imageExists/Provision check. Invalidated on Pull/Commit (our own
	// mutations) and on TTL expiry (external mutations via `docker rmi` etc.).
	// Short TTL so disk reclaim or manual rmi is reflected quickly.
	imageListMu    sync.Mutex
	imageListCache imageListCacheEntry
}

// TempContainerLabelKey and TempContainerLabelValue identify temporary
// containers created by Provisioner.createTempContainer. Exported so the
// orphan-temp sweeper (internal/api/crew_provisioning.go) can filter on them.
const (
	TempContainerLabelKey   = "crewship.temp"
	TempContainerLabelValue = "provision"
)

// ProvisionResult contains the output of a successful provisioning run.
type ProvisionResult struct {
	CachedImage  string                 // e.g. "crewship-cache:a1b2c3d4e5f6"
	ConfigHash   string                 // full SHA-256 hex digest
	Requirements AggregatedRequirements // runtime requirements bubbled up from features
}

// AggregatedRequirements contains runtime requirements bubbled up from the
// set of features a crew installs. These must reach the runtime HostConfig
// for features like DinD to actually work (privileged + docker.sock mount).
//
// PostStartCommands are feature-declared postStartCommand hooks concatenated
// in install order; the root-level postStartCommand is appended by the
// runtime resolver (not here) so that root hooks run last — matching the
// devcontainer spec where user customizations override feature defaults.

type AggregatedRequirements struct {
	ContainerEnv      map[string]string `json:"containerEnv,omitempty"`
	Mounts            []FeatureMount    `json:"mounts,omitempty"`
	Privileged        bool              `json:"privileged,omitempty"`
	Init              bool              `json:"init,omitempty"`
	CapAdd            []string          `json:"capAdd,omitempty"`
	SecurityOpt       []string          `json:"securityOpt,omitempty"`
	PostStartCommands []string          `json:"postStartCommands,omitempty"`
}

// aggregateFeatureRequirements merges runtime requirements across features.
// Root-level containerEnv takes precedence over feature-declared values.
// Privileged/Init are OR-ed; CapAdd/SecurityOpt are concatenated (callers may
// dedupe when applying to HostConfig). Feature-level postStartCommand hooks
// are flattened in install order.

func NewProvisioner(docker CommitClient, installer *Installer, downloader *FeatureDownloader, logger *slog.Logger) *Provisioner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provisioner{
		docker:         docker,
		installer:      installer,
		downloader:     downloader,
		logger:         logger,
		digestResolver: dockerutil.NewDigestResolver(0, 0), // package defaults
	}
}

// Step label helpers — kept centralized so the plan emitted via WithPlan and
// the per-step messages emitted via WithProgress always agree on exact text.
// The UI matches incoming progress messages against plan entries by string
// equality to drive the checklist; if these ever drift, every row sits stuck
// on "pending" forever.

const (
	miseStepLabel   = "Installing language runtimes"
	commitStepLabel = "Committing image"
)

func pullStepLabel(baseImage string) string {
	return "Pulling base image " + baseImage
}

func featureStepLabel(featureID string) string {
	return "Installing " + featureID
}

// featureLeafID extracts the leaf name from a feature reference.
//   ghcr.io/devcontainers/features/python:1 → "python"
//   common-utils:2                          → "common-utils"
// The leaf is what we display in the checklist and what install.sh-emitting
// features identify themselves by; matches `feature.Metadata.ID` after
// download for every feature we've seen in the wild.
func featureLeafID(ref string) string {
	// Drop a tag suffix.
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		ref = ref[:idx]
	}
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		return ref[idx+1:]
	}
	return ref
}

// commonUtilsFirst stable-sorts feature refs so any common-utils variant
// leads. The provisioning plan shown in the UI is built before features are
// downloaded (so progress appears immediately), so it can't run the full
// SortFeatures topological sort — but it can cheaply guarantee the one
// ordering users actually notice and that SortFeatures also enforces at
// install time: common-utils, which creates the agent user, comes first. The
// UI checklist matches steps to the plan by string equality, so plan order
// must mirror install order or rows stick on "pending".
func commonUtilsFirst(refs []string) {
	sort.SliceStable(refs, func(i, j int) bool {
		return isCommonUtilsRef(refs[i]) && !isCommonUtilsRef(refs[j])
	})
}

// provisionerSchemaVersion invalidates all cached images when this changes.
// Bump when:
//   - Provisioning logic changes meaningfully (feature install order, env injection, etc.)
//   - Core mount layout changes
//   - Any backward-incompatible runtime change

func (p *Provisioner) Provision(ctx context.Context, baseImage string, cfg *Config, miseConfig string, opts ...ProvisionOption) (*ProvisionResult, error) {
	o := &provisionOpts{}
	for _, fn := range opts {
		fn(o)
	}

	hash := configHash(baseImage, cfg, miseConfig)
	tag := cacheImageTag(hash)

	// 1. Check cache.
	exists, err := p.imageExists(ctx, tag)
	if err != nil {
		return nil, err
	}
	if exists {
		p.logger.Info("using cached image", "tag", tag)
		if o.onProgress != nil {
			o.onProgress(1, 1, "Using cached image")
		}
		return &ProvisionResult{CachedImage: tag, ConfigHash: hash}, nil
	}

	// Skip provisioning if no features, no postCreateCommand, no containerEnv, and no mise config.
	if len(cfg.Features) == 0 && cfg.PostCreateCommand == nil && len(cfg.ContainerEnv) == 0 && miseConfig == "" {
		p.logger.Debug("skipping provisioning - config has no customizations")
		if o.onProgress != nil {
			o.onProgress(1, 1, "No customizations needed")
		}
		return &ProvisionResult{CachedImage: "", ConfigHash: hash}, nil
	}

	p.logger.Info("provisioning devcontainer image", "base", baseImage, "tag", tag)

	// Compute the step plan up front so the UI can render a stable
	// checklist (done / active / pending). Granularity matches what we
	// actually emit below: pull + one per feature + (mise as a single
	// bucket) + commit. We can't run the full SortFeatures topological
	// sort here (features aren't downloaded yet), so the plan is
	// alphabetical — except common-utils, which SortFeatures always
	// installs first and which we hoist to the front here too so the
	// string-matched UI checklist lines up. Any remaining reordering from
	// installsAfter is a trivial UX cost compared to downloading every
	// feature before showing any progress.
	featureRefs := make([]string, 0, len(cfg.Features))
	for ref := range cfg.Features {
		featureRefs = append(featureRefs, ref)
	}
	sort.Strings(featureRefs)
	commonUtilsFirst(featureRefs)
	plan := make([]string, 0, 2+len(cfg.Features))
	plan = append(plan, pullStepLabel(baseImage))
	for _, ref := range featureRefs {
		plan = append(plan, featureStepLabel(featureLeafID(ref)))
	}
	if miseConfig != "" {
		plan = append(plan, miseStepLabel)
	}
	plan = append(plan, commitStepLabel)
	total := len(plan)

	if o.onPlan != nil {
		// Defensive copy — the callback may store the slice and we mutate
		// `plan` no further, but a clone is cheap insurance against future
		// edits creating an alias.
		dup := make([]string, len(plan))
		copy(dup, plan)
		o.onPlan(dup)
	}

	step := 0
	emit := func(message string) {
		step++
		if o.onProgress != nil {
			o.onProgress(step, total, message)
		}
	}

	emit(pullStepLabel(baseImage))

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
	resolvedFeatures, err := p.installFeatures(ctx, containerID, cfg, func(featureID string) {
		emit(featureStepLabel(featureID))
	})
	if err != nil {
		return nil, err
	}

	// 5. Handle mise configuration.
	if miseConfig != "" {
		emit(miseStepLabel)
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
	// Append root-level postStartCommand hooks after feature hooks — user
	// intent wins over feature defaults.
	requirements.PostStartCommands = append(
		requirements.PostStartCommands,
		cfg.NormalizedPostStartCommands()...,
	)
	if err := p.writeAggregatedContainerEnv(ctx, containerID, requirements.ContainerEnv); err != nil {
		return nil, err
	}

	// 8. Clean up caches inside the container.
	if err := p.cleanupCaches(ctx, containerID); err != nil {
		p.logger.Warn("cache cleanup failed", "error", err)
	}

	// 9. Commit the container as a cached image.
	emit(commitStepLabel)
	_, commitErr := p.docker.ContainerCommit(ctx, containerID, container.CommitOptions{
		Reference: tag,
	})
	if commitErr != nil {
		return nil, fmt.Errorf("committing container: %w", commitErr)
	}
	// New crewship-cache:* tag is now present locally — drop cached list.
	p.invalidateImageListCache()

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
// configured for provisioning (root user, writable filesystem).
//
// Deliberate asymmetry vs. runtime (EnsureCrewRuntime):
//   - Provisioning runs as root with mutable rootfs — install.sh scripts must
//     write to /usr, /etc, /var. The committed image then runs read-only at
//     runtime.
//   - ExtraHosts mirrors the runtime HostConfig so features that probe the
//     host (npm proxy, mise's internal registry lookups, curl-based download
//     scripts) behave consistently between provision and runtime.
//
// If a feature's FeatureMetadata requires ReadonlyRootfs we ignore it for the
// provisioning phase (contradicts the write-phase goal); the flag is honoured
// at runtime via AggregatedRequirements instead.
//
// Note: do NOT mount /tmp as tmpfs — Docker's CopyToContainer has issues
// finding paths created via exec inside tmpfs mounts. The container's normal
// /tmp (union filesystem layer) works correctly with both exec and
// CopyToContainer.
