# Mooring pilot editor SDK

Mooring's pilot connects a personal Artifact to an isolated editor and an agent.
It is an experimental protocol-1 integration, not an L3 collaboration claim or
public conformance certification. The host owns authorization, persistence and
receipts; the editor receives neither a Crewship credential nor an HTTP client.

The TypeScript SDK entrypoint is `lib/mooring/index.ts`. It has no React,
Crewship authentication or server dependencies. `channel.ts` supplies the common
opaque-frame handshake, protocol and package-pin checks, private MessagePort
transfer, bounded JSON messages and frame isolation attributes. GridCraft's
existing bridge uses that same channel implementation. Its cell reconciliation,
refusal decisions and legacy `gridcraft:*` wire adapter remain in
`lib/workbooks/bridge.ts`; those rules are application-specific and are not
replaced by a whole-document generic merge.

`createEditorSDK` provides the small non-Rust editor interface:

```ts
const editor = createEditorSDK({
  parentOrigin, bundle, environment,
  onState: state => renderConfirmed(state.snapshot),
  onStatus: (status, reason) => renderSaveStatus(status, reason),
  onReceipt: receipt => showReceipt(receipt),
})
editor.hello()
editor.draft(true) // typing is local and must not be called saved
editor.command("set", { text: "Hello" })
```

`environment` supplies the parent window, a window-message listener and the
initial hello sender. The SDK accepts the private port only from that exact
parent and configured origin with protocol 1. The host accepts a hello only
from its own opaque iframe after the first load, with the exact approved bundle
pin. Navigation or a second handshake ends the capability. All subsequent
content travels over the private port.

The host opens a document with `{rev, generation, snapshot}` and pushes later
confirmed states through the same port. A command includes a local sequence,
base revision, generation, command name and arguments. The host supplies the
mutation ID, authenticated identity and authorization. It resolves commands
against the application's declarative manifest and JSON Schema; editors cannot
select their own storage adapter or actor.

A command remains `saving` until its durable receipt and the corresponding
confirmed revision have both arrived. A delayed receipt or old snapshot never
rolls state backwards. The statuses are `connecting`, `draft`, `saving`, `saved`,
`offline` and `error`. A receipt cannot hide another local draft. A refusal
leaves the draft unsaved. The application must keep unsubmitted input separate
from confirmed state: rendering a server push must not replace a focused input.
The generic host retries uncertain writes with identical bytes and mutation ID;
server-side idempotency is required. It does not promise offline merge.

## Declarative application example

`examples/mooring/hello` is a small TypeScript editor outside the Craft engine.
It displays confirmed text separately from its editable draft, invokes the
manifest's `set` command and shows a receipt only after confirmation. The host
component `MooringEditorHost` has no branches for this application or its ID.
The same host mounts any registered application's approved editor URL and sends
its declared commands to the generic document API. Adding an application does
not add another host component, MCP tool, CLI command or application table.

The reference package is compiled reproducibly with the locked esbuild version:

```sh
crewship-heavy node scripts/prepare-mooring-editors.mjs
```

The helper prints the SHA-256 of the exact bundle manifest and writes the pinned
package into `internal/editororigin/packages/<digest>/`. The manifest pins every
HTML, CSS and JavaScript file. Generated assets are committed with their source
because Go embeds the reviewed package. A changed digest requires the manifest
registration to name the new reviewed pin; old published packages are immutable.
The existing isolated editor handler serves the package under
`/b/<digest>/index.html` with its normal CSP and sandbox restrictions.

## Verification and scope

`lib/mooring/editor-sdk.test.ts` and
`components/features/pages/__tests__/mooring-editor-host.test.tsx` are synthetic
SDK and host tests. They check foreign-window and version rejection, truthful
receipt states, ordering, unchanged retry bytes, draft retention, generic app
selection and frame removal on access loss. They do not run GridCraft. Existing
GridCraft bridge regressions run separately after the shared-channel extraction.
Deployment acceptance must exercise the actual editor package, authenticated
Artifacts UI, an agent command, receipt and reload.

Multi-person presence, PROPOSAL, shared-workbook DIRECT, per-participant undo,
XLSX fidelity, Rust SDK, public conformance and capacity qualification remain
outside this pilot. Rich GridCraft mutations remain blocked until their storage
and synchronization paths are supported. Customer-installation isolation policy
is a separate release decision; a development deployment is not its approval.
