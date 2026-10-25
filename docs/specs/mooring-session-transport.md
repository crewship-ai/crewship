# Mooring human session transport

This experimental Rust-backed session profile is separately disabled by default.
The server registers neither route unless trusted configuration enables it and
verified runtime assets pass startup checks. Invalid enabled configuration fails
closed. The [pilot JSON profile](mooring-pilot.md) remains a separate contract.
This reference describes implemented handlers, not deployed multiuser or agent
acceptance. Page revocation currently can close a shared runtime and disconnect
an unaffected participant; continuity through that event is not qualified.

## HTTP operations

| Operation | Purpose |
|---|---|
| `POST /api/v1/pages/{slug}/mooring/session` | Admit a human connection against current Page authority |
| `GET /api/v1/pages/{slug}/mooring/ws` | Upgrade the admitted connection to a WebSocket |
| `POST /api/v1/pages/{slug}/mooring/tasks` | Request the original human-authored chat message as a task in this document |

These operations require current human authentication, workspace context and Page
access. A slug or `@ID` selects the Page in that workspace. The restore fence runs
before authentication/body handling. Read access cannot authorize a writer.
Agent/internal credentials cannot substitute for the human session admission.

### Session request and response

The POST body is a JSON object with optional `write` (boolean, default `false`)
and `checkpoint_base64` (canonical base64 initialization checkpoint). Explicit
JSON null for either field has the same effect as omission. Unknown or
duplicate keys, case aliases and non-object bodies are refused. The request body
is limited to 2 MiB and a decoded checkpoint to 1536 KiB. A checkpoint cannot
replace an already-open document; existing canonical storage and checkpoint
validation govern initialization. There are no caller-selected role or identity
fields.

A successful HTTP 200 JSON response contains `session_id`, `checkpoint_revision`,
`sdk_prefix`, `participant_ref`, `ticket`, `connection_ref`, `origin`, `versions`
and `websocket_path`. Revision and origin are decimal strings; origin is a
host-reserved positive safe integer. The version tuple is protocol `0.4.0`, model
`0.2.0`, engine `0.10.0`. An optional `editor` contains `url` and `bundle_digest`
only when a verified editor provider supplies both. Responses use `Cache-Control:
no-store`. This admission response is not a committed mutation or a Saved receipt.

### WebSocket upgrade

Append the separately returned `connection_ref` query parameter to the returned
`websocket_path`, which already carries workspace context. Authentication must
match the original admission. A connection can be used only once. The `Origin`
header must exactly match the trusted frontend origin; a client cannot nominate
a different trusted origin. Missing, foreign, stale or reused references are
refused before upgrade. The first public frame is the original `session_join`
with its retained one-use ticket and version tuple.

Successful upgrade is HTTP **101 Switching Protocols**, with `Upgrade` and
`Connection` headers and no HTTP response body. Subsequent messages are protocol
frames, not JSON HTTP responses. Authentication/Page/restore failures before
upgrade use the surrounding API's JSON error envelope. A malformed WebSocket
handshake may instead return a plain HTTP handshake error from the transport.

## Confirmation and limits

The HTTP adapter forwards bounded original operations through the private Rust
transport and canonical host storage. A queued/pending frame is not a saved
change. Only a confirmed host commit authorizes the corresponding Saved response;
clients retain uncertain originals without silently reconstructing packets.
Delivery authorization expires after the configured lease (default five seconds,
never longer), measured from request send time. Revoked writers cannot acquire
fresh write authority through a reader connection.

Each newly saved atomic change also records immutable historical attribution in
the same SQL transaction. The original sealed submission supplies the human or
agent actor; an agent record retains its represented human, crew, chat/message,
work, exact run/attempt and host session. Request/model identity fields and logs
cannot manufacture this record. Original Page creation attribution is unchanged.
Rejected changes, failed transactions and identical known retries do not create
another record. Metadata insertion precedes the final current-author and owner
checks, and a failure rolls back both the document change and its attribution.
The output serializer remains pure. These rows are historical data, never a
restored task or permission; existing collaboration backup remapping/collision
refusals apply. This storage boundary alone does not establish chat result UI,
artifact creation, task-version history or deployed agent acceptance.

The current UI restricts undo/redo for deletions or changes overlapping another
participant's deletion and displays a refusal message without claiming a new
save. Complete two-browser-plus-agent behavior, unaffected-participant continuity
after Page revocation and deployed UI acceptance remain separate qualification
work. This contract does not declare those scenarios complete.

The matching CLI commands and their input/output behavior are documented in
[the Mooring CLI reference](../cli/mooring.mdx). Runtime configuration is listed
in [environment variables](../configuration/environment.mdx).


## Explicit original-message task action

`Run in this Doc` is available only for a persisted message whose original
stored author matches the current human. Its picker reuses the chat document
access query and selects an existing WordCraft document with a current agent
write grant and allowed policy. The picker is metadata, not a runtime capability
or a new grant. Existing document sharing remains the only grant-management path.

`POST /api/v1/pages/{slug}/mooring/tasks` requires `chat_id` and `message_id`
strings in a closed JSON body of at most 4096 bytes. Omitting `access` preserves
the original write task. The only accepted optional value is `access: "read"`,
which narrows the same original-message task to existing human and agent read
rights. Its attached native participant has no write capability and rejects
submissions even if a later grant includes write. Unknown values or fields are denied. The host reads the original
stored instruction, checks the original human, current chat audience, existing
human/agent rights at the selected access level and workspace policy, and preserves actual native runtime qualification (writable D1 for writes,
read-only D3 may be used by a read task). This admission is not proof of complete
D3 stable paged-read acceptance. It cannot add or widen an agent grant. Reusing an accepted original
message is refused; an uncertain response does not authorize a new instruction.
HTTP 202 returns `work_id` and `status: "queued"`. It is task acceptance, not a
committed document mutation or a Saved receipt. The existing CLI invocation creates the default write task:
`crewship mooring task <page-slug|@ID> --chat <chat-id> --message <message-id>`.

The supported private execution path exposes only `doc_read` and `doc_submit`
through the existing authenticated run sidecar. `doc_submit` canonicalizes
explicit typed commands and their original read preconditions against a retained
`doc_read` view without rereading or creating authority. On uncertainty, the
returned original submission must be retried unchanged. Actual native preparation,
SQL COMMIT and correlated receipts remain the Saved boundary. A test fixture or
an accepted task does not qualify an actual private agent environment.

`doc_read({})` retains the whole-D1 command view. `doc_read({"page":{}})`
uses the existing participant `read_request` / `read_result` messages over the
private session transport. It starts an immutable native projection of the
confirmed revision. Continue with `page.cursor` and the exact string-valued
`page.revision` returned by that read; a concurrent commit does not change
later pages. D3 native content is read-only. A cursor cannot select a document,
principal or workspace and confers no permission. The original task, participant,
current grants and delivery lease are checked on every request and at physical
HTTP flush. Read task admission and read-only native grants are a separate
prerequisite; this pagination path does not create or widen them.

Each item contains lossless application JSON as `data.native_json`, a JSON
string, so opaque native integers never pass through the protocol's binary64
number representation. The header preserves document-level fields; paragraph
items preserve stable IDs and complete native blocks. Each item's `revision`
is its target revision, while the result's `revision` identifies the frozen
document. Reassemble by decoding these strings and appending the paragraph
blocks to the header's `body` in page order. Missing `next_cursor` ends the read.

`page.max_bytes` is an integer from 1024 to 262144 (default 131072), bounding
the complete serialized inner `read_result`, including metadata and cursor.
The private frame also reserves its complete outer-envelope overhead; the MCP
JSON envelope is independently bounded at 2 MiB after text escaping. A native
block or header which cannot fit is refused without truncation. At most one
snapshot per connection is retained, with a 4 MiB aggregate serialized-data
budget per native actor; this is not an RSS estimate. A completed snapshot stays
available for retry. A successful new first page replaces the connection's old
snapshot; failed creation preserves it. Replacement, participant leave and
runtime restart invalidate old cursors. An unavailable snapshot returns a
definitive conflict requiring a fresh first page, never a silently newer page.
An unresolved transport request retains only its bounded correlation tombstone;
its abandoned content is discarded and cannot satisfy a later request.

After a validated recipient reopen, and before an update whose starting revision
is ahead of the recipient's observed cut, the host may replay a strictly
contiguous chain of immutable native deltas retained only after SQL COMMIT.
Each frame preserves the complete original receipt and actual atomic revision
interval, including multi-operation atomics, and passes the same public wire
validation and current reader/owner/delivery-gate checks at physical send. The
existing bounded live records and output FIFO also trigger catch-up for a quiet
reopened reader when no later write arrives. Missing cuts fail closed; replay
never creates a snapshot, intent, source identity, author consent or authority.
An initial reader without a physically observed basis receives no invented cut.


## Recorded edit metadata

The existing chat document-access response includes separate `artifacts` metadata
from immutable Saved edit attribution. This historical projection does not alter
`documents`, standing grants or live task capability. The default-off native
`GET /api/v1/pages/{slug}/mooring/provenance` route provides the same read projection
for a document's source-chat backlinks; CLI: `crewship mooring provenance`.
Every row requires current independent original-human Page/native and source-chat
read authority. No title, chat/message reference or link is returned for a denied
row. Both paths hold the original ingress lifetime and SQL writer through bounded
physical response flush. Recent candidates are capped at 64 and collapsed per
Page/chat; `artifacts_partial` is constant true, revealing no hidden-row count.
The UI labels only recorded edits, clears viewer-scoped metadata on errors,
visibility changes and a three-second send-time horizon, and retains the native
editor while metadata refreshes. First edit is not creation. Native creation,
crew/agent artifact listing and exact historical content/version browsing are
not established by this projection; legacy files retain unknown origin.

### Original agent task registration

The human task request may declare `allow_partial: true` for a writer task.
Omitting it retains the default of one atomic change for the entire task.
A read-only task cannot request partial writes. The declaration is captured in
the original work input and retained task proof; operation payloads and tool
view metadata cannot change it.

The host explicitly registers the confirmed task's immutable work ID,
participant and partial-result policy during native participant admission.
Registration uses the existing current-rights and writer-fence transaction,
before any task submission. An unknown task is never created by submitting
operations. Read-only tasks do not acquire a writer registration. Retries retain
the original participant and cannot replace an existing task's policy.

A canonical cancellation request for the current work attempt withdraws document
authority even while that attempt still reports running/confirmed. It cannot
register, read or submit through its retained original proof after cancellation.
