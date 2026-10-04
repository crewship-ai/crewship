# Managed CLI launch pilot

Agent runs normally retain the legacy launch path. Operators can select exact
crew IDs with the server environment variable `CREWSHIP_MANAGED_LAUNCH_CREWS`
(comma-separated, read at server construction). Removing an ID requires a
server restart and is an explicit return to legacy behavior. This selection
applies in common `RunAgent` admission, including assignment and chat dispatch;
requests cannot disable it. Interactive web terminals remain a separate path.

The first implementation supports **image-resident static Linux ELF executables**
including static PIE without an interpreter or external dependencies,
for the Codex adapter. Artifact reads are bounded to 512 MiB. Dynamically linked binaries, scripts,
env-shebang launchers, mise shims and executables in home/workspace/tmp are
unsupported and refuse the run. This narrow pilot does not qualify OAuth,
model runs, dynamic Claude packaging or complete runtime isolation.
Interpreter and library qualification is a subsequent capability (A2). Pilot
selection is restricted to Codex-only crews. Claude dispatch in a selected crew
returns an explicit dynamic-ELF/A2 capability error before creation.

The pilot requires a host-native server running under a UID other than 1001
or 1002. The official server Docker image runs as UID 1001 and is unsupported
for this pilot; recreating crew runtimes cannot correct that host UID collision.

Before selecting a crew, use the existing CLI environment flow:

1. Set an exact Codex pin in `crew config <crew> --mise <file>` and resolve/apply its
   native lock with `crew lock-resolve <file>` / `crew lock-apply <file> <resolution>`.
2. Build the environment with `crew start` or `crew provision` and inspect its
   immutable build revision with `crew revisions <crew>`.
3. The executable must reside at a canonical image path below `/opt` or `/usr`,
   owned by root or UID 1001, executable, and not writable by group/others. The
   read-only root and mount checks prevent UID 1001 from replacing image files. Sealing a shim
   does not turn it into a supported executable. Old images without captured
   executable evidence require a rebuild.
4. Check the revision's `toolchain.tools[].launch_artifact` and image-bound
   startup qualification. Then select that crew ID on the server and restart.
5. Recreate selected crew runtimes so they bind the immutable launcher generation.
   Existing fixed-name binds are refused with a recreation requirement.

No new API endpoint or credential bypass is introduced. `ai_cli_check: required`
retains its existing build-publication meaning and does not select this pilot.

The host captures executable bytes/hash through Docker's file-copy API from a
stopped container of the immutable image; no image entrypoint runs during this
capture. The launch descriptor binds the image, immutable revision, complete
native lock bundle digest, exact selector version, executable path and hash.
Changes to the current environment definition invalidate the old revision.
An unavailable resolver, missing reservation or unsupported runtime fails
closed before any legacy launch fallback.

Docker admission checks the exact reserved container, immutable image,
read-only root, privilege/capability restrictions, the host's read-only launcher
bind and executable/launcher mount overlap, including HostConfig tmpfs
destinations. When the pilot is enabled, Docker
stages the launcher under a full SHA-256 filename using atomic create-if-absent,
verifies existing bytes and host ownership/permissions, and never replaces that
path or eagerly deletes old generations. Enabling the pilot stages the same
launcher bytes in a digest-named generation for all newly created crew runtimes,
including non-pilot crews; existing container binds remain unchanged.
Old-generation or fixed-name binds
fail closed. A container archive comparison supplies secondary consistency
verification; Docker archives can remount the current host source and do **not**
prove the running bind inode. Read-only overlays are rejected
too. Parent-directory symlinks that could alias an image path into writable
home are rejected. Authority and runtime are checked again after preflight,
before the durable exec creation gate. The reservation survives execution.

The first process is the static host-bound `crewship-sidecar --managed-launch`
helper. It checks native format, canonical path, ownership/permissions and
SHA-256 before `execveat(AT_EMPTY_PATH)` of that same open descriptor, without
shell, stdbuf, agent PATH, tmux or writable
argument/environment files. The trusted launcher creates a new session and an exclusive 0600
`/tmp/crewship-direct-<run>.pid` file containing its PID and kernel start time
before artifact validation. Exec preserves that identity for the existing
stop, liveness and restart recovery probes; existing files or symlinks refuse
the attempt rather than overwriting another identity. This writable PID file
is lifecycle evidence, never authority to launch an executable. The existing
host-side `RunState.ManagedLaunch` marker must be persisted before exec.
Managed prompt assembly and command preparation failures finalize the run as
`error` before workload creation; they do not leave unresolved running occupancy.
The marker is retained by restart recovery. If that managed PID file disappears, probes return
UNKNOWN and retain the hold instead of falling through to tmux absence; an
authoritatively stopped or removed container can still prove absence. Lost
identity in a running container requires operator reconciliation/recreation.
Same-RunID re-execution refuses an existing identity file, matching legacy
direct-exec parity: retries must mint a new attempt ID.

Recovered callers carry the durable managed marker even after pilot selection is
removed. Only known managed probes read and require durable run state; nonpilot
probes retain the legacy script, state-error behavior and Docker call pattern.
Managed process-group signal probing calibrates against the probe's own group using
portable explicit-signal syntax. Unsupported utilities, permission errors and
failed calibration return UNKNOWN; only a definite no-such-process error
proves group absence. PID/starttime checks prevent accidental PID reuse. They do not establish an
adversarial isolation boundary between processes sharing UID 1001: that UID can
rewrite its PID file to a valid foreign PID/starttime in the same container.
The durable marker prevents false tmux absence and cross-run state confusion;
it does not attest the PID file against such deliberate forgery. This pilot
retains that legacy lifecycle limitation and does not qualify agent isolation.

Its child environment contains only names supplied
by host admission, with a fixed system PATH (`/usr/local/bin:/usr/bin:/bin`). Host-admitted
lowercase `http_proxy`, `https_proxy` and `no_proxy` are retained. Crew mise
and feature PATH additions are excluded: shell tools must use canonical
image paths when absent from the fixed PATH. This is a pilot limitation. Image-only variables, `LD_*`,
`DYLD_*`, `NODE_OPTIONS`, loader/interpreter options and tmux variables are
discarded. Existing host-admitted HOME, proxy and credential values remain;
moving credentials outside agents belongs to the later gateway work.

`agent_runs` state and `exec.command` payloads contain `managed_launch` admission
evidence. `version` is read from a single canonical native mise v3 locked entry and
must match the exact configured pin
and build revision; session self-reported versions remain separate observations.
This evidence describes admitted artifacts, not successful authentication or a
successful process start. Refusals before creation emit no CLI output.

The required managed-environment CI suite runs `TestManagedLaunchRealDocker`:
only this new fixture runs under sudo on the host; existing Docker fixtures retain
their non-root host execution. The production Codex command builder uses the
real static launcher against
synthetic native executables, with stale CLI/node/tmux in persistent home and
injected image environment. Wrong hashes exit 126. Unit regressions cover common
run admission, missing evidence, current-lock drift, tenant scope, mount aliases,
unsafe formats, immutable launcher generations and static PIE dependencies. No subscription account or model call is used.

For explicit upstream packaging qualification, provide a local mise image
using the test-only selector documented in
[codex_packaging_integration_test.go](../../internal/devcontainer/codex_packaging_integration_test.go).
Export that test-only image selector before running the command below.
A missing selector fails this explicit gate and supplies no packaging qualification:

```sh
go test -tags=integration ./internal/devcontainer -run '^TestCodexNativePackaging$' -count=1 -timeout 10m
```

This gate resolves the public Codex 0.160.0 native lock, installs with
`--locked --force`, records the native mise path and inspects the committed
image without starting it for inspection. A separate read-only, network-disabled
runtime executes the actual native binary through the static launcher with
`--version`. It makes no authentication or model requests.

Legacy inventory path/version observations remain PATH-first and keep their
existing publication-gate meaning. Managed evidence separately resolves the
canonical native mise installation, probes that exact binary's version and
captures its bytes from the stopped image. A shim or mise executable cannot
become the managed candidate; its independently observed version must match
the native lock and exact selector. Older revisions without independent
`managed_path` and `managed_version` evidence require a rebuild.

Artifact validation currently buffers up to 512 MiB per reader; operators must
budget memory for launcher validation in addition to the native CLI. The bound
limits individual allocations, not aggregate concurrency or an OOM guarantee.
Attestation uses a 20-second provider bound within 30-second admission.
Immutable launcher generations are retained. Conservative collection is tracked
in [issue #2906](https://github.com/crewship-ai/crewship/issues/2906); it must
preserve running and stopped container references and coordinate with launcher
publication and runtime creation before removing any generation.
Upstream Codex packaging qualification is an explicit opt-in test, separate from
the required synthetic managed-environment CI gate.

The once-run Linux amd64 Codex 0.160.0 native packaging qualification used
a 289,101,384-byte executable and a 1 GiB runtime memory limit. That result
does not qualify smaller memory limits, other platforms, authentication or
model calls. No crew memory defaults are changed by this pilot.
