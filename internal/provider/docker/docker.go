package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
)

var _ provider.ContainerProvider = (*Provider)(nil)
var _ provider.InteractiveExecProvider = (*Provider)(nil)
var _ provider.VolumeManager = (*Provider)(nil)

// Config holds Docker provider configuration for container creation and runtime selection.
type Config struct {
	RuntimeImage    string
	DefaultRuntime  string // "runc" | "runsc" (gVisor) | "kata-runtime" | "sysbox-runc"
	Network         string
	OutputBasePath  string
	ContainerPrefix string // Container name prefix (e.g. "crewship" -> "crewship-team-{slug}"). Allows multi-instance isolation.

	// SidecarBinaryPath is the host path to crewship-sidecar to bind-mount
	// into crew containers at /usr/local/bin/crewship-sidecar. Empty = no
	// bind mount (fall back to baked-in binary in the default image).
	SidecarBinaryPath string

	// EntrypointPath is the host path to entrypoint.sh to bind-mount into
	// crew containers at /usr/local/bin/entrypoint.sh. When set, the
	// container's Entrypoint is forced to that path so custom base images
	// (debian, ubuntu, etc.) use our init script instead of /bin/sh.
	EntrypointPath string
}

// DetectResult contains info about the detected container runtime.
type DetectResult struct {
	Runtime string // "docker" | "podman" | "colima" | "orbstack" | "rancher" | "nerdctl"
	Socket  string // socket path used
	Version string // server version string
}

// Provider implements provider.ContainerProvider using the Docker API.
// It auto-detects the container runtime (Docker, Podman, Colima, OrbStack, etc.)
// and manages crew containers with security isolation (non-root, cap-drop ALL).
type Provider struct {
	client   *client.Client
	cfg      Config
	logger   *slog.Logger
	detected DetectResult

	// digestResolver short-circuits repeated HEAD requests to the registry
	// for the runtime image. Shared helper (see internal/dockerutil) so the
	// provisioner uses identical semantics — one source of truth for
	// "is my local copy stale?".
	digestResolver *dockerutil.DigestResolver

	// crewLocks serializes concurrent EnsureCrewRuntime calls per crew_id.
	// Without this, a burst of N assignments to the same crew (e.g. 8
	// issues dispatched at once) races between "list containers → not
	// found" and "container create" — N-1 of them fail with
	// `Conflict: container name already in use`. Different crews still
	// run in parallel.
	crewLocks sync.Map // crew_id (string) → *sync.Mutex

	// checkVolumeMountpoint gates the host-side volume self-heal in
	// ensureVolume. The check os.Stat's the daemon-reported Mountpoint
	// (/var/lib/docker/volumes/<name>/_data), which is only reachable from
	// this process when the daemon shares the host filesystem (native Linux
	// daemon). On a VM-backed runtime (Docker Desktop / Colima / OrbStack on
	// macOS or Windows) that path never exists on the host, so the check
	// would false-positive on EVERY volume and destructively recreate it —
	// which recreates the crew container and trips the entrypoint perms bug.
	// Set by daemonSharesHostFS (daemon-locality probe) in New(); tests set it
	// explicitly.
	checkVolumeMountpoint bool
}

// lockForCrew returns the mutex for a given crew, creating it on first
// use. Cheap: load from sync.Map first, only LoadOrStore if missing.
func (p *Provider) lockForCrew(crewID string) *sync.Mutex {
	if mu, ok := p.crewLocks.Load(crewID); ok {
		return mu.(*sync.Mutex)
	}
	actual, _ := p.crewLocks.LoadOrStore(crewID, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// socketCandidate is a socket path + label for auto-detection.
type socketCandidate struct {
	path    string
	runtime string
}

// candidateSockets returns Docker-API-compatible sockets to try, in priority order.
// Covers Docker Desktop, Colima, OrbStack, Rancher Desktop, Podman (rootless/root), and nerdctl.
func candidateSockets() []socketCandidate {
	home, _ := os.UserHomeDir()
	uid := strconv.Itoa(os.Getuid())

	candidates := []socketCandidate{
		// Docker Desktop / Engine defaults
		{"/var/run/docker.sock", "docker"},
		// Colima (macOS)
		{filepath.Join(home, ".colima", "default", "docker.sock"), "colima"},
		// OrbStack (macOS)
		{filepath.Join(home, ".orbstack", "run", "docker.sock"), "orbstack"},
		// Rancher Desktop (macOS/Linux)
		{filepath.Join(home, ".rd", "docker.sock"), "rancher"},
		// Docker Desktop (macOS new path)
		{filepath.Join(home, ".docker", "run", "docker.sock"), "docker"},
		// Podman rootless
		{filepath.Join("/run/user", uid, "podman", "podman.sock"), "podman"},
		// Podman machine (macOS)
		{filepath.Join(home, ".local", "share", "containers", "podman", "machine", "podman.sock"), "podman"},
		// Podman root
		{"/run/podman/podman.sock", "podman"},
		// containerd/nerdctl
		{"/run/containerd/containerd.sock", "nerdctl"},
	}

	return candidates
}

// socketPingTimeout is the per-socket timeout for the Docker Ping call.
// Short enough that multiple failing sockets don't block the overall detection.
const socketPingTimeout = 1500 * time.Millisecond

// Detect probes for a Docker-API-compatible socket and returns info about
// the detected runtime. It checks DOCKER_HOST first, then iterates candidate
// sockets (Docker, Colima, OrbStack, Rancher, Podman, nerdctl). The ctx
// parameter is used as an outer deadline; each socket gets its own short timeout.
func Detect(ctx context.Context) (*DetectResult, error) {
	// If DOCKER_HOST is set, use that directly.
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("docker client (DOCKER_HOST=%s): %w", host, err)
		}
		defer cli.Close()
		info, err := cli.Ping(ctx)
		if err != nil {
			return nil, fmt.Errorf("docker ping (DOCKER_HOST=%s): %w", host, err)
		}
		rt := "docker"
		if strings.Contains(info.APIVersion, "libpod") {
			rt = "podman"
		}
		sv, _ := cli.ServerVersion(ctx)
		ver := sv.Version
		// Podman masquerades as Docker -- check server components
		for _, comp := range sv.Components {
			if strings.EqualFold(comp.Name, "Podman Engine") {
				rt = "podman"
				ver = comp.Version
			}
		}
		return &DetectResult{Runtime: rt, Socket: host, Version: ver}, nil
	}

	// Try candidate sockets in order, using a short per-socket timeout so a
	// hung daemon (socket file exists but daemon unresponsive) doesn't block
	// the entire detection for the full outer context deadline.
	for _, c := range candidateSockets() {
		if _, err := os.Stat(c.path); err != nil {
			continue
		}
		cli, err := client.NewClientWithOpts(
			client.WithHost("unix://"+c.path),
			client.WithAPIVersionNegotiation(),
		)
		if err != nil {
			continue
		}

		// Per-socket timeout: bail quickly if daemon is unresponsive.
		pingCtx, cancel := context.WithTimeout(ctx, socketPingTimeout)
		_, pingErr := cli.Ping(pingCtx)
		cancel()
		if pingErr != nil {
			cli.Close()
			continue
		}

		sv, _ := cli.ServerVersion(ctx)
		ver := sv.Version
		rt := c.runtime
		// Podman masquerades as Docker -- check server components
		for _, comp := range sv.Components {
			if strings.EqualFold(comp.Name, "Podman Engine") {
				rt = "podman"
				ver = comp.Version
			}
		}
		cli.Close()
		return &DetectResult{Runtime: rt, Socket: c.path, Version: ver}, nil
	}

	return nil, fmt.Errorf("no Docker-compatible runtime found (tried Docker, Podman, Colima, OrbStack, Rancher Desktop)")
}

// New creates a Provider by auto-detecting the container runtime and
// establishing a Docker API client connection. Returns an error if no
// compatible runtime is found.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	detected, detectErr := Detect(ctx)
	if detectErr != nil {
		return nil, fmt.Errorf("container runtime: %w", detectErr)
	}

	// Build client options based on detected socket.
	var opts []client.Opt
	if os.Getenv("DOCKER_HOST") != "" {
		opts = append(opts, client.FromEnv)
	} else {
		opts = append(opts, client.WithHost("unix://"+detected.Socket))
	}
	opts = append(opts, client.WithAPIVersionNegotiation())

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	if _, err := cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("docker ping: %w", err)
	}

	p := &Provider{
		client:         cli,
		cfg:            cfg,
		logger:         logger,
		detected:       *detected,
		digestResolver: dockerutil.NewDigestResolver(0, 0), // package defaults
		// The host-side volume self-heal only makes sense when the daemon
		// shares this process's filesystem. Detect that by probing the
		// daemon's reported storage root rather than guessing from GOOS —
		// Docker Desktop on Linux is VM-backed too, so a host-OS check would
		// false-positive there.
		checkVolumeMountpoint: daemonSharesHostFS(ctx, cli),
	}

	logger.Info("container runtime detected",
		"runtime", detected.Runtime,
		"version", detected.Version,
		"socket", detected.Socket,
	)

	if cfg.Network != "" {
		if err := p.ensureNetwork(ctx, cfg.Network); err != nil {
			logger.Warn("failed to create docker network", "network", cfg.Network, "error", err)
		}
	}

	return p, nil
}

// Detected returns info about the detected container runtime.
func (p *Provider) Detected() DetectResult {
	return p.detected
}

// DockerClient returns the underlying Docker SDK client. Used for low-level
// operations (image commits, container create/start/commit/remove during
// provisioning) that aren't part of the ContainerProvider interface.
func (p *Provider) DockerClient() *client.Client {
	return p.client
}

// ensureNetwork creates the Docker bridge network if it doesn't already exist.
func (p *Provider) ensureNetwork(ctx context.Context, name string) error {
	networks, err := p.client.NetworkList(ctx, dockernetwork.ListOptions{})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	for _, n := range networks {
		if n.Name == name {
			return nil
		}
	}
	_, err = p.client.NetworkCreate(ctx, name, dockernetwork.CreateOptions{
		Driver: "bridge",
	})
	if err != nil {
		return fmt.Errorf("create network: %w", err)
	}
	p.logger.Info("created docker network", "network", name)
	return nil
}

// localCacheImagePrefix tags devcontainer cache images the provisioner
// commits locally (e.g. "crewship-cache:0d08da4b8ac3"). These images exist
// ONLY in the local Docker daemon — they are never pushed to any registry —
// so a missing one can never be recovered by ImagePull.
const localCacheImagePrefix = "crewship-cache:"

// ErrCachedImageMissing signals that a crew's recorded local-cache image
// (crewship-cache:*) is absent from the local Docker daemon. Because that tag
// lives in no registry, pulling it is impossible; the crew must be
// re-provisioned (`crewship crew provision <slug>`) to rebuild it. Callers can
// detect this with errors.Is so they route to reprovisioning / a clear
// "rebuild your environment" message instead of surfacing the opaque registry
// error "pull access denied for crewship-cache, repository does not exist".
var ErrCachedImageMissing = errors.New("cached devcontainer image missing locally; crew needs reprovisioning")

// imagePresentLocally reports whether ref exists in the local image store.
// Best-effort: any inspect failure (not-found, transport error, timeout) is
// treated as "not present" so callers fall through to their not-present path.
// Bounded by a short timeout so a wedged daemon can't block the caller.
func (p *Provider) imagePresentLocally(ctx context.Context, ref string) bool {
	present, err := p.ImagePresentLocally(ctx, ref)
	return err == nil && present
}

// ImagePresentLocally reports whether ref exists in the local image store,
// distinguishing "definitely absent" (false, nil) from "couldn't tell"
// (false, err). Exported so higher layers — notably the chatbridge
// auto-provision gate — can detect a pruned cached image and re-provision
// rather than letting the run path hit the dead crewship-cache:* tag.
func (p *Provider) ImagePresentLocally(ctx context.Context, ref string) (bool, error) {
	inspectCtx, cancel := context.WithTimeout(ctx, dockerutil.DefaultHeadTimeout)
	defer cancel()
	if _, err := p.client.ImageInspect(inspectCtx, ref); err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ensureImage makes sure the agent runtime image is present locally and, when
// reachable, matches the current remote manifest digest. Mirrors the
// provisioner's ensureImage (internal/devcontainer/provisioner.go): a purely
// tag-based match would silently reuse a stale `:latest` tag across hosts
// with identical configs, breaking reproducibility for shared base images.
//
// Resolution order:
//  1. HEAD manifest on remote registry (best-effort, ≤runtimeImageHeadTimeout,
//     cached for runtimeDigestTTL).
//  2. ImageInspect locally for RepoDigests.
//  3. Local present AND RepoDigests contain the remote digest → done.
//  4. Otherwise → pull. Offline with a local image is accepted (warn + reuse).
func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	// Local-only cache images (crewship-cache:<hash>) are built and committed
	// by the devcontainer provisioner and exist in NO registry. If such a tag
	// is missing locally (pruned), an ImagePull can never succeed — it fails
	// with the opaque "pull access denied for crewship-cache, repository does
	// not exist", leaving the crew permanently broken. Short-circuit BEFORE
	// any registry interaction: present → done; absent → typed sentinel so the
	// caller can re-provision instead of attempting an impossible pull.
	if strings.HasPrefix(ref, localCacheImagePrefix) {
		if p.imagePresentLocally(ctx, ref) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrCachedImageMissing, ref)
	}

	remoteDigest := p.digestResolver.Remote(ctx, ref)

	// Cap ImageInspect with a short timeout. Older Docker Desktop versions
	// could block indefinitely on remote registry references — the timeout
	// treats that as "unknown local state" and lets the pull path decide.
	inspectCtx, cancel := context.WithTimeout(ctx, dockerutil.DefaultHeadTimeout)
	defer cancel()
	inspect, inspectErr := p.client.ImageInspect(inspectCtx, ref)
	localPresent := inspectErr == nil
	if localPresent && remoteDigest != "" && dockerutil.RepoDigestsContain(inspect.RepoDigests, remoteDigest) {
		return nil
	}
	if localPresent && remoteDigest == "" {
		// Offline or auth-gated registry; trust local presence.
		p.logger.Debug("runtime image present locally; skipping pull (remote digest unavailable)", "ref", ref)
		return nil
	}

	action := "pulling agent runtime image"
	if localPresent {
		action = "local runtime image stale, re-pulling"
	}
	p.logger.Info(action, "image", ref, "remote_digest", remoteDigest)
	reader, err := p.client.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		if localPresent {
			p.logger.Warn("runtime image pull failed; proceeding with local (possibly stale) copy",
				"image", ref, "error", err)
			return nil
		}
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer reader.Close()
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("drain pull stream for %s: %w", ref, err)
	}
	p.logger.Info("agent runtime image pulled", "image", ref)
	return nil
}

// crewResourceName builds a globally-unique, Docker-safe name for a crew's
// container or named volumes.
//
// Audit finding C1 (CRITICAL): a single crewshipd daemon serves many
// workspaces, but crew slug is unique only per workspace
// (crews UNIQUE(workspace_id, slug)). Keying container + home/tools volume
// names on the slug alone made two tenants with an identically-named crew
// (e.g. "backend") resolve to the SAME container and the SAME persistent
// home/tools volumes — cross-tenant read of ~/.ssh, /secrets and /workspace,
// or cross-tenant DoS. The crew id is a globally-unique CUID (the same value
// used to scope the per-crew bind mounts under OutputBasePath), so folding it
// into the name disambiguates tenants. The slug is kept as a human-readable
// segment for operators; the id is the part that guarantees uniqueness.
//
// Components are validated upstream (EnsureCrewRuntime runs both id and slug
// through safepath.ValidateComponent before any name is built), so the result
// only ever contains DNS/Docker-safe characters. Empty segments are dropped so
// callers that pass only an id (slug unset) still get a stable name.
func (p *Provider) crewResourceName(kind, id, slug string) string {
	prefix := p.cfg.ContainerPrefix
	if prefix == "" {
		prefix = "crewship"
	}
	parts := []string{prefix, kind}
	if slug != "" {
		parts = append(parts, slug)
	}
	if id != "" {
		parts = append(parts, id)
	}
	return strings.Join(parts, "-")
}

// CrewContainerName returns the container name for a crew. It incorporates the
// globally-unique crew id (not the per-workspace slug alone) to prevent
// cross-tenant container collisions — see crewResourceName / audit C1.
func (p *Provider) CrewContainerName(id, slug string) string {
	return p.crewResourceName("team", id, slug)
}

// homeVolumeName returns the Docker named volume name for a crew's persistent
// home directory, namespaced by crew id (audit C1).
func (p *Provider) homeVolumeName(id, slug string) string {
	return p.crewResourceName("home", id, slug)
}

// toolsVolumeName returns the Docker named volume name for a crew's persistent
// tools directory, namespaced by crew id (audit C1).
func (p *Provider) toolsVolumeName(id, slug string) string {
	return p.crewResourceName("tools", id, slug)
}

// buildMounts returns the full list of mounts for a crew container, including
// persistent bind mounts and named volumes for home/tools directories.
//
// Sidecar + entrypoint bind mounts are mandatory: the legacy agent-runtime
// image (which baked them in) was retired, so any user-provided base image
// needs them injected from the host. Returns an error if the config is
// missing either path — the operator should run 'make build:sidecar' or
// set CREWSHIP_SIDECAR_PATH / CREWSHIP_ENTRYPOINT_PATH.
func (p *Provider) buildMounts(id, slug, workspacePath, outputPath, crewPath, secretsPath string) ([]mount.Mount, error) {
	if p.cfg.SidecarBinaryPath == "" {
		return nil, fmt.Errorf("docker provider: SidecarBinaryPath is required (run 'make build:sidecar' or set CREWSHIP_SIDECAR_PATH)")
	}
	if p.cfg.EntrypointPath == "" {
		return nil, fmt.Errorf("docker provider: EntrypointPath is required (run 'make build:sidecar' or set CREWSHIP_ENTRYPOINT_PATH)")
	}
	mounts := []mount.Mount{
		{Type: mount.TypeBind, Source: workspacePath, Target: "/workspace"},
		{Type: mount.TypeBind, Source: outputPath, Target: "/output"},
		{Type: mount.TypeBind, Source: crewPath, Target: "/crew"},
		{Type: mount.TypeBind, Source: secretsPath, Target: "/secrets"},
	}
	if slug != "" {
		mounts = append(mounts,
			mount.Mount{Type: mount.TypeVolume, Source: p.homeVolumeName(id, slug), Target: "/home/agent"},
			mount.Mount{Type: mount.TypeVolume, Source: p.toolsVolumeName(id, slug), Target: "/opt/crew-tools"},
		)
	}
	mounts = append(mounts,
		mount.Mount{
			Type:     mount.TypeBind,
			Source:   p.cfg.SidecarBinaryPath,
			Target:   "/usr/local/bin/crewship-sidecar",
			ReadOnly: true,
		},
		mount.Mount{
			Type:     mount.TypeBind,
			Source:   p.cfg.EntrypointPath,
			Target:   "/usr/local/bin/entrypoint.sh",
			ReadOnly: true,
		},
	)
	return mounts, nil
}

// daemonSharesHostFS reports whether the Docker daemon's storage lives on THIS
// process's filesystem (a native daemon) rather than inside a VM or on a remote
// host. Only then can ensureVolume's self-heal meaningfully os.Stat a volume's
// reported Mountpoint. Detected by probing the daemon's own DockerRootDir:
// reachable (exists, or EACCES = exists-but-root-owned) ⇒ local; ENOENT or an
// Info() failure ⇒ VM-backed/remote, where we must trust VolumeInspect instead.
// This is more reliable than a GOOS check (Docker Desktop on Linux is VM-backed
// too).
func daemonSharesHostFS(ctx context.Context, cli *client.Client) bool {
	info, err := cli.Info(ctx)
	if err != nil || info.DockerRootDir == "" {
		return false
	}
	if _, statErr := os.Stat(info.DockerRootDir); statErr == nil || !os.IsNotExist(statErr) {
		return true
	}
	return false
}

// ensureVolume creates a Docker named volume if it doesn't already
// exist. If Docker's metadata claims the volume exists but its on-disk
// Mountpoint is missing (e.g. operator wiped /var/lib/docker/volumes/X
// while the daemon kept the metadata, or a partial-cleanup race),
// ContainerCreate later fails with "failed to populate volume: error
// evaluating symlinks from mount source …: no such file or directory"
// and VolumeCreate is a no-op because Docker thinks it's fine. Detect
// that state by inspecting the Mountpoint, force-remove, then recreate.
// (Issue #536.)
func (p *Provider) ensureVolume(ctx context.Context, name string) error {
	if existing, err := p.client.VolumeInspect(ctx, name); err == nil {
		// Docker tracks the volume. Confirm the backing directory
		// actually exists before trusting the metadata — on a healthy
		// host Mountpoint is e.g. /var/lib/docker/volumes/<name>/_data
		// and points at a real directory. If it's gone, this volume
		// will misbehave at next mount; rebuild it before continuing.
		//
		// `os.Stat` from a non-root daemon can return EACCES because
		// /var/lib/docker/volumes is typically root-owned 0700. EACCES
		// means "I can't see inside, but the directory exists" — that's
		// fine, Docker itself can still mount it, so we treat it as
		// healthy. Only ENOENT means the backing _data is genuinely
		// gone and we need to rebuild.
		// On a VM-backed daemon (macOS/Windows; always when not host-local) the
		// daemon-reported Mountpoint is not reachable from this process, so the
		// host-side self-heal can't run — trust the daemon's "exists" and skip
		// it. Without this, every provision would false-positive and recreate.
		if !p.checkVolumeMountpoint {
			return nil
		}
		if existing.Mountpoint != "" {
			if _, statErr := os.Stat(existing.Mountpoint); statErr == nil {
				return nil
			} else if !os.IsNotExist(statErr) {
				// Other stat errors (EACCES, EIO) — assume volume is
				// healthy because we lack the perms to disprove it.
				// Misclassifying a corrupt volume as healthy is the
				// safer error mode here; ContainerCreate will surface
				// the real symptom when the mount actually fails.
				return nil
			}
			p.logger.Warn("docker volume mountpoint missing on disk; recreating",
				"volume", name, "mountpoint", existing.Mountpoint)
			if rmErr := p.client.VolumeRemove(ctx, name, true); rmErr != nil {
				return fmt.Errorf("volume remove (mountpoint vanished) %s: %w", name, rmErr)
			}
		}
	}
	_, err := p.client.VolumeCreate(ctx, volume.CreateOptions{
		Name: name,
		Labels: map[string]string{
			"managed-by": "crewship",
		},
	})
	if err != nil {
		return fmt.Errorf("volume create %s: %w", name, err)
	}
	return nil
}

// RemoveCrewVolumes removes persistent named volumes for a crew (home + tools).
// Names are namespaced by the globally-unique crew id (audit C1) so a tenant
// can only ever target its own volumes.
func (p *Provider) RemoveCrewVolumes(ctx context.Context, id, slug string) error {
	for _, name := range []string{p.homeVolumeName(id, slug), p.toolsVolumeName(id, slug)} {
		if err := p.client.VolumeRemove(ctx, name, true); err != nil {
			p.logger.Warn("volume remove failed", "volume", name, "error", err)
		}
	}
	return nil
}

// Exec runs a command inside a container via Docker exec. Returns a reader
// for the combined stdout/stderr stream.
func (p *Provider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	execCfg := container.ExecOptions{
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
		User:         cfg.User,
	}
	if execCfg.User == "" {
		execCfg.User = "1001:1001"
	}

	exec, err := p.client.ContainerExecCreate(ctx, cfg.ContainerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}

	resp, err := p.client.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		defer resp.Close()
		_, _ = stdcopy.StdCopy(pw, pw, resp.Reader)
	}()

	return &provider.ExecResult{
		ExecID: exec.ID,
		Reader: pr,
	}, nil
}

// ExecInspect checks if an exec process is still running and returns its exit code.
func (p *Provider) ExecInspect(ctx context.Context, execID string) (bool, int, error) {
	resp, err := p.client.ContainerExecInspect(ctx, execID)
	if err != nil {
		return false, 0, fmt.Errorf("exec inspect: %w", err)
	}
	return resp.Running, resp.ExitCode, nil
}

// ExecInteractive creates an interactive TTY exec session with bidirectional I/O.
// Unlike Exec(), this supports stdin and returns a raw connection for terminal use.
func (p *Provider) ExecInteractive(ctx context.Context, cfg provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	execCfg := container.ExecOptions{
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		User:         cfg.User,
	}
	if execCfg.User == "" {
		execCfg.User = "1001:1001"
	}

	exec, err := p.client.ContainerExecCreate(ctx, cfg.ContainerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("exec interactive create: %w", err)
	}

	resp, err := p.client.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{Tty: true})
	if err != nil {
		return nil, fmt.Errorf("exec interactive attach: %w", err)
	}

	// Set initial terminal size.
	if cfg.Rows > 0 && cfg.Cols > 0 {
		_ = p.client.ContainerExecResize(ctx, exec.ID, container.ResizeOptions{
			Height: uint(cfg.Rows),
			Width:  uint(cfg.Cols),
		})
	}

	return &provider.InteractiveExecResult{
		ExecID: exec.ID,
		Conn:   resp.Conn,
	}, nil
}

// ExecResize changes the terminal dimensions of a running interactive exec session.
func (p *Provider) ExecResize(ctx context.Context, execID string, rows, cols uint16) error {
	return p.client.ContainerExecResize(ctx, execID, container.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

// HostAddress returns the hostname that containers should use to reach the host.
// Docker injects "host.docker.internal" via ExtraHosts in container creation.
func (p *Provider) HostAddress() string {
	return "host.docker.internal"
}

// ContainerIP returns the IPv4 address a container has on the given Docker
// network. Used by the port-expose reverse proxy to reach into a crew
// container. Returns an error if the container is not attached to that
// network, which doubles as an anti-spoof check: an agent can't ask us to
// expose a container sitting on some unrelated bridge.
func (p *Provider) ContainerIP(ctx context.Context, containerID, network string) (string, error) {
	inspect, err := p.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspect container %s on network %q: %w", containerID, network, err)
	}
	if inspect.NetworkSettings == nil {
		return "", fmt.Errorf("container %s has no network settings", containerID)
	}
	net, ok := inspect.NetworkSettings.Networks[network]
	if !ok || net == nil || net.IPAddress == "" {
		return "", fmt.Errorf("container %s not attached to network %q", containerID, network)
	}
	return net.IPAddress, nil
}

// CopyToContainer copies a tar archive into the container filesystem at dstPath.
func (p *Provider) CopyToContainer(ctx context.Context, containerID string, dstPath string, content io.Reader) error {
	return p.client.CopyToContainer(ctx, containerID, dstPath, content, container.CopyToContainerOptions{})
}

// Close releases the Docker API client connection.
func (p *Provider) Close() error {
	return p.client.Close()
}

// boolPtrIf returns a pointer to true if b is true, else nil. Used for
// HostConfig.Init which accepts *bool (nil = default, true = force init).
func boolPtrIf(b bool) *bool {
	if !b {
		return nil
	}
	return &b
}
