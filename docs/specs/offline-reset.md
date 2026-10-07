# Offline installation data reset

`crewship reset --data` resets a stopped local installation. An explicit absolute
`--data-dir`, `CREWSHIP_HOME`, or `CREWSHIP_DATA_DIR` is required; neither cwd nor
the fallback user home selects a destructive target. The command is part of the
full server binary, not the remote-only `crewship-cli` build.

Use `crewship reset --data --data-dir /absolute/installation --plan` to review
validated local targets without creating stores or connecting to Docker.
Execution has the following order:

1. Resolve the server configuration and reject unsafe or ambiguous paths.
2. Acquire the installation operation lock, existing SQLite writer lease and
   existing identity lock. A live server or another reset makes this fail.
3. Build a temporary clean database using this binary's ordinary and deferred
   migrations under the installation's `run` directory.
4. Stop and remove containers, then remove volumes and networks whose exact
   `crewship.instance-id` matches this installation. Reinspect ownership before
   removal. Docker refusal aborts before clearing application rows or host stores.
5. Recheck configured targets and clear supported file-store contents using a
   root-confined filesystem handle. Clear Bolt contents while retaining its inode.
6. In one SQLite transaction, restore application rows to the fresh database's
   migration defaults, preserving schema, migration history and installation
   nonce. Check foreign keys before commit.

The database file is not unlinked or replaced: the writer lease covers its inode
throughout reset. The nonce and existing `installations` files preserve Docker
ownership across resets and retries. Reset never creates, adopts or rekeys an
installation identity; a missing identity or copied/moved DB is an error.

## Scope and safety

Configuration, secrets, setup token, logs, backups and installation identity are
retained. The supported stores are `output`, `data`, `memory`, `page-projects`,
`chats`, `skills`, and the corresponding `state/data`, `state/memory`, and
`state/page-projects` layout. Only the stores selected by current configuration
are cleared, together with chats and installed skills. External overrides and
unproven custom directory layouts are refused; migrate them to managed paths
while stopped first. Stores overlapping preserved paths, SQLite, Bolt or the
installation root are refused. A source checkout cannot be the reset root.
Symlink escapes are refused; symlinks within cleared stores are removed without
following their external targets.

Images, build cache and legacy/unlabelled Docker resources are retained. Volume
and network removal is never forced; a resource still used elsewhere aborts the
operation. Apple container cleanup is not supported by this reset command.
Docker inventory is required; an unavailable daemon is not evidence that the
installation owns no resources.

The operation is repeatable, but is not a transaction spanning Docker, host
files and SQLite. A failure after Docker cleanup or file deletion can leave a
partially reset installation; keep the server stopped and repeat the command.
SQLite row replacement itself rolls back on error. Reset requires this binary's
complete schema; finish upgrades before resetting.

The server and offline reset hold `run/installation.lock` for their entire
lifetimes. Its anchor is retained and must never be deleted to bypass a lock.
Older servers without this operation lock are additionally detected by the
existing SQLite writer lease and identity lock.

## Operational sequence

```sh
# Stop through the installation's existing supervisor first.
crewship reset --data --data-dir /absolute/installation --plan
crewship reset --data --data-dir /absolute/installation
# Start through the same supervisor; wait for /readyz.
crewship seed --server https://explicit-target.example
```

This first reset implementation does not stop or restart a supervisor. It
replaces the original online/no-restart acceptance with an explicit offline
workflow. Provider-backed seed needs valid provider credentials; use
`--offline-demo` only for UI/control-plane fixtures.

Tests: `TestOfflineReset*`, `TestResetRefusesExternalOrPreservedStore`,
`TestResetApplicationData*`, `TestAcquireExistingIdentityNeverCreatesOrRekeys`,
and `TestResetOwnedDockerLabelsOnlyAndNoForce` exercise retained management data,
identity, retry, live-lock refusal, foreign Docker isolation, SQL rollback and
symlink confinement without destroying a live installation.

An atomic `run/reset-in-progress` marker is fsynced before the first destructive
step. It identifies the verified installation, not individual Docker resources.
Startup fails closed while it exists; a retry with the same identity is allowed.
Only complete success removes and syncs the marker. Cleared store directories
and truncated Bolt files are synced before success; retry also syncs an already
published marker before destructive work. Search indexes are cleared and rebuilt
from clean content, and AUTOINCREMENT sequences return to the clean
migration defaults. Relative SQLite targets and multiply linked database/state
files are refused for destructive reset even during the startup compatibility
window for relative database URLs.

An explicit YAML `--config` must also be absolute. With no flag, config.Load
uses defaults and environment; it does not discover a cwd YAML file. Pass the
same configuration used to start the installation.
