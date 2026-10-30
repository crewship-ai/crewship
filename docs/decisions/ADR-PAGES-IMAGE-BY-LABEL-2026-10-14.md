# ADR: Pages compiler image is installed by `crewship page enable` instead of a configured digest

Date: 2026-10-14 · Status: **accepted** — 2026-10-29, accepted by delegated maintainer decision · Issue: [#3108](https://github.com/crewship-ai/crewship/issues/3108)

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
   `crewship page enable` builds it, tags the result with the exact local tag
   `crewship-pages-build:<fingerprint[:12]>`, labels it
   `ai.crewship.pages.image=<build context fingerprint>` and **records the built
   image ID** in operator-owned state the server reads:
   `<page_projects_path>/pages-build-image.json`, written atomically with mode
   `0600` (given to the storage directory's owner when run with `sudo`), one
   entry per fingerprint. The fingerprint covers the whole build context,
   Dockerfile and base image digest included, so a patched base image is never
   satisfied by an older image. `crewship page disable` removes this version's
   entry and then its image; `--all` every recorded entry. An image is removed
   only after it verifies (recorded ID, exact tag, label), never because it
   merely carries the label.
2. When `CREWSHIP_PAGE_BUILD_IMAGE` is set, it wins, as before, and must still
   be an immutable ID or digest; the record is not read. This is the route for
   Docker Compose (the server container neither builds nor reads the host's
   files: `crewship page enable --image-only` prints the ID to pin) and for
   air-gapped hosts (`docker save`/`docker load`, then pin).
3. Otherwise the server uses only the image recorded for its own build context
   fingerprint, and only while `docker image inspect` reports that exact ID,
   the exact tag and the label (`internal/pagebuild/record.go`,
   `RecordedCompilerImage`). It never lists images by label and never picks the
   "newest". A record that is missing, not a regular file, writable by group
   or others, names another release, or names an image that is gone, retagged
   or relabelled means Pages Apps are not enabled (fail closed); the log and
   `crewship doctor` say why and name `crewship page enable`. A verified image
   is reused for 10 seconds and "not enabled" for 2 seconds, so `enable` takes
   effect without a restart while a disabled server runs at most one bounded
   (5 s) lookup per 2 seconds. The post-build profile fingerprint check is
   unchanged.
4. No verified record and no override means Pages Apps are disabled: no Pages
   container is started.

## Security reasoning

- **The trust anchor is the operator's record, not the label.** The label is
  public (anyone can compute the fingerprint from a release binary) and is
  inherited by every image built `FROM` a labelled base. Crewship itself puts
  images with tenant-chosen configuration on the same daemon: a workspace
  manager sets a crew's `runtime_image` to any registry image, and provisioning
  pulls it and builds `crewship-cache:*` images from it, with an
  attacker-chosen `Created` date. A label match therefore proves nothing
  (security review of #4497). What the server trusts is the conjunction
  recorded ID + exact local tag `crewship-pages-build:<fp12>` + label +
  fingerprint: the ID is written only by the operator's `crewship page enable`
  into the server's protected page project storage, and an image ID cannot be
  re-pointed to other content. Crew pulls and builds produce registry-qualified
  or `crewship-cache:`/`crewship-feat:` names, never that tag, and the tag is
  checked in addition to the ID, not instead of it.
- **Who can enable.** Writing the record needs write access to page project
  storage (the server's account or root) and building the image needs Docker
  daemon access; both are operator privileges on the server host. No API
  endpoint and no workspace role can write the record.
- **Explicit override retained.** An operator who wants to pin a specific image
  sets `CREWSHIP_PAGE_BUILD_IMAGE`; the record is then never consulted.
- **Builds never pull.** The resolved value is always a local immutable ID;
  builds keep `--pull=never` and `--network=none`. Network access is needed
  only inside `crewship page enable`, an explicit operator step.
- **Post-build fingerprint check retained.** The worker artifact still has to
  report the server's profile fingerprint (`decodeWorkerArtifact`), so a
  mismatched image fails closed. It is a consistency check, not a trust
  anchor: an image reports its own fingerprint.
- **Pages-off invariant preserved.** A verified record is the operator's
  explicit opt-in. Without it the server starts no Pages container.

## Consequences

- On a server that already runs with page project storage configured
  (`CREWSHIP_PAGE_PROJECTS_PATH`), Pages Apps are enabled with one command and
  no further configuration edit or restart; together with the derived loopback
  runtime origin (`internal/config/page_projects.go`, `PageRuntimeOrigin`) a
  localhost Studio needs no other Pages setting. This decision does not provide
  a default storage location: a fresh installation still sets the projects path
  and restarts once. That gap, and enabling Pages Apps on a fresh install with
  no configuration at all, is part 2 of #3108 (PRD §4.3), deferred to a later
  change.
- After a binary upgrade that changes the profile, the server ignores the old
  image until `crewship page enable` is run again. Old images stay recorded for
  rollback until `crewship page disable --all` or `docker rmi` removes them.
- `crewship page enable` must run as the server's account (or with `sudo`) and
  with the server's configuration, since it writes into page project storage.
- Air-gapped hosts receive the image with `docker save` / `docker load` from a
  machine running the same Crewship version and pin it with
  `CREWSHIP_PAGE_BUILD_IMAGE`; a loaded image is not recorded.
- The Docker Compose production stack cannot build inside the container (its
  socket proxy sets `BUILD=0`); operators run `crewship page enable
  --image-only` on the host with the same version and pin the printed ID.
- Reversing this decision means a new ADR; the explicit override remains the
  fallback in the meantime.

## Acceptance

Accepted on 2026-10-29 by delegated maintainer decision, on the reasoning
above: the explicit `CREWSHIP_PAGE_BUILD_IMAGE` digest override is retained;
the server trusts only the image ID the operator's `crewship page enable`
recorded, together with the exact tag and label (see the amendment below);
builds keep `--pull=never` and
`--network=none`; the post-build fingerprint check is retained; and the
Pages-off invariant holds. The decision covers every caller of the Page build
worker, including Artifacts of the Project kind, which build through the same
`POST /api/v1/pages/{slug}/project/build` path. Switching the repository's own
CI and `dev.sh` to `crewship page enable` is a separate CI-control change.

**Amended 2026-10-29 before merge**, after the independent security review of
#4497: the first version of this ADR trusted the newest image carrying the label
("the label grants nothing new"). That premise does not hold — crew runtime and
cache images reach the same daemon and inherit labels — so the decision now
anchors trust in the image ID recorded by `crewship page enable`, the exact tag
and the label together, and `disable` removes only recorded, verified images.
The filename is kept for link stability.
