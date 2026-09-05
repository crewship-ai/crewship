package orchestrator

// The sidecar's lifecycle, factored so both ways into a crew container use it.
//
// SidecarProxyEnv (exec_env.go) is the SECURITY half of #1473: every process
// that execs into a crew container carries the proxy variables, so the crew
// egress allowlist actually applies to it. That fix said nothing about whether
// a proxy is listening on the other end — and only ONE caller ever started one.
// ensureSidecar takes an *AgentRunRequest, so a routine `script` step, which
// has a crew but no agent, could not reach it. Its crew container came up
// through crewstart.StartResolved (the crew's declared SERVICE sidecars, not
// crewship-sidecar), and the step then ran with HTTP_PROXY pointed at a port
// nothing was bound to:
//
//	Failed to connect to 127.0.0.1 port 9119 after 0 ms: Couldn't connect to server
//
// settleSidecar is the shared check→decide→pkill→start sequence, and the two
// entry points differ only in what they can resolve. That divergence between
// crew starts is the recurring bug in this area (see internal/pipeline's
// crew_start.go), so the answer is one sequence with two callers, not two
// sequences.
//
// What a CREW-level start can and cannot supply, precisely:
//
//	network policy (mode + allowlist + private-endpoint opt-in)  YES — all
//	    three are columns on `crews`, the same value for every member, and this
//	    is the load-bearing part for a script step.
//	route-auth key                                               YES — derived
//	    per (workspace, crew), no agent involved.
//	credentials                                                  NO — delivery
//	    is defined per AGENT (api.loadDeliveredCredentials(agentID)); there is
//	    no crew-wide grant to resolve. A crew-started sidecar therefore injects
//	    no provider auth into outbound requests. A script step does not want
//	    that anyway: it authenticates with what its own `script.env` /
//	    `{{ secrets.* }}` put in its environment.
//	memory config, IPC config, MCP gateway                       NO — every one
//	    of them is keyed by agent id/slug.
//
// Because a crew-started sidecar is deliberately that thin, it must never be
// inherited by an agent run, which needs all of the above. It is stamped with
// crewOnlySidecarFingerprint on boot, and crewOnlySidecarMustBeReplaced makes
// the agent path replace it — including on an instance with no internal auth
// configured, where both sides' HMAC fingerprints are empty and the ordinary
// config-fingerprint comparison cannot tell them apart.

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/provider"
)

// crewOnlySidecarFingerprint is the config fingerprint a crew-level start
// stamps on the sidecar. It is a fixed label rather than an HMAC on purpose:
// sidecarConfigFingerprint returns "" when internal auth is unconfigured, and
// "no opinion" is exactly the state that must NOT be confused with "started
// without credentials". It cannot collide with a real fingerprint, which is
// always 24 lower-case hex characters.
const crewOnlySidecarFingerprint = "crew-only-no-credentials"

// crewOnlySidecarMustBeReplaced reports whether a running sidecar was brought
// up by the crew-level path (no credentials, no MCP gateway, no IPC) and the
// current caller needs more than that. Split out from sidecarNeedsRestart so
// the credential-set comparison there keeps its "empty means no opinion"
// contract, which the crew path relies on to REUSE a sidecar an agent run
// already started with a fuller configuration.
func crewOnlySidecarMustBeReplaced(health *sidecarHealth, crewOnlyCaller bool) bool {
	if health == nil || crewOnlyCaller {
		return false
	}
	return health.ConfigFingerprint == crewOnlySidecarFingerprint
}

// sidecarSettleSpec is everything settleSidecar needs to decide between reuse,
// restart and cold start, and to boot a sidecar when it picks one of the
// latter two.
type sidecarSettleSpec struct {
	containerID string
	// logID identifies the caller in the log line — the agent id on the agent
	// path, the crew id on the crew path.
	logID string

	// desiredMode / desiredDomains are compared against the running sidecar's
	// reported policy; networkPolicy is what a fresh start is booted with. They
	// must describe the same policy — the caller builds all three together.
	desiredMode    string
	desiredDomains []string
	networkPolicy  *SidecarNetworkPolicy

	// configFingerprint is stamped on a sidecar this call starts.
	// restartFingerprint is what the RUNNING sidecar's fingerprint is compared
	// against; "" means the caller has no opinion about the credential set.
	// The agent path passes the same value for both; the crew path stamps
	// crewOnlySidecarFingerprint but compares nothing.
	configFingerprint  string
	restartFingerprint string

	// crewOnly marks the crew-level caller — see crewOnlySidecarMustBeReplaced.
	crewOnly bool

	creds      []Credential
	memoryCfg  *SidecarMemoryConfig
	ipcCfg     *SidecarIPCConfig
	routeAuth  *SidecarRouteAuth
	members    []SidecarCrewMember
	mcpServers []MCPServerConfig

	// onStale fires when the running sidecar is an OLD bind-mounted binary
	// (#1008); onReuse fires when a healthy sidecar is kept. Both optional —
	// the agent path uses them for the journal signal and the per-agent memory
	// dir prep, neither of which the crew path has anything to say about.
	onStale func(runningHash string)
	onReuse func()
}

// settleSidecar leaves the container with a sidecar that matches spec's policy,
// reusing a healthy one where it can. Reports whether it started one.
//
// #1220: the whole check→decide→pkill→start sequence runs under a per-container
// lock. Without it, two execs dispatching at nearly the same moment both sample
// the same health state, both decide to (re)start, and both pkill +
// startSidecar — one killing the other's freshly started sidecar, or
// double-starting it. The lock is released on return, so it never spans the
// caller's own long-lived work (the agent exec, or the script step).
func (o *Orchestrator) settleSidecar(ctx context.Context, spec sidecarSettleSpec) (started bool, err error) {
	unlock := o.lockSidecarLifecycle(spec.containerID)
	defer unlock()

	needStart := true
	if health := checkSidecar(ctx, o.container, spec.containerID); health != nil {
		if health.Stale && spec.onStale != nil {
			// #1008: the running sidecar is an OLD bind-mounted binary from
			// before the last redeploy. It keeps serving stale memory/egress
			// behaviour with no other signal — surface it on a durable,
			// operator-watchable channel (#1160), not just stdout.
			spec.onStale(health.SidecarHash)
		}
		restart := sidecarNeedsRestart(health, spec.desiredMode, spec.desiredDomains, spec.restartFingerprint) ||
			crewOnlySidecarMustBeReplaced(health, spec.crewOnly)
		if !restart {
			// #1160: restricted mode used to restart UNCONDITIONALLY here
			// ("the domain allowlist may differ between agents, so we always
			// restart to pick up the latest set") — with multiple agents
			// sharing one crew container, that made every OTHER agent's exec a
			// guaranteed kill+relaunch of an otherwise-healthy sidecar.
			// sidecarNeedsRestart only says yes when the mode or the allowlist
			// itself actually changed.
			o.logger.Info("sidecar already running, reusing",
				"id", spec.logID, "container_id", shortID(spec.containerID))
			needStart = false
			if spec.onReuse != nil {
				spec.onReuse()
			}
		} else {
			o.logger.Warn("sidecar runtime configuration changed, restarting",
				"running_mode", health.NetworkMode, "desired_mode", spec.desiredMode)
			// Kill the existing sidecar and WAIT for it to actually exit before
			// startSidecar launches a replacement (#1160): pkill only sends the
			// signal and returns immediately, so without this wait a concurrent
			// exec's checkSidecar could sample the container mid-restart —
			// momentarily seeing the dying old process (or a not-yet-bound new
			// one) and misreporting staleness or network-mode drift on a
			// container that was never actually stale. Bounded to ~2s; falls
			// through to startSidecar regardless (best-effort).
			//
			// The pattern is anchored with `^` — this whole command runs as
			// `sh -c "<script>"`, and that wrapping shell's OWN
			// /proc/<pid>/cmdline contains the literal substring
			// "crewship-sidecar" (it's part of the script text passed to -c).
			// An UNANCHORED `pkill -f crewship-sidecar` matches that substring
			// anywhere in a process's command line — including its own parent
			// shell — so it self-SIGTERMs before ever reaching the wait loop
			// (verified live: exit code 143, i.e. killed by signal, with zero
			// loop iterations run). The real sidecar is launched as the bare
			// command `crewship-sidecar --addr 127.0.0.1:9119`
			// (startSidecar), so its cmdline STARTS WITH the pattern; the
			// wrapping shell's never does (it starts with "sh"). `^` excludes
			// exactly the self-match case while still catching the real target.
			_ = o.execPreflight(ctx, provider.ExecConfig{
				ContainerID: spec.containerID,
				Cmd: []string{"sh", "-c",
					`pkill -f '^crewship-sidecar' 2>/dev/null; i=0; while [ $i -lt 20 ]; do pkill -0 -f '^crewship-sidecar' 2>/dev/null || exit 0; sleep 0.1; i=$((i+1)); done; exit 0`},
				User: "0:0",
				// Killing the stale sidecar to reset the network policy
				// legitimately needs root; #1158 opt-in (see ExecConfig).
				// Failing this closed would leave the stale egress policy in
				// place — a worse security outcome than the root exec.
				AllowPrivileged: true,
			})
		}
	}
	if !needStart {
		return false, nil
	}
	if err := startSidecar(ctx, o.container, spec.containerID, spec.creds, spec.memoryCfg,
		spec.ipcCfg, spec.routeAuth, spec.members, spec.networkPolicy, spec.mcpServers,
		spec.configFingerprint, o.logger); err != nil {
		return false, err
	}
	return true, nil
}

// CrewSidecarSpec addresses one crew's shared sidecar without an agent. Every
// field is a property of the crew, so a caller that resolved the crew's runtime
// config (provider.CrewConfig) already holds all of them.
type CrewSidecarSpec struct {
	CrewID      string
	WorkspaceID string
	ContainerID string

	// NetworkMode is "free" (the default for an empty string) or "restricted";
	// AllowedDomains is the crew's egress allowlist, honoured in restricted
	// mode. AllowPrivateEndpoints is the crew's opt-in to RFC1918/loopback
	// destinations, still ANDed with the instance ceiling.
	NetworkMode           string
	AllowedDomains        []string
	AllowPrivateEndpoints bool
}

// EnsureCrewSidecar guarantees the crew's crewship-sidecar is listening on
// 127.0.0.1:9119 inside its container, with the crew's egress policy, using the
// same start / healthy-reuse / policy-change-restart semantics as an agent run.
//
// It exists for the paths that exec into a crew container WITHOUT an agent —
// today, routine `script` steps. Those carry SidecarProxyEnv (#1473) and so
// depend on the proxy being up; before this they depended on some agent run
// having warmed the same container first, which on a fresh install is exactly
// what has not happened.
//
// It is a no-op when the sidecar is disabled instance-wide
// (CREWSHIP_SIDECAR_ENABLED=false): there is then no fence to meet and no agent
// run starts one either. That is logged, not an error — an operator who turned
// the sidecar off should not find their script steps failing.
//
// Read the package-level comment above for exactly what a crew-level start can
// and cannot hand the sidecar. In short: the full network policy, no
// credentials.
func (o *Orchestrator) EnsureCrewSidecar(ctx context.Context, spec CrewSidecarSpec) error {
	if o == nil {
		return nil
	}
	if spec.ContainerID == "" {
		return fmt.Errorf("ensure crew sidecar: no container id for crew %s", spec.CrewID)
	}

	o.mu.RLock()
	sidecarEnabled := o.sidecarEnabled
	ipcToken := o.ipcToken
	o.mu.RUnlock()

	if !sidecarEnabled {
		o.logger.Warn("sidecar disabled instance-wide — a script step's egress is unproxied and the crew allowlist does not apply to it",
			"crew_id", spec.CrewID, "container_id", shortID(spec.ContainerID))
		return nil
	}

	desiredMode := strings.TrimSpace(strings.ToLower(spec.NetworkMode))
	if desiredMode == "" {
		desiredMode = "free"
	}
	var networkPolicy *SidecarNetworkPolicy
	var desiredDomains []string
	switch desiredMode {
	case "free":
		networkPolicy = &SidecarNetworkPolicy{Mode: "free"}
	case "restricted":
		// The crew allowlist widened by every per-agent contribution recorded
		// for THIS container (MCP stdio hosts, proxied model endpoints). Passing
		// an empty agent id with no extras records nothing and returns the union
		// as it stands, which is the same set the last agent exec computed — so
		// a script step does not restart a healthy sidecar just by holding a
		// narrower view of the allowlist than the members sharing it.
		desiredDomains = o.crewDesiredDomains(spec.ContainerID, "", spec.AllowedDomains, nil)
		networkPolicy = &SidecarNetworkPolicy{
			Mode:           "restricted",
			AllowedDomains: desiredDomains,
			// Same instance-ceiling AND that RunAgent applies (#974 S5): a crew
			// opt-in only takes effect when the instance permits it.
			AllowPrivateEndpoints: effectiveAllowPrivateEndpoints(spec.AllowPrivateEndpoints),
		}
	default:
		// Identical to the agent path: an unrecognised mode is not "free".
		// Refusing to start is the fail-closed answer — the caller surfaces it
		// and nothing runs with an unknown fence.
		return fmt.Errorf("ensure crew sidecar: unknown network mode %q for crew %s", spec.NetworkMode, spec.CrewID)
	}

	// The route-auth key is derived per (workspace, crew), so the crew path can
	// supply it in full. It lets the sidecar validate any member's derived LLM
	// route token without holding the master secret or a crew roster.
	var routeAuth *SidecarRouteAuth
	if key := internaltoken.DeriveLLMRouteKey(ipcToken, spec.WorkspaceID, spec.CrewID); key != "" {
		routeAuth = &SidecarRouteAuth{Key: key}
	}

	started, err := o.settleSidecar(ctx, sidecarSettleSpec{
		containerID:    spec.ContainerID,
		logID:          spec.CrewID,
		desiredMode:    desiredMode,
		desiredDomains: desiredDomains,
		networkPolicy:  networkPolicy,
		// Stamp the crew-only marker so the first agent run replaces this
		// sidecar rather than inheriting one with no credentials; compare
		// nothing, so a fuller sidecar an agent already started is reused.
		configFingerprint:  crewOnlySidecarFingerprint,
		restartFingerprint: "",
		crewOnly:           true,
		routeAuth:          routeAuth,
	})
	if err != nil {
		return fmt.Errorf("ensure crew sidecar: %w", err)
	}
	if started {
		o.logger.Info("crew sidecar started for an agent-less exec (no credentials, no MCP gateway, no IPC — egress policy only)",
			"crew_id", spec.CrewID, "container_id", shortID(spec.ContainerID), "network_mode", desiredMode)
	}
	return nil
}
