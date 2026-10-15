# ADR: Pages compiler image is discovered by profile label instead of a configured digest

Date: 2026-10-14 · Status: **proposed** — awaiting maintainer acceptance · Issue: [#3108](https://github.com/crewship-ai/crewship/issues/3108)

Motivated by [pages-build-worker-install-2026-10-14.md](../prd/pages-build-worker-install-2026-10-14.md)
§4.1 (step 0 of its plan). It changes one rule of the current Pages Apps
operations guide: the compiler image no longer has to be named in
configuration.

## Context

A Page app is compiled in a disposable container from the Pages tools image
(`tools/pages-build`). Until now the server used that image only when
`CREWSHIP_PAGE_BUILD_IMAGE` named it by immutable `sha256:` ID. To get there
an operator needed a source checkout, ran `docker build`, copied the ID into
configuration and restarted the server. Binaries, packages and Homebrew do not
ship `tools/pages-build`, so a non-source installation had no supported way to
turn Pages Apps on, and an upgraded binary failed builds with a generic error
until someone repeated the procedure.

The architecture ([pages-apps-architecture.md](../prd/pages-apps-architecture.md),
module cost section) requires that the tools image is optionally installed and
that disabled Pages Apps install nothing and start no worker. Installing the
image automatically at server start is therefore not an option; enabling has to
stay an explicit operator act.

## Decision

1. The binary embeds the tools image build context
   (`tools/pages-build/profile.go`, `WriteBuildContext`).
   `crewship page enable` builds it and labels the result
   `ai.crewship.pages.image=<build context fingerprint>`. The fingerprint covers
   the whole build context, Dockerfile and base image digest included, so a
   patched base image is never satisfied by an older image. `crewship page
   disable` removes this version's image; `--all` removes every labelled image.
2. When `CREWSHIP_PAGE_BUILD_IMAGE` is set, it wins, as before, and must still
   be an immutable ID or digest.
3. Otherwise the server uses the newest local image whose label equals its own
   build context fingerprint (`internal/pagebuild/resolve.go`). A found image is
   reused for 10 seconds and "not enabled" for 2 seconds, so `enable` takes
   effect without a restart while a disabled server runs at most one bounded
   (5 s) `docker image ls` per 2 seconds, not one per request. The post-build
   profile fingerprint check is unchanged. An image labelled for
   another fingerprint is ignored.
4. No such image means Pages Apps are disabled: no Pages container is started.

## Security reasoning

- **Same trust level as today.** Creating an image with the label requires
  access to the Docker daemon, which is effectively root on the host. Whoever
  can do that could already build an image and point the configured digest at
  it. The label grants nothing new.
- **Explicit override retained.** An operator who wants to pin a specific image,
  or distrusts discovery, sets `CREWSHIP_PAGE_BUILD_IMAGE` and discovery is
  never consulted.
- **Builds never pull.** The resolved value is always a local immutable ID;
  builds keep `--pull=never` and `--network=none`. Network access is needed
  only inside `crewship page enable`, an explicit operator step.
- **Post-build fingerprint check retained.** The worker artifact still has to
  report the server's profile fingerprint (`decodeWorkerArtifact`), so a
  mislabelled image fails closed.
- **Pages-off invariant preserved.** The presence of a labelled image is the
  operator's explicit opt-in. Without it the server installs nothing and starts
  no Pages container.

## Consequences

- Local installations enable Pages Apps with one command and no configuration
  edit; together with the derived loopback runtime origin
  (`internal/config/page_projects.go`, `PageRuntimeOrigin`) a localhost Studio
  needs no Pages setting at all.
- After a binary upgrade that changes the profile, the server ignores the old
  image until `crewship page enable` is run again. Old images are kept for
  rollback until `crewship page disable` or `docker rmi` removes them.
- Air-gapped hosts receive the image with `docker save` / `docker load` from a
  machine running the same Crewship version; the label travels with the image.
- The Docker Compose production stack cannot build inside the container (its
  socket proxy sets `BUILD=0`); operators run `crewship page enable` on the
  host with the same version.
- Reversing this decision means a new ADR; the explicit override remains the
  fallback in the meantime.
