# Docs D0 reproduction harness

This opt-in prototype evaluates collaborative document writes for contributors
reviewing [#3159](https://github.com/crewship-ai/crewship/issues/3159). It is an
experiment, not released Docs or an accepted deployment architecture. It adds
no production route, migration, Next.js route, Page type or runtime dependency.
It is stacked on the private-content authorization foundation in
[#3099](https://github.com/crewship-ai/crewship/pull/3099); HTML, Board and their
release gates are unchanged. The existing [Pages contract](../../docs/specs/pages.md)
continues to own released behavior.

## Reproduce

Use Node 24, Go from the repository toolchain and pnpm 10.11.0. The prototype
has its own pinned dependency graph; it does not change the application lockfile.
Provision Playwright Chromium in the usual development cache first. On a shared
Crewship host all builds and tests must use `crewship-heavy`.

```sh
cd tools/docs-d0
crewship-heavy pnpm install --frozen-lockfile
crewship-heavy pnpm build
crewship-heavy pnpm test
cd ../..
crewship-heavy env CREWSHIP_DOCS_D0=1 \
  PLAYWRIGHT_BROWSERS_PATH="$HOME/.cache/ms-playwright" \
  go test -tags docs_d0 ./internal/api -run '^TestDocsD0$' -count=1 -timeout 10m
```

The Go test creates a migrated temporary SQLite fixture, synthetic accounts,
real DB sessions/JWTs, a real scoped agent-run capability and a preexisting Page.
It serves an owned `httptest` authority and spawns an ephemeral loopback Node
service plus two real Chromium contexts. It executes no model, Docker, email,
production API or dev-environment mutation. It is an integration fixture, not a
second installation; do not point it at a shared server. Disposable full Crewship
installations still require `scripts/throwaway-server.sh`.

`CREWSHIP_DOCS_D0_OUTPUT` optionally names an absolute **JSON file** in an existing
directory, with a sibling `.png` screenshot. Otherwise evidence goes to an OS
temporary directory. Fixtures containing credentials have mode 0600 and are
removed by Go. Evidence contains synthetic content and measurements, never tokens.
The explicit `docs_d0` tag requires all prerequisites and fails instead of
skipping. The repository's `conformance` tag also compiles it; compile-only runs
use `-run '^$'`. Normal CI does not execute this browser acceptance.

## Actual write path

1. Browser and headless Node share `schema.mjs`: Tiptap/ProseMirror schema, IDs,
   canonical hashes, Yjs binary encoding, validators and range anchors. The
   editor and server use the same pinned `@tiptap/y-tiptap` conversion engine.
   Supported content is heading level 1, paragraph, text and a simple table.
   Unsupported nodes, attributes, raw Yjs metadata and marks are rejected before
   normalization can discard them. Validation also inspects retained Yjs Items,
   including deleted nodes, removed attributes and removed formatting; `gc:false`
   history cannot hide unsupported payloads. Supported deleted text and old IDs
   remain available to comment anchors. This is not the issues Markdown roundtrip.
   The qualified binding is `@tiptap/y-tiptap` 3.0.7. Tested 3.0.10 structural
   operations added an auxiliary position-migration root outside this contract
   and were rejected; upgrading requires an explicit metadata/anchor contract,
   not allowing arbitrary opaque roots or disabling validation. Retained validation
   depends on the internal Item/content representation of pinned Yjs 13.6.33;
   any Yjs upgrade must review that representation and rerun these regressions.
2. The browser edits optimistically and combines its transactions for 35 ms.
   Its custom WebSocket provider sends a request ID, schema, epoch and binary
   update. It displays `Ukládám`; provider sync alone never displays `Uloženo`.
3. Node queues every human batch, agent patch, comment, checkpoint and restore
   on one document queue. It reads authorized durable state from Go, applies the
   update to an isolated Y.Doc and validates the resulting document. There is
   no shared speculative server document and no pre-commit peer broadcast.
4. Go takes a SQLite writer fence, rechecks the exact DB session or live run
   delegation, trusted membership and the existing Page ACL in the transaction,
   then checks schema, epoch, `ifRev`, target `ifHash` and idempotency. Snapshot,
   projection, receipt and actor audit are committed together in separate D0
   tables. Node has no SQL connection or filesystem DB access. Its internal
   loopback HTTP requests require a random service capability as well as the
   human/run bearer. A deployed service would need separately reviewed socket
   or authenticated network isolation; this fixture does not install one.
5. Only after Go's acknowledgement does Node publish to still-authorized peers
   and send the author's durable acknowledgement. Both receive checks and a
   150 ms idle authorization poll can close revoked sockets. The transaction is
   the write authority; the poll is not the write authorization boundary.

Agent operations are deliberately restricted to `replaceText` targeting a
paragraph/heading ID, including paragraphs in table cells. They require both
`ifRev` and the canonical target hash, enter the same queue behind already
accepted human updates and create a checkpoint. Agents cannot submit raw CRDT
updates or edit grants. This proves the headless semantic path, not a complete
agent tool/CLI surface. A repeated request with different input conflicts;
successful retries return the original receipt after reauthorization.

Restore creates a new audited revision and increments the document epoch. Node
restart also acquires a new epoch. Clients discard the old Y.Doc and pending
queue on reset; no automatic replay can cross the fence. The fallback loses
unacknowledged local work on reset and has no recovery-copy UX. Previously
acknowledged changes are loaded from Go after engine death.
Rejected private proposals reset and close only their sender. They cannot force
unrelated editors to discard their pending work. The UI retains the rejection
warning instead of reporting the refused proposal as saved.

## Why this transport

`hocuspocus.test.mjs` uses two real Hocuspocus 4.7.1 providers and a latched
`onStoreDocument`. A receives a live broadcast, is revoked before storage, and
the storage callback refuses its batch. B already contains that text. B's later
authorized update persists both texts because storage sees the merged document
and B's latest context. Closing A does not remove the update from B.

That counterexample rejects attributing a debounced snapshot to its latest
author. It does **not** prove every public-hook/epoch strategy impossible.
No speculative-broadcast implementation was qualified here. The bounded D0
alternative is **durable-before-broadcast with browser micro-batches**. It
retains Tiptap/Yjs and Go authority, replacing the Hocuspocus sync transport.
Hocuspocus is only a test dependency, not secretly used by the fallback.

## Evidence and limits

The executable acceptance covers two humans typing in the same block, a semantic
agent with no browser, stale revision/hash conflicts, queued-human barrier,
timeout retry, SIGKILL of Node before/after commit, revocation of an accepted
pending human batch, comments, checkpoint restore, incompatible schema,
document recovery from a real SQLite backup, raw-agent transport rejection and
representative-document latency/memory. Direct Go requests separately prove
that the transactional boundary rejects stale revision/hash/epoch, revoked
delegation/session and invalid service identity without changing D0 data.

The Hocuspocus counterexample supplies the already-broadcast revocation case;
the fallback proves that denied pending text never reaches another peer. These
are distinct tests, not a claim that the fallback safely retracts speculative
broadcasts.

Important unqualified boundaries:

- The service supports **one fixture Page**, one Node writer and two users.
  Queue/connection/payload bounds are implementation guardrails, not measured
  capacity. No multi-writer failover or managed deployment is delivered.
- The test uses WAL and explicitly `synchronous=FULL` on its single Go DB
  connection. Fault injection kills Node, not Go, the kernel or power. The
  authority remains alive; power-loss and Go-process death remain untested.
- Binary snapshot compaction retains tombstones with `gc:false`. Comment ranges
  become attached, collapsed or detached, preserving their quote and original
  revision. Physical tombstone GC, history retention and long-term memory bounds
  are unqualified. No production history retention policy is changed.
- Backup uses `VACUUM INTO` on the owned application fixture, then restores the
  document's saved binary/projection, threads and revision boundary, fences old
  writers and revokes the old agent delegation. It does not claim integration
  with the production backup bundle or a full workspace restore. D0 has no assets.
- The fixture borrows existing Artifact run delegation and Page ACL contracts
  rather than shipping a second ACL engine. A distinct Docs descriptor,
  authoring skill, CLI parity, pending jobs, common revision UI, compare/export,
  copy/delete/transfer and production backup need separate implementation.
- Go trusts the authenticated Node service to validate CRDT/schema and compute
  projections. The browser is untrusted; validating raw binary structure and
  unknown content before conversion is necessary. This is not isolation from
  a compromised service process.

Measurements report Go writer-fence/auth/SQL-commit duration, Node–Go commit
roundtrip and browser acknowledgement plus peer observation separately. The
representative document adds 200 paragraphs (about 58k visible characters) to
the heading/table fixture; twelve append batches are measured. RSS includes
the whole process, transient clones and retained tombstones without forced GC.
It cannot establish per-document capacity or production latency guarantees.

## Separate S0

After D0 review, compare two equal candidates using the same five-slide corpus:
restricted HTML + deck metadata and a typed element tree. Give each stable slide
and element IDs, the same manual edits, targeted agent changes, reorder,
comment anchors and conflict fixtures. Require a generated PPTX to open in
PowerPoint and LibreOffice with supported text/shapes still editable, including
Czech text, long content, a table, PNG, missing font and overflow. Record
normalization, ID stability, edit/export fidelity, implementation cost and
explicit rasterization reports. Do not choose the format in advance or expand
the original Artifact/Board release gates.
