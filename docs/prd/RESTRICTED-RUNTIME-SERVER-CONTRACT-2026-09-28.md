# Restricted runtime: server contract and dev2 acceptance

Date: 2026-09-28. Follow-up: [#2709](https://github.com/crewship-ai/crewship/issues/2709).
Application authorization and release integration remain in
[#2704](https://github.com/crewship-ai/crewship/pull/2704), tracking
[#2703](https://github.com/crewship-ai/crewship/issues/2703).

Status: proposed integration contract, ready for the application-authority
owner's review. No runtime implementation, isolation acceptance or performance
measurement is claimed by this document. Agreement with the other workstream
has not yet been recorded. The user explicitly requires this contract before
substantial implementation; do not silently treat publication as agreement.

## Source and ownership boundary

The isolated dev2 worktree branches from main `8dc421fdb`, under
`crewship_2/.claude/worktrees/restricted-runtime-dev2`, branch
`feat/restricted-runtime-dev2`. The parent checkout and running dev2 are not
this build. No live deployment or shared daemon/host restart is authorized
by this prototype task. All experiments use owned disposable resources and
synthetic data. Do not run the worktree's dev launcher against a shared slot.

Read-only input from crewship_1 at `1c039b597`:

- [Runtime research](https://github.com/crewship-ai/crewship/blob/1c039b597/docs/prd/RESEARCH-AGENT-ACCESS-RUNTIME-HEARTBEAT-2026-09-27.md).
- [Release implementation tracker](https://github.com/crewship-ai/crewship/blob/1c039b597/docs/prd/AGENT-ACCESS-RELEASE-1-IMPLEMENTATION-2026-09-27.md).
- [A2/B acceptance matrix](https://github.com/crewship-ai/crewship/blob/1c039b597/docs/prd/AGENT-ACCESS-A2-B-TEST-MATRIX-2026-09-27.md).

This work owns runtime planning, process/mount/network isolation, direct
credential delivery, lifecycle of a run, test harness and measurements.
The application workstream owns the common grant store, API/WS authorization,
Settings, authorized memory selection and all production entrypoint adapters.
Both must use the contract below; neither gate alone completes A2/B.
The managed-service controller already present in the companion branch
(`internal/servicelifecycle/controller.go`) remains the service owner. It is
not present in this main-based worktree and must not be recreated here.

## Current code map, not a live exploit claim

| Surface | Current source | Consequence for restricted runs |
|---|---|---|
| Mounts | `internal/provider/docker/docker.go`, `buildMounts` | Crew-wide `/workspace`, `/output`, `/crew`, persistent `/home/agent` and `/opt/crew-tools` are broader than a private client context. Do not reuse this mount builder. |
| Container settings | `internal/provider/docker/docker_container.go`, container configuration | Crew settings include supplementary groups, host-gateway and crew network, with privileged/extra-mount options. Restricted creation must use its own closed profile, not copy CrewConfig and remove a few fields. |
| HOME, output, memory | `internal/orchestrator/run_paths.go` | HOME and secrets are per run, but `.memory` points to persistent agent memory. UID 1001 is shared inside a crew; names and permissions do not separate its agents. |
| Direct credentials | `internal/orchestrator/exec_env.go`, `exec_sidecar.go`, `orchestrator_run.go` | Delivery has environment, file and provider-login paths. `/secrets/shared` and refresh over live agent homes need an authority-specific replacement for restricted runs. |
| Processes and IPC | `internal/orchestrator/run_paths.go`, `internal/sidecar/server.go` | Crew runs share a process environment and a loopback sidecar. Exact `/proc` access depends on process/host controls and needs a real process test; do not infer universal readability from UID alone. |
| Run-token authority | `internal/sidecar/run_registry.go` | Legacy known-live runs can continue during authority outage. Restricted mode needs a bounded lease and must not inherit this availability fallback. |
| Recalled memory | `internal/episodic/recall.go` and run-path memory symlink | Container separation does not undo an unauthorized prompt/recall already assembled by the server. Server selection and on-disk isolation are independent required gates. |

This is an initial source map. The prototype report must add the actual full
mount table, namespaces, environment variable names (never real values),
endpoint reachability and all credential adapter paths it exercises.

## Authority is created by the server

An untrusted request supplies requested resource IDs and task input only.
It cannot supply an authoritative principal, delegation chain, mount source,
effective rights, policy revision, runtime mode or lease. The application
authenticates the caller and resolves these from current server state.

Proposed private Go boundary (names provisional, not a new public API):

```text
AuthorizeAttempt(ctx, authenticatedOrigin, requestedWork) -> opaque AttemptHandle
ResolveLaunch(ctx, AttemptHandle) -> authorized LaunchPlan
Renew(ctx, AttemptHandle, generation) -> Lease | denied
Revoke(ctx, authorityReference) -> RevocationStatus
Recover(ctx, durableAttemptID) -> current LaunchPlan | denied
```

The runtime accepts handles only from the trusted server adapter. A struct
with IDs is not proof of authority. In-process calls use a private validated
type; a later remote transport must authenticate the issuing server and bind
the audience, nonce, expiry and attempt generation. Signing an attacker-chosen
claim without first resolving its authority is not sufficient. Policy version
is a freshness/fencing value, not an authorization credential.

The resolved launch plan contains:

| Field group | Server-owned meaning |
|---|---|
| Identity | workspace ID, authenticated principal type/ID, agent ID, origin type and stable origin ID, conversation/project IDs where applicable |
| Attempt | durable work/run ID, attempt ID and generation, parent attempt where delegated; no reuse of an old attempt token on retry |
| Data audience | server-issued data-scope ID/revision binding the authorized audience and permitted persistent resources; never inferred from agent slug |
| Decision | authority references, revision vector or equivalent server decision revision, issue time, expiry, revocation handle and explicit `restricted` mode |
| Mount grants | stable resource ID and revision, role (input/project/memory/output/checkpoint), access mode and approved container target; no client-supplied host source |
| Credential grants | credential reference, grant/lease reference, account binding, delivery type, allowed env name or target, expiry and refresh rights; no broad crew fallback |
| Network and IPC | permitted destinations/operations and run-bound broker audience, all other paths denied |
| Execution | approved image digest/profile, command policy and explicit CPU, RAM, PID, tmpfs, log and storage budgets |

Task arguments remain untrusted even after admission. The runtime resolves
resource references through a trusted storage resolver, rejects unknown modes
or fields that would widen authority, and checks fresh authority immediately
before releasing the process to execute. Queued records store references to
authority, not a permanent allow snapshot or plaintext credentials.

Chat, routine, webhook, schedule and queue execution all reach this boundary.
A webhook uses its configured scoped server principal, not a `user_id` from
its payload. Scheduled work uses durable service authority, not the last chat
user. A retry/resume is a new admission decision.

For delegation, the server computes the intersection of the parent's
delegable rights, the target agent's rights, current origin authority and
requested child rights. The child cannot inherit the target's broader account
or memory context. Preserve principal/origin and the parent chain; bind the new
agent/attempt. Changing an ID or starting another chat does not reset the
ceiling. Child lease expiry cannot exceed the parent's current lease or any
contributing grant expiry; renewal rechecks the entire authority chain.
Revoking a parent authority invalidates affected descendants. A
privileged managed-service action is a separately authorized narrow operation,
never a general child shell that escapes the ceiling.

## Isolation profile and two clients of the same agent

Each restricted attempt gets a separate container with private PID, mount,
IPC and network namespaces, UID/GID 1001, read-only rootfs, no privileges,
all capabilities dropped and no-new-privileges. UID 1002 remains the broker/
sidecar identity. Do not inherit crew supplementary groups, devices, extra
mounts, namespace joins, host network, host-gateway or Docker access.
Use the host's default seccomp/LSM protections without disabling them.

Only explicit resources are mounted. HOME, temporary files and secrets are
attempt-private; persistent memory, checkpoints and outputs are scoped by the
server's data-audience identity. H1/A and H2/A get different private scopes
despite identical agent ID. Retry restores only the authorized scope and a
verified checkpoint revision. It does not mount the old HOME wholesale.
Widening an audience requires an explicit authorized sharing operation.

Record server-owned provenance for persisted state: all data and credential
grants available to the producing attempt. After a grant is removed, merely
omitting its original mount does not make an old checkpoint, summary or
writable workspace safe to reuse: the attempt could have copied its contents.
Refuse automatic restoration of state with broader provenance. Start a fresh
context/storage generation, retaining independently authorized source data;
old derived state stays quarantined under its original audience. Missing or
agent-supplied provenance is not an exemption. Test revocation followed by a
retry that deliberately tries to restore a canary copied before revocation.

Read-only resources must have no writable alias, writable mounted ancestor,
overlapping submount or hard-linked alias in another writable resource.
Mount targets are fixed and non-overlapping. The storage resolver must own
the source parents and prevent untrusted replacement between validation and
mounting. `Clean` or `EvalSymlinks` followed by a later path mount is not a
complete race defense. For the first harness use independently provisioned,
server-owned volumes/snapshots without cross-resource hard links; arbitrary
host directory imports remain unsupported until separately verified.

Compare the complete Docker mount configuration and the process's mountinfo
against the profile, including standard `/proc`, `/dev`, `/dev/shm` and Docker
configuration mounts. Unexpected volumes declared by an image also fail
admission. No host root, crew roots, daemon sockets, host `/proc`, broad IPC
socket directory or shared secret tree may be mounted under any name.

The first filesystem/process stage can run with network disabled, but that
does not pass the sidecar/network gate. The connected stage must expose only
a per-attempt broker with narrowly scoped operations, no crew-wide token or
arbitrary host-API forwarding. Enforcement cannot rely on HTTP_PROXY alone.
Test direct IPv4/IPv6, DNS, host/gateway, neighboring run and service paths
against owned synthetic endpoints. A timeout without a reachable positive
control is not evidence that policy blocked the connection.

Docker configuration must set CPU, memory, memory-swap, PID, tmpfs and log
limits explicitly; storage needs a separately enforced quota or bounded
snapshot/output collector. A memory limit is not a disk quota. Kernel sharing
and daemon trust remain outside the client-process boundary; this prototype
does not certify protection against kernel/container escapes. See
[Docker security](https://docs.docker.com/engine/security/) and
[resource constraints](https://docs.docker.com/engine/containers/resource_constraints/).

## Revocation: exact events and bounded draining

Define `T0` as the committed authoritative revocation. A server admission
decision and revocation are ordered by the authority store. A decision made
before T0 is already admitted work, even if transport completes later.
No decision made after T0 may use the removed grant. DB/cache unavailability
is denial for new restricted operations, never legacy fallback.

Proposed prototype timing budget: leases valid for at most 15 seconds,
renewed every 5 seconds, local watchdog termination budget 2 seconds. These
are test targets, not measured production guarantees. Renewal must recheck
the live grant and attempt generation. A stale response cannot extend a lease
beyond its server-issued expiry. Use monotonic elapsed time for the watchdog,
reject rollback/uncertain expiry, and require fresh admission after restart.

| Surface | Effect after T0 |
|---|---|
| Queued work | Reject at claim and final launch authorization. Never start from a stored allow snapshot. Pre-T0 admitted work falls under the bounded running window. |
| Running process | Push revocation immediately invalidates broker use and requests whole-container termination. Lost push or authority outage expires the local lease; an independent trusted watchdog terminates the entire attempt, including children, within the proposed 17-second healthy-host budget. Killing a tmux session alone is insufficient. |
| Open file descriptor/mmap | Removing a grant, unlinking or chmod is not revocation of already-open access. Access may continue during draining; confirmed process termination ends it. No claim of atomic mount revocation at T0. |
| HTTP/tool/credential refresh | Decisions after T0 deny. An already-admitted request may finish; returned output retains its original audience and is separately authorized before delivery. No refresh may switch account or expand credential scope. |
| User stream | Application delivery checks current audience before each event is committed to transport; events ordered after revocation are rejected and the subscription is closed. Already-enqueued/in-flight bytes cannot be recalled. This serialization is an application-workstream gate, not proved by killing the container. |
| Direct secret already issued | Stop future delivery/refresh and terminate the attempt. Its previously read bytes cannot be revoked. Revoking a long-lived upstream secret requires provider-side revocation/rotation; Crewship lease expiry alone does not invalidate a copied key. |
| Retry/recovery | Resolve current authority and storage ownership anew, issue a new generation/token and rematerialize only currently allowed secrets. Old tokens, copied launch plans and stale checkpoints cannot authorize recovery. |

Persist observable `revocation_requested`, `draining`, `terminated` or
`termination_unconfirmed` states with timestamps and safe reason codes.
Only mark termination effective after the runtime verifies no attempt process
remains. If the daemon/watchdog/host is unavailable or a process cannot be
killed, the 17-second target is not guaranteed: report unconfirmed termination,
deny broker operations and further admissions, and do not start a replacement
that could duplicate the old attempt. This availability limit must survive
restart and cannot be hidden behind a successful revoke response.

The watchdog is outside the untrusted process/container authority and cannot
be killed or reconfigured by UID 1001. Test expiry with the server unavailable
and with descendants holding open files. Revocation does not undo prior
external effects or erase copies of data already read.

## Direct credentials and artifacts

Deliver only the attempt's current credential grants, after final admission.
Prefer file delivery into private tmpfs; use env only where the adapter
requires it. Avoid secrets in image layers, Docker create env, argv, durable
plans, labels and provisioning logs. An adapter requiring env receives it
through a private launch channel inside its own namespace. The process is
allowed to read its own delivered secret; tests must include that positive
control. Broker-only credentials remain unavailable to UID 1001.

No shared credential directory or agent-wide login refresh broadcast is valid
for restricted mode. Refresh binds the exact attempt, audience, grant and
upstream account. Runtime secrets are not restored from backups or checkpoints.
An authorized process can deliberately copy its secret into its own output;
therefore HOME/log/checkpoint separation, output audience enforcement and
scrubbing are all necessary. Scrubbing alone cannot prevent encoded leakage.
Publishing/sharing a private output requires separate application authority.

## Required executable acceptance

All rows are pending. Tests use real UID-1001 shells/processes and synthetic
canaries for H1/A and H2/A concurrently in the same workspace. Include another
agent and workspace as additional controls. Denial must leave the target
unchanged; errors, skipped Docker tests and empty output are not a pass.

| ID | Positive control and attack | Required evidence |
|---|---|---|
| R1 | Each run reads/writes its own data; reads explicit RO share; attempts foreign HOME/output/memory/checkpoint and shared writes | Exit statuses, unchanged canary digests, complete expected mount inventory; no bytes from foreign scope |
| R2 | Write RO share through normal path, writable ancestor/alias, symlink, hard-link alias and crafted traversal; race replace a mount source | Configuration rejection before launch or actual write denial; no foreign/RO data mutation |
| R3 | Own env/file credential succeeds; B scans env, process argv, `/proc/*/{environ,cmdline,root,fd}`, filesystem, log and checkpoint paths for A | A's canary absent from B; sidecar-only canary unreadable; own canary proves delivery occurred |
| R4 | Authorized per-run sidecar call succeeds; missing/forged/other-run/expired token and broader IPC operations fail | Actual sidecar binary identity, request outcomes and synthetic upstream call counts; no wider host API token handed to the agent |
| R5 | Allowed synthetic network endpoint is reachable; host, neighboring service/run, DNS and direct egress attempts fail | Network profile, namespaces, endpoint positive controls and observed denial; Docker socket absent under all aliases |
| R6 | Revoke while queued, immediately before launch, while open FD/mmap/child process runs, and during renewal outage | T0/admission/last permitted operation/stop timestamps; no new post-revoke admission, whole-tree termination or explicit failure within timing target |
| R7 | Kill/remove only an owned runtime, preserve its scoped persistent data and recover; tamper with scope/volume labels; revoke while stopped | Own data restored, foreign data never attached, secrets freshly resolved, stale tokens denied and inconsistent identity fails closed |
| R8 | Seed private memory/session/checkpoint and secret-bearing log for A; ask B using the same agent, including after recovery | No A data in B's mounted state, server-selected prompt/recall, API log/artifact delivery or stream; full application part requires integration |
| R9 | Chat/routine/webhook/queue request equivalent work; delegate to a broader agent and forge policy/origin/parent fields | All server adapters use the same decision boundary; child rights only narrow; payload claims cannot grant authority |
| R10 | Stop/expire/recover agent runtime while an owned managed-service fixture runs; deny runtime creation/credential resolver | Service lifecycle remains independent; restricted execution fails without starting a shared crew runtime |

Collect startup latency from launch request through UID-1001 ready marker;
report sample count, image digest/cache state, p50/p95, failures, concurrency
and host conditions. Separate image pull from container startup and include
broker/watchdog overhead. Report measured cgroup current/peak memory and
sampling method, not configured RAM limits as consumption. Preserve test
command, source commit and result files without secret values. No LLM calls
are needed for the process prototype; model/session integration remains an
explicit separate gate.

## Delivery and integration checkpoints

1. Application owner reviews this contract: identity/data-scope mapping,
   origin adapters, narrowing delegation, revocation ordering and bounded
   runtime stop, stream delivery and recovery ownership. Record agreed
   revision and any differences here before substantial implementation.
2. Implement the independent Docker harness and private runtime package
   without modifying common grants, public API authorization or Settings.
   Pin dependencies on the companion service/credential work explicitly.
3. Run unit tests, actual-process negative tests, timings and the required
   Go verification loop. Test failure or missing enforcement keeps restricted
   mode unavailable. No "best effort" fallback to crew mode.
4. Integrate server adapters and execute the complete A2/B matrix on dev2
   within the separate deployment authorization boundary. Preserve internal
   shared mode only for explicitly trusted crews; unknown/missing mode on a
   restricted request is an error. Existing legacy crews require an explicit
   compatibility classification, not silent widening of new client runs.

Open gates: agreement with the application owner; executable prototype; all
R1–R10 evidence; production memory/prompt/output adapters; network broker
enforcement; termination timing and failure reconciliation; disk quota;
upstream revocation support per provider; isolated host reboot; stronger
sandboxing if the threat model includes kernel exploits. No release claim
or completion of #2709 follows from this document alone.
