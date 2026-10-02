# Container cleanup after crew deletion

Crew and workspace deletion leave crew tombstones. A periodic controller removes
runtime and managed service containers with a positively deleted crew owner and
an exact `crewship.instance-id` matching this installation. It scans stopped as
well as running containers. Missing owners, legacy containers without instance
labels, and foreign installation labels are never automatic cleanup candidates.
Container names and slugs do not establish ownership.

The installation identity is bound to the database, its location and the data
directory. The database holds only a random nonce; the identity itself lives in
`CREWSHIP_DATA_DIR/installations/<nonce>`, outside SQLite and workspace backups,
next to the canonical database location that claimed it. Several servers
sharing one data directory (for example `~/.crewship` on a development host)
therefore get distinct identities. A database found at a different location
than the one that claimed its nonce (a copy, or a moved file) is re-keyed with
a fresh nonce and gets a new identity, in the same data directory as well as in
another one, and even after the original has stopped. Moving a database file
therefore re-keys it: runtimes labelled with the old identity are no longer
adopted and must be removed by hand once. The running server holds an exclusive
lock on its identity; a second live server on the same database file starts
with automatic cleanup disabled and an empty instance label, as does a server
whose database has no stable location (in-memory, including
`mode=memory` URIs). The database location is the resolved path of the
database file itself, so a symlink to a live database is the same database and
meets its lock. Re-keying is a compare-and-swap on the nonce: several starts of
the same copy settle on one identity.
Do not copy the `installations` directory to another installation sharing a
daemon. New runtime
and service containers explicitly set the instance label, including an empty
value when identity is unavailable, to mask inherited image labels. Labels do
not cause an otherwise current legacy runtime to be rebuilt. Existing containers
labelled for another installation cannot be adopted, recreated or torn down
by the runtime/service creator, including when this server's own identity is
empty; the start error names the container. If this installation's identity
was reset (a new data directory), remove that container by hand without `-v`
so its volumes are kept, and the next run recreates it. An unreadable or
invalid installation identity fails server startup.

## Scope and retry

Automatic cleanup stops a container by immutable ID, records its limited mount
references, and removes it with `RemoveVolumes=false` and `Force=false`. It never
calls volume removal, image pruning, or host directory deletion. Processes and
the container writable layer are lost; named/anonymous volumes and bind source
data remain. Preserving an anonymous volume does not automatically reattach it
to a newly created runtime. This is not a user data recovery guarantee.

The existing explicit sidecar teardown performed by crew/workspace DELETE is a
separate contract: it may remove declared sidecar data volumes and reports its
own `sidecar_teardown` outcome. Automatic runtime cleanup does not call that
teardown, nor the CLI nuke pruner. Nuke behavior is unchanged.

The controller scans at boot and every 30 seconds. It reconnects for each scan,
including when the primary provider failed to initialize at boot. This restores
cleanup, not the server's primary execution provider; execution may still need
a restart after a boot-time provider failure. It limits removals to 100 attempts
per scan and uses 30-second operation deadlines. The complete inventory is never
capped. Missing containers are idempotent success; inspect, stop, remove,
inventory and diagnostic-write failures retry on later scans.

Owner deletion is rechecked before stop and after stop. This is eventual
consistency: a late create is removed by a later scan while its owner tombstone
exists. Revive/restore racing the last check can lose that container's process
and writable layer. Strong incarnation/restore coordination and persistent data
removal are outside this contract.

## Coordination with instance backups

A cleanup scan registers as a writer in the instance backup quiet window.
A new scan defers while admission is closed, and a consistent copy waits for
an in-flight candidate to finish its mount evidence, owner checks and
stop/remove. Large backlogs yield between candidates; no candidate yields
halfway through its destructive operation. The barrier also covers cleanup
diagnostic writes. This coordination is local to one server process; it does
not add the strong restore/revive fencing excluded above.

Idle runtime retention and cache eviction register with the same barrier,
including their database observations, and yield between eligible resources.
## Idle retention

Opt-in, separate from deletion cleanup, and never acting on deleted owners
(those are the controller above) or on another installation's containers.
Enabled by `CREWSHIP_IDLE_RUNTIME_RETENTION_DAYS` and `CREWSHIP_CACHE_EVICTION`
(`container.idle_runtime_retention_days`, `container.cache_eviction`); runs at
boot and every 10 minutes while the Docker provider is available.

**Stopped runtimes.** A runtime container (kind `crew`, this installation's
`crewship.instance-id`) of a crew that is live in this database is removed once
Docker reports it stopped at least the configured number of days. Docker's
`FinishedAt` is the stop time: a stopped runtime cannot have run anything since.
A zero or unreadable stop time, a running or never-started container, a
sidecar service, a legacy container without the installation label, a deleted
owner and an owner this database does not know all stay. The removal holds the
crew's start lock, re-inspects the container and re-checks the owner, records
its mount references in `resource_cleanup_mounts`, and removes it with
`Force=false` and `RemoveVolumes=false`; a start that won the race leaves a
running container Docker refuses to remove. Named volumes, anonymous volumes
and the host data behind them stay. The next start recreates the runtime.

**Cache images.** A `crewship-cache:*` image is evicted when no container on the
daemon uses it — any installation, any state — continuously for one hour
(tracked per installation in `resource_retention_images`; any use restarts the
hour), it is older than one day, and no `crewship-provision-*` build container
exists anywhere on the daemon. A database reference from a crew does not keep
an image: every crew start waits for a missing cache image to be rebuilt —
`crew start`, chat, dispatch, routine and pipeline steps, scheduled runs,
webhooks and the terminal all pass the same image gate in `crewstart` (about
35–40 s with the base image present). A failed rebuild fails the start with
`crew image not ready` instead of starting from another image. Removal uses `Force=false`; Docker refusing it keeps the image
and restarts its hour. Base images, `crewship-feat:*` images and BuildKit
cache are out of scope. Because tags are configuration hashes shared through
the daemon, eviction by one installation can make another rebuild on its next
start.

## Observations

Crew DELETE retains HTTP 200 and adds `cleanup` with `scope=containers`,
`state`, `observed_at`, `complete`, `remaining`, `unattributed`, and a fixed error
code when relevant. The immediate response is `pending`, not proof of physical
removal. The crew delete CLI prints the returned cleanup scope and state. Without a configured controller it is `disabled`.

`observed_clear` means no eligible container in the complete final scan for that
owner. It excludes volumes, host data, images, unlabelled legacy containers and
future late creates. `unattributed` counts legacy containers for that tombstone
owner in the initial scan. It is not a host inventory or retirement decision.
An incomplete scan or disconnected Docker is `unknown`; a failed candidate step
is `error`; a removal backlog is `pending`.

`GET /api/v1/admin/resource-cleanup` and `crewship admin cleanup` read persisted diagnostics. It uses the
authenticated admin gate. Instance administrators can read this installation's
observations across all workspaces without selecting or belonging to a workspace,
including observations left by deleted workspaces. Workspace ADMIN/OWNER users
continue to see only their own workspace; other tenants' crew IDs and states
are never exposed to them. `crewship doctor cleanup` remains a local read-only
alternative.
Freshness comes from one per-installation scan record written on every
complete scan. Per-owner records are written only when their state changes, and
a tombstone that never had a container needs no record at all, so the steady
write load does not grow with the number of deleted crews. A record whose crew
has since been revived is dropped rather than reported as current. When the last
complete scan predates process boot, is older than 90 seconds, or the latest
scan failed, every observation reads as `unknown` with `complete=false`; the
owner's or the scan's error code remains visible.

If no authorized workspace remains, `crewship doctor cleanup --format json`
reads the existing local database using local file permissions. It never runs
migrations, creates a database, contacts Docker, or performs cleanup. Its JSON
marks `observation=persisted`; the records are historical, not a live inventory.
DATABASE_URL selects the same local file as other readonly doctor probes.

Diagnostics and mount references live in `resource_cleanup_status`,
`resource_cleanup_scans` and `resource_cleanup_mounts`; the database nonce lives
in `resource_cleanup_installation`. None of them is part of a workspace backup. They have no owner FK, so deleting a workspace cannot
cascade away evidence for surviving mounts. They contain no env, commands,
container logs, or credential values. Mount evidence is retained until a future
retention contract authorizes removal; it grants no permission to delete data.
Workspace backup/replace survival of these installation diagnostics is not
promised.

## Verification

The acceptance suite uses disposable SQLite databases and a fake Docker REST
server. It covers deleted versus live/missing/foreign/legacy owners, runtime and
sidecar create labels, label-only contract stability, distinct identities
for databases sharing a data directory, a copied database, a second live holder,
change-only persistence, mount evidence before
removal, boot failure/reconnect, late create, removal backlog, restart staleness,
provider/inspect/stop/remove failures, the HTTP response, and local readonly
access without workspace membership. It does not execute a cleanup against a
shared development daemon.
