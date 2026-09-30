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
whose database has no stable location (for example in-memory).
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
existing authenticated ADMIN/OWNER workspace gate and, like every other admin
read, returns only crews of the caller's workspace; other tenants' crew IDs and
states are never visible. Once a workspace is deleted its rows are readable
only locally through `crewship doctor cleanup`.
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
