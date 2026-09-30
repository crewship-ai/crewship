# Project files

Project files are explicitly uploaded, versioned project resources. They do not
import existing workspace, crew, agent, HOME or host directories.

The project file API uses workspace authentication and exact resource grants:

| Route | Current authority |
| --- | --- |
| `GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files` | project.read |
| `GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download` | project.read; exact live version |
| `POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files` | MANAGER or higher and project.write |
| `DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}` | MANAGER or higher and project.write |

Uploads contain `name`, `expected_revision` and `content_base64`. Content is
standard base64; an empty content string is a valid empty file. Creation uses
revision zero and omits `file_id`. Replacement supplies the current `file_id`,
unchanged name and expected revision. A stale revision returns a conflict.
Retirement supplies `expected_revision`. Metadata contains stable file and
version IDs, name, revision, size, SHA256 and creation time; it does not expose
an uploader identity or host path.

Only live head versions can be listed, downloaded or selected as input.
Replacement and retirement permanently tombstone the previous version and
remove its stored bytes while retaining its immutable version ID, resource,
revision, size and hash. A retired filename can be uploaded again with a new
file identity. Neither operation resurrects older attempts or versions.
Downloads recheck current read authority and the exact version before each
16 KiB chunk. A detected revocation ends delivery; already delivered bytes
cannot be recalled.

A file is bounded to 1 MiB. SQLite enforces aggregate base64 TEXT byte limits
of 32 MiB per project and 128 MiB per workspace, 256 live files per project and
4096 retained version records per project. Replacements reclaim old bytes in
the same transaction; a failed write rolls back its retirement. Version metadata
is retained, so exceeding the historical version count requires explicit
administrative storage management rather than silent provenance deletion.
Project resources and their base64 bytes are included in workspace backups.
Attempt-specific input capabilities are excluded and require fresh admission.

`access.Store.ProjectFileRights` resolves explicitly selected version IDs under
current project.read before admission. `BindProjectFileInputs` freezes the IDs,
project, scope, revision and hash on the active attempt before context is built.
A selection contains at most 16 unique strict IDs and 16 MiB of decoded bytes.
Attempt resolution rechecks these source edges; replacement, retirement or
provenance deletion revokes their consumers and derived context. Removing a
row cannot leave the old consumer authorized. Grant changes retain the ordinary
membership-revision boundary.

`FrozenProjectInputs` is host-only materialization and verifies content length
and digest after decoding. It does not accept host paths or select files from
task text. This source API does not by itself enable native mounts or change
Page input selection. Runtime adapters must separately freeze and audit an
explicit read-only snapshot before launching a model.

The native adapter accepts the optional structured `project_file_versions` list.
A text adapter rejects a nonempty selection. The host binds versions before
building context and passes source metadata as task data, never instructions.
The trusted native catalog creates an attempt-specific 32 MiB tmpfs volume;
selected content remains limited to 16 MiB. It stages validated regular files
through a fixed keyless UID1001 worker, mounts the volume read-only into the
UID1002 hold container, removes the staging writer, and checks every remaining
alias before verifying UID1001 ownership, immutable modes and exact hashes.
The provider broker and model start only after that verification succeeds.
No caller host path, selector from task text, or arbitrary mount is accepted.

The catalog journals its random instance identity and owned Docker objects in
an owner-private directory. Reconciliation removes abandoned snapshots only
after consumers have been removed; an unexpected alias or unavailable Docker
daemon fails closed. The worker image must include the fixed verifier command;
a selected-file attempt cannot silently fall back to an older image. Empty
selections retain the existing native path. Page actions do not accept project-file selections.

Implementation: `internal/access/project_files.go`, `internal/api/project_files.go`.
Verification: `TestProjectFilesTwoHumansAndImmutableSelection`,
`TestProjectVersionReplaceRetireAndTransitiveRevocation`,
`TestProjectFileRoleCASCapacityAndBytes`, and
`TestProjectFilesAuthenticatedRoutesAndStreamingRevocation`, and
`TestLiveNativeInputFreezeReadonlyAndRestartCleanup` (explicit disposable Docker
acceptance gate).

The CLI `crewship run` accepts repeated `--project-file-version` selections
for a restricted native run (maximum 16). Selection is empty by default;
trusted runs reject this flag. The server rechecks every version and its exact
project permission before building context or mounting bytes.
For restricted native private chats, the composer offers **Project inputs**.
For a new draft, **Prepare project inputs** explicitly creates the conversation
and checks its current profile without sending a model message. The picker
appears only after the native profile and current source grants permit it.
Selections start empty, reset when the user, workspace or conversation changes,
and are cleared after sending. Filename, project and size identify each file;
users do not need to enter version IDs. Refresh clears the selection and reloads
current metadata. A changed grant, version or runtime profile can make a selected
file unavailable; the server rechecks it before context or model execution.

`GET /api/v1/chats/{chatId}/project-input-options?workspace_id=...&search=...`
returns `{files, has_more}` with live head-version metadata and project names
only. It requires the caller's exact private-chat audience, current native
profile, agent.chat and project.read grants. It returns at most 100 matches;
search is a substring of filename or project name and is limited to 128 bytes.
This read does not admit an attempt, load source bytes, or reveal uploader
identity. Text, group and routine transcript chats do not offer the picker. Selected IDs travel in
the existing structured `project_file_versions` request field, separate from
message content; arbitrary metadata stays unsupported on the restricted route.

## API wire contracts

The version metadata below is returned for live heads. Authentication uses the
normal workspace session or JWT; resource grants remain an additional ceiling.
Errors use `application/json` with the response body `{"error":"message"}`.

| Metadata field | Type | Meaning |
| --- | --- | --- |
| `version_id` | string | Immutable selected version ID |
| `file_id` | string | Stable file identity across replacement |
| `project_id` | string | Source project |
| `name` | string | Portable relative filename |
| `revision` | integer | Positive file revision |
| `size_bytes` | integer | Decoded size, 0 through 1048576 |
| `sha256` | string | 64 lowercase hexadecimal characters |
| `created_at` | string | RFC3339 creation time |

### GET

```http
GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files
```

Auth: workspace membership and current project.read.
Request: workspace and project path parameters; no request body.
Response: `200 application/json`, `{"files":[version_metadata]}`; an empty list
is `[]`, not null, and at most 256 live heads are returned.
Statuses: `200` success, `401` unauthenticated, `403` workspace/token ceiling,
`404` unavailable project or source, `500` middleware/database failure.

### POST

```http
POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files
```

Auth: MANAGER or higher plus current project.write.
Request: workspace and project path parameters and a strict JSON request body:

| Request field | Type | Requirement |
| --- | --- | --- |
| `name` | string | Required portable name, at most 240 UTF-8 bytes; unchanged on replacement |
| `file_id` | string | Omitted for creation; current file ID for replacement |
| `expected_revision` | integer | Zero/default for creation; current positive revision for replacement |
| `content_base64` | string or null | Standard base64, at most 1 MiB decoded; omitted, null or empty string gives empty content |

Unknown request fields are rejected. A new upload omits `file_id`; a replacement
matches both current identity and revision. Aggregate storage limits are checked
in the write transaction.
Response: `201 application/json`, the complete version metadata object.
Statuses: `201` success, `400` malformed JSON/base64, `401` unauthenticated,
`403` role or workspace/token ceiling, `404` unavailable source/authority,
`409` stale revision or capacity conflict, `500` middleware/database failure.

### DELETE

```http
DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}
```

Auth: MANAGER or higher plus current project.write.
Request: workspace, project and file path parameters and strict JSON
`{"expected_revision":positive_integer}`; no unknown fields.
Response: `204`, no response body. Retirement retains immutable provenance and
removes current bytes in the same transaction.
Statuses: `204` success, `400` malformed request, `401` unauthenticated,
`403` role or workspace/token ceiling, `404` unavailable source/authority,
`409` stale revision, `500` middleware/database failure.

### GET

```http
GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download
```

Auth: workspace membership and current project.read for the exact live head.
Request: workspace, project and version path parameters; no request body.
Response: `200 application/octet-stream`, exact decoded bytes with
`Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, the exact
`Content-Length`, and immutable `X-Content-SHA256`.
The filename uses the source basename. Revocation during streaming terminates
further delivery. A short body is an interrupted response, not a complete
download; verify length and SHA256 against version metadata before publishing
the downloaded file.
Statuses: `200` success, `401` unauthenticated, `403` workspace/token ceiling,
`404` unavailable version/authority, `500` middleware/database failure.

### GET

```http
GET /api/v1/chats/{chatId}/project-input-options
```

Auth: the authenticated restricted principal's own private chat, current native
profile and agent.chat; every returned source independently requires current
project.read. A broad workspace, agent assignment or chat grant is insufficient.
Request: chat path parameter, required `workspace_id` query parameter, optional
`search` query substring of filename/project name up to 128 UTF-8 bytes; no body.
Response: `200 application/json`, `{"files":[option_metadata],"has_more":boolean}`.
Each option contains all version metadata fields and `project_name`; at most 100
matches are returned. `has_more` asks the user to narrow the search. No source
bytes, uploader identity or attempt admission are included.
Statuses: `200` success, `400` oversized search, `401` unauthenticated,
`403` workspace/token ceiling, `404` unavailable chat/native profile/authority,
`503` projection/database unavailable, `500` middleware failure.
