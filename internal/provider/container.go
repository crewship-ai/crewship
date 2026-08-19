package provider

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// CrewRef identifies a crew by its globally-unique id and workspace slug. The
// legacy-resource detector/pruner take a list so they can both TARGET the
// slug-only legacy names and PROTECT the live id-scoped names (a slug equal to
// another crew's "<slug>-<id>" string would otherwise collide).
type CrewRef struct {
	ID   string
	Slug string
}

// CrewConfig describes the resource requirements and network policy for a
// crew's container runtime.
type CrewConfig struct {
	ID       string
	Slug     string
	MemoryMB int
	CPUs     float64
	// Passed through for orchestrator/sidecar layer; not consumed by providers directly yet.
	NetworkMode    string   // "free" (default) or "restricted"
	AllowedDomains []string // domains allowed when NetworkMode is "restricted"
	TTLHours       int      // auto-stop after idle period; 0 = no TTL
	Image          string   // custom runtime image; empty = provider default
	CachedImage    string   // provisioned Docker image tag; empty = use Image or default
	// ContainerEnv is extra env vars from devcontainer.json containerEnv.
	// CREWSHIP_* keys are reserved for platform-managed vars and silently
	// skipped. Providers merge these into the container's Env at create time.
	ContainerEnv map[string]string

	// LoginPath is the agent user's full login-shell PATH, captured at
	// provision time (AggregatedRequirements.LoginPath) and persisted via
	// cached_requirements. When set, the provider sets it verbatim as the
	// container's PATH so a non-login `docker exec` (how the agent CLI runs)
	// sees feature/pipx tool dirs like /usr/local/py-utils/bin, which are
	// otherwise only added for login shells via /etc/profile.d. Empty when the
	// crew wasn't provisioned or capture failed; the provider then falls back
	// to prepending the well-known devcontainer bin dirs to the image PATH.
	LoginPath string

	// Runtime requirements bubbled up from devcontainer features. Applied to
	// the HostConfig at create time. Critical for features like DinD which
	// need Privileged + a docker.sock bind mount.
	Privileged  bool
	Init        bool
	CapAdd      []string
	SecurityOpt []string
	ExtraMounts []CrewMount

	// PostStartCommands are shell commands that run in the container on every
	// start / restart, not only first create. Concatenation of feature-level
	// postStartCommand hooks (install-order) followed by the root-level
	// devcontainer.json postStartCommand. Providers that run these must
	// execute as UID 1001:1001 (the agent user) with stdout/stderr captured
	// for debugging. A failing post-start command logs a warning but does
	// not prevent the container from coming up — agents may recover via
	// retry. Intentionally excluded from the provisioning hash; mutating the
	// list does not invalidate the cached image.
	PostStartCommands []string

	// InitHookEnabled opts the crew into auto-executing /crew/init.sh on every
	// container start. /crew is a persistent bind mount on the host, so an
	// agent (UID 1001) with write access can stash a script there that will
	// run as 1001 on every restart — a persistence backdoor that survives
	// docker rm -f, crew restart, even sidecar reinstall. Default false:
	// the soft-promotion path now requires explicit operator opt-in per
	// crew via the manifest. Operators who do flip the bit accept the
	// trust model that everything in /crew/init.sh is code they wrote
	// or audited.
	InitHookEnabled bool

	// ProvisionSink, when set, receives structured ProvisionEvents for the
	// runtime container-preparation steps (start → container_create → ready,
	// plus failed) emitted by EnsureCrewRuntime. It mirrors the image-build
	// sink (devcontainer.ProvisionSink) so the agent-run / ensure-container
	// path is journaled and live-streamed exactly like the explicit
	// provisioning-job runner — no container preparation is silently
	// un-logged. nil (the default for callers that pass only {id, slug}) is a
	// no-op. Must be cheap/non-blocking; it runs on the ensure goroutine.
	ProvisionSink func(devcontainer.ProvisionEvent)

	// Services are sidecar containers (Redis, Postgres, etc.) the
	// provider should start alongside the crew's runtime, on the
	// same network, so the agent can reach them by Service.Name.
	// Empty / nil means "no sidecars", which is the historical
	// default. Providers that don't yet support sidecars should
	// log + ignore — the orchestrator surfaces a warning to the
	// user through the manifest-apply path.
	Services []CrewService
}

// CrewService is one sidecar container declaration. Mirrors the
// manifest's Service shape but lives in provider/ to avoid a cyclic
// dependency between provider and manifest packages — the API layer
// translates from the on-disk JSON into this struct before invoking
// the provider.
type CrewService struct {
	Name        string
	Image       string
	Command     []string
	Env         map[string]string // literal env vars (already resolved)
	Ports       []string          // "5432" or "5432/tcp"
	Volumes     []CrewServiceVolume
	Healthcheck *CrewServiceHealthcheck
}

// CrewServiceVolume names a per-crew named volume and where it
// mounts inside the sidecar.
type CrewServiceVolume struct {
	Name  string
	Mount string
}

// CrewServiceHealthcheck mirrors docker's healthcheck shape so the
// provider can wait for HEALTHY before reporting the crew ready.
type CrewServiceHealthcheck struct {
	Test        []string
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	StartPeriod time.Duration
}

// CrewMount declares an additional bind or volume mount to apply to the crew
// runtime, typically sourced from a devcontainer feature's metadata.
type CrewMount struct {
	Source string // host path (bind) or volume name (volume)
	Target string // path inside the container
	Type   string // "bind" (default) or "volume"
}

// ExecConfig describes a non-interactive command to execute inside a container.
type ExecConfig struct {
	ContainerID string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
	// Stdin, when non-nil, is streamed to the command's standard input and the
	// write side is then half-closed so the process observes EOF. nil (the
	// default) means no stdin is attached — byte-for-byte the historic
	// behaviour. Used to deliver an oversized agent prompt that would exceed
	// the kernel's per-argv MAX_ARG_STRLEN limit (128 KiB on Linux) if passed
	// as a positional command argument.
	Stdin io.Reader

	// AllowPrivileged opts this single Exec out of the #1158 fail-closed guard
	// that otherwise refuses a root/privileged User. It exists ONLY for the
	// handful of orchestrator preflight steps that legitimately require root
	// inside the crew container (killing a stale sidecar to reset the network
	// policy; pre-creating dual-writer files like /crew/manifest.json). Setting
	// it is an explicit, auditable decision at the call site — never a default,
	// and never reachable from agent- or request-supplied input.
	AllowPrivileged bool
}

// IsPrivilegedExecUser reports whether a resolved or configured container
// exec user string is unsafe to run a command as. Shared by keeper's
// /execute path (#1060, PR #1135) and the docker provider's generic Exec
// fail-closed default (#1158) — both refuse to run a command as root, or as
// anything that cannot be strictly proven to be a non-root numeric uid[:gid],
// because their containment models assume the command runs unprivileged.
// The check is deliberately strict and fails closed: only the Docker "uid"
// and "uid:gid" forms are accepted, every part must parse as a positive
// integer, and a zero uid *or* zero gid (root's primary group) is rejected.
// A named alias like "root" or an image /etc/passwd entry aliasing uid 0
// (e.g. "toor") is rejected too, since it isn't one of the two accepted
// numeric forms.
func IsPrivilegedExecUser(user string) bool {
	u := strings.TrimSpace(user)
	parts := strings.Split(u, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return true
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return true
		}
	}
	return false
}

// ExecResult holds the exec ID and output stream from a container exec command.
type ExecResult struct {
	ExecID string
	Reader io.ReadCloser
}

// ContainerStatus reports the current state and uptime of a crew's container.
type ContainerStatus struct {
	ID     string
	State  string // "creating", "running", "idle", "stopped", "error"
	Uptime string
	// RuntimeContract reports whether this container was created with the
	// container configuration the running build applies today: "current",
	// "stale", or "" when the provider has no opinion.
	//
	// It exists because a crew container is created once and reused
	// indefinitely, so a merged change to the HostConfig — Init, the core-dump
	// ulimit, supplementary groups, swap, /dev/shm — reaches only crews whose
	// container is recreated afterwards, and nothing used to say which those
	// were (#1642). A running container is deliberately never torn down for
	// this; it is reported instead, and applied the next time the container is
	// recreated (an idle-TTL stop, or `crewship crew restart-agents`).
	//
	// Providers that do not track it leave it empty, which every surface
	// renders as "no opinion" rather than as "current".
	RuntimeContract string

	// MemoryMB and CPUs are the limits THIS CONTAINER was created with, read
	// off the container itself — the effective values, as opposed to the
	// configured ones the crews row carries (#1681).
	//
	// A crew's memory or CPU limit can be edited at any time, and both are
	// applied at ContainerCreate and nowhere else, so a running container keeps
	// the figures it was born with until it is recreated while `crew get`
	// reports the new ones. Whoever holds the crew's configuration can compare
	// the two and say so; nothing else can, and nothing else should try —
	// re-deriving the intended value at the comparison point is a second
	// reconstruction free to disagree with the builder, which is exactly what
	// #1642's canonical digest refused to introduce here.
	//
	// Zero means the container declares no limit, or the provider does not
	// report one. Never rendered as "0 MiB" — an absent number is not a small
	// one, and treating it as one would invent drift out of silence.
	MemoryMB int
	CPUs     float64
}

// The RuntimeContract vocabulary. Declared beside the field rather than in the
// provider that computes it, because the server, the CLI and any future
// provider all have to agree on the two strings — and the third state, the
// empty string, is "this provider has no opinion" and must never be rendered
// as "current".
const (
	RuntimeContractCurrent = "current"
	RuntimeContractStale   = "stale"
)

// ContainerMetrics holds point-in-time resource usage metrics for a container
// including CPU, memory, network I/O, and process count.
type ContainerMetrics struct {
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryUsed  int64     `json:"memory_used_bytes"`
	MemoryLimit int64     `json:"memory_limit_bytes"`
	MemoryPct   float64   `json:"memory_percent"`
	NetRx       int64     `json:"net_rx_bytes"`
	NetTx       int64     `json:"net_tx_bytes"`
	PIDs        int       `json:"pids"`
	Timestamp   time.Time `json:"timestamp"`
}

// ContainerProvider defines the interface for managing crew container runtimes.
// Implementations include Docker and Apple Containers.
type ContainerProvider interface {
	EnsureCrewRuntime(ctx context.Context, team CrewConfig) (string, error)
	StopCrewRuntime(ctx context.Context, containerID string) error
	RemoveCrewRuntime(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error)
	ContainerStats(ctx context.Context, containerID string) (*ContainerMetrics, error)
	Exec(ctx context.Context, cfg ExecConfig) (*ExecResult, error)
	ExecInspect(ctx context.Context, execID string) (bool, int, error)
	// CrewContainerName returns the container name for a crew. It is keyed by
	// the globally-unique crew id (not the per-workspace slug alone) so two
	// tenants with an identically-named crew never collide on a shared daemon
	// (audit C1). The slug is retained as a human-readable name segment.
	CrewContainerName(id, slug string) string
	// CopyToContainer copies a tar archive into the container filesystem at dstPath.
	CopyToContainer(ctx context.Context, containerID string, dstPath string, content io.Reader) error
}

// AdmissionGate holds a crew container start until the host can afford one
// more, and reports why while it waits. Implemented by
// internal/admission.Controller; declared here so the providers depend on the
// two-method shape rather than on that package.
//
// Providers call it at the statements that make a container resident —
// ContainerCreate, and the start of a container that was stopped — and NOT on
// the warm path, because reusing a container that is already running adds
// nothing to the host and must never queue behind a memory check.
//
// It lives at this depth on purpose. The bound that existed before, the
// orchestrator's runSem, is taken inside RunAgent, by which point all eleven
// of its callers have already created and started their container; container
// creates were therefore unbounded. Repairing that with a gate the callers
// invoke first would leave a gate eleven call sites have to remember — the
// same class of defect, waiting for the twelfth caller. Here, the only way to
// create a crew container is to go through the code that asks.
//
// onHold is invoked the first time a start is actually held, again if the
// binding reason changes, and then periodically for as long as the wait lasts
// — rate-limited, and spreading out as it goes on. It is how the run's own
// provisioning stream says "waiting for capacity" rather than going quiet.
// One line at the start is not enough: the common hold keeps the same reason
// for its whole life, and a 30-minute silence after it reads exactly like a
// hang (#1675).
type AdmissionGate interface {
	Admit(ctx context.Context, crewID, crewSlug string, onHold func(reason, detail string)) (release func(), err error)
}

// HostAddressProvider is an optional interface that container providers can
// implement to advertise the hostname/IP that containers should use to reach
// the host machine. Docker uses "host.docker.internal"; Apple Containers use
// the host's actual IP since each container runs in its own VM.
type HostAddressProvider interface {
	HostAddress() string
}

// SidecarProvider is the optional capability for container providers
// that can start crew-scoped sidecar containers (Redis, Postgres,
// etc.). The docker provider implements it; the apple-container
// provider does not yet, and orchestrator callers that need
// sidecars must type-assert at the call site (graceful degradation:
// if the provider doesn't support sidecars, the manifest's
// `services:` block is ignored at runtime with a warning).
//
// Every method is keyed by the globally-unique crew ID, with the slug carried
// alongside only for names and log lines. Keyed on the slug alone (as these
// were until #1732) two workspaces with an identically-slugged crew shared one
// sidecar container and one data volume, and either crew's teardown destroyed
// the other's live database. `crews` is UNIQUE(workspace_id, slug); one daemon
// serves every workspace.
type SidecarProvider interface {
	EnsureCrewServices(ctx context.Context, team CrewConfig) (map[string]string, error)
	StopCrewServices(ctx context.Context, crewID, crewSlug string) error
	RemoveCrewServices(ctx context.Context, crewID, crewSlug string) error
}

// ServiceVolumeRemover is the second half of a sidecar teardown: the named
// volumes the sidecars mounted (a postgres data directory, a redis dump).
//
// It is a separate interface from SidecarProvider rather than a fourth method
// on it for two reasons. Removing a crew's DATA is a strictly bigger decision
// than removing its containers, so a provider is allowed to implement one and
// not the other. And SidecarProvider is discovered by type assertion at every
// call site: adding a method to it would silently demote every existing
// implementation (including test fakes) to "not a SidecarProvider" — the
// assertion would just start returning false, with nothing failing to compile.
//
// Callers remove volumes AFTER RemoveCrewServices; docker refuses to remove a
// volume that a container still references.
type ServiceVolumeRemover interface {
	// RemoveCrewServiceVolumes removes only volumes that prove they belong to
	// crewID. An implementation must not select them by name prefix: crew slugs
	// may contain hyphens, so one crew's prefix can prefix another's volume
	// names (#1732).
	RemoveCrewServiceVolumes(ctx context.Context, crewID, crewSlug string) error
}

// CrewContainerLookup is an optional interface that container providers
// can implement to expose a non-mutating "does a container for this crew
// already exist?" lookup. Used by Server.Start for boot-time rehydration:
// containers persist across `crewshipd` restarts, so the stats collector
// + listening-port scanner stay blind to them unless we re-register on
// startup. Providers that don't implement this just skip rehydration —
// existing crews start being tracked again the next time their crew is
// dispatched (i.e. the next EnsureCrewRuntime call).
type CrewContainerLookup interface {
	// FindCrewContainer returns the existing container ID for a crew
	// slug. `running` is false for stopped-but-present containers (so
	// the caller can decide whether to start it). When no container is
	// found, returns ("", false, nil) — only error path is for transport
	// failures talking to the runtime.
	FindCrewContainer(ctx context.Context, id, slug string) (containerID string, running bool, err error)
}

// VolumeManager is an optional interface for managing persistent volumes
// associated with crew containers (home directories, tool storage).
type VolumeManager interface {
	RemoveCrewVolumes(ctx context.Context, id, slug string) error
}

// LegacyResourcePruner is an optional interface for removing pre-C1 (slug-only)
// crew runtime resources that survive a normal DB nuke+reseed. checkNoLegacyCrewResources
// only DETECTS them (and blocks provisioning); this REMOVES them so the
// id-scoped runtime can start. Legacy names carry no crew id, so the caller
// passes every live crew slug. Returns the names actually removed.
type LegacyResourcePruner interface {
	PruneLegacyCrewResources(ctx context.Context, crews []CrewRef) (removed []string, err error)
}

// LegacyResourceDetector is the read-only counterpart to LegacyResourcePruner:
// it reports whether any pre-C1 slug-only resource exists for the given crews,
// without removing anything. Powers the admin legacy-resources endpoint that
// `crewship doctor` surfaces as a WARN before agent runs start failing.
type LegacyResourceDetector interface {
	HasLegacyCrewResources(ctx context.Context, crews []CrewRef) (present bool, err error)
}

// CrewRuntimePruner tears down the LIVE (id-scoped) docker runtime for the
// given crews — the agent container plus its home/tools volumes AND each crew's
// sidecar container(s)+volumes — WITHOUT removing shared cached devcontainer
// images (crewship-cache:<hash>). It powers the workspace full-teardown behind
// seed --nuke: crew DB deletion is a soft-delete that never touched docker, so
// containers and volumes would otherwise orphan. Preserving the image cache is
// deliberate — a subsequent reseed reuses it instead of forcing a slow rebuild.
// Unlike LegacyResourcePruner (instance-wide, slug-only names) the caller passes
// exactly the crews of one workspace, each with id AND slug. Returns the names
// actually removed; a per-resource failure is skipped, a daemon-list failure is
// returned WITH the partial removed list.
type CrewRuntimePruner interface {
	PruneCrewRuntimes(ctx context.Context, crews []CrewRef) (removed []string, err error)
}

// InteractiveExecConfig configures an interactive (TTY) exec session.
type InteractiveExecConfig struct {
	ContainerID string
	Cmd         []string
	Env         []string
	WorkingDir  string
	User        string
	Rows        uint16
	Cols        uint16
}

// InteractiveExecResult holds the bidirectional connection to an interactive exec.
type InteractiveExecResult struct {
	ExecID string
	Conn   io.ReadWriteCloser // raw bidirectional PTY stream
}

// InteractiveExecProvider is an optional interface for providers that support
// interactive (TTY + stdin) exec sessions, used by the web terminal.
type InteractiveExecProvider interface {
	ExecInteractive(ctx context.Context, cfg InteractiveExecConfig) (*InteractiveExecResult, error)
	ExecResize(ctx context.Context, execID string, rows, cols uint16) error
}

// ShortID returns first 12 chars of a container ID, or the full string if shorter.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// CrewServiceStatus is one live sidecar container's raw facts, read
// directly from the container runtime — no DB, no manifest. Name has
// the "<namePrefix>-svc-<crewSlug>-" prefix stripped, so it matches
// the service name from the manifest's services: block. Deliberately
// thin: type inference (postgres/redis/…) is an API-layer concern
// (inferDatastoreType), not something the provider should know about.
type CrewServiceStatus struct {
	Name   string   // service name (manifest name, prefix stripped)
	Image  string   // image reference the container is currently running
	Status string   // docker's human status string, e.g. "Up 2 hours"
	State  string   // "running" | "stopped" | "creating" | "error" — same vocabulary as ContainerStatus
	Ports  []string // "5432/tcp" etc, container-internal only (sidecars never publish to the host)
}

// ServiceLister is an optional interface for container providers that can
// enumerate a crew's live sidecar containers straight from the daemon — the
// live counterpart to the crews.services_json DB snapshot, which can drift
// (a sidecar stopped by hand, or OOM-killed, still reads "configured" there).
// The docker provider implements it; providers that don't (apple-container
// today) leave the GET /services endpoint answering an empty list rather
// than erroring.
type ServiceLister interface {
	// ListCrewServices enumerates only the sidecars owned by crewID. Selecting
	// them by slug would list an identically-slugged crew's services from
	// another workspace (#1732).
	ListCrewServices(ctx context.Context, crewID, crewSlug string) ([]CrewServiceStatus, error)
}

// The CrewContainerInfo.Kind vocabulary — which of a crew's containers a row
// describes. Declared beside the type rather than inside the docker provider
// because the server, the CLI and the web UI all key off the two strings.
const (
	// CrewContainerKindCrew is the crew's own agent runtime container — the
	// one agents exec into. At most one per crew.
	CrewContainerKindCrew = "crew"
	// CrewContainerKindSidecar is one of the crew's declared service
	// sidecars (postgres, redis, …).
	CrewContainerKindSidecar = "sidecar"
)

// CrewContainerInfo is one live container belonging to a crew — its agent
// runtime, or one of its sidecars — read straight from the container runtime.
//
// It is the whole-crew superset of CrewServiceStatus, which answers only for
// sidecars and renames them to their manifest service name. Here the name is
// the container's real runtime name, because the surface this backs is a
// Docker view: an operator reading it is about to type it into `docker logs`.
//
// Deliberately carries no usage metrics. CPU and memory come from
// ContainerStats, which is a second call per container with a cost this
// listing should not silently pay for callers that only want the inventory.
type CrewContainerInfo struct {
	ID    string // runtime container id
	Name  string // container name as the runtime knows it, no leading "/"
	Image string // image reference the container is currently running
	Kind  string // CrewContainerKindCrew | CrewContainerKindSidecar
	State string // "running" | "stopped" | "creating" | "error" — same vocabulary as ContainerStatus
}

// CrewContainerLister is an optional interface for container providers that
// can enumerate EVERY container belonging to a crew — the agent runtime and
// the sidecars — in one pass.
//
// ServiceLister answers the narrower "which sidecars are up" question and
// cannot answer this one: the crew's own runtime container is what a Docker
// view is mostly about, and it carries no crewship.svc label, so it is
// invisible to that listing by construction.
//
// The docker provider implements it. Providers that do not (apple-container
// today) leave the GET /containers endpoint answering an empty list rather
// than erroring — the same "unsupported → empty, don't fail" convention
// ServiceLister documents.
type CrewContainerLister interface {
	// ListCrewContainers enumerates only the containers owned by crewID.
	// The slug is a display/name-fallback input only; selecting by slug
	// alone would list an identically-slugged crew's containers from
	// another workspace (#1732).
	ListCrewContainers(ctx context.Context, crewID, crewSlug string) ([]CrewContainerInfo, error)
}

// CrewImageState answers "is the image this crew is actually running still the
// image its tag names?" for one crew — the read half of #1845.
//
// It is deliberately a REPORT and never an action. A crew container is created
// once and reused for as long as the crew is alive, so the digest check that
// ensureImage performs at create time is the last time anything looks; a
// long-lived container silently becomes a snapshot of whatever the registry
// held on the day it was born. That is the condition self-hosters hit and
// nobody notices, and nothing measured it.
//
// Every field is descriptive so a caller can render the situation without
// re-deriving it. Behind is the single boolean anything downstream keys off,
// and Reason exists so "not behind" can distinguish "confirmed current" from
// "could not tell" — collapsing those two is how a freshness check quietly
// becomes a check that always passes.
type CrewImageState struct {
	// Image is the reference the crew is configured to run, resolved through
	// the same CachedImage > Image > provider-default chain EnsureCrewRuntime
	// uses. Re-deriving it anywhere else would be free to disagree with what
	// actually starts.
	Image string

	// ContainerID is the live crew container, or "" when none exists. No
	// container means nothing is running a stale image: the next start pulls.
	ContainerID string

	// Running distinguishes a container that is up from one that exists but is
	// stopped. A stopped container gets RESTARTED, not recreated, so it carries
	// its old image forward and is still worth reporting on.
	Running bool

	// RunningDigest is the registry manifest digest of the image the live
	// container was created from, read back off the daemon. Empty when that
	// image has no registry digest at all — a locally built crewship-cache:*
	// derivative, or a daemon reporting no RepoDigests.
	RunningDigest string

	// ResolvedDigest is what Image resolves to on the registry right now.
	// Empty when the registry could not be reached (air-gapped host, wedged
	// credential helper, throttling).
	ResolvedDigest string

	// Behind is true ONLY when both digests are known and they differ. An
	// unknown on either side is never reported as behind — see Reason.
	Behind bool

	// Reason explains a false Behind that was not a clean match: "registry
	// unreachable", "no container", "image has no registry digest". Empty when
	// Behind is true, or when the two digests were compared and agreed.
	Reason string
}

// CrewImageRefresh is what a refresh actually did.
type CrewImageRefresh struct {
	// Image is the reference that was refreshed.
	Image string

	// PreviousDigest is what the crew's container was running before, and
	// NewDigest what the tag resolves to after the pull. Both may be empty for
	// the same reasons CrewImageState's are.
	PreviousDigest string
	NewDigest      string

	// ContainerRemoved reports whether the crew's runtime container was
	// dropped so the next agent exec recreates it from the fresh image. False
	// when there was no container to drop.
	//
	// Recreating it eagerly here would start a container nobody asked for, on
	// a host that may be at its admission limit, for a crew that may then sit
	// idle for days. Dropping it IS the remediation: EnsureCrewRuntime is the
	// only thing that creates crew containers, and it always ensures the image
	// first.
	ContainerRemoved bool
}

// CrewImageFreshness is the optional capability behind #1845's delivery half:
// report whether a crew's container is behind its image tag, and refresh it.
//
// Optional, and discovered by type assertion at the call site, for the same
// reason ServiceLister is: the apple-container provider has no registry digest
// story, and adding a method to ContainerProvider would silently demote every
// implementation (including every test fake) to "not a ContainerProvider" with
// nothing failing to compile.
//
// Both methods take the full CrewConfig rather than an id+slug pair because
// the image a crew runs comes off that struct (CachedImage > Image > default);
// passing less would force callers to re-implement the chain EnsureCrewRuntime
// owns.
type CrewImageFreshness interface {
	// CrewImageState reports on the crew's live container. It is read-only: it
	// never pulls, never creates and never removes. A registry lookup failure
	// is reported through CrewImageState.Reason, not as an error — errors are
	// reserved for a daemon that could not be talked to at all.
	CrewImageState(ctx context.Context, team CrewConfig) (*CrewImageState, error)

	// RefreshCrewImage pulls the crew's image and drops its container so the
	// next agent exec starts from the fresh copy. Idempotent: refreshing a
	// crew that is already current transfers nothing.
	RefreshCrewImage(ctx context.Context, team CrewConfig) (*CrewImageRefresh, error)
}
