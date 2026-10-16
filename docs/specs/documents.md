# Docs in Pages

Status: D1a persistence and D1b Pages integration are proposed, disabled by
`documents.enabled: false` by default. They are separate from the static
Artifacts 1.0 release. Review readiness and runtime qualification do not activate
Docs on a deployment. [Pages](pages.md) owns identity, ownership and grants;
HTML artifacts and Boards keep their storage and routes.

## Create a document from chat

When Docs are enabled, a human can choose **Create document** before sending a
message in a trusted agent chat. One Send records the original human, workspace,
chat, message and submission identity. Ordinary message metadata, replayed
transcripts and the existing HTML authorization cannot grant this capability.
The trusted transcript append syncs JSONL before its SQL mirror/consent transaction;
a later mirror failure preserves the message but reports that the run did not
start. Ordinary transcript replay never reconstructs the missing consent.

The live bound run can call `doc_create` or protected
`crewship doc create --file draft.json`. The original run bearer and the fixed
sidecar destination are required; human profiles, server overrides and fallback
credentials are unavailable. The input contains `slug`, `name`, `mutation_id` and
readable `blocks`: paragraph, heading(level1..3), bulletList, orderedList or
codeBlock. It permits at most128 input blocks,512 generated blocks,256KiB total
UTF8 text and64KiB per text/list item, within a1MiB request. Images, raw Yjs,
caller-provided IDs/hashes and arbitrary rich node JSON are rejected.

Private Node preparation validates schema3 and generates block identities before
Go opens the short writer transaction. Current authority is rechecked there;
Page ownership, initial document revision0, creation provenance, receipt and
single-use allowance commit together. Node/SQL failure publishes no empty Page.
Retries must reuse the exact original payload and mutation ID: an authorized
exact retry returns the same durable result before materializing again; a changed
payload conflicts. The saved result includes PageID, workspace, current address,
initial revision and a stable workspace/Page link. The chat card reads durable
provenance under current chat and Page access, independent of model reply text.

This capability creates one human-owned private document. It adds no agent Page
grant or run write delegation; subsequent edits require separate human authority.
Creation provenance is backed up and its opaque run identity is remapped on fork;
nonportable consent/run authority is excluded. Physical instance recovery also
clears creation consent and its bound run authority while retaining provenance.
This is an initial creation flow,
not structural editing, arbitrary revision reads or model-runtime qualification.

## Service and activation

Configure distinct clean absolute `authority_socket`, `service_socket` and
`service_token_file` paths of at most 100 bytes. The secret must be a regular
mode-0600 file, at most 512 bytes, holding 32–256 trimmed bytes. Go and Node use
matching numeric UIDs. They open secret files once with Unix no-follow/nonblocking
flags, validate that descriptor and read at most 513 bytes through it, rejecting
growth beyond the limit. The packaged healthcheck uses the same Node helper.
Environment equivalents are
`CREWSHIP_DOCUMENTS_ENABLED`, `CREWSHIP_DOCUMENTS_AUTHORITY_SOCKET`,
`CREWSHIP_DOCUMENTS_SERVICE_SOCKET`, `CREWSHIP_DOCUMENTS_SERVICE_TOKEN_FILE`.
A configured authority startup failure prevents Go serving. Node has no DB
mount or public listener. The [service package](../../tools/docs-service/README.md)
provides frozen dependencies, readiness/drain, a Dockerfile and an optional
profile requiring an explicitly qualified immutable image. Shared-host
installation remains an Ansible delivery.

Private protocol 3 at `/internal/docs/v1/` requires service authentication and,
for document operations, the original human or live-run bearer. These routes
never mount on the public Router. The engine boot barrier is service-only and
runs after exclusive ownership of its configured Unix socket. Its bounded boot
identity makes retries idempotent. Go and Node must agree on protocol 3 before
recovery can fence writers. The matched pair additionally requires explicit latest content capability3; missing/older/newer capability fails before recovery. Private agent state carries
the immutable selected request scope and commits include the actual bounded
UTF-16 edit interval. This metadata is excluded from public document reads.
Until Go acknowledges recovery, Node rejects
document admission and reports not ready. Operators must prove a crashed process
has exited before removing its stale socket; Node's native close cannot protect
against pathname replacement by another directory writer.

## Pages API and client lifecycle

Authenticated humans use `/api/v1/pages/{slug}/document` in their workspace:

- `GET` returns schema, durable `rev`, opaque `epoch`, binary baseline/replay,
  JSON content, block outline with IDs/hashes, and current capability hints.
- `POST /initialize` accepts `{id,schema,page_id,body}` on an empty human-owned Page.
- `POST /patch` accepts `{id,schema,page_id,epoch,kind:"human",update}` for a bounded
  binary update, or `{id,schema,page_id,epoch,target,text,ifRev,ifHash}` for semantic
  text replacement. The unchanged prefix and suffix retain character identities.
  Bound requests compute the changed interval within their original selected
  range, including repeated text. Original operation IDs/payloads identify exact retries.
- `GET /comments`; `POST /comments` accepts
  `{id,schema,page_id,epoch,ifRev,block,from,to,text}`. Offsets are aggregate UTF-16 positions within one paragraph or heading,
  including multiple CRDT text children. New anchors retain at most 64 relative
  per-child spans and fit the existing 8192-byte durable anchor limit. Existing
  one-child anchors remain readable; an older reader detaches a new cross-child
  anchor rather than silently attaching it elsewhere.
  `POST /comments/resolve` accepts
  `{id,schema,page_id,epoch,ifRev,comment,version,resolved}`. Read/comment/edit rights
  remain distinct. Anchors report attached, collapsed or detached positions;
  resolution is retained separately.
- `POST /checkpoint` accepts `{id,schema,page_id,epoch,ifRev}`;
  `GET /checkpoints` lists retained states; `GET /checkpoints/{checkpoint}`
  returns validated content for comparison.
- `POST /restore` accepts `{id,schema,page_id,epoch,ifRev,checkpoint}` and creates a
  new durable revision and resynchronization generation.
- `GET /export` downloads escaped Markdown, including simple lists and tables. Unrepresentable inline line breaks, table code blocks and odd backslash runs before table-code pipes return actionable422 instead of changing content.
- `GET /requests` lists requests and eligible private agent chats;
  `POST /requests` accepts `{page_id,targets,chat_id,instruction,base_rev,submission_id}`,
  with one server-verified paragraph/heading target `{id,hash,from,to,quote}`.

`GET /api/v1/documents/status` reports whether the bridge is configured.
Capability hints never grant authority. Go rechecks live membership, exact
session or CLI token/scopes, Page ACL and run delegation under the SQL fence.
Scoped CLI access uses `pages:read` and `pages:write`. Sidecar document reads,
semantic patches and checkpoints additionally require the original admitted run
bearer. The host compares the original authenticated sidecar workspace and crew with the run principal before dispatch; a valid run capability cannot replace that transport identity. Raw-agent updates and agent restores are denied.

The first editor uses bounded polling of authorized durable state, rather than
speculative peer broadcast. Local Yjs updates are coalesced; once attempted a
batch and its ID remain immutable across uncertain retries. "Saved" follows Go
acknowledgement only. Disconnection, rejection, revocation and generation change
are explicit states. A changed generation freezes the old editor and offers a
recovery copy; old pending binary is never automatically replayed after restore.
Unacknowledged edits are memory-resident; leaving warns, and a browser crash can
lose them. This does not claim an offline durable draft store.

## Writer fencing and schema

One managed Node engine serializes operations per workspace/Page. It retains a
random fenced writer lease across operations, renews with each operation's live
principal, and releases on idle/drain. The browser resynchronization epoch is
opaque: Go process identity, Node boot identity and durable generation. Writer
lease acquisition alone does not invalidate other editors. Fresh process
identities prevent physical-backup rollback from reusing a prior editor epoch.
Commits, comments, checkpoints and restores check generation/revision inside Go's
transaction; semantic writes additionally check target hashes. Mutations commit
before peers can observe them.

Schema 2 adds bounded lists and heading levels 1–3 to paragraphs and simple
tables. Every block has a stable ID. Node validates visible and retained Yjs
structures, including tombstones, and rejects unknown metadata or unresolved
CRDT dependencies. Tiptap 3.32.0, Yjs 13.6.33 and `@tiptap/y-tiptap` 3.0.7 are pinned.
Human CLI schema upgrade uses `crewship doc upgrade <slug> --file request.json`
(or `--file -` for stdin). It submits the original Page/schema/target schema,
revision, epoch and operation ID unchanged; conflicts do not trigger a fresh
read or automatic retry.

Existing schema1 content remains readable and keeps its legacy schema2-client commit compatibility. Explicit human CAS upgrade to3 binds Page ID/revision/epoch and preserves the original binary, while rotating generation. Schema3 adds closed HTTP(S) link/code marks and plain code blocks. Images are not part of3; future4 requires its own immutable logical-reference/pin contract. Unchanged text identities/marks survive semantic edits, inserted text is plain, and Go verifies full-tree subtree hashes/preorder against stored state and the original immutable selection. JSON comparison alone coalesces adjacent equal-mark text spans; binary/hashes are never normalized. Each private before/after proof is bounded500000UTF8 bytes within the existing3MiB HTTP envelope;

unsupported future schemas fail closed. JSON/Markdown are projections, never
Docs persistence. Yjs garbage collection remains disabled.

## Current text-block reads

`GET /api/v1/pages/{slug}/document?block_id=ID` returns only the current paragraph,
heading or code block as `{page_id,schema,rev,generation,epoch,block}`. The block
contains its stable `id`, `type`, nullable `parent`, canonical `hash` and complete
plaintext `text`. It excludes Yjs bytes and unrelated blocks. Human CLI:
`crewship doc read report --block stable-id`. This is current state, not
`sinceRev`, `atRev`, quote disambiguation, cell selection or structural editing.

The original caller is forwarded to the existing managed reader. Before returning
any selected outcome, the server rechecks exact credentials/live Page authority
and the entire persisted source in one SQL snapshot. A concurrent source change
returns 409; missing blocks return 404, nontext targets or text over 256 KiB UTF8
return 422 without truncation. The managed outline uses complete plaintext.
Materialization still uses the existing full read envelope (snapshot, replay,
content and outline), capped at 4 MiB; even a small selected block in a large/deep
source can therefore return an explicit bounded-read502. A projection-only
private transport is not implemented by this cut.

Only a singleton valid `block_id` is accepted on the current GET. Other operations,
empty/duplicate IDs and unknown selector query combinations return 400. The
optional singleton `workspace_id` must equal the authenticated context. Sidecar
forwards only the validated block selector. Its existing allowed patch/checkpoint
POSTs may carry the CLI's matching workspace context, which is checked and
stripped. No arbitrary query passthrough or new agent delegation is introduced.
Existing generic and selected run reads retain their current Page authority;
this does not add a protected agent-runtime CLI credential loader.

## History, requests and recovery

`documents` stores baseline and projection; `document_updates` stores ordered
incremental replay; `document_receipts` stores audit/retry identities;
`document_checkpoints` pins explicit states. These remain separate from HTML
revision tables. Every 32 updates or 64 KiB the baseline advances atomically and
only covered replay updates are removed. Receipts/checkpoints are not pruned.
Default bounds are 128 documents per instance, 1 MiB snapshot+projection,
256 KiB replay, 10,000 receipts and 16 MiB checkpoint data per document. Node
admits 32 document queues, 32 operations per key and 128 globally. Quota refusal
preserves confirmed data. Physical GC and arbitrary keystroke history are not
implemented.

`document_comments` retains relative anchors and discussion.
`document_requests` retains the human instruction, base revision, trusted
admission and result identity. A real request binds the existing trusted human
chat admission to a live run. Completion requires an agent-authored durable
patch and that run's exact checkpoint; model text alone cannot mark success.
Admission is bounded to three active, 30/hour and 2,000 total requests per Page.
Backup preserves historical results and comments but clears runnable request
capabilities. Workspace/fork and physical recovery fence writers; old credentials
never become newly authorized by restoring content.

Integration evidence must identify its exact source. D0 browser proof is scoped
to its prototype; D1b browser and genuine model-run acceptance are separate.
A held SQL writer or delayed fixture acknowledgement is not a slow-disk or
power-loss test. Multi-replica operation, production capacity, offline recovery,
images and richer formats remain outside this slice.

### Operational admission and retry retention

Receipts remain durable for the lifetime of their Page. The authority never
prunes them to admit new edits: a human update does not carry an original
revision, so deleting its receipt could execute an old request again. Exact
retries require current authority and the original actor/payload identity, return
the original result, and do not refresh the receipt timestamp or consume another
operation. Restart, checkpoint restore and admission-window expiry do not expire
this identity. Full access/session revocation still denies receipt access.

New edits, checkpoints, restores, upgrades and comment mutations share a rolling
server-time window: by default 10,000 receipts in 600 seconds per Page. Exceeding
it returns temporary HTTP 429 `document_rate_limit`; retry the original request
after the window clears. Byte/log/history limits remain separate permanent
`document_quota` failures. Receipts and retained history still grow; this change
is not receipt compaction or unlimited document storage.

The default capacity is 128 Docs **per workspace**, independent of other
workspaces. YAML `documents.max_documents_per_workspace`,
`documents.max_operations_per_window` and `documents.operation_window_seconds`
configure these limits. Matching environment variables are
`CREWSHIP_DOCUMENTS_MAX_DOCUMENTS_PER_WORKSPACE`,
`CREWSHIP_DOCUMENTS_MAX_OPERATIONS_PER_WINDOW` and
`CREWSHIP_DOCUMENTS_OPERATION_WINDOW_SECONDS`. Zero selects defaults, negative values are invalid, and
the configured window cannot exceed 86,400 seconds. No deployment configuration
is changed automatically.

The managed engine retains at most 32 scheduler tails, evicting the least
recently used idle tail when needed. Busy keys remain serialized and cannot be
evicted. The per-key 32 and global 128 queued-operation bounds remain active;
32 busy keys can still temporarily reject a new key. Scheduler eviction does
not discard durable content or release another caller's writer lease.
