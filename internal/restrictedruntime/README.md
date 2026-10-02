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
15 seconds. There is no legacy fallback. A second lease guard runs as the
container's protected init child under UID 1002. The Manager atomically renews
its absolute deadline through a private Docker-stdin protocol. Without renewal
it exits, causing init/the PID namespace to terminate even after Manager
SIGKILL. Agent commands still run as UID 1001 and cannot signal init or write
its lease. This does not promise progress during kernel/host suspension or
prove provider-side invalidation of a previously read secret.

`Session.Output` is bounded, currently reauthorized and literal-redacted,
including secrets split across writes. This is not a public stream API or a
proof of data classification. Application audience/stream enforcement remains
mandatory. Encoded secrets and data intentionally copied by an authorized run
are not removed by a literal scrubber. Persistent state carries conservative
provenance covering all mounted resources and credential references; reducing
rights requires a new storage generation rather than restoring broader state.

The original profile is **offline Docker on Linux**: private PID/IPC/
network namespaces, agent UID 1001, protected init/broker UID 1002,
no privileges/capabilities, read-only rootfs,
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
builds its own image containing a trusted bootstrap, the **actual Crewship
sidecar** and the native input snapshot verifier, then removes its image and
temporary build context. The verifier does not require a Codex binary or perform
model inference. Tests create
uniquely named owned containers/volumes and remove them. The sidecar fixture
uses UID 1002, its own private `/broker` state and an in-namespace synthetic
upstream. It has no usable host-API credential or external network access.
Its local synthetic budget authority accepts only the pinned fixture agent,
credential and provider; the live gate checks that refusal prevents upstream
access. Other IPC operations are unavailable.
This verifies direct secrets and actual proxy/run-token behavior without
changing sidecar production code. Sidecar startup is harness-owned, not yet a
production runtime adapter; it must be provisioned before untrusted agent code
and expose only approved broker operations in the eventual connected profile.

`go test ./internal/restrictedruntime` runs policy/unit tests and
excludes the build-tagged live gate (`restrictedruntime_live`). The script
sets `CREWSHIP_RESTRICTED_LIVE=1` and supplies
its image through `CREWSHIP_RESTRICTED_IMAGE`; failures are not skipped.
Production chat/routine/webhook/queue adapters, server-selected recall,
external network mediation, hard disk quotas, upstream credential revocation,
host reboot and full application acceptance remain integration/release gates.
Controller SIGKILL expiry is covered by `TestLiveControllerCrashExpiry`.

## Fixed-operation connected prototype

`Plan.Profile="brokered-http-v1"` selects a separate, restricted prototype.
The container still has **network=none**. A UID-1002 loopback broker passes only
fixed operation IDs and bounded bodies over private Docker stdin/stdout to a host
relay. Host-side `BrokerAuthority` reauthorizes operations and broker-only secrets,
including exact account/revision binding. DNS is checked on each operation, including connected interface subnets; dial
uses the validated IP and TLS verifies the original hostname. No redirects,
proxy environment inheritance, arbitrary destination/method/header forwarding,
CONNECT, WebSocket, SSE, streaming, refresh or persistent RW storage is supported.
This is not general model/provider networking or completed Release 1.0 isolation.

Agent bootstrap receives `CREWSHIP_BROKER_URL` and `CREWSHIP_BROKER_TOKEN`.
It can POST a bounded body to `/v1/operations/<server-granted-ID>` with the token
in `Authorization: Bearer …`. No other local route is offered. The opaque token
is per attempt; retries cannot reuse it. The broker must be ready before launch;
relay/broker failure cancels active work, stops and removes the owned container.
No new upstream call is admitted after observed revocation. Already-started
external effects cannot be undone. Responses are buffered and reauthorized.

See `docs/prd/RESTRICTED-HTTP-BROKER-CONTRACT-2026-09-28.md` for the exact scope,
limits, ownership boundary and acceptance contract. The same live harness now
also tests the fixed-operation broker with synthetic TLS/DNS/neighbor controls.
The private test transport seam maps a checked synthetic routable address to an
owned loopback TLS endpoint; the production constructor exposes no such bypass.

## Bounded streaming transport

`Plan.Profile="brokered-http-v2"` and network version 2 additionally support an
explicit `HTTPGrant.ResponseMode="sse"`. Version 1 retains buffered-only behavior;
no operation implicitly gains streaming. The endpoint, method, account and
request budget remain server-selected. This transport does not itself supply a
production model/provider adapter or enable an application entrypoint.

An SSE grant permits at most 1 MiB of upstream bytes and delivered bytes per
response and a timeout of at most five minutes. Only a successful HTTP 200
`text/event-stream` response with identity encoding is accepted. Each emitted
frame rechecks live authority and the issued credential's expiry. The watchdog
also ends an idle stream when authority expires. A denied/truncated stream aborts
the local HTTP connection and cannot be mistaken for the reply to another
request. Client disconnect closes the broker instead of recycling an outstanding
reply into another request. There are no redirects, arbitrary headers, WebSocket
or general proxy capabilities.

Literal broker secrets are removed across read boundaries; an unfinished secret
prefix is withheld at EOF. This does not remove encoded secrets or classify
authorized model output. Model-specific completion semantics, pricing, credential
grant adapters and downstream application audience checks remain separate gates.

An explicit `HTTPGrant.Responses` policy adds a stateless text-only OpenAI
Responses operation and the local SDK alias `POST /v1/responses`. It fixes the
model and per-request output ceiling, forbids remote resources/tools/state, and
uses the same host-side authorization for both local paths. It does not enable
native agent tool loops or production dispatch. Contract and acceptance:
[`RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md`](../../docs/prd/RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md).
