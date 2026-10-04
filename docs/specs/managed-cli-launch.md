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
bind and executable/launcher mount overlap. When the pilot is enabled, Docker
stages the launcher under a full SHA-256 filename using atomic create-if-absent,
verifies existing bytes and host ownership/permissions, and never replaces that
path or eagerly deletes old generations. Old-generation or fixed-name binds
fail closed. A container archive comparison supplies secondary consistency
verification; Docker archives can remount the current host source and do **not**
prove the running bind inode. Read-only overlays are rejected
too. Parent-directory symlinks that could alias an image path into writable
home are rejected. Authority and runtime are checked again after preflight,
before the durable exec creation gate. The reservation survives execution.

The first process is the static host-bound `crewship-sidecar --managed-launch`
helper. It checks native format, canonical path, ownership/permissions and
SHA-256 before `execve`, without shell, stdbuf, agent PATH, tmux or writable
argument/environment files. Its child environment contains only names supplied
by host admission, with a fixed system PATH. Image-only variables, `LD_*`,
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
the production Codex command builder uses the real static launcher against
synthetic native executables, with stale CLI/node/tmux in persistent home and
injected image environment. Wrong hashes exit 126. Unit regressions cover common
run admission, missing evidence, current-lock drift, tenant scope, mount aliases,
unsafe formats, immutable launcher generations and static PIE dependencies. No subscription account or model call is used.

For explicit upstream packaging qualification, provide a local mise image:

```sh
CREWSHIP_CODEX_QUALIFICATION_IMAGE=<local-mise-image> go test -tags=integration ./internal/devcontainer -run '^TestCodexNativePackaging$' -count=1 -timeout 10m
```

This gate resolves the public Codex 0.160.0 native lock, installs with
`--locked --force`, records the native mise path and inspects the committed
image without starting it for inspection. A separate read-only, network-disabled
runtime executes the actual native binary through the static launcher with
`--version`. It makes no authentication or model requests.
