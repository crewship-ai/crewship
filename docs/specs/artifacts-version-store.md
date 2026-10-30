# Artifact version store

Status: current contract. Experimental and off by default; see
[Availability](#availability).

The artifact version store keeps the confirmed content of an artifact as a
sequence of immutable, numbered versions. Multi-file project artifacts (HTML
projects, Dashboard and Slides content), Design canvases and native Word and
Sheet documents all persist their confirmed content through this one store. A
version is always the complete content of the artifact, never a diff. Live
collaborative state, such as the Design Yjs document, is kept separately and
becomes a version only when it is saved (see
[Design Yjs authority](artifacts-design-yjs.md)).

## Availability

Each content family has its own gate. Nothing here is enabled by default.

| Family | Server gate | Browser build switch |
|---|---|---|
| `artifact_project` (projects, Dashboard, Slides) | workspace flag `artifact_projects`, or `CREWSHIP_ARTIFACT_PROJECTS_ENABLED=true` for every workspace | `NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS` for creation from chat; `NEXT_PUBLIC_ARTIFACTS_LIBRARY` for the Artifacts library |
| `design` | workspace flag `artifacts_design`, or `CREWSHIP_ARTIFACTS_DESIGN_ENABLED=true` | `NEXT_PUBLIC_ARTIFACTS_DESIGN` |
| `wordcraft`, `gridcraft` | instance setting `craft_native.enabled` / `CREWSHIP_CRAFT_NATIVE_ENABLED=true` and a qualified profile | see [Native Craft](craft-native.md) |

Enable a workspace flag with `crewship feature-flag enable artifact_projects`
or `crewship feature-flag enable artifacts_design`. Production builds set
`NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS=false`; the other `NEXT_PUBLIC_*` switches
are off unless set to `true` at build time. Dashboard data and Slides have their
own additional workspace flags, `artifact_dashboard_data` and `artifact_slides`.

When a project or Design family is disabled, its routes answer `404` as if the
artifact did not exist. Native document routes answer `503`.

## Data model

Three tables hold the history of one artifact. All rows are scoped to a
workspace and a Page and are deleted with them.

| Table | Purpose |
|---|---|
| `artifact_version_heads` | One row per artifact: `content_type` (the family), `latest_version` and `shared_version`. |
| `artifact_versions` | One row per version: the manifest, message, actor, creation time and `restored_from`. |
| `artifact_version_blobs` | File contents keyed by SHA-256 hash. |

A manifest lists every file path with its media type and content hash. Files
with identical bytes are stored once per artifact, so an unchanged file costs
nothing in a new version.

The family is fixed by the first publication. A later attempt to write the same
artifact through another family is refused, and the database refuses to change
`content_type` in place.

`shared_version` is `0` until an owner shares a version.

## Durability

Every read and write runs in one database transaction that first checks the
SQLite `synchronous` setting. If it is below `FULL`, the store refuses the
operation rather than accept a version that a power cut could lose. Project
routes then answer `503` and ask the user to keep the draft and contact the
operator. The server opens its database with `FULL` by default.

## Authority

The caller's authority is checked twice inside the same transaction: before the
work starts and again immediately before commit. Both checks must return the
same actor, otherwise the write is refused with `403`. A grant that is removed
while a request is running therefore cannot be used to finish that request.
The actor that is recorded with the version is the human user ID, or
`agent:<agent-id>:<run-id>` for an admitted agent run.

## Publishing and conflicts

Publishing is a compare-and-swap on the latest version. The request names
`base_version`, the latest version the caller read. The store accepts the
publication only when `base_version` equals the current `latest_version`, and
the new version is `latest_version + 1`. Creating an artifact uses
`base_version` `0`.

When another writer published first, the store refuses with a conflict. Project
routes answer:

```json
{"error": "This project changed. Read the current version and compare your changes before publishing again. Your draft has not been discarded.", "code": "artifact_version_conflict"}
```

with status `409`. The server never merges or retries. Clients keep the draft,
read the current version and publish again with the new `base_version`.

An agent run is held to its own starting point as well. Its `base_version` must
equal the version the owner selected when the run was requested, or the last
version this same run published. A version published by someone else in the
meantime is a conflict for the agent too.

## Limits and quota

| Limit | Value | Result when exceeded |
|---|---|---|
| Files per version | 256 | `400` |
| File path | 512 bytes, relative, normalised, no `..`, `\`, `?`, `#` or NUL | `400` |
| Media type | 255 bytes, single line | `400` |
| Version message | 4096 bytes | `400` |
| Total file bytes in one version | 32 MiB | `413` |
| History per artifact | 256 MiB (16 GiB for native Word and Sheet) | `413` |
| Project request body | 36 MiB | `413` |

History usage counts every stored blob once plus the size of every version
record. A warning flag is set from 80% of the history limit.

When the history quota is full, project routes answer `413` with code
`artifact_history_quota`. Every existing version is kept; only new versions are
refused. Design routes answer `413` with "Design capacity is full; existing
work is retained", and native document routes with "native history quota
reached; existing versions retained". There is no command that deletes old
versions or raises the limit for one artifact.

## Retention

The store keeps every version. Nothing expires or is pruned. The instance
retention settings (`crewship admin instance retention`) do not apply to
artifact versions.

The `quota.retention` field in a project read describes what the caller can see:

| Value | Caller |
|---|---|
| `keep-all` | The owner. The response includes usage, limit and the full version list. |
| `latest-version-only` | A user with an explicit Edit grant. Only the latest version is readable. |
| `shared-version-only` | A reader. Only the shared version is readable and no usage is reported. |

Retention reflects visibility, not deletion: all versions remain stored.

## Restore and share

Restore reads an earlier version and publishes its files as a new version, with
`restored_from` set to the source version and the message
`Restore version <n>`. History is never rewritten. Restore uses the same
`base_version` check as publishing.

Share sets `shared_version` to an existing version. It also requires
`base_version` to equal the latest version, but it does not create a version.
For projects, sharing ends the private audience of the Page and writes an audit
entry. Restore on projects requires the Page owner or an admitted agent run
acting for the owner. Share requires the Page owner; an agent run can never
share.

## Project HTTP routes

All routes require authentication and workspace membership. `{slug}` is the
Page slug, or `@<page-id>`. Mutations require a token with the `pages:write`
scope; reads require `pages:read`.

| Route | Who | Notes |
|---|---|---|
| `GET /api/v1/pages/artifacts/availability` | Members | `{"enabled": true\|false}` for the workspace. |
| `POST /api/v1/pages/artifacts` | Page creators, admitted agent runs | Creates a private Page and version 1. `201`. A workspace that already has 100 Pages answers `413`. A taken slug answers `409` with code `artifact_slug_taken`. |
| `GET /api/v1/pages/{slug}/artifact` | Owner, editors, readers | Optional `version`. Owners can read any version; editors only the latest; readers only the shared version. |
| `GET /api/v1/pages/{slug}/artifact/versions` | Same as above | Same response as the read; the version list is complete only for the owner. |
| `POST /api/v1/pages/{slug}/artifact/versions` | Owner, editors, admitted agent runs | Body: `base_version`, `entrypoint`, `files` (`path`, `content`, `media_type`), `message`. |
| `POST /api/v1/pages/{slug}/artifact/restore` | Owner, the owner's agent run | Body: `base_version`, `version`. |
| `POST /api/v1/pages/{slug}/artifact/share` | Owner | Body: `base_version`, `version`. Returns `shared_version` and a link. |

A Page that does not exist, is not an artifact, or is not visible to the caller
answers `404`. A reader who can see the shared version but tries to write gets
`403`.

The matching CLI commands are `crewship page artifact create`, `publish`,
`get`, `versions`, `restore` and `share`; see the [Page CLI](../cli/page.mdx).

## Native Word and Sheet documents

Native documents store each saved document as one version in the `wordcraft`
or `gridcraft` family, with a 16 GiB default history budget. Writes additionally
require the current edit lease and its generation. Their routes map a conflict
or a changed lease to `409`, a full history to `413` and an invalid document to
`422`. The lease protocol is specified in [Native Craft](craft-native.md).
