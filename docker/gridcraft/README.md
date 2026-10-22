# Offline GridCraft worker image

This default-off worker is a persistent private service. The Go application
sends only native workbook snapshots and narrow commands to its authenticated
Unix socket. The worker starts fresh pinned CLI processes for each candidate,
then removes their private temporary directory. Go remains the sole storage
authority; no database, agent tokens, Docker socket or network is available.

Run `bash docker/gridcraft/prepare.sh` to download and verify the v0.10.0
linux/amd64 release and build the service. It verifies archive SHA-256
`b78a28605cd34594fc35dfc1ad36a924054571a1382625fb7506fd70cdfb9197`
and CLI SHA-256
`000100860eb14b641da48e5e0d2dcd6dcc5ddcd65a4b267a0ef2fcdfadcdd984`.
The ignored inputs include the two upstream license texts. The recipe copies
these texts and the Crewship license. Build with
`docker build -f docker/gridcraft/Dockerfile .`.

The qualification workflow tests the image, retains an immutable image archive
and binds its digest to the exact source SHA and workflow run/attempt. This is
component evidence; it does not authorize installation or replace review.
An operator installs the pinned image through the host's infrastructure workflow.

Required deployment profile: network none, read-only root, non-root UID/GID 1000,
capdrop ALL, no-new-privileges, 256 MiB memory/swap ceiling, one CPU, 32 PIDs,
32 MiB private tmpfs at `/scratch`. Bind only the private socket directory at
`/run/worker` and the regular 0600 service secret read-only at
`/run/secrets/service-token`. Both socket and secret directories must be private
to the service/application identity. Only the managed host launcher owns
container lifecycle and stale socket cleanup; the service refuses any
preexisting socket path. The supervisor must restart the container whenever the
worker exits.

The base image is distroless `cc` (glibc, libm, libgcc_s), pinned by digest. It
contains no shell, package manager or interpreter.

## Engine containment

The CLI is treated as untrusted code. The worker reads the secret once at
startup and keeps it in memory. Each CLI execution:

- starts with an empty environment except `PATH`, `HOME` and `TMPDIR` set to
  its own candidate directory, without the secret in environment or arguments,
  and with stdin/stdout/stderr on `/dev/null`;
- is confined with Landlock before the CLI starts: the worker re-executes
  itself in an internal launcher mode, restricts that thread and then
  executes the CLI. The CLI can read and execute only `/usr`, `/lib`, `/lib64`
  and `/etc/ld.so.cache`, and can write only its own candidate directory. It
  cannot read the service secret, list or change `/run/worker` (no unlinking
  or replacing the socket), reach other candidates' scratch, read `/proc`,
  ptrace the worker, or use TCP. Landlock needs no capability and works under
  `no-new-privileges` with Docker's default seccomp profile. If the kernel
  lacks Landlock, the worker refuses to start; there is no unconfined fallback;
- runs in its own session and process group. After every execution (success,
  error, timeout or cancellation) the whole group is killed with `SIGKILL` and
  reaped.

The worker is PID 1 and a child subreaper, so every orphaned engine descendant
is reparented to it. After each execution it inspects its children in `/proc`
and attributes them by session: processes of other executions still in flight
are left to their own sweep, so concurrent candidates never see each other's
dying processes as escapes. A process that left its group but kept the
execution's session (`setpgid`) is that candidate's escape. A process in a new
session (`setsid`) cannot be attributed, so every candidate in flight fails
closed. Escaped processes are killed. The worker then becomes unavailable and
exits non-zero, which ends the container's PID namespace and every process in
it, so the supervisor starts a clean container and socket. The worker reads
engine output only from a regular, single-link file opened without following
links.

## Concurrency

`CREWSHIP_GRIDCRAFT_WORKER_CAPACITY` sets how many candidates run at once
(default 2, 1 to 8). A request past capacity gets `429` before any scratch or
engine process exists. Each candidate has its own scratch directory and
Landlock write scope; a crashing candidate fails alone. One writer per
workbook is enforced by the application, which admits at most one candidate per
artifact. The default fits the profile above. Each concurrent candidate needs
about 64 MiB of memory (request buffers, a transient launcher, the CLI, and
scratch) and up to 8 PIDs (the PID limit counts threads). For capacity N, set
memory to at least 128 MiB + N × 64 MiB, PIDs to at least 16 + N × 8, and
`/scratch` to at least N × 4 MiB. The application's `max_workers` should not
exceed the worker capacity, or candidates are refused with `capacity_exhausted`.

Residual risk, accepted for the default-off pilot:

- The CLI runs under the worker's UID (1000). With all capabilities dropped,
  the worker cannot switch it to another UID. Landlock covers the file system,
  ptrace and TCP. On kernels older than 6.12 (Landlock ABI below 6), a
  compromised CLI can still send signals to the worker. That is denial of
  service only: the worker exits and is restarted. It can also connect to the
  socket, but cannot authenticate without the secret.
- Between the CLI exiting and the sweep, an escaped process runs for a few
  milliseconds inside the same Landlock domain. It cannot reach the secret,
  socket directory or other candidates.
- A complete UID split needs a launcher change: start the container as root
  with only `CAP_SETUID`/`CAP_SETGID` (everything else dropped), let the worker
  open the socket and read the secret, then permanently drop to UID 1000 and
  run the CLI under a dedicated unprivileged UID/GID (for example 10101) with
  no access to `/run/worker` or the secret. Alternatively, run one short-lived
  container per candidate. Landlock remains in place in both designs.

The listener is `/run/worker/service.sock`; authenticated requests use
`X-Crewship-Worker-Service`. `POST /v1/candidate` accepts a bounded snapshot plus
`import` or `cell.set`; `GET /v1/health` checks transport without computing.
`crewship-gridcraft-worker --healthcheck` is the packaged readiness probe.

Only native JSON imports and `cell.set` on the active sheet are implemented.
Formula recalculation changing another cell is rejected as a known limitation.
Generic commands, file paths, UI control and arbitrary MCP calls are unavailable.
