# PRD-DEVCONTAINER-BUILDKIT-2026

**Status**: design — Phase 1 in progress
**Owner**: devcontainer / provisioning subsystem
**Date**: 2026-06-24
**Related**: PR #673 (`fix/devcontainer-feature-install-order` — feature install order fix that unblocked provisioning)

## Background

Provisioning an agent container today uses a **container-commit** pipeline
(`internal/devcontainer/provisioner.go` + `provisioner_install.go` +
`installer.go`):

1. Run a temp container from the base image as root.
2. `CopyToContainer` each downloaded feature tar to `/tmp/devcontainer-features/{id}`.
3. Run each feature's `install.sh` sequentially via `docker exec` (`bash -e`).
4. Install mise runtimes, run postCreate hooks, write aggregated containerEnv.
5. `ContainerCommit` → `crewship-cache:{configHash[:12]}`.

**The problem: it's slow and rebuilds everything on any change.**

- **No layer cache.** Adding one feature changes `configHash` → the *entire*
  image rebuilds from scratch, including `common-utils` (apt update + user
  creation) on every single build.
- **No package-manager cache.** Every `install.sh` re-downloads apt/npm/pip
  artifacts; nothing is reused between builds.
- **Sequential installs**, opaque progress, and the build only starts on the
  first chat/trigger rather than when the operator finishes picking features.

## Product framing — who this is for (agent-first)

Crewship is **agent-first**: an agent inside the container can install any tool
it needs at runtime. So why a feature picker at all?

- The picker is a **convenience for the architects / orchestration creators** —
  the people who design the workspace. They click the tools their crews need so
  the **end client ("Pepík")** — who just *uses* the workspace and may not know
  which programs an agent needs — gets a container that works out of the box.
  We move the "what tools do I need" burden off the end user.
- Pepík can still ask mid-session: *"install LibreOffice in the container."*
  The agent just does it. **Plug-and-play.**

### Decision: runtime tool installs must prompt for persistence + audit

When an agent installs a tool ad-hoc at runtime, it must **always ask**:

| Choice | Effect |
|--------|--------|
| **Persist** | Append to the devcontainer config (a feature or a Dockerfile layer) → image rebuilt → **auditable**, survives container recreation, reproducible for the crew. |
| **Ephemeral** | Install only in the live container right now. Fast, no audit trail, lost on recreate. |

Ad-hoc installs are **ephemeral by default**; promotion to the image is an
explicit, audited action. The Dockerfile/config is the source of truth — this
is exactly why a generated-Dockerfile pipeline (below) is the right foundation:
promotion = append a layer + rebuild (cheap, thanks to layer cache).
*(Promotion flow is a later phase; Phase 1 builds the foundation.)*

## Decision: BuildKit + generated Dockerfile, behind an `ImageBuilder` provider

Replace container-commit with a **generated Dockerfile built by BuildKit**,
wrapped in an `ImageBuilder` provider abstraction that mirrors the existing
`ContainerProvider` (Docker | K8s) pattern.

### Why BuildKit

1. **Layer cache** — one `RUN` layer per feature, in install order
   (common-utils first). Adding the 6th feature rebuilds one layer, not the
   image.
2. **Cache mounts** — `RUN --mount=type=cache` keeps apt/npm/pip caches across
   builds → seconds off every build.
3. **Enables the custom-base-image future** — a Dockerfile makes
   `FROM <user-image>` trivial. An operator can drop in their own image (e.g. a
   full GitLab runner image) and have features layer on top. Container-commit
   makes this awkward; Dockerfile generation makes it natural.

### Cross-platform is a HARD requirement (macOS / Linux / Windows)

This is **crucial and non-negotiable** — the pipeline must work everywhere.

- BuildKit is **not Docker-only and not host-OS-specific**. It is the default
  build engine in modern Docker Engine / Docker Desktop (≥ 23). Containers are
  Linux containers regardless of host (Docker Desktop runs a Linux VM on
  macOS/Windows), so **building a Linux image with BuildKit behaves identically
  on all three host OSes** as long as Docker is present — which Crewship already
  requires.
- **Phase 1 drives BuildKit through the Docker daemon** (the build path the
  daemon already exposes, BuildKit-on-by-default), so there is **no hard
  dependency on the `buildx` CLI plugin** and nothing OS-specific to install.
- **Robust fallback:** if BuildKit is unavailable, fall back to the current
  container-commit path. Provisioning never hard-fails because of the build
  engine. (Detected once, cached.)
- **Arch note:** build native arch (arm64 on Apple Silicon, amd64 elsewhere).
  Multi-arch / cross-build is out of scope for Phase 1.
- **kaniko / buildkitd-as-a-service** is the **Kubernetes** story (Phase 3) —
  *not* needed for the cross-platform desktop requirement, which buildx/daemon
  BuildKit already satisfies.

## Phases

### Phase 1 — Dockerfile + BuildKit on Docker (THIS PHASE)
- `ImageBuilder` interface + `DockerBuildKit` implementation.
- **Dockerfile generator** (`GenerateDockerfile`) — pure, deterministic,
  fully unit-testable without Docker. One `COPY`+`RUN` layer per feature in
  install order; `# syntax=docker/dockerfile:1`; apt cache mount on each layer;
  feature env inlined and shell-quoted; deterministic key ordering for stable
  cache hits.
- Build via the Docker daemon's BuildKit; tag `crewship-cache:{hash}` (keep the
  existing content-addressable cache key so nothing downstream changes).
- **Fallback** to container-commit when BuildKit is unavailable.
- Preserve all security invariants: runtime still non-root UID 1001,
  `--cap-drop=ALL` — those are *runtime HostConfig* concerns, independent of how
  the image is built. The feature privilege-stripping in `UnmarshalJSON` stays.

### Phase 2 — Registry cache export/import (likely needed)
- `--cache-to` / `--cache-from` a registry so layers are shared across machines
  and across a multi-worker / restart. Requires buildx; still cross-platform.

### Phase 3 — Kubernetes builder
- `K8sKaniko` (build pod) or `buildkitd` service implementation of
  `ImageBuilder`. Reuses the same Dockerfile generator.

### Phase 4 — Custom base image
- Let operators pick any OCI base (`FROM <user-image>`); features layer on top.
  Directly enabled by the Phase 1 Dockerfile foundation.

### Cross-cutting (parallel to phases)
- **Background pre-provision** on config save (debounced; cancel in-flight on
  change) so the image is warm before the first chat. Reuses `EnqueueForCrew`.
- **Stream build logs** per layer into the UI for foolproof, legible progress.

## Out of scope (now)
- The runtime tool-promotion UX (persist-vs-ephemeral prompt) — designed for
  above, built later on this foundation.
- Multi-arch images.
- Removing the container-commit path (kept as fallback).

## Acceptance (Phase 1)
- `GenerateDockerfile` deterministic + unit-tested (no Docker needed).
- Provisioning a crew via BuildKit produces a runnable image; agent user works.
- Adding a feature reuses cached layers for unchanged earlier features.
- BuildKit-absent host transparently falls back to container-commit.
- `go test ./... && go vet ./...` green; works on macOS, Linux, Windows.
