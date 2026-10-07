# Server installation paths

This contract describes server path resolution, startup protection and developer
defaults implemented for #2977. It is intended for operators and contributors.
Client configuration and Docker installation identity have separate ownership;
this change does not move either of them or migrate existing data automatically.

`crewship paths` reports the effective paths and configuration sources without
creating an installation or loading client profiles. Local offline reset follows
[the reset contract](offline-reset.md).

## Root and precedence

Server startup chooses an absolute root from `start --data-dir`, `CREWSHIP_HOME`,
the compatibility alias `CREWSHIP_DATA_DIR`, then the user's `.crewship`
directory. Conflicting nonempty home aliases are rejected. Relative
`CREWSHIP_HOME` or `--data-dir` is rejected; relative `CREWSHIP_DATA_DIR` remains
compatible. Resolving a root does not create it.

`internal/config/resolve_paths.go` records the source of each effective path.
Explicit environment values win over YAML, which wins over derived defaults.
An explicit YAML path equal to a historical default is still explicit. Empty
environment values retain the configured value; an empty YAML memory root
disables file-based workspace memory. Mandatory empty paths are rejected.
The existing database flag wins over `DATABASE_URL`.

| Server resource | Derived location |
| --- | --- |
| SQLite | `<root>/crewship.db` |
| Crew storage | `<root>/output` |
| Agent logs | `<root>/logs` |
| Workspace memory | `<root>/memory` |
| Bolt | `<root>/state.db` |
| IPC | Existing platform socket derivation from the root |

Setting an explicit database no longer changes derivation of other paths.
Explicit relative SQLite URLs emit a deprecation warning; they and relative
path overrides retain their working-directory interpretation for compatibility.
They are not a portable
installation configuration; use absolute paths. The socket length fallback and
packaged socket behavior remain compatible.

During server lifetime, `TMPDIR`, `TMP` and `TEMP` point to `<root>/run/tmp`.
This contains temporary build, import and backup work; callers' environment is
restored when startup returns. Update-check caches live under `<root>/cache`.
The short socket fallback is selected before scratch temporary paths change.

## Existing data protection

Before bootstrap writes and database opening, startup compares persistent
locations against those the previous startup algorithm would have used for the
same inputs. If a changed path would leave existing files behind, startup stops
and identifies the override needed to retain the old store. This also covers
previously ignored explicit storage and log overrides. A populated destination
does not make abandoning the old store safe.

Empty directory trees and canonical aliases of the same path are not migration
conflicts. An unreadable relevant old path or dangling symlink is an error.
The guard examines only known prior paths; it does not discover every historical
layout. It neither adopts another installation's data nor moves or deletes it.
An operator must pin the previous location or migrate it while stopped.

## Development script

`dev.sh` loads its trusted shell `.env.local` once while preserving incoming
exported values, including explicitly empty values. Fresh co-located sidecar
and entrypoint artifacts still take precedence to preserve runtime compatibility.

Absent an explicit root, the development installation lives under
`${XDG_DATA_HOME:-$HOME/.local/share}/crewship/dev/<checkout-checksum>`. Its
database, data, state, logs, Page projects and build artifacts use that root.
PID and socket files use a per-installation XDG runtime directory when available,
otherwise the root's `run` directory. Explicit path pins retain compatibility.
Relative storage, log, Bolt, Page project, socket and memory overrides are
resolved once against the checkout, so start and cleanup use the same locations
from any invoking directory.
This wrapper selects its own explicit layout; the server's defaults above do
not change to match the wrapper's directory names. Default Docker prefixes and
networks derive from the canonical installation home; explicit pins win.

The script rejects abandoning nonempty legacy temporary data directories and
refuses startup or destructive cleanup while a verified old-layout dev server
is still running. Stop the old instance before restarting with the new layout.
Destructive directory cleanup refuses source checkout paths and their parents.

## Seed boundary

Demo seeding requires an explicit server flag and an Anthropic API key, Claude
OAuth token or supported renewable Codex login before bootstrap or workspace
mutation. Seed no longer loads `.env.local` itself. The dev wrapper still loads
its configuration before invoking seed.

Stored credentials and workspace selection must match the explicit target URL.
An explicit matching profile can distinguish workspaces on the same server.
Bootstrap saves credentials into a matching or generated target profile without
repointing unrelated defaults; a fresh client can select its first target profile.
Profiles are currently matched by URL, not by a
new public installation-ID handshake.

An explicit offline demo creates control-plane fixtures without claiming usable
model execution. It does not provision model credentials or run model producers.

Memory seeding uses `POST /api/v1/memory/initialize`, restricted to OWNER/ADMIN
and scoped to the target workspace's crew and agent records. The server resolves
the canonical memory roots from its storage configuration; the client supplies
document bytes only. Fresh memory is initialized before provisioning can change
ownership of the tree. Known demo tier paths, content checks and size limits are
validated before writes. Publication is durable and atomic without replacing
existing files, including operator edits. Partial filesystem failures are fatal
and retry preserves completed writes. Symlink parents and leaves are refused.
Already container-owned trees may require the operator to restore host write
access for missing files; ordinary live memory import retains its container writer.
Demo users use the existing OWNER/ADMIN member provisioning API with
`create_only=true`, followed by its single-use account setup token redemption
against the explicit seed target. Public signup remains disabled if configured
that way. Existing accounts are never reset: out-of-workspace accounts must
prove their fixture credentials before membership is restored. Any missing or
drifted role fails the four-user fixture. No new authentication bypass is added.

## Health compatibility

`/healthz` reports liveness and returns 503 on lost writer ownership. `/readyz`
also checks database and state readiness. `/api/health` uses the liveness handler,
including its writer check, and preserves the healthy `status: ok` field.
This change does not move lease loss to readiness or add a new restart policy.

## Verification boundaries

- `internal/config/resolve_paths_test.go`: path origins, DB independence,
  precedence, cwd, legacy content and symlink cases.
- `cmd/crewship/cmd_start_paths_test.go`: legacy rejection before bootstrap writes.
- `internal/database/datadir_home_test.go`: root selection and read-only resolution.
- `cmd/crewship/cmd_seed_preflight_test.go`: preflight before requests and target
  profile isolation.
- `scripts/dev-sh-test.sh`: shell precedence, isolated dev paths and cleanup guards.
- `scripts/host-paths`: AST check and shrinking baseline for known independent
  host roots; this is not a sandbox or proof of all filesystem writes.

Docker cleanup remains governed by [container cleanup](container-cleanup.md).
Page project lifecycle remains governed by [Pages Apps](pages-apps.md).
