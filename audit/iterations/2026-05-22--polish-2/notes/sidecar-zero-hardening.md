# Design note — Sidecar container zero-hardening

**Status:** design only — no PR yet (>200 LoC + multi-image testing
required, per loop spec).
**Source:** `audit/iterations/2026-05-21--polish-1/REPORT.md` —
"Sidecar zero-hardening (`provider/docker/sidecar.go:348-351`)".
**Author:** audit-loop, 2026-05-22.

## Current state

`internal/provider/docker/sidecar.go:348-351` builds `HostConfig` with
**only** `Mounts` + `RestartPolicy`. Every other knob is the Docker
default:

```go
hostCfg := &container.HostConfig{
    Mounts:        mounts,
    RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
}
```

Implications:

- `Privileged` defaults to `false` — OK.
- `CapAdd: nil`, `CapDrop: nil` — sidecar runs with Docker's full
  default capability set (CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL,
  MKNOD, NET_BIND_SERVICE, NET_RAW, SETFCAP, SETGID, SETPCAP, SETUID,
  SYS_CHROOT, AUDIT_WRITE).
- `SecurityOpt: nil` — no `no-new-privileges`, no seccomp tightening,
  no AppArmor/SELinux profile. **A setuid binary inside a sidecar
  image can escalate to root inside the container.**
- `ReadonlyRootfs: false` — sidecars get writeable `/`.
- `PidsLimit: nil`, `Memory: nil`, `CPUQuota: nil`, etc. — no
  resource caps. A bug or attacker can fork-bomb the host.
- `Tmpfs: nil` — typical hardening of `/tmp` / `/run` not applied.

## What's safe to ship

Two changes are **universally safe** for the common sidecar image set
(redis, postgres, mariadb, mongo, rabbitmq, nats, qdrant, chromadb,
ollama):

1. **`SecurityOpt: ["no-new-privileges:true"]`** — disables setuid
   privilege escalation. Equivalent to `--security-opt
   no-new-privileges` on docker run. No legitimate sidecar image
   relies on setuid post-startup; if any does, the test matrix
   below catches it.

2. **`PidsLimit: ptr(int64(512))`** — caps process count per
   container. 512 is generous (postgres + autovacuum + walwriter
   typically sits under 30 procs; redis stays under 5). Defends
   against fork-bomb-style DoS without breaking workloads.

These two combined drop the obvious privilege-escalation and
resource-exhaustion exits without touching the capability set, which
is where image-specific breakage lives.

## What's NOT safe to ship without per-image testing

- **`CapDrop: ["ALL"]`** — would break every image. Even minimal
  alpine-based sidecars need `CHOWN`/`SETUID`/`SETGID`/`DAC_OVERRIDE`
  for entrypoint user-switching.
- **`CapDrop: [<select set>]`** — would require running each common
  image and asserting it starts. Plausible drop candidates:
  `NET_RAW` (raw sockets — none of the sidecars need),
  `SYS_PTRACE` (debugger only), `MKNOD` (device creation),
  `AUDIT_WRITE` (kernel audit log writes). But "plausible" needs
  evidence. **Out of scope until image-test matrix exists.**
- **`ReadonlyRootfs: true`** — breaks redis (writes `/etc/redis.conf`
  on first start), breaks postgres (initdb writes everywhere on first
  boot), breaks any image with a runtime-mutable rootfs. Would
  require companion `Tmpfs: { "/etc": "", "/var": "", "/tmp": "" }`
  per image. Too much per-image surface.
- **`Memory: <cap>`, `CPUQuota: <cap>`** — workload-dependent. Would
  need a user-tunable knob via `services_json`, not a global default.
  Separate feature.

## Recommended PR (when picked up)

```diff
 hostCfg := &container.HostConfig{
     Mounts:        mounts,
     RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
+    SecurityOpt:   []string{"no-new-privileges:true"},
+    Resources: container.Resources{
+        PidsLimit: ptr(int64(512)),
+    },
 }
```

Plus:

- Helper `ptr[T](v T) *T` if not already present in the package.
- One integration test per common sidecar image (`redis:7-alpine`,
  `postgres:16-alpine`, `mongo:7`, `qdrant/qdrant:latest`,
  `ollama/ollama:latest`) that asserts the container reaches
  `healthy` status with hardening on. Lives under
  `internal/provider/docker/sidecar_hardening_test.go`, gated by
  `-tags=integration` so it doesn't run in the default CI suite.

Estimated effort: ~150 LoC source + ~200 LoC test = ~350 LoC. Above
the loop's 200 LoC threshold — hence this note, not a PR.

## Out of scope (intentionally deferred)

- Per-image `CapDrop` curation — needs the integration matrix first.
- `ReadonlyRootfs` + targeted tmpfs mounts — image-by-image work.
- Per-sidecar memory / CPU caps — needs a user-facing knob.
- AppArmor / SELinux profile authoring — Linux-only, distro-specific.

## How to pick this up

1. Branch `fix/audit-sidecar-hardening-baseline` from `main`.
2. Apply the diff above to `internal/provider/docker/sidecar.go`.
3. Add `internal/provider/docker/sidecar_hardening_test.go` with the
   5-image matrix gated behind `//go:build integration`.
4. Run `go test -tags=integration ./internal/provider/docker/...` on
   a host with Docker. Verify all 5 reach `healthy`.
5. PR title: `fix(provider/docker): sidecar baseline hardening
   (no-new-privileges + pids cap)`.
