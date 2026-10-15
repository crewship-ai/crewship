# Docs in Pages

Status: D1a persistence and D1b Pages integration are proposed, disabled by
`documents.enabled: false` by default. They are separate from the static
Artifacts 1.0 release. Review readiness and runtime qualification do not activate
Docs on a deployment. [Pages](pages.md) owns identity, ownership and grants;
HTML artifacts and Boards keep their storage and routes.

## Service and activation

Configure distinct clean absolute `authority_socket`, `service_socket` and
`service_token_file` paths of at most 100 bytes. The secret must be a regular
mode-0600 file, at most 512 bytes, holding 32–256 trimmed bytes. Go and Node use
matching numeric UIDs. Environment equivalents are
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
recovery can fence writers; schema 2 remains unchanged. Private agent state carries
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
- `POST /initialize` accepts `{id,schema:2,body}` on an empty human-owned Page.
- `POST /patch` accepts `{id,schema:2,epoch,kind:"human",update}` for a bounded
  binary update, or `{id,schema:2,epoch,target,text,ifRev,ifHash}` for semantic
  text replacement. The unchanged prefix and suffix retain character identities.
  Bound requests compute the changed interval within their original selected
  range, including repeated text. Original operation IDs/payloads identify exact retries.
- `GET /comments`; `POST /comments` accepts
  `{id,schema:2,epoch,ifRev,block,from,to,text}`. Offsets are aggregate UTF-16 positions within one paragraph or heading,
  including multiple CRDT text children. New anchors retain at most 64 relative
  per-child spans and fit the existing 8192-byte durable anchor limit. Existing
  one-child anchors remain readable; an older reader detaches a new cross-child
  anchor rather than silently attaching it elsewhere.
  `POST /comments/resolve` accepts
  `{id,schema:2,epoch,ifRev,comment,version,resolved}`. Read/comment/edit rights
  remain distinct. Anchors report attached, collapsed or detached positions;
  resolution is retained separately.
- `POST /checkpoint` accepts `{id,schema:2,epoch,ifRev}`;
  `GET /checkpoints` lists retained states; `GET /checkpoints/{checkpoint}`
  returns validated content for comparison.
- `POST /restore` accepts `{id,schema:2,epoch,ifRev,checkpoint}` and creates a
  new durable revision and resynchronization generation.
- `GET /export` downloads escaped Markdown, including simple lists and tables.
- `GET /requests` lists requests and eligible private agent chats;
  `POST /requests` accepts `{page_id,targets,chat_id,instruction,base_rev,submission_id}`,
  with one server-verified paragraph/heading target `{id,hash,from,to,quote}`.

`GET /api/v1/documents/status` reports whether the bridge is configured.
Capability hints never grant authority. Go rechecks live membership, exact
session or CLI token/scopes, Page ACL and run delegation under the SQL fence.
Scoped CLI access uses `pages:read` and `pages:write`. Sidecar document reads,
semantic patches and checkpoints additionally require the original admitted run
bearer; raw-agent updates and agent restores are denied.

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
Existing schema-1 content can be read and upgraded on a validated commit;
unsupported future schemas fail closed. JSON/Markdown are projections, never
Docs persistence. Yjs garbage collection remains disabled.

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
