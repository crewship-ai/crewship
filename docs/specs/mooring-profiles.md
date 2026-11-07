# Mooring implementation profiles and SDK boundaries

Implementation reference for application authors and host integrators, dated
2026-11-04. This inventory describes the clients and storage paths in this
repository. It does not certify a deployment, complete editor acceptance or a
common conformance suite. The names in the table are descriptive labels, not
new negotiated wire-profile identifiers.

Mooring has shared identity and authority boundaries, with several existing
application profiles. Their transports, versions, conflicts and collaboration
semantics differ. Choose the existing path that owns the content; a shared
MessagePort helper or a Yjs payload does not make two profiles interchangeable.

## Existing clients and profiles

| Profile | Existing client surface | Content and concurrency | Confirmation and recovery |
|---|---|---|---|
| Pilot JSON application | `lib/mooring/index.ts`, `createEditorSDK`; `MooringEditorHost` | Protocol-1 isolated editor, manifest-declared JSON commands, personal-document revision/generation CAS. Shared DIRECT operations are refused. | A command is saved only after its receipt and confirmed revision; uncertain retries keep the exact mutation ID and packet. The generic pilot does not promise recovery of drafts after forced closure. |
| Pilot native workbook adapter | `createEditorSDK` native overload; `lib/mooring/native-profile.ts`, `lib/workbooks/bridge.ts` | Shares the pilot connection core while carrying the original workbook envelopes. The workbook model owns cells, pending mutations, drafts and undo. | The native model reports its applied revision/status through the adapter's `reportStatus`, which invokes the local `onStatus` callback; a transport failure overrides Saved. This is separate from the whole-native Craft profile below. |
| Whole-native Word and Sheet | `lib/mooring/native-editor-port.ts`, `native-document-client.ts`, `single-editor-save.ts`, `native-draft.ts` | Whole native snapshots under one server EditLease, with session, generation, pinned engine, base version and content hash. Multiple live writers are not merged. | Validated node-durable receipt, editor acknowledgement and local draft custody. An uncertain save uses the original attempt's reconciliation path; a recovered draft is offered for compare/copy, not automatically written over a new holder. |
| Multi-file artifact project | `hooks/use-artifact-project.ts`; existing project API and CLI | One immutable version manifest covers the complete file set. `base_version` CAS rejects concurrent publication. No native EditLease or CRDT merge is implied. | Publication/reconciliation and restore use the artifact version store; restore creates a new version. Project drafts have their own lifecycle rather than the native journal API. |
| Design canvas | `components/features/artifacts-design/use-design.ts`, `design-document.ts`; existing Design API and CLI | Server-authorized Yjs sync/awareness for the live canvas. Versioned operations, snapshots and restore are separate from live sync. Reader projections follow shared-version visibility. | The server persists accepted live updates; retained artifact versions capture the canvas. A live update and a retained version are different confirmations. Reconnect/draft recovery remains specific to Design. |
| Dashboard data | `lib/pages/artifact-dashboard.ts`; existing artifact data API and CLI | Declared collections over an artifact project. Code version selects the manifest; document writes use `ifVersion` and an atomic batch. Data changes do not publish a new code manifest. | The server checks the caller's current authority and selected collections. Producer identity and update time are data provenance; they are not evidence of a live external query or connector freshness. |
| Structured Docs | `lib/documents/contracts.ts`, existing Document editor, `internal/documents`, `tools/docs-service` | Validated Tiptap/Yjs structured content with its own schema, revision, epoch/generation and Go writer lease. The private engine service uses protocol 3 and schema capability 3. | Go retains state, updates, operation receipts and checkpoints; explicit restore advances the head and fences old writers. Yjs representation alone does not establish the Design websocket's multiuser collaboration guarantee. |

The original Rust-backed Mooring session and command transports remain a separate
experimental contract in [Mooring session transport](mooring-session-transport.md).
They are not the persistence route for whole-native Word/Sheet snapshots and do
not upgrade the profiles above to a common collaboration level.

## Shared boundaries

The document anchor is the existing Page ID within its workspace. Slugs are
addresses; callers must preserve the immutable identity instead of falling back
to another Page that later takes a slug. The Go host remains responsible for
current authentication, Page access, agent admission, write preconditions,
quotas and audit on the selected route. A browser capability or advertised
`can_write` value does not replace those server checks.

CLI, browser and agent tools use those authorized application paths. An agent
cannot supply a human identity, mint its own mandate or gain write authority
from a reader preview. Sharing a page does not delegate the author's credentials
to a Dashboard query, nor does a producer label grant access to an external
source.

The pilot JSON and whole-native frame paths use the existing channel helpers
where applicable: verify the exact iframe window, opaque origin, protocol and
approved bundle before transferring a private port. Content then travels on
that port. The whole-native application binding additionally includes the
application ID, engine pin and document/session scope. The two payload protocols
and their size limits remain distinct. Design sockets, Dashboard preview
bindings and the Docs engine service are not that same editor port.

## What the root SDK exports

`lib/mooring/index.ts` currently exports the pilot editor SDK, its connection
helpers, protocol/channel helpers and revision-push parsing. This entrypoint
contains no Crewship authentication, HTTP client or private storage access.
See the [pilot editor SDK](../development/mooring-editor-sdk.md) for its exact
handshake and editor lifecycle.

Native HTTP admission, snapshot saving, draft journals and version operations
are separate host modules. They are not exports of that root SDK. Project,
Design, Dashboard and Docs clients also retain their existing profile-specific
entrypoints. There is no shipped common `lib/mooring/sdk` facade to import from
this inventory. Adding a wrapper must preserve the underlying profile's authority, receipts and recovery behavior.

## Profile limits are separate

These bounds describe different payloads; none is a universal document limit.
Workspace/instance storage admission and each profile's history budgets can
refuse a smaller request as well.

| Profile boundary | Implemented bound |
|---|---|
| Pilot JSON channel | 64 KiB port messages; command arguments are limited to 32,768 bytes (32 KiB), and `replace-state` uses those arguments as the whole next state. New commands are refused once the revision reaches 1,000; existing revisions are not pruned. |
| Pilot native workbook adapter | 1 MiB snapshot body plus 16 KiB host-message metadata; outgoing editor messages remain 64 KiB. |
| Whole-native Word/Sheet | 32 MiB native bytes, 45 MiB encoded JSON request; native artifact history defaults to 16 GiB. |
| Artifact project version | 256 files and 32 MiB total file bytes; artifact history defaults to 256 MiB. |
| Design live state | 4 MiB encoded Yjs document; retained Design history defaults to 256 MiB. |
| Dashboard data | 256 KiB per data document, 50 writes and 1 MiB per batch, 8 MiB per collection, 32 collections and 4,096 documents per collection. |
| Structured Docs validation | 1 MiB binary document; admitted position-history metadata has its own 64 KiB bound within that document. |

The source constants and owning contracts remain authoritative:
`lib/mooring/channel.ts`, `lib/mooring/native-profile.ts`,
`lib/mooring/native-editor-port.ts`, `internal/mooring/store.go`,
`internal/mooring/native_store.go`, `internal/api/craft_native_documents.go`,
`internal/artifacts/version_store.go`, `internal/artifacts/data_store.go`,
`internal/design/engine.mjs` and the
[Docs engine compatibility contract](../../tools/docs-service/README.md).
A quota check, a free-disk probe and an actual reservation are different
properties; a payload limit does not establish either disk admission guarantee.

## Activation and compatibility

Availability is checked per profile. Server flags, configured engine/service
assets and browser build switches are separate requirements; a catalog entry
or installed source file does not prove the feature is enabled. Use:

- [Native Craft](craft-native.md) for qualified Word/Grid profiles, agent tasks
  and the separately gated version-history surface.
- [Artifact version store](artifacts-version-store.md) for project/Design
  family flags and limits, and [Design Yjs authority](artifacts-design-yjs.md)
  for its live transport.
- [Dashboard guide](../guides/artifacts/dashboard.mdx) for data collection and
  browser gates.
- [Document storage](../../internal/documents/README.md) and
  [Docs engine](../../tools/docs-service/README.md) for engine readiness,
  schema compatibility and rollback constraints.
- [Pilot JSON profile](mooring-pilot.md) for manifest registration, receipts and
  the distinct original workbook adapter.

The protocol-1 frame handshake requires the expected protocol and approved
bundle; an incompatible editor is refused instead of silently converted. Native
engine pins and application format remain bound to the document. Docs readiness
checks its own engine protocol/schema capability, and retained structured
metadata can require a compatibility reader during rollback. These checks do
not constitute a universal old-client/new-host compatibility promise. A release
must exercise the actual profile and supported upgrade/rollback pair.

## Verification boundaries

Existing regression suites cover separate boundaries:

| Boundary | Existing executable reference |
|---|---|
| Pilot handshake, revision ordering and truthful editor Saved state | `lib/mooring/editor-sdk.test.ts`, `components/features/pages/__tests__/mooring-editor-host.test.tsx` |
| Native port binding, save receipts, lost outcomes and local journal privacy | `lib/mooring/native-editor-port.test.ts`, `single-editor-save*.test.ts`, `native-draft*.test.ts`, native host tests |
| Atomic project manifests, authorized publication and restore | `internal/artifacts/version_store_test.go` and project API tests |
| Design sync/awareness and reader/writer boundaries | `internal/api/pages_design_ws_test.go`, Design browser/client tests |
| Dashboard code/data independence and document CAS | `internal/api/pages_artifact_data_test.go`, `internal/artifacts/data_store_test.go` |
| Structured Docs receipts, checkpoints, leases and engine validation | `internal/documents` tests and `tools/docs-service` tests |

These references identify implemented regression coverage, not a newly executed
cross-profile conformance result. In particular, unit or fixture transport tests
do not establish a real connector, full native-engine roundtrip, screen-reader
acceptance or a person's manual product walkthrough. There is no single shared
conformance runner that certifies all of these profiles today.
