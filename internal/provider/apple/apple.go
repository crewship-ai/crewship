package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/provider"
	"time"
)

var _ provider.ContainerProvider = (*Provider)(nil)
var _ provider.InteractiveExecProvider = (*Provider)(nil)
var _ provider.VolumeManager = (*Provider)(nil)

// The idle reaper's boot-time door. Server.rehydrateContainers skips any
// provider that fails this assertion, and skipping it is what kept
// CrewConfig.TTLHours unreachable here for a container that survived a
// restart — see FindCrewContainer.
var _ provider.CrewContainerLookup = (*Provider)(nil)

// agentContainerUser is the user Apple crew containers are created to run their
// init process (and therefore the agent) as — the value the create path passes
// as --user (apple_create_args.go). It is what a crew container this provider
// created will report back, but it is NOT what ContainerUser answers with: see
// that method for why the difference matters.
const agentContainerUser = "1001:1001"

// ContainerUser returns the container's configured run-as user, read from the
// container itself.
//
// It used to `return agentContainerUser, nil` for any id at all. That answer is
// right for a crew container this provider created and a fabrication for
// everything else — a crew built from a custom base image whose USER differs
// (verified: an image with `USER 1500:1500` reports 1500:1500 and the process
// really runs as uid 1500), or a container running as root.
//
// The fabrication defeated the guard that depends on this method. Both
// consumers — resolveExecUser (apple_exec.go, #1158) and keeper's /execute
// (#1060) — refuse to run a command when the resolved user is empty or
// privileged. A constant is neither, so the branch they exist for ("this
// container has no safe user of its own; do not exec") could never be taken.
// Answering from the runtime is what gives it something to refuse.
//
// Contract, shared with the docker provider (docker.go:1558): an empty string
// means the user could not be determined, and both callers treat that as a
// refusal. So an unreadable payload yields "" rather than a guess, and an
// inspect that failed yields an error — never a fallback to the constant,
// which is the fabrication being removed.
//
// It is cached, because reading it from the runtime turned every exec that
// leaves ExecConfig.User empty into an extra `container` process. Those callers
// are the polling ones — the listening-port scanner execs every crew container
// every 15 seconds, containerstate.Capture fires four probes per snapshot — so
// the cost is recurring rather than one-off, and on this provider an inspect is
// a fork+exec of a CLI (with a 5-minute watchdog, because it has been seen to
// wedge) rather than docker's unix-socket call. The value itself cannot change
// under a container: it is `configuration.initProcess.user`, fixed at create.
//
// See containerUserCacheTTL and forgetContainerUser for how the one hazard —
// same name, different container — is bounded. The short version: every
// in-product recreation goes through this provider and drops the entry, the TTL
// backstops out-of-band ones, and resolveExecUser re-checks privilege on
// whatever it is handed, so a stale answer can never admit root.
func (p *Provider) ContainerUser(ctx context.Context, containerID string) (string, error) {
	if user, ok := p.cachedContainerUser(containerID); ok {
		return user, nil
	}
	info, err := p.inspectContainer(ctx, containerID)
	if err != nil {
		// Not cached: a failed inspect says nothing about the user, and
		// remembering it would turn one unreachable-runtime moment into a whole
		// TTL window of refused execs.
		return "", fmt.Errorf("inspect container %s for run-as user: %w", containerID, err)
	}
	user := info.ConfiguredUser()
	p.rememberContainerUser(containerID, user)
	return user, nil
}

// containerUserCacheTTL bounds how long a container's run-as user is trusted
// without re-reading it.
//
// It is a backstop, not the primary invalidation. The exact one is
// forgetContainerUser, called from every path in this provider that creates,
// starts, stops or removes a container — which is every way a crew container is
// replaced in the product. The TTL exists only for the case that cannot reach:
// an operator running `container delete` / `container run` against the same
// name by hand.
//
// A minute rather than seconds or hours: it collapses both the burst (one
// containerstate snapshot's four probes) and the steady state (four
// listening-port scans) into a single inspect, while keeping the window in
// which a hand-recreated container could be answered for shorter than the time
// it takes to notice one. The residual exposure is small by construction —
// resolveExecUser re-runs IsPrivilegedExecUser on the cached value, so the
// worst a stale entry can do is exec as the previous container's non-root uid
// or refuse; it can never admit root.
const containerUserCacheTTL = time.Minute

type containerUserEntry struct {
	user     string
	cachedAt time.Time
}

// clock is the provider's time source, injectable so the TTL is testable
// without sleeping. Nil-safe: several constructors build a Provider literal.
func (p *Provider) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *Provider) cachedContainerUser(containerID string) (string, bool) {
	p.userMu.RLock()
	entry, ok := p.userCache[containerID]
	p.userMu.RUnlock()
	if !ok {
		return "", false
	}
	if p.clock().Sub(entry.cachedAt) >= containerUserCacheTTL {
		return "", false
	}
	return entry.user, true
}

// rememberContainerUser caches an answer that was actually read from the
// runtime. "" (the payload recorded no user) is cached like any other: it is a
// real reading of a real container, both consumers refuse on it, and re-asking
// would produce the same answer until the container is replaced — which drops
// the entry.
func (p *Provider) rememberContainerUser(containerID, user string) {
	p.userMu.Lock()
	defer p.userMu.Unlock()
	if p.userCache == nil {
		p.userCache = make(map[string]containerUserEntry)
	}
	p.userCache[containerID] = containerUserEntry{user: user, cachedAt: p.clock()}
}

// forgetContainerUser drops a container's cached run-as user. Called from every
// lifecycle transition this provider performs, because on Apple Containers the
// container id IS the name (configuration.id, set by --name): a crew recreated
// from a different image reuses the cache key exactly, and the answer it would
// otherwise be served belongs to a container that no longer exists.
func (p *Provider) forgetContainerUser(containerID string) {
	if containerID == "" {
		return
	}
	p.userMu.Lock()
	defer p.userMu.Unlock()
	delete(p.userCache, containerID)
}

// Config holds Apple Container provider configuration.
type Config struct {
	RuntimeImage    string
	Network         string
	OutputBasePath  string
	ContainerPrefix string

	// SidecarBinaryPath is the host path to crewship-sidecar, bind-mounted
	// read-only into crew containers at /usr/local/bin/crewship-sidecar. It is
	// how the in-container egress proxy that enforces `restricted` network
	// mode gets there at all; without it the mode is reported and not
	// enforced (#1648). Required — buildCreateArgs refuses to build a create
	// invocation without it.
	SidecarBinaryPath string

	// EntrypointPath is the host path to entrypoint.sh, bind-mounted
	// read-only at /usr/local/bin/entrypoint.sh and forced as the container's
	// entrypoint so a user-provided base image runs it instead of its own
	// CMD. Required, as above.
	EntrypointPath string

	// Admission holds a container start until the host can afford it (#1668).
	// nil disables admission control, which is the pre-#1668 behaviour.
	//
	// Note what this does and does not do on this provider's platform. Apple
	// Containers run on macOS, which publishes neither /proc/meminfo nor
	// /proc/pressure/memory, so the HOST MEMORY leg of the gate is inactive
	// there and says so on the status surface. The concurrency bound and the
	// stagger still apply: they need no kernel file, and each Apple container
	// is a full lightweight VM, which is if anything a stronger argument for
	// not starting twenty of them at once.
	Admission provider.AdmissionGate
}

// Provider implements provider.ContainerProvider using the Apple Container CLI.
// Apple Containers run each container as a lightweight VM on macOS (Tahoe+).
// Since there is no Go SDK, all operations shell out to the `container` CLI.
type Provider struct {
	cfg    Config
	logger *slog.Logger
	hostIP string

	mu      sync.RWMutex
	execSeq atomic.Int64
	execs   map[string]*execEntry
	done    chan struct{}
	// mounts records each container's bind mounts (container path → host path)
	// so CopyToContainer can write through them; see hostPathFor.
	mountsMu sync.RWMutex
	mounts   map[string]map[string]string

	// userCache holds each container's run-as user, read from the runtime. Its
	// own lock: ContainerUser is on the exec hot path and must not queue behind
	// exec registration. See ContainerUser / forgetContainerUser.
	userMu    sync.RWMutex
	userCache map[string]containerUserEntry

	// now is the provider's clock, overridden only by tests so the cache TTL is
	// exercisable without sleeping. nil means time.Now — see clock().
	now func() time.Time
}

type execEntry struct {
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
	// finishedAt is when the process exited, and gates collection: a caller
	// inspects an exec after it ran, so an entry swept the moment it completed
	// answered "not found" for work that had succeeded. Written before done is
	// closed and read only after observing it, so the channel close carries the
	// happens-before — no extra lock.
	finishedAt time.Time
}

// containerJSON is the structure returned by `container inspect`.
// Apple Container CLI uses nested "configuration" and "networks" array.
type containerJSON struct {
	// Both the lifecycle state and the networks live INSIDE status in the CLI
	// payload. Declaring `status` as a string and `networks` at the top level
	// meant inspect failed to decode at all — and where it was tolerated, the
	// gateway address it exists to read came back empty, so HostAddress was
	// never learned from a real container (#1779).
	Status struct {
		State string `json:"state"` // "running", "stopped", "created"
		// StartedDate is when the runtime started this container, RFC3339 as
		// the CLI writes it (captured verbatim in testdata/). It is the only
		// timestamp in the payload that survives a crewshipd restart, which is
		// what makes it the idle reaper's durable clock — see ContainerStatus.
		StartedDate string `json:"startedDate"`
		Networks    []struct {
			IPv4Address string `json:"ipv4Address"` // e.g. "192.168.67.4/24"
			IPv4Gateway string `json:"ipv4Gateway"` // e.g. "192.168.67.1"
			Hostname    string `json:"hostname"`
		} `json:"networks"`
	} `json:"status"`
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
		// InitProcess.User is the container's run-as user — the only place the
		// payload records it. See containerUser for the union it arrives as.
		InitProcess struct {
			User containerUser `json:"user"`
		} `json:"initProcess"`
	} `json:"configuration"`
}

// containerUser is `configuration.initProcess.user` from `container inspect`.
//
// It is a Swift enum encoded with the case name as the key, so exactly one of
// two disjoint objects arrives — captured verbatim from container CLI 1.2.0
// (fixtures in testdata/):
//
//	"user" : { "raw" : { "userString" : "1001:1001" } }   // --user, or the image's USER
//	"user" : { "id"  : { "uid" : 0, "gid" : 0 } }         // neither: the runtime's default, root
//
// Both arms are pointers so "the payload said root" stays distinguishable from
// "the payload said nothing". Collapsing them would report a readable root
// container as undeterminable — a refusal for the wrong reason — and, worse,
// invites the reverse mistake of treating an unreadable payload as a known
// safe user. This is the third struct in this package to be written against
// this CLI's output; the first two were written from imagination (`status` as
// a string, an image list with a top-level "reference") and both failed as a
// silent absence rather than an error (#1779), which is why these came off a
// live container instead.
type containerUser struct {
	Raw *struct {
		UserString string `json:"userString"`
	} `json:"raw"`
	ID *struct {
		UID int `json:"uid"`
		GID int `json:"gid"`
	} `json:"id"`
}

// State is the container's lifecycle state, or "" when the payload carried none.
func (c containerJSON) State() string { return c.Status.State }

// ConfiguredUser is the container's run-as user in the "uid:gid" / "uid" /
// name vocabulary provider.IsPrivilegedExecUser is written against, or "" when
// the payload recorded none.
//
// The id arm is rendered even when it is 0:0. That is the honest reading — the
// container really does run as root — and it is the whole point: rendered, the
// exec guard rejects it; swallowed to "", the caller would still refuse but on
// the weaker "couldn't tell" evidence, and a future caller tempted to treat
// "unknown" as benign would let root through.
func (c containerJSON) ConfiguredUser() string {
	u := c.Configuration.InitProcess.User
	if u.Raw != nil {
		if s := strings.TrimSpace(u.Raw.UserString); s != "" {
			return s
		}
	}
	if u.ID != nil {
		return fmt.Sprintf("%d:%d", u.ID.UID, u.ID.GID)
	}
	return ""
}

// StartedAt is when the runtime started this container, or "" when the payload
// carried none.
func (c containerJSON) StartedAt() string { return strings.TrimSpace(c.Status.StartedDate) }

// GatewayIP is the first network's IPv4 gateway — the address the host is
// reachable at from inside the container. Empty when unknown.
func (c containerJSON) GatewayIP() string {
	if len(c.Status.Networks) == 0 {
		return ""
	}
	return c.Status.Networks[0].IPv4Gateway
}

// containerListEntry is one item from `container list --all --format json`.
// The Apple Container CLI nests the container ID inside "configuration".
type containerListEntry struct {
	// Status is an OBJECT in the CLI payload — networks, startedDate and the
	// actual lifecycle under `state`. It was declared as a string, so decoding
	// the whole list failed with a type error and findContainer reported "not
	// found" for a container that was plainly running; EnsureCrewRuntime then
	// tried to create it again and got "container already exists" (#1779).
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Configuration struct {
		ID    string `json:"id"`
		Image struct {
			Reference string `json:"reference"`
		} `json:"image"`
	} `json:"configuration"`
}

// State is the container's lifecycle state ("running", "stopped", …), or "" if
// the payload carried none.
func (e containerListEntry) State() string { return e.Status.State }

// New creates an Apple Container Provider. It verifies the `container` CLI
// is available and the system service is running.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Provider, error) {
	if logger == nil {
		logger = slog.Default()
	}

	detected, err := Detect(ctx)
	if err != nil {
		return nil, fmt.Errorf("apple container runtime: %w", err)
	}

	p := &Provider{
		cfg:    cfg,
		logger: logger,
		hostIP: detected.HostIP,
		execs:  make(map[string]*execEntry),
		done:   make(chan struct{}),
	}

	logger.Info("apple container runtime detected",
		"version", detected.Version,
		"host_ip", detected.HostIP,
	)

	if cfg.Network != "" {
		if err := p.ensureNetwork(ctx, cfg.Network); err != nil {
			logger.Warn("failed to create apple container network", "network", cfg.Network, "error", err)
		}
	}

	go p.gcExecs()

	return p, nil
}

// HostAddress returns the IP address that containers should use to reach the host.
// Apple Containers run in dedicated VMs so they need the host's actual IP.
func (p *Provider) HostAddress() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.hostIP
}

func (p *Provider) ensureNetwork(ctx context.Context, name string) error {
	out, err := runCLI(ctx, "network", "list", "--format", "json")
	if err != nil {
		// Network commands require macOS 26+; non-fatal if unavailable
		p.logger.Debug("network list not available", "error", err)
		return nil
	}

	var networks []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &networks); err != nil {
		return nil
	}
	for _, n := range networks {
		if n.Name == name {
			return nil
		}
	}

	_, err = runCLI(ctx, "network", "create", name)
	if err != nil {
		// Ignore "already exists" errors (race or stale list cache)
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("create network %s: %w", name, err)
	}
	p.logger.Info("created apple container network", "network", name)
	return nil
}

// parseImageListNames extracts every name the local image store knows.
//
// `container image list --format json` carries no top-level "reference": the
// name lives in the descriptor annotations and `id` is the digest. Decoding
// into {Reference string} read every entry as "", so no local image was ever
// recognised and ensureImage always fell through to a pull — invisible while
// the only image came from a registry, fatal once a crew ran its own
// provisioned image, which exists locally and nowhere else (#1779).
//
// Both the fully qualified name and its docker.io/library-stripped form are
// recorded, so `alpine:3.20` matches an entry annotated
// `docker.io/library/alpine:3.20`.
func parseImageListNames(raw []byte) (map[string]bool, error) {
	var images []struct {
		Configuration struct {
			Descriptor struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"descriptor"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(raw, &images); err != nil {
		return nil, fmt.Errorf("parse image list: %w", err)
	}

	names := make(map[string]bool, len(images)*2)
	for _, img := range images {
		for _, key := range []string{
			"com.apple.containerization.image.name",
			"io.containerd.image.name",
		} {
			n := strings.TrimSpace(img.Configuration.Descriptor.Annotations[key])
			if n == "" {
				continue
			}
			names[n] = true
			names[strings.TrimPrefix(n, "docker.io/library/")] = true
		}
	}
	return names, nil
}

// ensureImage makes ref available locally, pulling only when it is not already
// in the store. A crew's provisioned image is built on this host and is in no
// registry, so a pull is not a fallback for it — it is a guaranteed failure.
func (p *Provider) ensureImage(ctx context.Context, ref string) error {
	out, err := runCLI(ctx, "image", "list", "--format", "json")
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}
	names, err := parseImageListNames(out)
	if err != nil {
		return err
	}
	if names[ref] || names[strings.TrimPrefix(ref, "docker.io/library/")] {
		return nil
	}

	p.logger.Info("pulling agent runtime image", "image", ref)
	// Apple Container CLI uses --scheme auto which picks HTTP for localhost
	if _, err = runCLI(ctx, "image", "pull", ref); err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	p.logger.Info("agent runtime image pulled", "image", ref)
	return nil
}

// namePrefix returns the configured container-name prefix
// (Config.ContainerPrefix), falling back to the "crewship" default when
// unset.
func (p *Provider) namePrefix() string {
	if p.cfg.ContainerPrefix != "" {
		return p.cfg.ContainerPrefix
	}
	return "crewship"
}

// CrewContainerName returns the container name for a crew. It folds in the
// globally-unique crew id (not the per-workspace slug alone) so two tenants
// with an identically-named crew never collide on a shared host (audit C1).
func (p *Provider) CrewContainerName(id, slug string) string {
	parts := []string{p.namePrefix(), "team"}
	if slug != "" {
		parts = append(parts, slug)
	}
	if id != "" {
		parts = append(parts, id)
	}
	return strings.Join(parts, "-")
}

// EnsureCrewRuntime creates or starts an Apple Container for the given crew.

func (p *Provider) findContainer(ctx context.Context, name string) (*containerListEntry, error) {
	entry, err := p.lookupContainer(ctx, name)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("container %q not found", name)
	}
	return entry, nil
}

// lookupContainer returns the list entry for a container name, (nil, nil) when
// the runtime knows no such container, and an error only when the runtime could
// not be asked. Absence and failure are separated here rather than at the call
// sites because FindCrewContainer's contract turns on the distinction and
// findContainer's does not.
func (p *Provider) lookupContainer(ctx context.Context, name string) (*containerListEntry, error) {
	out, err := runCLI(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}

	var containers []containerListEntry
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil, fmt.Errorf("parse container list: %w", err)
	}

	// In Apple Containers, configuration.id IS the container name (set via --name on create).
	// There is no separate "name" field in the CLI output.
	for _, c := range containers {
		if c.Configuration.ID == name {
			return &c, nil
		}
	}
	return nil, nil
}

// FindCrewContainer is the non-mutating "does this crew already have a
// container, and is it running" lookup (provider.CrewContainerLookup).
//
// It is what lets CrewConfig.TTLHours survive a crewshipd restart on this
// provider. Server.rehydrateContainers type-asserts the provider to this
// interface and returns immediately when the assertion fails, so without it a
// container that outlived the process was never re-registered with the idle
// reaper — and, never being registered, was never stopped again for as long as
// the host stayed up (#1662, which fixed the same class of hole for docker).
// The same assertion also gates the stale-internal-token orphan sweep
// (internal/api/admin_reap_orphan_containers.go), which 503s without it.
//
// Contract, from the interface: ("", false, nil) when no container exists — an
// error is reserved for failures talking to the runtime, because both callers
// skip a crew on error and would otherwise be unable to tell "this crew has
// never started" from "the runtime is unreachable".
func (p *Provider) FindCrewContainer(ctx context.Context, id, slug string) (string, bool, error) {
	name := p.CrewContainerName(id, slug)
	entry, err := p.lookupContainer(ctx, name)
	if err != nil {
		return "", false, fmt.Errorf("find crew container %s: %w", name, err)
	}
	if entry == nil {
		return "", false, nil
	}
	return entry.Configuration.ID, entry.State() == "running", nil
}

func (p *Provider) inspectContainer(ctx context.Context, id string) (*containerJSON, error) {
	out, err := runCLI(ctx, "inspect", id)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", id, err)
	}

	// inspect may return an array or a single object
	var containers []containerJSON
	if err := json.Unmarshal(out, &containers); err != nil {
		var single containerJSON
		if err2 := json.Unmarshal(out, &single); err2 != nil {
			return nil, fmt.Errorf("parse inspect output: %w", err)
		}
		return &single, nil
	}
	if len(containers) == 0 {
		return nil, fmt.Errorf("empty inspect result for %s", id)
	}
	return &containers[0], nil
}

// StopCrewRuntime gracefully stops a crew container.
//
// Unconditionally, before the stop is even attempted: a stop is the point at
// which a crew container is most likely to come back as a different one (the
// idle reaper stops precisely so the next start picks up the current container
// configuration), and an entry kept across that would be answering for the
// container that was.
func (p *Provider) StopCrewRuntime(ctx context.Context, containerID string) error {
	p.forgetContainerUser(containerID)
	_, err := runCLI(ctx, "stop", "--time", "10", containerID)
	if err != nil {
		return fmt.Errorf("stop crew runtime %s: %w", provider.ShortID(containerID), err)
	}
	return nil
}

// RemoveCrewRuntime forcefully removes a crew container.
//
// Ahead of the delete, and regardless of whether it succeeds: the intent to
// replace the container is already enough to make the cached answer untrusted,
// and a delete that failed for a container that is already gone must not leave
// its user behind.
func (p *Provider) RemoveCrewRuntime(ctx context.Context, containerID string) error {
	p.forgetContainerUser(containerID)
	_, err := runCLI(ctx, "delete", "--force", containerID)
	if err != nil {
		return fmt.Errorf("remove crew runtime %s: %w", provider.ShortID(containerID), err)
	}
	return nil
}

// ContainerStatus inspects a container and returns its current state.
//
// Uptime carries the container's own start time verbatim, which is what makes
// CrewConfig.TTLHours work here across a restart. Server.seedCrewReaperClock
// parses this field to date a boot-discovered container's idle clock; when it
// is empty the parse fails and the clock is dated from now instead, so every
// crewshipd restart hands every surviving container a fresh full TTL window
// and on a host that redeploys more often than the TTL nothing is ever reaped
// (#1662, fixed for docker by reporting inspect.State.StartedAt in the same
// field). A start time is a lower bound on last activity, so it can only
// over-estimate idleness — and over-estimating costs one container start,
// while the two things that survive a restart (a detached tmux session, a live
// port exposure) are both probed by the reaper before any stop.
func (p *Provider) ContainerStatus(ctx context.Context, containerID string) (*provider.ContainerStatus, error) {
	info, err := p.inspectContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("container inspect: %w", err)
	}

	var state string
	switch strings.ToLower(info.State()) {
	case "running":
		state = "running"
	case "created", "starting":
		state = "creating"
	case "stopped", "exited":
		state = "stopped"
	default:
		state = "error"
	}

	return &provider.ContainerStatus{
		ID:    containerID,
		State: state,
		// Empty when the payload recorded none: the caller's documented
		// fallback (date the clock from now, for one TTL window) is a bounded
		// degradation, where a fabricated timestamp would not be.
		Uptime: info.StartedAt(),
	}, nil
}

// ContainerStats is not supported on Apple Containers and always returns an error.

// defaultCLITimeout bounds a single `container` invocation.
//
// Apple's CLI can wedge: a `container create` was observed sitting at 6+
// minutes with an argument vector that completes in seconds by hand, and the
// image builder showed the same shape after finishing its work. runCLI passed
// the caller's context straight through, and most callers have no deadline —
// so one wedged call took the whole crew start with it, indefinitely.
//
// Generous rather than tight: creating a container from a multi-GB devcontainer
// image genuinely takes tens of seconds, and unpacking one for the first time
// can take minutes. The point is not to be strict, it is that there is a
// ceiling at all — an operator can act on "timed out after 5m", never on a
// spinner (#1779).
const defaultCLITimeout = 5 * time.Minute

// heavyCLITimeout bounds the operations that move real data: creating a
// container unpacks the image's root filesystem into a fresh VM disk, and
// pulling fetches it.
//
// Measured, not guessed — a mistake this file has already paid for twice. A
// `container create` from a provisioned devcontainer image on an M-series Mac
// took 454s, and the 5-minute generic bound killed it mid-work, failing a crew
// whose container was being built correctly. Twenty minutes leaves room for a
// slower host and a larger image while still ending a genuine wedge (#1779).
const heavyCLITimeout = 20 * time.Minute

// heavyCLIOps are the subcommands that get heavyCLITimeout. Keyed on the first
// argument, which is the subcommand runCLI is always called with.
var heavyCLIOps = map[string]bool{
	"create": true,
	"run":    true,
	"start":  true,
	"pull":   true,
	"build":  true,
	"cp":     true,
}

func runCLI(ctx context.Context, args ...string) ([]byte, error) {
	timeout := defaultCLITimeout
	if len(args) > 0 && heavyCLIOps[args[0]] {
		timeout = heavyCLITimeout
	}
	// `image pull` and `builder start` carry their verb second.
	if len(args) > 1 && heavyCLIOps[args[1]] {
		timeout = heavyCLITimeout
	}
	return runCLIWithin(ctx, timeout, args...)
}

// runCLIWithin runs one `container` invocation under its own deadline.
//
// On timeout the whole process group is signalled, not just the CLI: Apple's
// CLI spawns helpers, and killing the parent alone leaves them holding their
// pipes — which is exactly how the build side stayed stuck after its own
// watchdog fired.
func runCLIWithin(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if timeout <= 0 {
		timeout = defaultCLITimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "container", args...)
	ownProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.Bytes(), fmt.Errorf("container %s: timed out after %s (stderr: %s)",
				strings.Join(args, " "), timeout, stderr.String())
		}
		return stdout.Bytes(), fmt.Errorf("container %s: %w (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// gcExecs periodically cleans up finished exec entries.

// crewImage picks the image a crew's container is created from:
// CachedImage > Image > the provider default.
//
// This provider used to run p.cfg.RuntimeImage unconditionally, which meant
// provisioning built a crew an image with its devcontainer features baked in
// and then never ran it — on macOS the mise toolchains, github-cli and python
// a crew asked for were all installed into an image nothing started (#1779).
// Same chain as the Docker provider (docker_container.go), so "which image is
// my crew running" has one answer per crew rather than one per provider.
func (p *Provider) crewImage(team provider.CrewConfig) string {
	if team.CachedImage != "" {
		return team.CachedImage
	}
	if team.Image != "" {
		return team.Image
	}
	return p.cfg.RuntimeImage
}
