# Member resource policy: API and CLI

This is an administration surface for the authority introduced in #2717, not
activation of restricted model execution. An operator can explicitly restrict a
member and edit individual agent, project and routine operations. Unintegrated routes remain
denied; the policy does not bypass roles, token scopes or conversation audience.

Only a **current trusted OWNER/ADMIN** of the target workspace may read or replace
these policies. Scoped CLI tokens additionally require `workspace:admin` (or its
wildcard). Restricted administrators cannot administer policy. OWNER/ADMIN members
cannot be changed to restricted mode, and an account restricted anywhere cannot acquire OWNER/ADMIN in another workspace. An append-only database migration enforces both directions on membership insertion and role, identity or access-mode updates; handler checks return friendly conflicts. The current global
restricted-user boundary also applies to users with mixed trusted/restricted
memberships: adding a restricted membership closes unintegrated routes across
their session, including administration of another workspace.

Routes:

- `GET /api/v1/workspaces/{workspaceId}/members/{memberId}/access`
- `PUT /api/v1/workspaces/{workspaceId}/members/{memberId}/access`
- `GET /api/v1/workspaces/{workspaceId}/members/{memberId}/access/effective`

The path uses the membership row ID. CLI commands accept either that ID or the
user ID and resolve it through the workspace roster:

```sh
crewship --workspace WORKSPACE --format json workspace member access get MEMBER > policy.json
# Edit only mode and rights, retaining membership_id and revision.
crewship --workspace WORKSPACE --format json workspace member access set MEMBER --file policy.json
# --file - reads the complete replacement from stdin.
crewship --workspace WORKSPACE --format json workspace member access explain MEMBER
```

The replacement body is the same shape as the GET response:

```json
{
  "membership_id": "MEMBERSHIP_ROW_ID",
  "mode": "restricted",
  "revision": 1,
  "rights": [
    {"kind": "agent", "id": "AGENT_ID", "operation": "chat"}
  ]
}
```

`rights: []` explicitly revokes all resource grants in restricted mode. Omitted
or null rights are rejected, as are unknown body fields. `trusted` requires an
empty rights array and restores ordinary workspace access; it is not a narrower
client setting. The actor always comes from authentication, not JSON.

Every accepted replacement advances the membership revision. Treat it as an
opaque version: grant/role triggers can advance it by more than one. A stale revision
returns 409 and the CLI neither refreshes it silently nor retries the update.
Removing/re-adding a member changes its membership ID; an old policy must not be
reused for the new membership. Membership, grants and administration authority
are checked transactionally by `access.Store`; old attempts do not regain rights
when the same grants are later restored.

## Model decision (2026-10-14, #3067)

Restricted access is presented as **Guest**, an access mode rather than a new
workspace role. One restricted membership places the entire account behind the
restricted route ceiling. Existing trusted memberships retain their defaults.
The resource policy is the same versioned `access_grants` model for agents,
projects and routines; the backend checks it on every integrated operation.
Changing a Guest's role to ADMIN/OWNER requires explicitly restoring trusted
access first. A role change cannot silently remove or bypass the restriction.

| Original PRD choice | Decision | Reason |
| --- | --- | --- |
| New GUEST workspace role | Changed: restricted access mode, Guest in UI, account-wide | The current global route fence has no mixed-mode bypass. Keeping one account ceiling avoids granting trusted routes through another workspace while per-resource integration is incomplete. Separate workspace-scoped guest accounts remain a future migration. |
| New resource_grants registry | Changed: extend versioned access_grants | Reuse the shipped membership revision, resource foreign keys and execution revocation fences rather than introduce two authorities. Pages moves last as originally planned. |
| Per-object sharing and meaningful type levels | Kept as target; only agent operations, routine Run and project read/contribute delivered | View/Edit and crew/credential subjects need separate enforcement and UI work. Routine/project Share dialogs reuse the versioned member policy. |
| Pages model migration | Deferred, as in original phases | Preserve current Pages ACL, issuer checks and data during the first routine/project slice. |

Guest invitation acceptance, restricted provisioning and direct member policy
replacement refuse accounts that are OWNER or ADMIN anywhere, including
unclaimed accounts. Consent cannot override
this protection. A matching stale Guest invitation returns typed 409 and rolls
back signup or membership changes.

PRD Q2: an explicit administrator-issued routine grant authorizes invocation;
there is no second per-run administrator confirmation in this slice. Agent/provider
authority, inputs and budgets remain checked at invocation. Q3: no default grant
expiry is introduced; invitation proofs still expire after seven days, while
grant expiry is deferred pending its execution/revocation contract. Q4: Guest
members count under the existing member licensing limits; no billing exception
is introduced.

Unattended schedule, webhook, wake, nested-call and automation-rule runs require
the human author to remain active and a trusted member of the routine's own
workspace. Guest membership elsewhere does not revoke that workspace's unattended
routines. Agent/crew-authored routines retain their existing execution policy.

Settings › Members edits this policy and offers a server-derived explanation
of the **saved** policy. `access/effective` is administration-only, returns the
membership revision, account restriction scope and per-grant state/reason, and
never includes the member's private conversation or run contents. An execution
grant can be conditional or unavailable: provider/profile dependencies, inputs,
budgets and installed runtime still matter. Unshared resources remain denied.

Members can inspect the same explanation for themselves with
`GET /api/v1/workspaces/{workspaceId}/access/me` or
`crewship workspace access me`. The target identity comes only from authentication;
query parameters cannot select another member. This route requires current local
membership and a live account, is allowlisted for Guests, and fences the response
against membership revision changes. Guest Settings includes **Your access** with
current decisions and execution prerequisites. This explains saved grants rather
than enumerating unshared resources. Trusted members receive their role-based
explanation; the endpoint does not grant new authority.

Resource grants recheck their issuer at use time. The issuer must remain an
active, trusted OWNER/ADMIN in the resource's workspace and must not have an
account-wide Guest ceiling. This applies to discovery, Issues projections,
project inputs, chat audiences, private preflight, routine catalogs, admission,
recovery and dispatch. Stored grants remain visible in the saved policy but are
inert while the issuer lacks authority. This retains stored shares like Pages;
Pages currently has a narrower issuer check and is not yet unified with this model.
Role loss, removal, suspension or a Guest ceiling advances affected recipient
revisions transactionally. Restoring the issuer can authorize a new use of the
preserved grant; previously admitted work remains fenced and requires a fresh
admission. Changes in an unrelated workspace do not revoke valid local shares.
The upgrade preserves valid and inert grant metadata and fences work whose
issuer was already unavailable. Effective access explains an unavailable issuer.

Supported operations:

| Resource | Operations | Current scope |
| --- | --- | --- |
| Agent | discover, chat, run, delegate | Discovery is separate from conversation and execution authority. |
| Routine | run | Explicit root-routine grant **and** run on every resolved agent; cross-agent delegation additionally needs delegate. Nested definitions are frozen dependencies. |
| Project | list, read, write, delete | List exposes issue metadata, read exposes issue detail and project source files. Write/delete apply to project files with their existing role floor; guest issue mutations are not integrated. |

Routine grants replace the workspace-wide `routine.run` requirement for Guests.
They do not grant edit access to definitions. A new migration preserves existing
agent/project grants and trusted memberships, while advancing restricted member
revisions to fence jobs admitted before routine authority was bound. Operators
must explicitly add routine grants to existing Guests; no automatic broad grant
is created.

Both invitation creation and account provisioning accept `access_mode` and
`rights`. Omitted mode retains trusted compatibility; restricted mode may start
with an empty rights array. Initial grants, membership and invitation redemption
commit together, including signup redemption. Removed/cross-workspace resources
refuse redemption; an existing membership is not silently converted or regranted.
The Add member dialog can select Guest and initial resources before creating an invitation. Existing accounts explicitly accept the account-wide restriction; direct provisioning refuses to restrict an existing claimed account.
Invitation acceptance remains available to an already restricted account, but
still requires the matching token, live email and an unrestricted authentication
credential. Accepting another workspace does not remove its account ceiling.

Guest Issues use dedicated projections:

- `GET /api/v1/workspaces/{workspaceId}/restricted-issues`
- `GET /api/v1/workspaces/{workspaceId}/restricted-issues/{issueId}`

CLI: `crewship issue shared --project PROJECT_ID` lists a shared project;
`crewship issue shared ISSUE_ID` reads its current detail. `--limit` and
`--offset` page the catalog without using shared workspace issue endpoints.

The list accepts `project_id`, `limit` and `offset`, filters by project.list in
one SQL query and returns identifiers, titles, status and project identity. The
detail accepts an issue ID or identifier, requires project.read and adds its
description. Neither returns crew/agent references, comments, activity, relations
or run history. Foreign and revoked details return 404. The shared issue board,
search, statistics and mutation routes remain closed to Guests. Collection
`X-Total-Count` counts the filtered total before pagination; the next-offset header supports pagination.

Crews, credentials, routine View/Edit levels, full Issues Manage, explicit
Page-action selection and a Pages grant migration remain separate work. This
contract does not claim complete resource-access PRD or release-1.0 acceptance.

## What a restricted session's shell offers

For an account with a restricted membership, every row of `GET /api/v1/workspaces`
carries `restricted_session: true` and `restricted_surfaces`: the screens the
selected workspace membership may open, narrowed within the server route allowlist
(`internal/api/restricted_surfaces.go`). A surface is listed only when **every**
route it needs is allowlisted. Chat navigation requires a discover grant on a live
agent; only a chat grant offers the composer. Issues navigation requires list
on a local project. Routines and Pages require an authorized recipe or declared action, using
the same graph, provider and Page policies as their catalogs. Runtime installation
is independent: an unavailable executor does not revoke otherwise authorized
navigation. Account security remains available without resource grants. A trusted
account's rows carry neither field.

The web app shows only those screens in the sidebar, the phone menu and the phone
tab bar. Any other URL shows "Not available with your access" without mounting
the screen, so none of its requests are sent. `/` goes to the first available
surface (Chat when authorized). Selection uses the current workspace row, never
another membership's projection. Settings shows
only account security: password, browser sessions and revoking CLI tokens.
Profile edits and issuing new tokens are not on the allowlist. A restricted row
without `restricted_surfaces` allows nothing (fail closed).

Navigation is an existence projection: it stops after the first authorized
routine or Page action rather than constructing every frozen execution contract.
Denied candidates may still require a scan; this is not a constant-cost promise.
A workspace-local catalog failure omits that workspace row, while healthy rows
remain usable. `X-Workspace-Directory-Complete` is false for such a partial
projection. Only a complete directory permits automatic recovery from a missing
preference during a fresh login; active drafts still require a guarded selection. Failure of every workspace projection returns an error, never an
empty grant projection. Logs contain no resource names or session credentials.
A chat grant alone still permits direct access to a known agent's private chat;
it does not grant directory discovery. Assign discover and chat for navigable chat.
Installed adapters' static profile declarations constrain navigation. Missing
runtime installation preserves known declaration authority; unknown profiles
remain denied. Operational readiness checks stay at execution time.

Complete policy saves preserve the id, issuer and creation time of unchanged
rights, including inactive shares. `grant_details` is read-only administrative
metadata with `issuer_id`, `created_at` and `live`; submitted metadata never
confers authority. To deliberately issue an inactive share under the current
administrator, include that exact right in both `rights` and `reissue`.
The Settings editor labels inactive shares and offers “Reissue as me”; an
unrelated edit never silently reissues them. A reissue uses the authenticated
actor and fresh metadata and participates in the same revision check.
Issuer triggers fence only live-to-inactive transitions, not later changes to
an already inactive issuer. Role, suspension and membership changes preserve
stored provenance. Hard deletion of an issuer account is an exception: its
grants are deleted to satisfy the user foreign key.

Policy replacement and deliberate reissue append `member.access_changed` atomically with the policy, recording previous/current rights, issuers, revisions and mode. API and CLI may explicitly reissue live shares; Settings offers reissue for inactive shares.
Guest Page actions retain the Pages ACL checks and additionally require an explicit `routine:run` grant and the routine’s agent grants. Existing `routine.run` capabilities are not backfilled into routine grants.

## Sharing from a routine or project

Trusted workspace OWNER/ADMIN members can open **Share** on the routine or project
identity card. The dialog lists trusted role-based members and each Guest's
explicit shares on that object, including inactive issuer state. Routine sharing
currently offers Run; project sharing offers independent List and Read. It does
not introduce routine View/Edit or full Issues Manage; project Contribute is described below.

Each Guest save retains their membership ID, access mode and revision, preserves
all other resources and unsupported future operations, and sends only writable
policy fields. The backend preserves unchanged issuer provenance. An inactive
share remains inactive unless explicitly selected for **Reissue as me**. A 409
blocks further saves until the administrator reloads and reviews current shares;
there is no automatic retry or account conversion. The existing versioned member-policy API remains the transport.
The object-oriented CLI offers `share`, `share list` and `unshare` for routine
Run and project List/Read. It uses the same member-policy endpoint, preserves
unrelated rights, never converts account mode and never retries a conflict.
`share --reissue` deliberately changes selected share authority. The member
may be identified by membership ID, user ID or exact email; resource arguments
are IDs. See [the CLI contract](../cli/share.mdx).


## Guest CLI authentication bootstrap

A verified interactive session may create its own standard CLI token through
the self-scoped token endpoint. The middleware supplies verified identity and
authentication kind to the restricted classifier before evaluating this rule.
CLI bearer credentials cannot use this new Guest minting entrypoint, preserving
their scope ceiling. Minted tokens retain account restriction, object grants,
existing tier/scope checks and ordinary self-revocation behavior.


Pages’ existing grant resolver now also requires an active trusted issuer account, including no Guest restriction elsewhere. This applies to workspace administrators, user owners and owning crew members. Stored Page grants remain available to administrators with an account-status reason when inactive. The two Pages ACL models have not yet been migrated to access_grants.
## Project contributions and explicit comment audiences

Project `contribute` supplements `read`, independently of source-file `write`.
Dedicated Guest routes create unassigned Backlog requests in an administrator's
versioned project intake crew, post shared comments and arrange intake between
BACKLOG and TODO. They recheck live issuer grants, membership and active account
inside the mutation transaction. Content changes commit activity and durable journal outbox intents atomically, following the issue mutation contract; journal projection retries after commit. Intake configuration keeps its synchronous atomic journal. Guest-authored issues and comments carry explicit unavailable execution origins, so Contribute cannot become autonomous execution authority. Unknown mutation
fields are refused; there is no agent/crew/assignee/routine override or trusted
board route exposure. Review/execution states and project reassignment stay
behind the existing trusted workflow. Full Issues Manage is not implemented.

Every historical comment stays private. Trusted authors may explicitly mark a
new comment as shared with Guests; Guest contributions are explicitly shared.
Each publication is bound to the issue's project at that time. A later project
move does not expose the original client's thread. Guest responses contain only
shared bodies, timestamps, comment IDs and generic author labels, with no
member email, agent metadata, mention expansion or private comment count.

The Share dialog configures intake and grants Contribute; Settings and CLI offer
the same grant. Guest forms preserve failed drafts, explain unconfigured intake
and never offer trusted execution or deletion controls. The new endpoints have
CLI equivalents and built-binary acceptance, and join the route-walking leak test.

Shared comments confirm their expected project in the request. Staff use `expected_project_id` with `guest_visible`; Guest comments require `project_id`. A move returns 409 rather than silently changing the audience. Both composers preserve the draft and require an explicit new audience choice.

Guest issue mutations accept optional actor-scoped idempotency keys; creation replay also rechecks the created issue’s current project and live grants. Content, operation receipt and durable audit intent share one transaction. This does not grant agent execution authority.
