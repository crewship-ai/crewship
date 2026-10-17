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
| Per-object sharing and meaningful type levels | Kept as target; agent operations, routine Run, definition View, explicit regular Results and project read/contribute supported | Full ordered View/Run/Edit, private-queue result audiences and crew/credential subjects need separate enforcement and UI work. Routine/project Share dialogs reuse the versioned member policy. |
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

### Ordered preset foundations (not enabled)

The immutable v1 catalog defines routine View (`view,results`), Run
(`view,results,run`) and draft-only Edit (`view,results,run,edit`), plus project
View (`list,read`), Contribute (`list,read,contribute`) and Manage
(`list,read,contribute,manage`). Publication and `delete_issues` remain separate.
These are future explicit bundles: this foundation exposes no preset application
route, CLI command or selector and changes no existing grants or access mode.

An empty-on-upgrade `access_preset_selections` table preserves deliberately saved
choice provenance separately from canonical `access_grants`. Matching independent
grants remain Custom. Managed grant edits invalidate a saved label; orthogonal
rights, unrelated revisions and issuer liveness changes preserve saved intent.
Inactive grants stay inactive. Versioned definitions are immutable, with no
backfill or automatic reissue. Applying and previewing these bundles, including
regular-results disclosure and retained destructive consequences, require the
subsequent API/CLI/UI work tracked by #3271.

An internal transaction-scoped planner now computes explicit before/after
operations, retained issuer/timestamp provenance, deliberate reissue intent and
independent Results, execution, destructive and file-role consequences. Its
confirmation digest binds the current member incarnation/revision, caller and
issuer authority, resource state and immutable definition in one snapshot.
The selected resource is validated live; FK-valid historical grants for unrelated unavailable resources remain in the canonical before/after plan without regaining resource authority. The planner performs no writes and the digest supplies no authority. No public
preset preview or application endpoint, CLI command or selector is enabled by
this internal slice; transport and atomic application remain follow-up work.
An empty starting grant collection serializes as `before: []`, including in the
canonical confirmation fingerprint; preview collections remain JSON arrays.

Supported operations:

| Resource | Operations | Current scope |
| --- | --- | --- |
| Agent | discover, chat, run, delegate | Discovery is separate from conversation and execution authority. |
| Routine | view, results, run, edit | Explicit root-routine grant **and** run on every resolved agent; cross-agent delegation additionally needs delegate. Nested definitions are frozen dependencies. View exposes the stored definition; Edit with View allows collaborative draft changes, with trusted testing/review before publication. |
| Project | list, read, contribute, manage, delete_issues, write, delete | List exposes issue metadata; Read exposes shared issue details/comments and project source files. Contribute with Read permits configured intake requests, shared comments and arranging intake work. Manage with Read explicitly shares editable project details and milestones. Delete issues additionally requires Read/Manage and reviewed disposable work. Write/delete apply to project source files with their existing role floor. |

Routine grants replace the workspace-wide `routine.run` requirement for Guests.
Run alone does not grant definition or draft access. A new migration preserves existing
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
`--search TEXT` filters shared issue titles and identifiers, without searching
private descriptions or comments. It is only valid for the collection command.
On a temporary 429 the browser preserves the current result view and search
draft, pauses collection polling and disables search/refresh/pagination until
the bounded Retry-After interval ends, then checks again. User/workspace
changes clear that state. Definitive 401/403/404 responses discard the result
projection; a subsequent 429 or failed query cannot restore its cached titles.
Only a fresh successful response restores results. Project metadata and
contribution forms retain their separate requests. CLI prints the server retry guidance, exits 6 and does not
retry automatically.

The list accepts `project_id`, `limit`, `offset` and optional `q` (up to 256
Unicode characters, trimmed). Search is a literal substring of title or identifier,
using SQLite ASCII case folding; `%` and `_` are literal characters. Search and
its total count use the same live project.list grants and workspace filter.
Live grants are materialized before searching issues by the project index, so
hidden workspace issues are not scanned. Issuer demotion or suspension returns
no list rows and a zero total. Read/Contribute without List remains excluded
from the catalog; Read still permits an individually authorized detail.
The list returns identifiers, titles, status and project identity. The detail
accepts an issue ID or identifier, requires project.read and adds its description. Neither returns crew/agent references, comments, activity, relations
or run history. Foreign and revoked details return 404. The shared workspace
issue board, global search and statistics routes remain closed to Guests;
searching this dedicated Guest projection does not open them. Collection
`X-Total-Count` counts the filtered total before pagination; the next-offset header
supports pagination.

Shared issue collection requests (including empty search and pagination) have a
per-process authenticated budget shared by browser sessions and CLI tokens of the
same user/workspace: default 20/minute, burst 15, plus 20/minute, burst 5 for
each explicitly authorized project or the all-project selection. Both budgets
are charged together only when admitted. Query text, offset and IP do not create
new budgets. Detail, comments and project metadata are outside this policy.
Invalid inputs and forbidden explicit projects retain their existing errors.
A refusal returns generic 429, `Cache-Control: no-store` and rounded-up
`Retry-After` seconds; it discloses no audience or workspace counts.

The lazy limiter retains at most 4096 user/workspace entries and 257 audiences
per entry (256 valid List projects plus all-projects), and 32768 audiences
globally. Existing audiences remain usable at capacity; a refused new audience
does not allocate an empty account or charge either budget. Capacity overflow is the
same transient 429; active entries are never evicted to grant a fresh burst.
Idle entries expire after at least five minutes and the configured full-refill
interval. Rate settings are exposed in Rate Limiters; process restart resets
these in-memory budgets and replicas enforce their own budgets. Rate limiting
does not truncate counts or bound the cost of one large authorized search.

The web catalog keeps an unsubmitted new-issue draft when the search changes.
A confirmed creation shows its identifier and, when readable, a detail link even
if the current filter hides the new row. An unconfirmed or other-project response
retains the draft for a retry under the same operation key.

Crews, credentials, direct routine publication, private restricted-output audiences, full Issues Manage, explicit
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
only account security: password, browser sessions, revoking CLI tokens and
own CLI bootstrap from a verified browser session. Profile edits and minting
successor credentials from a CLI bearer are not on the allowlist. A restricted row
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
not introduce routine Edit or full project Manage; project Contribute is described below. Definition View is a separate explicit operation, described below.

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
existing tier/scope checks and ordinary self-revocation behavior. Browser device
pairing start/poll uses the same verified-session boundary. The account menu
offers Connect CLI to trusted and Guest accounts once access mode is resolved;
it does not expose trusted-only profile, notification or system-version reads.


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
Intake changes return 409 while a linked routine is queued, running or waiting,
even if its issue execution has become superseded. The same transactional
active-work check protects the deletion preview and DELETE; rejected requests
preserve the issue and execution. Completed runs do not keep work busy.

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


## Explicit routine definition View

Routine `view` is an independent additive operation alongside `run`. It grants
read access to the current stored definition, including prompts and defaults,
for that one nondeleted routine. It does not require an execution runtime,
agent Run/Delegate grants or an active runnable graph. The Guest navigation and
self/admin access explanations expose View independently of invocation.
Run keeps the existing root-routine plus every agent/provider dependency checks.

| PRD model | Kept / changed / deferred | Reason |
| --- | --- | --- |
| View without Run | Kept for definition inspection | A reviewer can understand a routine without execution authority or a runtime. |
| Run automatically implies definition View | Changed for existing operations | Existing Run shares deliberately exposed only a minimal invocation contract. Automatically revealing prompts/defaults would expand their disclosure without administrator review; View is explicit. |
| Regular routine run history and outputs | Kept through deliberate Results with View | Existing definition-only and invocation-only shares are not widened. The new share exposes past/future regular results while retaining every inherited private conversation audience. |
| View includes other participants’ private outputs | Deferred to an explicit result-audience contract | A routine share must not grant access to another client’s private execution/chat. Existing private run receipts keep their current actor and execution fences. |
| Edit and version publication | Draft editing delivered; direct Guest publication deferred | The collaborative draft shares current View/Edit authority. Trusted publication retains testing, exact revision review, author provenance and unattended execution protections. |

`GET /workspaces/{workspaceId}/restricted-routine-library` returns only live
routine View/Run shares, with `can_view`, `can_results` eligibility, current graph `can_run`, and runtime
installation metadata. The detail route adds definition/hash/version only with
a live View share. Both use the authenticated Guest and selected workspace;
the envelope includes no issuer/member identity or run records. Stored definition literals are deliberately shared as authored; vault credentials are never resolved. Revoked,
foreign and deleted definitions return 404. View does not imply credentials,
crew/agent directory access, publication or execution.
## Guest project details management

An explicit project `manage` share combined with `read` permits changing the
project name and description through dedicated thin Guest routes. Manage explicitly shares that description; existing Read shares retain their prior issue/file projection. It does not
imply Contribute, source-file writes, intake configuration or access administration.
The UI preserves failed drafts and requires explicit reload after a changed
snapshot; CLI requires the reviewed version and never automatically retries it.
Live account, local membership, Read/Manage issuers, update and journal entry
share one transaction. Other client projects and private metadata remain hidden.

Known-project metadata and milestones also open at
`/issues/manage?project=<known-project-id>` with Read+Manage and no List share.
The exact route checks only that resource through the dedicated API before
showing protected fields; it adds no Issues navigation, selector, issue list,
search or count. Query data never grants access or selects another workspace.
User/workspace/project changes require fresh verification and reset protected
drafts; current credential hints still control mutation affordances. Temporary
workspace-directory loss preserves the existing draft behind the availability
boundary; it cannot admit another resource. Actual resource access loss clears
protected fields. An optional explicit `issue_id` opens only that issue's fresh
deletion preview after project verification, with matching project and current
Read+Manage+`delete_issues`. Successful confirmed deletion stays on this route
and preserves the project draft; it never opens an issue collection.

Milestones and explicit issue deletion use dedicated scoped routes. Remaining
project settings require further scoped enforcement. The original
PRD's level hierarchy remains a target: current operations are explicit, additive
shares, so administrators must choose the lower operations they intend to share.


## Guest milestone management

Project Read+Manage also authorizes milestone metadata through dedicated scoped
routes. Each write uses current account, local grants, live issuers, a reviewed
version and atomic journal. Lists are paginated with a filtered total and current
credential mutation hint. These routes never expose issue internals or member
identity. Only empty milestones can be deleted: linked issue work, including
legacy cross-project references, is never unlinked by a Guest. Issue deletion uses the separate explicit share below. Additional project
settings and original PRD level inheritance remain open.

UI edits preserve drafts, block stale snapshots and pin a deletion confirmation
to the version actually reviewed. CLI drives the same projection and requires
version/confirmation for deletion, with no conflict refresh or retry.
## Explicit Guest issue deletion

Project `delete_issues` is a separate destructive share and requires Read and
Manage on the issue's current project. The dedicated preview and DELETE use that
intersection; existing Manage and source-file `delete` grants do not imply it.
Deletion requires a reviewed opaque snapshot, matching project and explicit
confirmation. Only Backlog or Cancelled issues without active work or sub-issues are eligible.
Authority, version, row cascade, human chat receipts and audit commit atomically.
The historical receipt audience remains trusted; Guest chat/operation access does
not expand. No deleted comment bodies or private execution metadata are returned.
Attachment storage cleanup follows commit and targets exact orphaned digests.

| PRD model | Kept / changed / deferred | Reason |
| --- | --- | --- |
| Project managers may remove disposable issues | Kept with current Backlog/Cancelled and active-work guards | Preserves the ordinary issue lifecycle and avoids interrupting trusted execution. |
| Manage automatically includes deletion | Changed to explicit `delete_issues` | The earlier Manage increment was presented as metadata editing; a destructive capability requires a deliberate new share. |
| A single inherited project-level preset | Deferred | Current explicit operations preserve saved grant provenance; a later level UI must describe and apply its full bundle deliberately. |


## Shared routine draft editing

An explicit routine `edit` share supplements `view`. It shares the existing
collaborative unpublished draft with that editor, including unfinished prompts
and defaults. View alone continues to expose only the published definition.
Existing Run grants gain neither disclosure nor editing rights automatically.

- `GET /api/v1/workspaces/{workspaceId}/restricted-routine-library/{routineId}/draft`
- `PUT /api/v1/workspaces/{workspaceId}/restricted-routine-library/{routineId}/draft`

The projection contains `draft_id`, `revision`, `base_revision`, `name`,
`description`, `definition` and the credential's `can_edit` hint. No author,
issuer, execution origin, trigger or gate-override metadata is returned. PUT
accepts only the six content/snapshot fields; it does not accept `can_edit`.
Current local Guest membership, active account, live issuers and both rights
are read with the draft write and content-free journal event in one transaction.
PUT additionally requires the existing `workspace:admin` token scope.

A missing draft starts at revision zero against the published recipe. Every
save checks the exact draft ID/revision and publication base. A stale editor or
changed published base receives 409 without automatic refresh or retry; an audit
failure rolls back the edit. Saving does not change executable bytes or version
history, create a trigger, run a test or approve risk. Server-owned draft metadata
and origin remain intact. The trusted authoring workflow must review and test the
exact revision before publishing. Direct Guest publication and full Routine Edit
acceptance remain unfinished PRD work.

The Guest definition view offers **Edit shared draft** only with current View,
Edit and mutation scope. Transient errors/conflicts retain local work; hard
access loss clears the editor. The CLI reads the same JSON snapshot with
`crewship routine shared-draft ROUTINE` and saves it with
`crewship routine shared-draft save ROUTINE --file draft.json` (or `--file -`).
Read-only `can_edit` is removed before PUT; the CLI performs no hidden GET or
conflict retry.


### Explicit routine history and outputs

The `results` operation is a deliberate addition to definition View. Existing
View and Run shares are not widened or backfilled. Administrators can select
**View runs and outputs** in routine Share or the member editor; it also needs
current View and active issuers. The share exposes past and future regular
pipeline run metadata and outputs for this routine, including runs invoked by
other people or automation. Review authored/output content before sharing.
Inputs are not shown directly; outputs may contain them. It does not grant execution or disclose requester identities, errors,
costs, transcripts, internal origin IDs or credentials resolved from the vault.

Dedicated GET collection/detail routes under the Guest definition library use
one read transaction for local active membership, live View/Results grants,
recipe existence, origin audience, filtered count and response projection.
Lists contain only run ID, routine ID, status, mode and timestamps; selected
results additionally expose final output and `pipeline_run_step_outputs`.
Counts and pagination use the same visibility predicate before pagination.
The run, its pinned version and the live recipe retain inherited conversation
read audiences; a routine share cannot override a private chat or make a
missing/non-chat origin readable. Detaching a live recipe does not erase a
historical run/version origin. No execution runtime or agent Run grant is
needed for an ordinary shared history read.

Private restricted-runtime receipts retain their separate actor/attempt/proof
ceiling and existing own-result surface; they are not read from raw output
storage through the history projection. Generalizing those result audiences,
full ordered View/Run/Edit presets and direct Guest publication remain open.
UI hard 401/403/404 responses remove result data, while transient refresh errors
retain the last successful authorized projection with a notice.

Shared history excludes issue-triggered runs and their replays. NULL-origin
runs also require valid start-journal evidence and the common autonomous-run
predicate; recorded human authority, missing requester authority, malformed
payloads, missing evidence and unknown-origin replays are hidden. Replay
preserves its original origin and any compatible trusted caller origin.
Selected output content is capped at 256 KiB before loading; the encoded
response is capped at 1 MiB. Oversized details return 413 while list metadata
remains available. Final and step outputs are credential-scrubbed on read,
including historical stored content; final output is also scrubbed on persist.

Preset selection workspace, membership, resource and selector foreign keys have leading indexes. The workspace index also covers member lookups, so parent cleanup and scoped backup reads avoid scanning every selection.

### Administrative preset catalog

The read-only `/workspaces/{workspaceId}/access/presets` catalog and `workspace member access presets` CLI expose immutable definitions and hashes. Both retain the existing administrative member-policy credential and live authority checks; listing writes no grants, selection metadata, audit entries or membership revisions. Application is not enabled by the catalog slice.

The OpenAPI `MemberAccessAgentRight` component names the existing agent-right variant for reuse in administrative policies. It preserves exact operation choices, required IDs and rejection of undeclared fields, without changing grants or runtime authority.

OpenAPI also names `MemberAccessProjectRight`, `MemberAccessRoutineRight` and their complete `MemberAccessRight` union. The schemas preserve existing primitive operation choices, IDs and closed objects exactly; naming them does not add inheritance or alter grant authority.
