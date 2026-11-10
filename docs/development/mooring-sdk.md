# Experimental Mooring SDK for Craft applications

**Status: experimental.** `lib/mooring/sdk` is a TypeScript surface over the
existing single-editor Craft paths (WordCraft/GridCraft whole-native
documents) and the artifact project's version history. No external
application uses it, no product code imports it, and there is no
compatibility promise: names, shapes and labels may change in any release
without a deprecation period. It wraps the existing host modules; it does not
replace or change them, so no existing path changes behavior.

The boundary this entry point keeps is part of the
[implementation profile inventory](../specs/mooring-profiles.md#the-experimental-sdk-entry-point).
The protocol-1 iframe layer stays documented in the
[pilot editor SDK](mooring-editor-sdk.md).

## Import

```ts
import { createLeaseActions, describeLease, recoveryOffers, saveLabel, createVersionHistory } from "@/lib/mooring/sdk"
```

Import it explicitly. The root `lib/mooring` is the in-frame editor SDK and
does **not** re-export this entry point; an isolated editor bundle imports
`lib/mooring` and never `lib/mooring/sdk`. `lib/mooring/sdk/editor` is the same
in-frame surface as the root, re-exported for convenience.

| Module | Purpose |
|---|---|
| `sdk/editor` | The root in-frame editor client (protocol 1), unchanged. |
| `sdk/lease` | The server's single-writer lease: parse the view, describe its controls, order observations, acquire, request hand-off, hand off, release. |
| `sdk/save` | Vocabulary for the host's save lifecycle: phases, labels, `SaveError`, types. |
| `sdk/recovery` | Compare/copy offers over a journal handle the host supplies. |
| `sdk/versions` | Version history over a transport the host supplies: `nativeCraftVersions` for native Word/Sheet, `artifactProjectVersions` for artifact projects. |

## What the SDK does not contain

The SDK does not export, and does not call, the host's save lifecycle factory
(`createSingleEditorSave`), the private draft journal opener
(`openNativeDraftJournal`) or the native admission client
(`createNativeDocumentClient`). The host constructs those for the signed-in
person and the admitted Page and hands them to the application: a save
factory, a journal handle and an authorized transport. A test
(`lib/mooring/sdk/__tests__/boundary.test.ts`) fails when the root entry
reaches a host module or the SDK, when the SDK exports one of those names, or
when an SDK file runtime-imports anything from a host module beyond
`validNativeVersion` and the `NativeSaveError` class.

## Who owns what

The Go backend is the only authority for rights, mandates, leases, versions,
quota and audit. CLI and MCP are clients of the same authorized interface. The
SDK carries requests over transports the host gives it and validates replies;
it never grants anything, and every advisory check (`ownsLease`,
`describeLease`) is repeated by the server on the actual request.

| The host (Crewship) owns | Your application owns |
|---|---|
| Authentication and the admitted Craft session (`createNativeDocumentClient`) | Rendering the document and the person's input |
| The lease transport and the lease event stream | Turning input into bytes (`engine.snapshot`) and validating them (`engine.validate`) |
| The save lifecycle factory (`createSingleEditorSave`) | Showing the lease label, save status and recovery offers |
| The private draft journal (`openNativeDraftJournal`), purge on read loss | Deciding *when* to call `save`, `flush`, `reconcile` |
| Receipts, versions, quota and audit | |

## Lifecycle

1. **Read and observe the lease.** `parseLease` validates the server view
   with the native host's rules; `createLeaseState().observe(view)` ignores a
   view at an older generation than one already seen.
2. **Show who edits.** `describeLease(view, mayEdit)` returns the label
   (`Upravuje Alice` or `Volné`) and the available actions. The parity test
   compares the actions with the native lease bar, which uses English labels.
3. **Acquire.** `createLeaseActions(transport, signal).acquire()`. Writing is
   only attempted while `ownsLease(view, generation)` holds; the server
   decides.
4. **Edit and save.** Ask the host's save factory for one lifecycle per
   editing lease, from the confirmed server version, with its **own** journal
   handle. Never share a journal between lifecycles: a later lifecycle would
   overwrite, then confirm away, a draft retained from an earlier lost custody.
   A lifecycle that revoked writing (phase `read-only`, `stopped` or `revoked`) is never
   reused; make the input read-only and start a fresh lifecycle if the server
   still reports your lease. Freeze input before any await that ends a
   lifecycle (restore, hand-off). Call `edited()` on input; the lifecycle
   autosaves after idle, journals every capture before sending it, and reports
   the phases in `SAVE_PHASES`: `idle`, `dirty`, `saving`, `saved`, `draft`,
   `uncertain`, `read-only`, `stopped`, `revoked`. `saveLabel(state)` gives the default
   Czech labels.
5. **Read revocation.** The host reports terminal `revoked` and requests a
   best-effort journal purge. Clear the document, recovery offers and copied
   text from the view. A transport refusal with `SaveError` reason
   `read_revoked` enters the same handler, including after the lifecycle has
   closed. Save and flush refuse after revocation; reopen only after fresh
   admission and a successful read. Closing or signing out reports `stopped`.
6. **Hand off or release.** `handOff(view, requestId, custody)` /
   `release(view, custody)` confirm the final save, revoke local writing and
   capture a durable draft, ask the server, then prove the old generation is
   gone. A failed step stops before the next; the draft stays local.
7. **Recover.** `recoveryOffers(journal)` lists drafts other mounts left
   behind as `{ draft, actions: ["compare", "copy"], compare(server), copy() }`.
   There is no save action; dismissing an offer deletes nothing.
8. **Versions.** `createVersionHistory(transport).list()` is newest first with
   exactly one `current`; `truncated` says the server returned only the newest
   part. `restore(target, { base, generation })` must come back as `base + 1`.
   Restore never rewinds history. `nativeCraftVersions(client, binding)` uses
   the host client's `versions`/`restore`, whose replies
   `lib/mooring/native-versions.ts` already validates; the listing is
   `versions_unavailable` while the workspace flag `craft_version_history` is
   off.

## What the contract tests cover

Each item has a test under `lib/mooring/sdk/__tests__/` or in
`examples/mooring/notecraft/notecraft.test.ts`. They run against the real
host save lifecycle with an in-memory transport and journal, not against the
Go server or a browser IndexedDB.

- A delayed receipt or an old snapshot does not move confirmed state
  backwards; a late lease view at an older generation does not restore an old
  holder.
- Input typed while a save is in flight keeps the phase `dirty` after that
  save's receipt.
- A lost reply is `uncertain`; `save()` does not resend it and `reconcile()`
  proves it with byte-identical content.
- A 409 leaves the phase `draft` with its reason, keeps the journal row and
  stops sending.
- Every write carries the lease generation and the base version; a receipt
  for another generation is unconfirmed; a release or hand-off at a stale
  generation is refused before any request.
- After custody moves, input stays local; recovered drafts have no save
  action.

## Example

```ts
import { createSingleEditorSave } from "@/lib/mooring/single-editor-save"   // host side
import { createLeaseActions, createLeaseState, describeLease, saveLabel } from "@/lib/mooring/sdk"

const lease = createLeaseState(), actions = createLeaseActions(client, signal)
let view = lease.observe(await client.lease(signal))!
showLeaseBar(describeLease(view, mayEdit))            // "Upravuje Alice" · buttons
view = lease.observe(await actions.acquire())!
const doc = await client.read(signal)
const save = createSingleEditorSave({                  // built by the host
  workspaceId, pageId, applicationId, enginePin, sessionId,
  generation: view.generation!, baseVersion: doc.version, loaded: { bytes: doc.bytes },
  journal, transport: client,
  engine: { snapshot: async () => ({ bytes: encode(text()), editorRevision: `note:${edits}` }), validate: async () => {} },
  onState: state => showStatus(saveLabel(state)),
})
input.oninput = () => { edits++; save.edited() }
await actions.handOff(view, view.handoff_requested_by[0].request_id, {
  generation: view.generation!, flush: () => save.flush(),
  confirmedVersion: () => save.confirmedVersion(), revokeWrite: () => save.revokeWrite(),
})
```

## Example application: NoteCraft

`examples/mooring/notecraft/notecraft.ts` is a plain-text Craft application
that imports only `lib/mooring/sdk`. The host passes it the save factory, a
journal opener and the transports. It shows *Upravuje X*, request/hand-off and
release, a save status with *Ověřit výsledek* for an uncertain save, local
recovery offers with *Porovnat* and *Kopírovat*, and version history with
restore.

NoteCraft is **an example, not part of the product.** It is not imported by
any product code, not bundled into the UI, not packaged into
`internal/editororigin/packages/` and has no server-side application ID or
manifest pin; registering a Craft application is security-sensitive server
work this change does not do. It runs only against `fake-server.ts`, an
in-memory double that applies the server's lease-holder, generation and
base-version fences, in tests and in a local demo. It fences custody by actor
only, not by editor session or lease expiry, so the tests say nothing about
two tabs of the same person.

NoteCraft tries not to close or overwrite typed text before it is saved or
journaled. `close()` sends the final save, or journals the text when the save
is not confirmed. It resolves `durable`, `local-copy` when neither succeeded
(the view then stays usable so the copy can still be stored or discarded), or
`revoked` when the view ended without NoteCraft closing it. When custody is
lost (or a restore finishes) and no journal can persist the unsaved text,
NoteCraft keeps the note editable as a local copy instead of loading the
server version over it; *Znovu uchovat koncept* retries the journal and
*Zahodit místní kopii* discards it. A read revocation is handled in one
place, `terminate`, reached from a transport refusal or terminal host state.
It clears the note, offers and copied text. A live controller can purge
through its admitted journal. A refusal from an older request cannot
retire a view after it has confirmed a newer read, and never opens a fresh
journal or purges drafts from a newer admission. A capture that finishes after read
loss can remove only its own exact row. A sign-out retirement (`stopped` with no read
refusal seen) also removes the text but keeps the journal rows.

```sh
crewship-heavy pnpm exec vitest run examples/mooring/notecraft lib/mooring/sdk
# Local demo, two people on one in-memory server:
crewship-heavy pnpm exec esbuild examples/mooring/notecraft/demo.ts --bundle --format=esm \
  --outfile=/mnt/dev-workspace/tmp/builds/notecraft/demo.js
cp examples/mooring/notecraft/index.html /mnt/dev-workspace/tmp/builds/notecraft/
# then serve that directory with any static file server and open index.html
```

## Known gaps

- **Experimental, unused.** Nothing in the product imports the SDK; the
  native Craft host keeps its own lease parsing and lease bar.
  `describeLease` does not model the lease bar's "another tab of yours"
  label (`self`), and the parity test covers only the listed cases.
- **Lease ordering is by generation only.** The lease view has no sequence
  number, so a delayed vacant view, or views reordered within one generation,
  apply in arrival order. Writes are still fenced by the server.
- **No real server or browser storage in the tests.** The contract tests use
  in-memory doubles; they do not exercise the Go routes, IndexedDB, a real
  editor iframe or the native host.
- Multi-person editing, proposals UI and C1/Yrs are outside this SDK. C1/Yrs
  are frozen.
