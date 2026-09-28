# Restricted runtime prototype

This package is **not connected to production dispatch**. It implements the
runtime half of the server contract in
`docs/prd/RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md`. The existing crew
runtime and managed-service controller are unchanged.

The trusted server implements two interfaces:

- `Authority.Resolve(ctx, opaqueHandle)` returns a current, server-constructed
  `Plan`. `Secrets` resolves exactly the credential references in that plan.
  Both must honor context cancellation. Handles must be unguessable server
  capabilities bound to authenticated origin and durable attempt generation;
  never deserialize a public request into a Plan. The fixture authority in the
  tests is synthetic and is not an application grant store.
- `Catalog.Volume(ctx, plan, mount)` resolves stable resource IDs to pre-existing,
  independently provisioned Docker local volumes. The runtime validates their
  workspace/scope/resource/revision/provenance labels. Host bind paths, volume
  driver options, repeated volumes and overlapping targets are rejected.
  `resourceLabels` currently defines a prototype-local format; application
  storage provisioning must explicitly adopt/version it before integration.

Construct `New(privateStateDirectory, docker, authority, catalog, limits)`,
call `Reconcile` on startup, then `Start(ctx, handle)`. Fresh state has nothing
to reconcile. Retrying uses a new attempt ID/generation and fresh authority.
`Session.Stop` terminates the complete container and records whether Docker
confirmed it. `Close` drains owned attempts. A host-side watchdog rechecks
at five-second intervals and enforces the issued lease, which cannot exceed
15 seconds. There is no legacy fallback. The watchdog is part of this host
process: a host/controller crash is an explicitly unproven live-revocation
case until restart reconciliation, not a promised independent daemon.

`Session.Output` is bounded, currently reauthorized and literal-redacted,
including secrets split across writes. This is not a public stream API or a
proof of data classification. Application audience/stream enforcement remains
mandatory. Encoded secrets and data intentionally copied by an authorized run
are not removed by a literal scrubber. Persistent state carries conservative
provenance covering all mounted resources and credential references; reducing
rights requires a new storage generation rather than restoring broader state.

The only supported profile is **offline Docker on Linux**: private PID/IPC/
network namespaces, UID 1001, no privileges/capabilities, read-only rootfs,
explicit CPU/RAM/PID limits and bounded private tmpfs. Network is `none`.
Image-declared volumes and unexpected daemon configuration fail the audit
before user code/secret delivery. Agent credentials enter over stdin into
private tmpfs/env, never Docker create env or argv. Docker remains trusted.
Named-volume disk usage is not quota-enforced; do not expose this prototype
to hostile clients or advertise it as a production tenant sandbox.

Run executable acceptance from the repository root:

```sh
scripts/restricted-runtime-probe/run.sh
```

Requires local Docker, the already-present `alpine:3` image, and Go. The script
builds its own image containing a trusted bootstrap plus the **actual Crewship
sidecar**, then removes its image and temporary build context. Tests create
uniquely named owned containers/volumes and remove them. The sidecar fixture
uses UID 1002, its own private `/broker` state and an in-namespace synthetic
upstream. It has no usable host-API credential or external network access.
This verifies direct secrets and actual proxy/run-token behavior without
changing sidecar production code. Sidecar startup is harness-owned, not yet a
production runtime adapter; it must be provisioned before untrusted agent code
and expose only approved broker operations in the eventual connected profile.

`go test ./internal/restrictedruntime` runs policy/unit tests and explicitly
skips the live gate. The script sets `CREWSHIP_RESTRICTED_LIVE=1` and supplies
its image through `CREWSHIP_RESTRICTED_IMAGE`; failures are not skipped.
Production chat/routine/webhook/queue adapters, server-selected recall,
external network mediation, hard disk quotas, upstream credential revocation,
controller-crash expiry and host reboot are remaining integration/release gates.
