# Design note — Docker SDK v28 → v29 upgrade for CVE chain

**Status:** blocked — v29 not yet published to the Go module proxy.
Major version bump with breaking-change risk across
`internal/provider/docker/` once v29 lands.
**Source:** `audit/iterations/2026-05-21--polish-1/REPORT.md` —
"Docker SDK 5-CVE chain (CVE-2026-34040 has a v29.3.1 fix)".
**Author:** audit-loop, 2026-05-22.

## Blocker (2026-05-22 attempted bump)

`go list -m -versions github.com/docker/docker` returned versions up
to **v28.5.2+incompatible**. There is no v29.x available on the Go
module proxy yet — even though the upstream moby/moby repository's
release-29 branch has cut tags, those don't appear at the
`github.com/docker/docker` import path that Crewship uses.

Until the v29 line is published to the module proxy, this design
note tracks the work but the bump itself cannot proceed. Re-attempt
once `go get github.com/docker/docker@v29` resolves.

In the meantime, Dependabot's `gomod` ecosystem (configured in
`.github/dependabot.yml`) will surface the first v29 minor as a PR
the moment it lands, so the bump won't sit unreviewed.

## Current state

`go.mod` pins `github.com/docker/docker v28.5.2+incompatible`. Audit
report identifies 5 CVEs in the chain, with CVE-2026-34040 fixed in
v29.3.1.

## Breaking-change risk surface

The audit-loop grep found Crewship calls these SDK methods:

```text
ContainerList, ContainerCreate, ContainerStart, ContainerStop,
ContainerRemove, ContainerInspect
ContainerExecCreate, ContainerExecAttach, ContainerExecInspect,
ContainerExecResize, ContainerExecStart
ImagePull, ImageInspect, ImageList, ImageRemove
NetworkList, NetworkCreate, NetworkRemove, NetworkInspect
VolumeList, VolumeCreate, VolumeRemove
ContainerStats (in metrics/quartermaster paths)
```

A major version bump in `github.com/docker/docker` historically
involves:

- Type moves between `types` ↔ `types/container` ↔
  `types/network` ↔ `types/image` ↔ `types/volume` packages.
- Renamed Options structs (`StartOptions` → `ContainerStartOptions`,
  etc., direction differs per version).
- Field deprecations on container/network configs.
- Removal of long-deprecated convenience methods.

Each of the ~25 callsites in `internal/provider/docker/` will need to
be re-verified against the new SDK surface. Fake test infrastructure
in `internal/provider/docker/fakeapi_test.go` will need its mocks
updated.

## Why this can't be a loop PR

1. **Compile breakage is the expected starting state**, not the
   exception. The fix is iterative: bump go.mod, compile, fix each
   call site, repeat. Plausibly 30+ small edits.
2. **Test mocks need updating in lockstep**: the
   `internal/provider/docker/fakeapi_test.go` server fakes specific
   wire shapes — those shapes can drift across SDK majors.
3. **Devcontainer / sidecar provisioning paths run real Docker
   end-to-end**. A bumped SDK against the existing daemon version
   pinned by the dev environment may surface API-version-negotiation
   issues that don't show up in unit tests.

This is the kind of upgrade that lands in its own focused session
with the freedom to iterate against a real Docker daemon, not a 20-min
loop tick.

## How to pick this up

1. Branch `chore/audit-docker-sdk-v29-bump` from `main`.
2. `go get github.com/docker/docker@v29.3.1` (or latest patch on the
   29.x line).
3. `go mod tidy`.
4. `go build ./...` — read every compile error, fix the call site.
5. `go test ./internal/provider/docker/...` — fix every mock
   mismatch.
6. `go test ./...` — confirm no spillover into devcontainer /
   orchestrator paths.
7. Spin up a dev devcontainer and confirm the full container
   lifecycle still works end-to-end (the SDK-vs-daemon API version
   negotiation is the most likely surprise).
8. PR title: `chore(deps): bump docker/docker to v29.3.1 (CVE chain)`.
9. PR body: cite each CVE by ID + Docker release notes section.

## Alternative: pin a patched v28

If v28 still receives security patches, a patch bump within v28.x is
lower-risk than the major bump. As of writing, the audit referenced
"v29.3.1 fix" specifically for CVE-2026-34040 — verify whether v28.x
has a backport. If yes, prefer that path.

Verification commands:

```bash
# List published Docker SDK versions
go list -m -versions github.com/docker/docker
# Inspect a candidate version's CHANGELOG
go doc -all github.com/docker/docker@v29.3.1 2>/dev/null | head -100
```
