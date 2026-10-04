# Staged runtime start

The Docker egress pilot (`container.egress_fence_crews` or
`CREWSHIP_EGRESS_FENCE_CREWS`) starts selected restricted, nonprivileged runc
crews using a trusted static Linux keeper as UID1002. The image entrypoint,
shell, loader and healthcheck do not run before the current boot's fence has
been installed and read back from the kernel. Unselected crews retain their
legacy startup behavior. Native Linux is the qualified host platform.

The host binds content-addressed copies of its static sidecar and bootstrap
under the runtime data directory, read-only at reserved targets. An unreadable,
mutable, dynamic, wrong-architecture or missing artifact fails admission.
Overlapping mounts, image-declared VOLUMEs, additional security options or
capabilities, host namespaces and Docker socket mounts are unsupported in the
pilot. Ownership initialization uses only a trusted offline static helper,
never a program from the image; it does not follow symlinks or fall back to
world-writable directories.

Each keeper boot creates a new random nonce. The host checks actual immutable
container configuration, installation and crew ownership, current StartedAt,
live keeper response and fresh kernel fence readback. A UID1001 static gate
must consume the current nonce's bootstrap permit before running the fixed
bootstrap. A restart between host Check and Docker exec rejects the old nonce
before any image program starts. Each boot consumes at most one bootstrap
permit; nonzero or unknown completion cannot mark the boot ready.

The keeper stops an unreleased boot after its bounded 30-second prepare or first-contact reconciliation window without deleting data.
After a lost exec response the controller reconciles keeper status instead of
blindly starting another bootstrap. Confirmed completion is reusable; ambiguous
or failed boots remain unavailable. Controller restart preserves a live keeper's
state. This is not a guarantee of durable model execution or arbitrary exactly
once side effects after a crash.

Ordinary and interactive exec, startup hooks and provider-wired backup/restore
exec enter through a static gate that checks the current ready nonce inside the
executing namespace. The existing trusted launch qualification for AI CLI
artifacts is a separate contract. The keeper does not start the credential
sidecar; the orchestrator still does so after runtime admission.

An already running or stopped legacy container cannot be opted in by changing
a desired configuration or checking a label. Its actual entrypoint and mounts
must match the staged contract; otherwise start/reuse is denied before restart
or removal. Drain and explicitly recreate an owned runtime to activate the
pilot, retaining persistent binds and named volumes. Already-created staged
runtimes retain their gate until explicitly recreated, even after removing the
selector. A failed stage does not fall back to legacy.

S01 retains the legacy fence's UID1002 network exception and crew-shared data.
It does not deliver per-agent instance isolation, a B gateway, hidden credentials,
OAuth qualification, new dispatch, or financial hard-cap guarantees. See
[container isolation](../security/container-isolation.mdx) for the existing
pilot's network and credential limitations.

Original expanded workload environment stays in private, unmounted host runtime material. Docker metadata contains only an opaque random reference; reads check installation, crew, runtime and image scope, trusted ownership, size and symlink safety. Missing or corrupt material denies launch. Runtime recreation is required for config or artifact generation changes.
