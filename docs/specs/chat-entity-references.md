# Chat entity references

This contract covers durable Issue operations, their reference API and CLI.
It supplies data for chat cards and their viewer-scoped frontend. Proposal
confirmation remains outside this contract.
Mutation producers must persist operations and references through the host's
verified authority checks; these read endpoints never create or execute work.

## Agent operations

The sidecar exposes typed tools at `/mcp/issues`, injected into supported CLI
adapters as `crewship-issues`. Creation, update, comment and explicit show use
the same handlers as HTTP `/issue/...` calls. Tool arguments cannot supply an
actor, chat, workspace or human authority; these come from authenticated tokens
and durable host run admission. Read-only calls do not create cards.

Every agent mutation requested by a human requires an explicit operation key, at most
200 bytes. HTTP accepts `operation_key` or `Idempotency-Key`; conflicting
values return `409`. MCP mutation tools require `request_key`. Reuse that key
when retrying the same operation; never generate another key for a retry.
The scope is workspace plus actor. Agent calls use the agent ID; explicitly
keyed autonomous routines without an author agent use the persisted routine and
crew IDs as a system actor. The same key with a different request or run
returns `409 operation_key_conflict`. A successful retry returns the original
result and operation ID with `replayed: true`, after fresh authorization. An
issue that is no longer readable is not returned through replay.

The host verifies both the agent's crew scope and the requesting human's
current authority inside the mutation transaction. A card is anchored to the
host-admitted run and triggering message. Forged or unavailable run identity,
ended runs and revoked requester authority fail closed. Mutation, operation
result and reference commit or roll back together. Successful no-op updates
also reserve their key. Genuine host-admitted autonomous work retains its
existing mutation behavior without generating a human chat card. Explicitly
keyed autonomous operations also persist retry results; keyless autonomous
HTTP calls retain legacy behavior. Agentless autonomous routine actions must
come from authenticated host dispatch, match a persisted running routine in
its live workspace and crew. Keyless autonomous actions retain their legacy
workspace reach; explicitly keyed mutations and replay remain within the
admitting crew. A missing agent never removes a stored human origin. Human-origin
routine mutations still require an identified agent. Explicit show requires a
human chat origin.

Legacy crews without run tokens retain unkeyed HTTP and MCP issue mutations; the sidecar drops keys it cannot bind to a verified run. These calls retain legacy permissions and do not claim verified requester guarantees. Normal current execution provisions v2 tokens, including channel jobs and delegated runs; a v2 token cannot downgrade through an old sidecar. Replace retained pre-v2 sidecars and agent processes with old v1 credentials during rollout before claiming the new authority guarantees for them. Once run tokens are enabled, old agent tokens cannot mutate issues. This
delivery supports human-run create, update, comment and show. Work execution,
relations and attachment writes from human runs return
`403 chat_issue_action_not_supported` until their host paths implement the same
transactional authority contract; autonomous runs retain those existing verbs.
This guard is intentional and is not a claim that every issue action has been
migrated.

## Delegated and deferred work

The host stores an immutable execution origin separately from the executing
agent. Assignments, missions, peer queries, mentions, routine drafts, published
routine heads and versions, schedules, pending work and pipeline runs preserve
that origin. Completing the parent run does not turn its children autonomous.
Each subsequent issue mutation rechecks the original human's current authority
and the executing agent's access inside the mutation transaction. Revocation
also prevents publishing a delegated result comment, while allowing the run to
settle. Deleting the source chat revokes the origin without deleting its record.

Only the authenticated host dispatcher may supply a direct pipeline execution
context. Agent or crew tokens cannot assert an execution origin, and routine
arguments cannot override identity fields. A missing or invalid persisted
origin fails closed; it never falls back to autonomous execution.

A new chat invocation supplies its own origin even when invoking a previously
published routine. Non-chat public routine invocation retains the existing
routine-launch authorization policy and any stored authoring origin; this
contract does not replace standing automation grants with a new launcher role
policy. Immutable pinned versions retain their original authoring origin. Unpinned schedules store only their own explicit invocation override; otherwise they resolve the current head at execution, rather than freezing its origin at schedule creation. A fresh chat save can bind the head to a fresh verified source. An authenticated OWNER/ADMIN may explicitly clear the head and selected schedule bindings through the audited [operator origin-binding API](routine-origin-binding.md). This never clears immutable versions or already admitted work. Clearing a binding does not re-enable a circuit-broken schedule; use its ordinary operator enable action afterward.

Authenticated public issue creation, starting work, handing work back to an
agent and commenting capture a web requester admission, rather than inventing
a chat receipt. The resource identifies where admission happened; it does not
grant unrestricted access or restrict all subsequent actions to that one issue.
Existing backend action, target and crew checks remain authoritative. It pins
the actual requesting person and membership identity plus a dedicated
`web_execution_revision`. Only role, capabilities and access-mode changes
advance that fence; unrelated chat participant/grant changes do not revoke it.
Execution rechecks the live account, membership and target permissions.
Recurring issue admissions bind the authenticated recurring definition and
derive separate per-issue receipts without renewing the captured membership
epoch. Deleting a template does not revoke work it already admitted.
Existing admitted work retains its own origin when a later request starts new
work. These verified web origins authorize operations but do not manufacture
chat cards or a triggering chat message. Narrow CLI tokens cannot be converted
into a broader standing execution grant. Their permitted issue CRUD, comments
and recurring-template edits/disable remain available without acquiring a new origin;
explicit Start and human-to-agent handoff require capture-capable authority.
Unrestricted tokens and the explicit global wildcard follow the same admission
policy as the web session. The existing account-level restricted-member rule
is intentionally global across workspaces, consistently with chat admissions.
Permanent invalid recurring receipts disable the template, advance its due
bucket and notify a current eligible member. Transient database failures retry.
Legacy/scoped NULL-origin templates may generate ordinary issue records, but
do not authorize agent execution. Each such occurrence receives an immutable
unavailable origin (`unverified-recurring:<issueID>`, empty chat/message and
`human_authority={}`), including templates without creator attribution. This
prevents later assignment dispatch from classifying unknown provenance as
autonomous. The template remains NULL-origin and its schedule advances, with one reauthorization
notice to an eligible owner per template. A fresh authenticated Start can authorize
the occurrence explicitly. Repeated pauses deduplicate notices per template/origin.
Scoped execution-affecting edits preserve a valid origin only for its original
human. Another scoped editor must use a full authenticated session to capture
their own admission; otherwise a `403` problem with
`requester_authority_required` in its detail leaves the
entire template unchanged. Only labels and disable may preserve another person’s
origin. Mixed disable/content edits require the content editor’s admission.
Unbound scoped templates remain unbound; explicit scoped re-enable is refused
without changing the template.

Channel jobs without a verified authenticated admission may answer,
but their runs carry a host-derived missing-requester marker. Issue mutations
and further delegation fail closed; they do not inherit the completing run's
authority or become autonomous. Queued mentions are grouped by their actual
stored origin and author, never the preceding run. A delegated execution's
chat is resolved from its own host admission; the sidecar's boot chat is never
a substitute for run identity. A verified source lacking a persisted execution
chat returns `403 delegation_execution_chat_unavailable`; no chat is invented
and no unrelated issue receives delegation side effects.

Routine agent steps have durable `ROUTINE` journal admissions and publish agent
RUNNING state. Admission failure fails the step; this is intentional so every
issued run token has a durable authority record.

Backup and fork include origins alongside their dependent records. Forks clear
human receipts without granting copied work autonomy. Membership rows are restored,
but each imported membership receives a new web execution epoch above live and
archived epochs in the same restore transaction. Historical web receipts are
preserved for audit and become stale, including after replacement restore;
a fresh authenticated action must admit new web-origin work. Chat receipts still
require current audience authority. Restore does not promise exactly-once model
delivery: nonterminal mention reconciliation and fork claimed-state handling
remain tracked in #3103.

## Upgrade boundary

The migration preserves legacy records, terminal history and queue status.
For unfinished assignments, orchestration missions and routine work, recovery
requires unique durable host-admission evidence. Exact historical human receipts
are copied without renewing membership/chat epochs, then checked live at execution.
Positively host-admitted machine roots retain autonomy. Missing, malformed,
duplicate, cyclic or excessively deep provenance remains fenced until a fresh
authenticated request admits replacement work. This includes historical UI/cron
work without positive evidence; absence of a human receipt is not proof of autonomy.

Legacy backup imports use the same rule when the origin column is absent.
Omitting the Journal category does not authorize unknown work. Current backups
with explicit autonomous `NULL` origins remain unchanged. Standing routine
grants remain unchanged. Old autonomous journal admissions without an explicit
host-admission marker also fail closed. Check affected queued work before deployment.

## Authorization

Every request requires authentication and a selected workspace, through the
same workspace middleware as chat history. Supply the workspace in
`X-Workspace-ID` or `workspace_id`.

Only ordinary trusted sessions are supported. Restricted sessions receive an
opaque `404`; these routes are absent from the restricted route allowlist.
This includes restricted Guest viewers even when ordinary issue access permits
them to read a shared project. The E1 frontend must project this unsupported
card capability as a generic "Unavailable" state, without exposing an object
ID, title, kind, action or count. It must not infer card visibility from ordinary
issue access, fetch through another identity, or display cached protected data.
The list requires current chat access, a live workspace and a live chat agent.
Each returned issue must belong to the selected workspace, matching the current
ordinary issue GET read predicate. A readable chat alone does not make a
foreign-workspace issue visible. Authorization is checked again after gathering
a page; a changed chat audience invalidates the response.

An operation is readable only when its issue is currently readable and at least
one of its source chats is readable in the selected workspace. Missing and
inaccessible operations both return `404`. Operations without an authorized
source reference are unavailable through this endpoint.

Hard deletion through the public issue DELETE handler stores a minimal human
operation and deletion reference in every chat that previously referenced the
issue, atomically with deletion. Its actor is the actual human deleter; its run
and message IDs retain the original chat anchor. Old non-deletion references
are omitted after deletion. Explicit deletion references use `entity.deleted:
true` and an empty `identifier`; no old title or arbitrary result is returned.

Only exact version-1 snapshots of the current ordinary trusted-workspace access
boundary are readable, with current workspace membership and chat access.
Restricted sessions, foreign workspaces, unsupported versions, duplicate or
additional snapshot fields fail closed. Future narrower issue access requires
a new evaluator. Issues deleted through other paths without a receipt remain
omitted.

All responses set `Cache-Control: no-store`.

Limited CLI tokens cannot mint an unrestricted WebSocket ticket or enqueue
steering text: both endpoints return `403 scoped_token_chat_unavailable` until
scope-bearing chat authority is implemented. Unrestricted sessions/tokens and
the global `*` scope retain access. A version marker rejects old WebSocket
tickets that predate this gate, including cached tickets; clients must request
a fresh ticket on reconnect. Access and refresh tokens are unchanged. Public
journal projections omit human admission receipts and triggering-message IDs;
their durable host records remain available for authorization.

## List references

`GET /api/v1/chats/{chatId}/entity-refs`

| Query parameter | Contract |
| --- | --- |
| `limit` | Integer from 1 to 100; defaults to 100. Invalid values return `400 limit_invalid`. |
| `cursor` | Opaque `next_cursor` from a preceding response. Omit for a new snapshot. |

The response has these fields:

| Field | Meaning |
| --- | --- |
| `contract_version` | Integer `1`; version of this snapshot and invalidation contract. |
| `reset` | Start a replacement list; discard the preceding list rather than append to it. |
| `refs` | At most `limit` authorized reference records. |
| `next_cursor` | Opaque cursor for continuation or the next poll. |
| `snapshot_id` | Identifies pages belonging to one replacement list. |
| `has_more` | Another page remains in this snapshot. |
| `snapshot_complete` | This page completes the replacement list. |

The initial page has `reset: true`. Continuation pages have `reset: false` and
the same `snapshot_id`. Passing a completed cursor starts a new bounded
replacement snapshot, also with `reset: true`. This deliberately uses full
replacement polling instead of claiming an incomplete permission fingerprint
can safely support incremental visibility changes.

A snapshot fixes the upper reference position for pagination. It is not a
frozen historical copy of issue data: current authorization and object existence
are checked on each request. New references after that position appear in the
next replacement snapshot. Internal positions and counts of hidden records are
never returned.

Cursors are authenticated and encrypted with a process-local key, bind the
viewer, workspace, chat and captured authority, and expire after 15 minutes.
A cursor belonging to a different viewer/chat/workspace, a corrupted cursor,
an expired cursor or a cursor from a previous server process returns
`400 {"error":"cursor_invalid"}`. Discard local pagination state and retry
without it. If the captured authority changes while the viewer retains access,
a valid cursor starts a new replacement snapshot. If access is gone, the
request returns `404` instead. A database failure returns `503`; an authority change while gathering one page returns `503 entity_refs_authority_changed`, requiring a fresh snapshot.

### Client reconciliation

1. Load the first page without a cursor.
2. On `reset`, immediately remove the previous displayed list and stale
   authorized detail data; append subsequent pages only to this `snapshot_id`.
3. Follow `next_cursor` while `has_more` is true.
4. Reconcile cached entities against the complete replacement list only when
   `snapshot_complete` is true. Absence from the first page does not prove that
   an entity is absent from the complete list.
5. Poll again using the completed cursor. This starts another replacement list.

Keep unsaved editor drafts separate from authorized display data. A reset must
not silently destroy a draft, but a draft grants no permission to keep displaying
old server data or submit an action. Independently reauthorize a selected detail
before displaying it again. Failed synchronization must never be labelled live.

### WebSocket invalidation hints

Successful writes emit these `ServerMessage` hints on `chat-hints:{chatId}`.
The same hints remain on `session:{chatId}` for existing clients and NDJSON
streams. Both channels use the exact same current trusted-audience predicate
at subscription and delivery. Card-only clients subscribe to `chat-hints` to
avoid receiving session run frames:

```json
{"type":"chat.entity_refs_changed","channel":"chat-hints:c_1","payload":{"contract_version":1}}
{"type":"issue.revision_changed","channel":"chat-hints:c_1","payload":{"contract_version":1}}
```

Payloads contain no chat/object IDs, revisions, counts or content. A session
receives a hint only when it contains a completed reference visible under the
same trusted-workspace issue predicate as this endpoint. Each socket's live
trusted chat audience is checked at delivery, including suspended users,
restricted profiles, removed membership/participants and deleted agents or
workspaces. The session channel is already known to its authorized subscribers;
these hints are never broadcast workspace-wide.

`chat.entity_refs_changed` is emitted after verified internal create/update/
comment/show operations append a reference, only in the originating chat, and
after human deletion appends receipts in each affected chat. Show emits no
revision hint. `issue.revision_changed` reaches referenced chats after issue
mutations, including agent relations (both endpoints), review policy, takeover,
and the primary successful public issue create/update/start notifications, `issueEvents.record`
notifications, and attachment/code-link publish paths. No hint is emitted for a
failed transaction, or when an idempotent replay only returns its saved result.
Delivery/outbox retries do not republish these hints.

This is deliberately a bounded set of application write paths, not a database
change feed. Bulk writes, relation deletion, child changes without a primary
issue notification, and direct DB or background writes can still change revision counters without a hint. A show
operation adds a reference without increasing issue revisions. Consumers treat
either event as a coalescible request to refetch their authorized snapshot and
current revisions, never as object data or proof that a revision increased.

Hints are best effort and have no durable delivery or replay guarantee. Polling
and reconnect/focus reconciliation remain necessary; WebSocket connectivity
alone is not evidence that a card is current. A failed read clears authorized
display data according to the reconciliation rules above.

### Reference record

A reference contains `event_id`, `operation_id`, `kind`, `verb`, `entity`, `provenance`, `anchor`
and `revision_at_action`. The supported kind is `issue`. The operation records
its verb and actor identity; the list does not expose operation keys, request
hashes, authority receipts, access snapshots or stored mutation payloads.

`operation_id` is accepted by `crewship operation get`. Live issues also include
`current`: title, status, priority, nullable assignee type/ID, current field and
aggregate revisions, and `allowed_actions`. This bounded batch projection and
run status are gathered with all references in one query, without per-card
operation/detail requests. Deleted receipts omit `current` entirely.

`entity` contains `id`, current `identifier` and `workspace_id`.
`provenance` contains `actor: {type, id}` and `at`. Actor display names are not
part of this projection. `revision_at_action` contains the issue's `revision`
and `aggregate_revision` as recorded after the operation, not current counters.

`anchor` contains `trigger_message_id`, `run_id` and `run_status`. Status comes
from the durable run journal and can be `running`, `completed`, `failed`,
`cancelled`, `timeout`, or `null` when no matching run record exists. A failed
run can have a persisted reference even without a final assistant message;
clients can render it behind the triggering user message.

## Read an operation

`GET /api/v1/operations/{operationId}`

Returns a sanitized projection with `id`, `status`, `kind`, `verb`, `entity`,
`provenance` and `revision_at_action`. It intentionally does not return the raw
stored `result`, operation key, hash, human authority or access snapshot.
This is a human-viewer projection, not a mutation replay endpoint.

## Read current revisions

Both routes support an issue identifier or internal ID:

- `GET /api/v1/issues/{identifier}/revision`
- `GET /api/v1/workspaces/{workspaceId}/issues/{identifier}/revision`

The selected workspace and any workspace path parameter must agree. The
response is `{ "revision": 1, "aggregate_revision": 1 }`, with current integer
counters. Missing or inaccessible issues return `404`. Revision counters come
from the issue and child-data database triggers; they are not timestamps or an
assertion that unrelated run/automation data has been refreshed.

The counters cover issue-owned fields and child records, not a version of the
entire rendered detail. Joined crew, project, routine, creator, assignee, owner,
delegate and worker display names, membership-dependent name visibility, run
state and `allowed_actions` are read-time projections. Renaming an assignee or
changing an active run can change a GET response while both counters stay equal.
Label membership belongs to the field revision; label definition changes also
invalidate the aggregate through the label trigger.

An open detail must refresh its current GET on focus, reconnect and a bounded
poll while visible, even when the revision counters match. Resource/roster
events can refresh joined names sooner but do not replace reconnect recovery.
Until dedicated projection freshness is supplied, show the detail's last
successful fetch time; counter equality alone must never label the entire
detail as live. Refresh permissions before presenting an action after a stale
interval; the mutation endpoint remains authoritative. The reference endpoint
already returns replacement snapshots of current card data on each completed
poll, including current actions.

Existing issue detail responses also include `revision`, `aggregate_revision`
and `allowed_actions`. Revisions are selected with the issue fields, rather
than assigned after another query. Actions describe the viewer's current
permission and issue state. Delete is offered only for BACKLOG/CANCELLED;
start_work only for BACKLOG/TODO with no active assignment/routine or human
hold; workflow changes are omitted for terminal issues. Mutation endpoints
still reauthorize and validate state.

## Human card edits and comments

Existing `PATCH /api/v1/crews/{crewId}/issues/{identifier}` accepts optional
`expected_revision` (positive integer) or `If-Match` (positive integer, optionally
quoted). If both appear they must agree. A stale edit returns
`409 {"error":"revision_conflict","revision":2}` without mutation.
This header is a numeric issue-field precondition, not general HTTP ETag
negotiation: wildcard `*`, weak validators and validator lists are rejected.
Clients must not infer an ETag for the complete joined detail from this number.

PATCH and `POST .../comments` accept `Idempotency-Key` (1–200 UTF-8 bytes),
scoped to the authenticated human and selected workspace. The reserve, mutation
and successful response commit together. A retry with unchanged intent returns
the original response/status and `Idempotency-Replayed: true`; both responses
include `Operation-ID`. Changed intent returns `409 operation_key_conflict`.
Current account and membership authority are checked before replay; a successful
old response cannot authorize access after revocation. Replays do not repeat
comments, activity or mention dispatch. PATCH labels and fields are atomic.
Mutation-owned activity records and durable delivery intent commit in the same
transaction. A bounded server worker resumes pending intent after disconnect,
restart or a lost acknowledgement. Stable journal replay IDs and stable mention
assignment IDs prevent repeating completed effects. Human activity uses an
`issue-activity:<event_id>` audit trace, never a fabricated agent run ID.
Pending journal audit intent survives hard deletion with a historical issue
reference; pending model mentions are cancelled when their comment or issue is
deleted. Recovery preserves the original audit event without exposing a deleted
object snapshot through the chat reference API.
Human and agent Create, PATCH/comments, bulk edits, attachment add/remove and
code-link add/remove persist audit intent with their mutation. Other issue
workflow audit paths retain their existing behavior; this contract does not
claim all historic issue emitters were converted.
Request-side recovery is bounded to the authenticated/verified workspace
(32 jobs, one second); it may recover other jobs in that same workspace, never
another workspace. The background worker handles global recovery.
Retries back off exponentially from five seconds to ten minutes and stop after
eight attempts. Malformed payloads and permanent authority refusals fail
immediately, without blocking the tail. Only terminal failures tell the author
to retry; their private notice and failed receipt commit together. Administrators
can inspect sanitized diagnostics with `crewship issue deliveries failed`.
Successful ACKs clear the payload and completed receipts expire after seven
days. Failed rows remain for diagnosis; they are not retried automatically.
A committed autonomous pipeline mention carries a server-only admission marker
and source crew snapshot; neither can come from tool arguments. Recovery checks
its persisted origin-free pipeline run/version and live source actor. Ordinary
agent machine roots require positive host `run.started` admission. Unproven
legacy agent sources do not acquire autonomous mention execution authority.
Journal outages retain retry intent and do not starve unrelated mention work.
Normal restore recognizes an exactly matching already-written journal effect;
forks retain history but never replay source notifications or model dispatches.
Label membership changes advance the field revision as well as the aggregate
revision, so concurrent labels-only editors cannot overwrite a newer label set.
Replacing a label set with the same IDs, in any order or with duplicates, is a
no-op and does not advance either counter. A changed set writes only its diff.

These parameters remain optional for old clients. Chat card editors MUST send
both an explicit expected revision and stable retry key; they retain the same
key/precondition after a timeout and start a new operation after resolving a
conflict. Comment composers MUST retain their key until success is known.

## CLI

```bash
crewship chat refs <chat-id> --limit 100
crewship chat refs <chat-id> --cursor '<next_cursor>' --limit 100
crewship operation get <operation-id>
crewship issue revision ENG-42
crewship issue update ENG-42 --title 'New title' --expected-revision 7 --idempotency-key edit-7
crewship issue comment ENG-42 --body 'Result' --idempotency-key comment-7
```

The commands use the authenticated API with the configured workspace and print
JSON, retaining pagination metadata. `--cursor` is transmitted without parsing
or rebuilding it. Neither command follows every page automatically or executes
work. API authorization failures produce a failing CLI exit status.

## Implementation and fixtures

- [Handlers and cursor codec](../../internal/api/chat_entity_refs.go)
- [Handler, authority and route tests](../../internal/api/chat_entity_refs_test.go)
- [Handler-verified JSON fixtures](../../internal/api/testdata/chat-entity-refs/)
- [CLI commands](../../cmd/crewship/cmd_chat_entity_refs.go)
- [Built-binary CLI acceptance](../../cmd/crewship/acceptance_chat_entity_refs_test.go)
- [Operation and reference storage](../../internal/chatentities/)

Fixtures cover the initial bounded page, continuation, authority reset,
a failed run, an invalid cursor, an empty list, a deleted receipt, an edit
conflict, a repeated comment and the sanitized operation response. Random
cursor and snapshot values are normalized in the fixtures; they are not usable
credentials. Tests compare these fixtures with the production handlers.

Escalations from run-token callers carry the verified source run and actor. The
host verifies the token proof, live run and matching agent/workspace/chat from
the durable execution record. A conflicting chat, actor or run is refused; the
shared sidecar boot chat never replaces an unknown execution chat. Escalating
asks for help and does not authorize work: missing or revoked requester authority
may still escalate, without restoring permission to mutate or delegate. A
configured v2 sidecar requires run-token proof. The host's proof-less legacy
path trusts its internal sidecar IPC service credential, which is not issued to
agents; supplying a source run without its proof is always refused.

Routine action arguments cannot override server identity or operation keys with
Unicode/case-fold aliases. Explicit canonical operation keys remain supported
for legacy autonomous calls; human-origin calls use the dispatcher's stable key.

## Closeout regression matrix and delivery limits

Automated evidence and live-agent acceptance are separate. These tests protect
existing paths; the dev-slot real-agent restart/retry/revocation/restore scenario
and the 500-reference / 50-object / 10-viewer performance profile remain required
before promoting this E0 delivery.

| Existing path | Regression entry point | Evidence boundary |
| --- | --- | --- |
| UI issue -> agent mutation/comment/result | `TestWebIssueStartSidecarMutationsDelegationResultAndRevocation` | Real router and sidecar; synthetic executor |
| UI comment mention | `TestHumanCommentDisconnectAndReplayKeepOneDurableMention` and `TestIssueOutbox` tests | Handler/outbox admission, not a live model |
| Comment-triggered automation comments again | `TestCommentingAutomationStopsAtChainDepth` | Human and agent handlers, durable outbox/journal, pending-run store and database chain reader; routine execution simulated; stops at the eight-hop composition cap |
| Legacy and new recurring templates | `TestLegacyRecurring`, `TestRecurringPermanentAuthorityFailureLeavesGlobalDueQueue` | Dispatcher and real migrated database |
| Cron/webhook/manual routine authority | `TestRoutineChatOrigin`, `TestResolveAutonomousRunRequiresHostAdmission` | Handler/authority tests; complete trigger-to-sidecar integration pending |
| Nested routine | `TestAssignmentChainOrigin_RoutineDispatchInheritsTheCausingRunsChain` | Chain attribution; complete nested execution pending |
| Peer query and second-run assignment | `TestQueryUsesVerifiedActorIDWithoutRosterSlug`, `TestDelegationRejectsBootIssueAndDerivesVerifiedExecutionChat` | Host handler identity; combined sidecar execution pending |
| Boot/nonboot/fenced escalation | `TestEscalationRealHostRecoversSecondRunChatAndRejectsMismatch` | Real router and sidecar |
| Scoped token CRUD and refused execution | `TestScopedIssueCRUDDoesNotCaptureExecutionGrant`, `TestScopedRecurringCRUDPreservesStandingGrantAndCanDisable` | Real handlers and migrated database |
| Legacy crew MCP | `TestIssuesMCPLegacyCrewDropsUnscopedOperationKey` | Sidecar MCP; no claim of v2 requester guarantees |
| Backup, restore, fork | `TestIssueMutationOutboxRestoreAndForkKeepDeliveryBoundaries`, `TestRestoreFencesWebReceiptsWithoutGrantingAutonomousWork` | Real migrated database; runtime recovery acceptance pending |

Projection-only attachment/code-link drains never consume mention attempts.
Delivery outcome ACK/backoff/terminal writes detach from the request deadline
with a separate two-second bound and retain the same lease fence. Foreground
recovery is still workspace-scoped, not restricted to the originating transaction.
Plain comments now produce `mission.comment` audit events and follow existing
`issues.comment` notification preferences and automation rules; no preference is
silently enabled or muted. The `issue-activity:` audit trace is not an agent run
and renders as an empty `event.run_id` in automation inputs.

Remaining bounded follow-ups: ordinary CRUD versus unavailable admission (#3101),
legacy comment source attribution (#3102), restore/fork mention reconciliation
(#3103), failed payload retention and safe journal recovery (#3104), and bulk
label/project audit semantics (#3105). Failed jobs currently retain payloads;
exhausted journal projections require operator diagnosis and have no requeue API.
Bulk status/priority/assignment audit is transactional; bulk label/project fields,
internal relation changes, workflow and Inbox mutation doors are not covered by
this PR's durable audit guarantee.

## Chat issue cards

The chat transcript renders authorized Issue references behind the last message
of their durable run. Runs without an assistant reply anchor behind their
triggering user message and show the durable run status. Both ordinary and
virtualized transcripts use the same card renderer.

Cards reconcile complete replacement snapshots on `chat-hints:{chatId}` invalidation hints,
reconnect, foreground return and every 60 seconds while visible. The E1 hint
contract uses `chat.entity_refs_changed` and `issue.revision_changed` with
`{ "contract_version": 1 }`; hints carry no object data. A client also accepts
unversioned E0 snapshot responses, and rejects unsupported version markers.
The current batch projection is the card's freshness source; no per-card
revision requests are necessary. Failed reconciliation hides protected fields
and is never labelled live. Restricted and Guest sessions show only a generic
“Unavailable” capability state and make no card/object requests.

Opening a card reuses `IssueDetailSurface` in the right panel, or a focus-trapped
sheet on phones. A completed snapshot independently reauthorizes that detail
and clears its previous server data. Detail freshness is its own fetch time;
run and automation data have separate fetches. Allowed actions are intersected
with the detail's current server permissions and govern individual controls.
Legacy detail callers retain the existing `editable` fallback. Direct work
execution, handoff and workflow controls are excluded from the initial chat
embed; the authorized issue-page link opens the existing workflow. This slice
does not implement a budget or approval preflight for starting work from chat.

Chat field editors and comments send stable idempotency keys. Fields additionally
send the revision captured when editing began. Validation retains input;
conflicts require explicit reload while retaining the draft. Unknown transport
outcomes freeze the intent, check an operation receipt when its ID is known,
then allow retry with the same key and exact request. New intent is blocked
until that result is resolved. Local drafts are separate from authorized server
data and survive reconciliation without keeping protected fields displayed.

Browser acceptance uses the production static export with handler-verified
reference fixtures: `pnpm exec playwright test --config=playwright.restricted.config.ts`.
It requires a preceding frontend build and an installed Chromium, and contacts
no live application or model provider.
