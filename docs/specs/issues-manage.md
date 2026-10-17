# Shared Issues management

This is the bounded contract for restricted project management. Rights are
independent operations. Existing `manage` shares do not acquire `list`,
`contribute` or `delete_issues`. The [member resource policy](member-resource-policy.md)
defines live issuer checks, account-wide Guest restrictions and grant revisions.

## Action inventory

Every operation is bound to the current workspace and project. An unrelated
client's project is unavailable even when its ID is known.

| Operation | Explicit project rights | Additional condition |
| --- | --- | --- |
| Search/list issue titles and identifiers | List | Only currently authorized projects; no private comments or execution data |
| Read a known issue and shared comments | Read | Dedicated shared projection only |
| Create intake issue, add shared comment, BACKLOG/TODO transition | Read + Contribute | Configured intake crew; active-work fence; no caller-selected runtime authority |
| Read project name/description | Read + Manage | Known project ID is sufficient; List is not required |
| Update project name/description | Read + Manage | Reviewed opaque version and mutation token scope |
| List/read project milestones | Read + Manage | Project-local pagination; no linked issue disclosure |
| Create milestone | Read + Manage | Valid name/date/status; server assigns initial position |
| Update milestone metadata/status/position | Read + Manage | Reviewed opaque version and mutation token scope |
| Delete milestone | Read + Manage | Reviewed version; empty of all linked issues, including legacy cross-project links |
| Preview known issue deletion | Read + Manage + delete_issues | Current project and dedicated deletion projection |
| Delete issue permanently | Read + Manage + delete_issues | Explicit confirmation, project ID and reviewed version; BACKLOG/CANCELLED; no active work or subissues |

Read alone does not disclose project administration metadata. Manage alone is
also insufficient. Source-file `delete` and issue `delete_issues` are separate
rights; neither implies the other. Manage does not imply Contribute.

Scoped project and milestone writes additionally require the route's
`workspace:admin` token scope. A scope limits credentials; it does not make a
Guest a workspace administrator. Read responses advertise current `can_manage`
and eligibility hints, rather than inviting a mutation the credential cannot
perform. Server checks still apply when a client ignores those hints.

## Matching surfaces

The API uses the dedicated workspace `restricted-projects` and
`restricted-issues` projections. The CLI exposes known-ID project details with
`project shared`, metadata edits with `project shared update`, and milestone
operations under `project shared milestones`. Destructive issue operations are
`issue shared-delete-preview` and `issue shared-delete`; deletion requires
`--confirm`, `--project` and `--version`.

The shared Issues UI offers project administration through its authorized
project selector, milestone forms and explicit issue deletion controls. That
selector requires List and must not discover projects from Manage shares.
The API and CLI's known-ID management contract itself does not require List.
The ordinary Issues board, global navigation and trusted catalogs must not be
opened to work around that distinction.

## Excluded operations and data

Manage permits name/description and milestone administration, not project
ownership, intake configuration, sharing, membership, credentials, providers or
runtime settings. It does not permit editing an existing issue's title or
description, changing its project, assigning agents/milestones, or arbitrary
status changes. It grants no execution, replay, review, approval or blocking
authority. Those require their own current contracts and rights.

Private comments, chat receipts, requester identities, run outputs/errors,
costs and internal execution context remain outside these projections. A
milestone's delete eligibility may be advertised as a boolean; linked private
issue IDs or counts are not administration data. Permanent deletion does not
open trusted receipts or tombstone endpoints to a Guest.

## Writes, revocation and conflicts

Each write checks current account/membership, explicit grants, live issuers,
credential scopes and local resource binding. Project/milestone snapshots and
deletion previews carry opaque versions. The server rejects a stale version
with a conflict; clients keep the draft and require deliberate review of the
current snapshot. They must not silently fetch a newer version and resubmit.

Successful changes and their audit entry commit in one transaction. Audit
failure rolls the change back. Authority loss refuses a write made through an
already-open editor or existing CLI session. An issuer's demotion must not
revoke an independent grant issued by a different, still-authorized issuer.

Queued, running and waiting pipeline work protects an issue even when an older
issue execution row is superseded. Metadata administration must not terminate,
unlink or bypass that work as a side effect. Empty milestone deletion and
disposable issue deletion retain their own conflict fences.

## Executable verification

`TestAcceptanceProjectManageRouterCLI` drives the built CLI against the
production Router and a migrated database. Two unrelated clients have
Read+Manage without List. It covers own-project controls, foreign refusal,
metadata and milestone writes, stale versions, single-write/no-refresh behavior,
transactional audit rollback and issuer demotion with existing sessions.

`TestAcceptanceIssueManageRouterCLI` checks separate destructive shares,
foreign issue refusal, narrow credential hints and write denial, stale deletion
versions, queued/running/waiting linked work despite superseded execution,
completed-work recovery, audit rollback and exactly one confirmed deletion.
These known-ID actions leave List and private journal access unavailable.

The API suites `TestGuestProjectDetailsManagementKeepsLocalAuthority`,
`TestGuestMilestoneManagementIsScopedVersionedAndAtomic` and
`TestGuestIssueDeletionIsVersionedScopedAndAtomic` additionally cover narrow
credentials, destructive intersections and handler rollback. These synthetic
fixtures establish authorization behavior; they do not certify a live deployed
environment or grant runtime execution.
