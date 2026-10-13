# Chat entity references

This contract covers durable Issue operations, their reference API and CLI.
It supplies data for chat cards; it does not provide a frontend card renderer,
proposal confirmation or dedicated entity WebSocket events.
Mutation producers must persist operations and references through the host's
verified authority checks; these read endpoints never create or execute work.

## Agent operations

The sidecar exposes typed tools at `/mcp/issues`, injected into supported CLI
adapters as `crewship-issues`. Creation, update, comment and explicit show use
the same handlers as HTTP `/issue/...` calls. Tool arguments cannot supply an
actor, chat, workspace or human authority; these come from authenticated tokens
and durable host run admission. Read-only calls do not create cards.

Every human-requested mutation requires an explicit operation key, at most
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
its live workspace and crew, and remain within that crew for both mutations
and replay. A missing agent never removes a stored human origin. Human-origin
routine mutations still require an identified agent. Explicit show requires a
human chat origin.

Once run tokens are enabled, old agent tokens cannot mutate issues. This
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
policy. Immutable pinned versions retain their original authoring origin.

Backup and fork include origins alongside their dependent records. Forks clear
the human receipt rather than silently granting the copied work autonomous
rights. Normal restore preserves provenance, but refreshed chat authority makes
old receipts stale: restoring a backup does not authorize future work on behalf
of a previously admitted human.

## Upgrade boundary

The migration preserves legacy records and terminal history. Existing
`PENDING`, `QUEUED` and `RUNNING` assignments and resumable orchestration
missions receive an immutable invalid origin when their admission provenance
cannot be recovered. Recovery therefore cannot reinterpret them as autonomous.
Submit a fresh authenticated request to authorize replacement work; old records
remain auditable and may settle as failed. Legacy backup imports apply the same
fence when the origin column is absent, while current explicit autonomous
`NULL` origins remain autonomous.

Standing routine grants remain unchanged. Pending and active pipeline chains
are fenced only when durable journal evidence identifies a chat-human root.
Old autonomous run admissions without an explicit host-admission marker also
fail closed. This rollout boundary is intentional and requires checking queued
work before deploying.

## Authorization

Every request requires authentication and a selected workspace, through the
same workspace middleware as chat history. Supply the workspace in
`X-Workspace-ID` or `workspace_id`.

Only ordinary trusted sessions are supported. Restricted sessions receive an
opaque `404`; these routes are absent from the restricted route allowlist.
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
request returns `404` instead.

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

No new entity-specific WebSocket event is emitted by this read surface. Polling
and reconnect/focus reconciliation remain necessary; WebSocket connectivity
alone is not evidence that a card is current.

### Reference record

A reference contains `event_id`, `kind`, `verb`, `entity`, `provenance`, `anchor`
and `revision_at_action`. The supported kind is `issue`. The operation records
its verb and actor identity; the list does not expose operation keys, request
hashes, authority receipts, access snapshots or stored mutation payloads.

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

Existing issue detail responses also include `revision`, `aggregate_revision`
and `allowed_actions`. Revisions are selected with the issue fields, rather
than assigned after another query. Actions describe the viewer's current
permission; mutation endpoints still reauthorize and validate state.

## CLI

```bash
crewship chat refs <chat-id> --limit 100
crewship chat refs <chat-id> --cursor '<next_cursor>' --limit 100
crewship operation get <operation-id>
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
a failed run, an invalid cursor and the sanitized operation response. Random
cursor and snapshot values are normalized in the fixtures; they are not usable
credentials. Tests compare these fixtures with the production handlers.
