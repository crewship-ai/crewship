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
selections retain the existing native path. Pages remain text-only.

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
