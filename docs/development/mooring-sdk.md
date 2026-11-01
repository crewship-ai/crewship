# Mooring SDK v1 — building a Craft application

**Purpose.** The single entry point for anyone writing a Craft application or
an artifact type on Mooring. It explains the lifecycle, what is guaranteed,
what the host owns and what your application owns, and shows a complete
example. The protocol-1 iframe details stay in the
[pilot editor SDK](mooring-editor-sdk.md).

**Status.** v1 is a TypeScript surface over the existing, working paths
(WordCraft/GridCraft single-editor Craft and the artifact project). It wraps
those modules; it does not replace or change them. Nothing in the product
imports it yet, so no existing path changes behavior.

## Import

```ts
import { createLeaseActions, createSingleEditorSave, recoveryOffers, createVersionHistory } from "@/lib/mooring/sdk"
```

`lib/mooring/index.ts` re-exports the same surface and is therefore host-side:
it pulls in the private draft journal and the admitted transport. Code that runs inside the
isolated editor iframe imports only `lib/mooring/sdk/editor`, which has no
Crewship authentication, HTTP, storage or UI dependencies (a test enforces this).

| Module | Purpose |
|---|---|
| `sdk/editor` | In-frame editor client (protocol 1): handshake, private port, confirmed state, statuses. |
| `sdk/lease` | The server's single-writer lease: parse the view, describe its controls, order observations, acquire, request hand-off, hand off, release. |
| `sdk/save` | The single-editor save lifecycle: phases, receipts, idempotent reconcile, 409 conflict, labels. |
| `sdk/recovery` | Local recovery drafts after a crash, reload or lost lease: compare and copy only. |
| `sdk/versions` | Version history; restore creates a new version. Includes an adapter for the artifact project. |

## Who owns what

The Go backend is the only authority for rights, mandates, leases, versions,
quota and audit. CLI and MCP are clients of the same authorized interface. The
SDK carries requests and validates replies; it never grants anything.

| The host (Crewship) owns | Your application owns |
|---|---|
| Authentication and the admitted Craft session (`createNativeDocumentClient`) | Rendering the document and the person's input |
| The lease transport and the lease event stream | Turning input into bytes (`engine.snapshot`) and validating them (`engine.validate`) |
| The private draft journal (`openRecoveryJournal`), purge on read loss | Showing the lease label, save status and recovery offers |
| Receipts, versions, quota and audit | Deciding *when* to call `save`, `flush`, `reconcile` |

## Lifecycle

1. **Read and observe the lease.** `parseLease` validates the server view;
   `createLeaseState().observe(view)` ignores a view at an older generation than
   one already seen, so the holder shown never rolls back.
2. **Show who edits.** `describeLease(view, mayEdit)` returns the label
   (`Upravuje Alice` or `Volné`) and which actions are available — exactly the
   affordances the native lease bar renders (a test keeps them in parity).
3. **Acquire.** `createLeaseActions(transport, signal).acquire()`. Writing is
   allowed only while `ownsLease(view, generation)` holds.
4. **Edit and save.** Create one `createSingleEditorSave` per editing
   lifecycle, from the confirmed server version, with its **own** journal from
   `openRecoveryJournal` (one row per journal). Never share a journal between
   lifecycles: a later lifecycle would overwrite, then confirm away, a draft
   retained from an earlier lost custody. A lifecycle that revoked writing
   (phase `read-only`, `stopped` or `revoked`) is never reused; make the input read-only
   and start a fresh lifecycle if the server still reports your lease. Freeze
   input before any await that ends a lifecycle (restore, hand-off). Call `edited()` on input; the lifecycle
   autosaves after idle, journals every capture before sending it, and reports
   phases: `idle`, `dirty`, `saving`, `saved`, `draft`, `uncertain`,
   `read-only`, `stopped`, `revoked`. `saveLabel(state)` gives the default Czech labels.
   `revoked` (reason `read_revoked`) is terminal and emitted exactly once,
   whichever call learned of the read revocation (save, reconcile, the lost-reply
   proof inside `revokeWrite`, or the host's `revokeRead()`): the lifecycle stops
   and purges its journal. Afterwards `revokeWrite()` resolves `false` and
   `flush()`/`save()` reject with `SaveError("read_revoked")`. Remove the text
   from the view on this state; never keep a local copy.
5. **Hand off or release.** `handOff(view, requestId, custody)` /
   `release(view, custody)` confirm the final save, revoke local writing and
   capture a durable draft, ask the server, then prove the old generation is
   gone. Any failed step stops before the next; the draft stays local.
6. **Recover.** `recoveryOffers(journal)` lists drafts other mounts left
   behind as `{ draft, actions: ["compare", "copy"], compare(server), copy() }`.
7. **Versions.** `createVersionHistory(transport).list()` is newest first with
   exactly one `current`; `restore(target, { base, generation })` must come back
   as `base + 1`. Restore never rewinds history.

## Guarantees (each has a contract test)

- **Statuses never roll back.** A delayed receipt or an old snapshot never
  moves confirmed state backwards; a late lease view never restores an old
  holder.
- **A late receipt does not hide a draft.** Input typed while a save is in
  flight keeps the phase `dirty` after that save's receipt.
- **Idempotent retry.** A lost reply is `uncertain`. `save()` does not resend
  it; `reconcile()` proves it with byte-identical content.
- **409 conflict keeps the draft.** A refused write leaves the phase `draft`
  with its reason, keeps the journal row, and stops sending.
- **A stale generation is refused.** Every write carries the lease generation
  and the base version; a receipt for another generation is unconfirmed; a
  release or hand-off at a stale generation is refused before any request.
- **A draft is never auto-saved after hand-off.** After custody moves, input
  stays local; recovered drafts have no save action at all.

Tests: `lib/mooring/sdk/__tests__/{editor,lease,save,recovery,versions}.test.*`
and `examples/mooring/notecraft/notecraft.test.ts`.

## Example

```ts
const lease = createLeaseState(), actions = createLeaseActions(client, signal)
let view = lease.observe(await client.lease(signal))!
showLeaseBar(describeLease(view, mayEdit))            // "Upravuje Alice" · buttons
view = lease.observe(await actions.acquire())!
const doc = await client.read(signal)
const save = createSingleEditorSave({
  workspaceId, pageId, applicationId: "notecraft", enginePin, sessionId,
  generation: view.generation!, baseVersion: doc.version, loaded: { bytes: doc.bytes },
  journal, transport: client,
  engine: { snapshot: async () => ({ bytes: encode(text()), editorRevision: `note:${edits}` }), validate: async () => {} },
  onState: state => showStatus(saveLabel(state)),
})
input.oninput = () => { edits++; save.edited() }
// Hand-off to a person who asked:
await actions.handOff(view, view.handoff_requested_by[0].request_id, {
  generation: view.generation!, flush: () => save.flush(),
  confirmedVersion: () => save.confirmedVersion(), revokeWrite: () => save.revokeWrite(),
})
```

## Reference application: NoteCraft

`examples/mooring/notecraft/notecraft.ts` is a plain-text Craft application
that imports only `lib/mooring/sdk`. It shows *Upravuje X*, request/hand-off
and release, a truthful save status with *Ověřit výsledek* for an uncertain
save, local recovery offers with *Porovnat* and *Kopírovat*, and version
history with restore.

No typed text is closed or overwritten before it is durable. `close()` sends
the final save, or journals the text when the save is not confirmed. It
resolves `durable`, or `local-copy` when neither succeeded (the view then stays
usable so the copy can still be stored or discarded), or `revoked`. When custody is lost (or a restore
finishes) and no journal can persist the unsaved text, NoteCraft does not load
the server version over it: the note stays editable as a local copy, the
status shows *Koncept zůstává v tomto editoru* and the alert says the text
exists only in this window. *Znovu uchovat koncept* retries the journal;
*Zahodit místní kopii* discards it deliberately. A read revocation is the
exception on every path (close, custody loss, restore, transfer). NoteCraft
handles it in one place, `terminate`, reached from the SDK's `revoked` state
and from its own guarded document, lease and version calls (`run()` never
swallows `read_revoked`). It purges once more through a fresh handle, removes
the text from the note, the offers and memory, and never makes a local copy.
`close()` then resolves `revoked`.

```sh
crewship-heavy pnpm exec vitest run examples/mooring/notecraft lib/mooring/sdk
# Local demo, two people on one in-memory server:
crewship-heavy pnpm exec esbuild examples/mooring/notecraft/demo.ts --bundle --format=esm \
  --outfile=/mnt/dev-workspace/tmp/builds/notecraft/demo.js
cp examples/mooring/notecraft/index.html /mnt/dev-workspace/tmp/builds/notecraft/
# then serve that directory with any static file server and open index.html
```

NoteCraft is **not packaged** into `internal/editororigin/packages/`. The
pinned packaging serves an isolated iframe editor, and registering a new
application requires a server-side manifest pin and a Craft application ID,
both of which are security-sensitive server registration. NoteCraft is a
host-side application exercised against `fake-server.ts`, an in-memory double
that applies the same lease, generation and base-version fences as the server.

## Known gaps (v1)

- **Craft version listing.** The server restores a native Craft version
  (`POST …/craft/document/restore`, the `restore` action of `crewship craft connect`) but has no
  native version-list route, and the host client has no `restore` method yet.
  `VersionsTransport` is the contract; the artifact project adapter
  (`artifactProjectVersions`) is the only built-in transport.
- **Lease ordering is by generation only.** The lease view has no sequence
  number, so a delayed vacant view, or views reordered within one generation,
  apply in arrival order. Writes are still fenced by the server.
- **Lease parsing is duplicated.** The native host keeps a private copy of the
  lease validation; `parseLease`/`ownsLease` apply the same rules. The host can
  switch to the SDK once the open host PRs land.
- Multi-person editing, proposals UI and C1/Yrs are outside this SDK. C1/Yrs
  are frozen.
