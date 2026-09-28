# Restricted runtime: dev2 implementation and evidence

Date: 2026-09-28. Issue [#2709](https://github.com/crewship-ai/crewship/issues/2709),
draft [PR #2710](https://github.com/crewship-ai/crewship/pull/2710). Companion
application work remains [#2704](https://github.com/crewship-ai/crewship/pull/2704).
The user authorized implementation after reviewing the
[server contract](RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md).

## Delivered boundary

`internal/restrictedruntime` is a runnable **offline Linux/Docker prototype**,
not a production dispatcher replacement. It accepts only an opaque handle
resolved by a trusted server `Authority`, revalidates before execution, narrows
parent/child mount and credential authority, and rejects unknown modes. It
never calls `EnsureCrewRuntime` or falls back to shared crew execution.

Each attempt has a private PID/IPC/network namespace, UID 1001, no additional
groups/capabilities/devices, read-only rootfs, no-new-privileges, and explicit
CPU/RAM/swap/PID limits. Runtime-owned HOME/secrets/broker/tmp areas are bounded
tmpfs. Only independently provisioned named volumes are permitted under
non-overlapping `/data/<resource>` targets. The driver rejects host paths,
volume driver options, repeated resources/volumes, image-declared volumes and
unexpected inspected container settings before releasing agent code.

The `Catalog` resolves stable resource IDs; Docker volume labels bind workspace,
data audience, resource, revision and a hash of **all** readable mount and
credential grants. A permission reduction therefore refuses old derived data,
including secret-bearing checkpoints, even without a revision bump. This is a
conservative whole-context quarantine, not selective sanitization of old files.
The shared-directory fixture uses independently authorized read-only snapshots,
not a live writable directory jointly shared between clients.

The trusted bootstrap receives direct credentials over stdin, writes private
files, and execs the task with its private environment. Docker create env,
labels and command arguments contain no credential values. The host bounds
output to 1 MiB and redacts literal credentials, including a secret split
across writes; reads recheck current authority. A process can still encode or
copy its own authorized secret: output classification and audience enforcement
remain application responsibilities.

The host manager stores private durable attempt records, uses an exclusive
owner lock and kills the entire container on revocation/expiry. Lease maximum
is 15 seconds, refresh every 5 seconds, termination budget 2 seconds. Docker
or journal uncertainty is reported as `termination_unconfirmed`; admission is
blocked until reconciliation. Startup reconciliation validates exact ownership
labels and plan fingerprint, fences old processes and requires fresh admission.
It never revives old tokens, replays secret files or adopts a neighboring
service. `Close` reports an unconfirmed drain as an error.

## What the processes can see

The baseline crew implementation is mapped in the contract. Its shared UID,
crew mounts, shared tools, agent-level memory symlink, shared network and
sidecar are **source-derived findings**, not a live reading of another crew's
secrets. Existing internal crews are unchanged.

| Surface | Prototype observation / enforcement |
|---|---|
| Own data | Both humans using agent A read their own data, memory, checkpoint and direct secret; outputs persist across container reconstruction. |
| Foreign data | Other audience's resource is absent; its canaries are absent from visible files, proc environment/argv/root/fd probes and private log/checkpoint data. |
| Read-only share | Read succeeds. Direct write, symlink write, hard-link alias and remount attempts fail; original content remains intact. Full inspected mount list matches the plan. |
| Processes and control | UID 1001 sees its private PID namespace. Container termination includes a child holding an open secret FD. No host PID or IPC namespace is joined. |
| Sidecar | The actual compiled `cmd/crewship-sidecar` runs as UID 1002. Its `/broker` key/state is unreadable to UID 1001. Each human has an independent sidecar and synthetic upstream at identical loopback ports. |
| Credential proxy | Valid per-run token reaches the correct synthetic account. Other run's real token, forged token, missing identity and stale fingerprint are denied without increasing upstream call count. Fresh reconstruction uses a new run-bound key; the old token fails. |
| Host and network | Network mode is `none`, only loopback exists, no host-gateway or Docker socket is supplied. An owned host listener has a host-side positive control and is unreachable from the process; external IPv4, IPv6 and DNS probes fail. |
| IPC | There is no usable host/API capability. Sidecar memory/assignment/credential host routes cannot fetch host data in this offline fixture. This is not a claim that a future connected host API is already narrowed. |
| Restoration | Fresh attempt/generation remounts authorized persistent data; secrets are freshly delivered. Changed audience/provenance or a forged recorded container identity is refused. |

No agent/sidecar UID changed. No customer credential, live database, existing
crew, host, Docker daemon or shared dev instance was restarted or modified.
Only owned synthetic containers, volumes, image tags and temporary files were
created. The service independence fixture remains running while the manager
stops/reconciles its agent containers; the existing service controller code is
untouched.

## Reproduction and validation

Run from the isolated worktree:

```sh
go test -race ./internal/restrictedruntime -count=1
scripts/restricted-runtime-probe/run.sh
scripts/restricted-runtime-probe/run.sh -race
```

The script builds its own bootstrap and the real sidecar, records their
SHA-256s, pins the existing Alpine base by repo digest and removes its own
image/context afterwards. Live tests are opt-in; a normal `go test` skip is
not counted as process evidence. All failures in the opted-in gate are fatal.
The fixture authority is server-side synthetic state, not a mocked allow flag
inside the agent, but it does not substitute for the production grant store.

The first harness iteration used an image ID in Dockerfile FROM, which
BuildKit interpreted as a repository name; it was corrected to repo digest.
An initial sidecar fixture used the wrong provider path/token format; the
final fixture uses `/llm/openai-compat` and real per-run route-token derivation.
Negative shell checks use explicit failure branches, so `set -e` exemptions
for a bare `! command` cannot silently mark an allowed attack as denied.

Final validation and measurements are recorded below after the final run.

## Acceptance mapping and remaining release gates

| Contract row | Prototype coverage | Not claimed |
|---|---|---|
| R1–R3 | Real own/foreign data, direct env/file, proc/argv, private log/checkpoint, mount/alias/traversal/symlink checks | Every provider's OAuth/SSH/certificate adapter; host/kernel exploit resistance |
| R4 | Actual sidecar plus two independent same-agent accounts; wrong/missing/stale token, UID separation, fresh-run key | Production connected broker initialization/operation allowlist and current companion D1/D2 grant refresh |
| R5 | Kernel offline network profile, owned host endpoint positive control, direct IPv4/IPv6/DNS denial | Real-provider egress; offline denial is not a connected egress firewall |
| R6 | Queued/final-admission denial, open-FD child termination, lease expiry, authority outage, explicit Docker transport failure | Instant stop at DB commit; controller/host crash survival; provider-side revocation of already-copied keys |
| R7 | Data-preserving reconstruction, fresh secrets, changed provenance denial, exclusive owner, foreign identity refusal | Host/Docker reboot, backup import and distributed control-plane failover |
| R8 | On-disk private memory/checkpoint/log isolation and conservative provenance quarantine | Production recall/consolidation/model-session selection, API output audience and stream delivery |
| R9 | Runtime handle validation, all origin kinds and narrowing delegation with parent revocation/cycle rejection | Wiring actual chat/routine/webhook/queue endpoints to the new Authority |
| R10 | Independent owned service survives agent stop/recovery; no shared-mode fallback | Deployment or modification of the companion managed-service controller |

The host watchdog currently lives in the Manager process. Loss of authority
connectivity while it remains alive is tested. Killing that process or its
systemd cgroup can leave a container alive until restart reconciliation;
there is **no independent daemon watchdog guarantee** in this prototype.
This must be resolved or explicitly excluded before promising bounded
revocation during control-plane process failure. Kernel stalls/daemon failure
also prevent a universal physical-stop deadline; uncertainty stays visible.

Named volumes currently have no enforced disk quota. Log and tmpfs bounds do
not solve persistent-disk exhaustion. Secret scrubbing is literal, not a
classifier, and secret values legitimately read by a process cannot be recalled.
Short-lived upstream credentials or provider-side revocation remain required
where immediate external invalidation is promised. Containers share the host
kernel and Docker administrator authority remains trusted.

## Integration and rollout

The application team supplies current origin/principal/agent/attempt identity,
data-audience scope, generation, authority revision, exact resource/credential
references and lease deadline through `Authority`; storage supplies pre-existing
approved volumes through `Catalog`. `Plan` is a private host interface, never
an accepted client request schema. See the package README for the executable
API and lifecycle ordering. The runtime adds no grant tables or API routes.

Before production wiring, version the resource-label/provenance format and
add durable grants/generation fencing in the server adapter. Provision any
sidecar/broker before releasing untrusted code, with run-specific keys and
only permitted host operations. The harness currently owns this initialization;
it carries no real host credential and is not a ready-made connected adapter.

Keep existing internal crews classified as explicitly trusted/shared. Enable
restricted mode only after the application A2/B matrix, connected network
broker, storage limits and crash/revocation gates pass. Missing authority,
unsupported runtime or failed credential resolution is an error; it cannot
select the legacy crew path. No live deployment or Release 1.0 completion is
claimed by this prototype PR.
