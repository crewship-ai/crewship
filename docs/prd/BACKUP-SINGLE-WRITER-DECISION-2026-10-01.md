# Instance backups: one enforced writing server for release 1.0

Date: 2026-10-01 · Status: **product decision accepted; implementation and release verification pending** · Issue: [#2756](https://github.com/crewship-ai/crewship/issues/2756)

## Decision

Release 1.0 supports one writing Crewship server per persistent local SQLite
database. This is an enforced ownership contract, not an operator convention.
A second server must fail clearly before opening SQLite for writes, running
migrations or starting background workers. Read-only inspection commands can
continue to use their existing read-only database connections; a second
read-only server mode is not introduced by this decision.

The process-lifetime OS lock binds the actual database file, catching alternate
symlink and hard-link names. It is held until SQLite closes and released by the
kernel on process exit. A pathname is never unlinked to recover a stale lease.
The implementation must verify that the configured name still identifies the
locked file; replacing a live database invalidates the owner.

A whole-instance quiet window verifies that same owner before admission and
again after draining writers and acquiring its guards. The copy checks the
captured owner before reading and after copying, before releasing the window.
A missing or changed owner aborts the operation and discards staging; it must
never publish a bundle as consistent. Health and readiness expose lost
ownership without disclosing database paths. The server stops when its owner
check fails.

## Supported deployment and upgrade

Local filesystems with an established lock contract are supported. The Linux
implementation accepts ext2/3/4, XFS, Btrfs, tmpfs, overlay, ZFS and F2FS; macOS
requires a local mount and Windows a fixed local drive. Network, FUSE and
unknown Linux filesystems are refused. A filesystem reporting local storage
is not permission to mount the same block device read/write on multiple hosts.
Such shared storage is unsupported.

Upgrade requires stopping every old daemon using the database before starting
the enforcing version. Older binaries and arbitrary SQLite tools do not
participate in the new OS lock protocol. Offline mutating maintenance must run
with the server stopped; the gated local database commands, telemetry consent and ledger repair
acquire the same exclusive ownership. Live administrative changes use the API.
Do not move, unlink or replace a database while its server is running.

Existing scheduler leader election remains useful for its lease semantics, but
is not a promise of multiple writing replicas in 1.0. An active multi-replica
runtime or shared-storage database needs a separate PRD covering distributed
writer fencing, consistent file storage and backup ownership. A heartbeat row
or a check only on the process starting the backup is not sufficient.

## Acceptance gates

- A second process using the same database or a supported path alias is refused.
- Crash/restart releases ownership without deleting data or lock anchors.
- Loss before Begin, after drain and during copying aborts the backup and
  prevents publication, including callers supplying an already-open window.
- Lost ownership makes health/readiness fail with a non-sensitive reason.
- Final-SHA CI and actual review pass; a cold upgrade and backup/restore are
  checked in an isolated environment before release acceptance is claimed.

This decision does not close off-site recovery acceptance, the complete writer
inventory, service ownership recovery or cleanup/revive fencing by itself.

OS contracts: [Linux flock](https://man7.org/linux/man-pages/man2/flock.2.html)
and [Windows LockFileEx](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex).
