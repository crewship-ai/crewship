# Quota service snapshots

This implementation checkpoint provides offline image capture and authenticated
helper import primitives. Complete service restore and operator recovery wiring
are release gates; this checkpoint does not claim a service backup/restore
roundtrip through the HTTP API or CLI.

A backup that declares fixed-ext4 service volumes requires a host snapshot
transport and a durable service intent. Missing transport or intent rejects the
backup rather than producing a bundle that advertises missing service data.
Ordinary crew filesystem sections do not stand in for standalone service data.

The collector acquires a durable per-crew maintenance fence only when no
controller operation has an active lease. Controller selection and atomic lease
claims exclude fenced crews. The fence also rejects service intent mutations,
service declaration changes and crew/workspace removal. Original desired state
and intent version remain unchanged. A failed capture retains the fence so a
process restart cannot resume writers against a partial snapshot.

The host stops exactly owned service containers, disables their restart policy,
removes stopped containers and detaches the exact labeled Docker volume alias.
The quota helper refuses export while any catalog reference or mount alias
remains. After unmount propagation, the helper verifies that no mount in any namespace
remains, including a private alias at the same canonical path. Export unmounts the verified image,
detaches its loop device, checks ext4 offline, streams exactly its fixed physical
size and restores only the helper mount. It never copies a mounted image.

Each image and its metadata live inside the payload encryption boundary.
Metadata records source namespace, crew identity, service, volume, immutable
generation, physical size, SHA-256, desired state and intent version. A successful
bundle releases maintenance only after writing the complete bundle. Bundle
format 4 prevents older restorers from silently discarding the new sections.

Helper import accepts a host-selected destination key in its immutable catalog
namespace. It refuses existing generations, physically reserves aggregate disk
capacity, verifies the complete offline ext4 image, and publishes recoverable
metadata only after verification. Archive paths never select a helper root or
host mount location. UID/GID and file modes are retained by the filesystem image.

The library restore validates image hashes and exact owner/intent revisions,
selects a fresh host generation after ID remapping, and holds restored desired
state behind maintenance until every image import and database commit succeeds.
Existing service operations use durable leases and re-admit immediately before
Docker Start. Unknown outcomes retain maintenance or bounded operation leases.

Expired producer maintenance can only be adopted by an explicit recovery option;
a live producer/controller/service operation rejects adoption. Publication holds
the SQLite writer lock while proving the exact unexpired epoch, so an old
producer cannot publish after recovery takes over. Failures never auto-resume.

The existing admin API carries the host-configured transport. Backup create and
restore expose `--recover-services`; status lists only maintenance in the selected
workspace, including whether its producer is still live. The option never clears
a live producer or writer lease.

This remains an implementation checkpoint: the complete encrypted two-owner
disposable roundtrip and controller restart proof are pending. It must not be described as completed backup/restore
acceptance. Targeted tests cover fresh generation selection, missing legacy data,
and stale publication; opt-in physical tests cover filesystem ownership, quota,
namespace binding and partial import rejection.
