# Native Craft human sessions, leases and whole-document API

The separate whole-native WordCraft/GridCraft profile is experimental and off by
default. `craft_native.enabled: true` or `CREWSHIP_CRAFT_NATIVE_ENABLED=true`
enables its human session and EditLease HTTP API. It requires a Linux boot-aware
clock. A qualified Word profile is advertised as `wordcraft_native` by the authenticated
workspace document status endpoint. New Doc then creates a fresh empty personal
Page, initializes its native Word model and releases the initial lease before
opening the full editor. An ambiguous initialization is never blindly replayed. Native format metadata on
Page reads reopens an existing native Page through that same fresh admission.
A lost creation-release response cannot erase or reinitialize its confirmed data.
This does not convert existing documents, enable agent proposals, or qualify the
complete editor acceptance.

Availability requires a readable Page with an explicit `craft_native_documents`
profile. Other profiles, inaccessible Pages and absent Pages report disabled.
Ordinary authentication and workspace membership remain required.

## Routes

Authentication: ordinary human authentication and current workspace/Page access
are required. Request headers: `Authorization` or a session cookie, plus
`X-Craft-Session` after admission. Query parameters: `workspace_id`. The slug
path parameter accepts a slug or `@<page-id>`. The private header never travels
to an editor frame. Responses are JSON, except the explicitly documented SSE.
Statuses: 401 for missing authentication, 403 for denied current authority,
400 for malformed requests, 409 for competing/stale custody, and 503 when a
mutation is disabled or unavailable. A generic denial does not prove read loss.

| Method | Path | Request | Response / success status |
|---|---|---|---|
| GET | `/api/v1/pages/{slug}/craft/availability` | No Craft admission. | `{enabled: boolean}`, 200. |
| POST | `/api/v1/pages/{slug}/craft/sessions` | `{}`. | `session_token`, public `session_id`, `application_id`, `page_id`, 201. |
| POST | `/api/v1/pages/{slug}/craft/sessions/reauthorize` | Separately refreshed original authentication; no body consumed. | `{renewed: boolean}`, 200. |
| GET | `/api/v1/pages/{slug}/craft/lease` | Craft admission header. | Lease status described below, 200. |
| POST | `/api/v1/pages/{slug}/craft/lease` | `{}`; acquire when free. | Lease status, 200. |
| POST | `/api/v1/pages/{slug}/craft/lease/acquire` | `{}`; explicit acquisition alias. | Lease status, 200. |
| POST | `/api/v1/pages/{slug}/craft/lease/renew` | `{generation: "1"}`. | Lease status, 200. |
| POST | `/api/v1/pages/{slug}/craft/lease/release` | `{generation: "1"}`. | Released lease status, 200. |
| POST | `/api/v1/pages/{slug}/craft/lease/request-handoff` | `{}`. | `{request_id, generation}`, 201. |
| POST | `/api/v1/pages/{slug}/craft/lease/handoff` | `{generation: "1", request_id: "public-request-id"}`. | New holder's lease status, 200. |
| GET | `/api/v1/pages/{slug}/craft/events` | Craft admission header; keep the connection open. | `text/event-stream`: `lease` status or `session_retired` metadata; 200. |
| POST | `/api/v1/pages/{slug}/craft/initialize` | Existing empty personal Page owner, original human `pages:write`; Qualified Word or Grid native bytes. | Atomic native profile, lease and version1, with private admission, 201. |
| GET | `/api/v1/pages/{slug}/craft/document` | Craft admission header. | Complete base64 native bytes, version and pinned editor/engine metadata, 200. |
| POST | `/api/v1/pages/{slug}/craft/document/save` | Bound whole snapshot described below. | Exact committed snapshot receipt, 200. |
| POST | `/api/v1/pages/{slug}/craft/document/import` | Same whole snapshot contract; existing explicit native Page only. | Exact committed snapshot receipt, 200. |
| POST | `/api/v1/pages/{slug}/craft/document/reconcile` | Exact original snapshot binding and hash; no blind retry. | Original pinned commit receipt,200; never an arbitrary latest head. |
| POST | `/api/v1/pages/{slug}/craft/document/restore` | Current binding and targetVersion. | New committed version and node durability, 200. |

Except availability, initialization and initial session creation, send `X-Craft-Session` as well
as ordinary authentication. Session creation returns `session_token`, the distinct
public `session_id`, `application_id`, and `page_id`. Keep the token only in the
mounted host closure, never in editor messages, local drafts, URLs or public DTOs.
The editor host and its lease controls share one admission; separate admissions
represent separate connections even for the same human.

Renew/release require `{generation: "1"}`. Handoff requires
`{generation: "1", request_id: "public-request-id"}`. Other lease mutations use
`{}`. Generations are canonical positive decimal **strings**, never JavaScript
numbers. Before the first acquisition, the status generation is `null`.

A lease status has `holder: {actor_id, display_name, kind} | null`, `mine`,
`generation`, `expires_at`, `can_write`, `acquired_at`,
`handoff_requested_kind`, and `handoff_requested_by`. Requests are an **array**
of `{request_id, actor_id, display_name, kind}`, visible to the holder. Current
admissions are human-only. `acquired_at` is presently `null`: the lease does not
persist acquisition wall time, and renewal time must not impersonate it.

Competing acquisition and stale generations return 409. A handoff advances the
server generation; a late renew or write from the old connection is rejected.
Neither a display name nor a public request ID establishes write authority.

## Authorization and lifetime

Every operation revalidates the original current human credential, workspace,
Page rights and captured authority epochs. Lease mutations also recheck current
authority after SQL publication. Runtime tokens are hashed in memory; a process
restart invalidates them. Durable lease generations and restore fences prevent
old custody from being revived by backup replacement.

The SSE connection renews only its own authorized current holder every five
seconds, with a fifteen-second lease. Browser focus does not control custody.
No document bytes travel through this stream. `lease` events contain advisory
status; `session_retired` stops delivery and explicitly says `draft: "retain"`
or `draft: "purge"`. Generic 401/403 responses alone do not prove read loss and
must not trigger local erasure. Unclassified authority changes conservatively
retire the old admission; a fresh read admission is separate from reacquisition.

Original expired or revoked credentials never regain an old admission. Explicit
reauthorization accepts only a separately authenticated newer expiry for the same
still-live identity and epoch. It never extends an already exhausted boot-time
cap or adopts another source identity.

## Whole-document configuration and receipts

`craft_native.editor_bundle_dir` names the operator-owned bundle directory.
The existing editor-origin handler verifies each profile digest against its
manifest before serving it under `/_editor/b/<digest>/index.html`, or under
`<configured-editor-origin>/b/<digest>/index.html` when an existing dedicated
editor origin is configured. The profile URL must match that exact mount. Native-only
mode activates no legacy editor or transport. When a legacy mount is also
active, both must explicitly name the same directory; its existing allowlist
order is preserved and native pins are appended. Incompatible configuration
fails closed. Native bundle requests carrying credentials are refused; the
opaque sandbox and credentialless frame policy remain unchanged.

`craft_native.profiles` is a trusted startup map keyed by application. Each
profile has `application`, `engine_revision` (40 lower-hex), `cli_path` (absolute
operator-owned executable), `cli_sha256`, `editor_bundle` (both 64 lower-hex), and
`editor_url`. The operator must qualify the exact source, executable and WASM
bundle together. Startup rejects malformed profiles or a different CLI hash.
Each validation invokes the real `NativeCLIValidator`, hashes its opened file
descriptor again, and rejects repair warnings or lossy roundtrips. Requests never
choose an executable, validator callback or profile. A missing application
profile returns 503. Availability never converts an old Page into this profile.

Initialization accepts `application_id: "wordcraft"` or `"gridcraft"` and base64
`bytes`; the selected application must have an operator-qualified profile.
It requires an existing empty personal Page owned by the authenticated human,
current folder policy and live membership. Crew-owned Pages, existing content,
read-only credentials, reinitialization and client-supplied pins are refused.
The server captures immutable original authority epochs, validates with its
qualified CLI, and commits profile, initial lease and version1 atomically with
final authorization. It publishes the private admission only after commit;
validation or final authorization failure leaves no partial native state.
Ordinary Page creation remains separate.

The native-only operator override `CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR`
selects the bundle directory. Per-application release bindings are explicit:

| Binding | Word | Sheet |
| --- | --- | --- |
| Engine revision | `CREWSHIP_CRAFT_NATIVE_WORD_ENGINE_REVISION` | `CREWSHIP_CRAFT_NATIVE_GRID_ENGINE_REVISION` |
| CLI path | `CREWSHIP_CRAFT_NATIVE_WORD_CLI_PATH` | `CREWSHIP_CRAFT_NATIVE_GRID_CLI_PATH` |
| CLI SHA256 | `CREWSHIP_CRAFT_NATIVE_WORD_CLI_SHA256` | `CREWSHIP_CRAFT_NATIVE_GRID_CLI_SHA256` |
| Editor bundle | `CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_BUNDLE` | `CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_BUNDLE` |
| Editor URL | `CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_URL` | `CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_URL` |

These override corresponding YAML
fields without enabling the product flag or any legacy profile. Explicit empty
values remain empty and fail qualification; every startup/manifest/hash check
still applies.

Save and import accept `workspaceId`, `pageId`, `applicationId`, `enginePin`,
public `sessionId`, `generation`, `baseVersion`, opaque `editorRevision`,
`contentHash` in `sha256:<hex>` form, and complete native `bytes` as base64.
Versions and generations are canonical positive int64 decimal **strings**.
Native bytes are bounded to 32 MiB; the encoded JSON body is bounded to 45 MiB.
The lease and current original credential/Page rights are checked in publication
transactions, and pins must match the Page's immutable native profile. No HTML
or partial patch stands in for the native model.

A snapshot receipt repeats the exact scope/session/generation/revision/hash and
base binding, gives the exact successor `version`, matching `candidateHash`,
`issuedBy: "crewship"`, `durability: "node"` and `phase: "committed"`. Only this
receipt acknowledges Saved. Reconcile sends the original attempt metadata; it
may return that same pinned commit after another version exists. A lost ACK
does not authorize replaying a save. Restore supplies the current binding and
`targetVersion`, producing a new version rather than replacing history.

Read returns `version`, base64 `bytes`, `applicationId`, `enginePin`,
`editorBundle`, `editorURL` and quota (`usedBytes`, `limitBytes`, `warning`,
`retention`). Quota byte counters are strings. Quota exhaustion returns 413;
conflict or stale custody 409; invalid native data 422. Existing versions remain
available. Generic denied writes retain the local draft; only proven read loss
requires its erasure.

## Current implementation boundary

The backend has direct-handler and real-Router two-human regression coverage.
New Doc offers Word and Sheet only when their independent `wordcraft_native` and
`gridcraft_native` status capabilities are literal true. These capabilities
advertise live, qualified profiles without enabling the older Docs engine.
The owner-only native initializer receives the selected original engine's
canonical blank bytes for a fresh Page. Creation releases only its exact lease
generation; an ambiguous release can reopen the confirmed document with fresh
read-only admission, while an ambiguous initialize is never repeated. Existing
native Pages retain their application descriptor when opened from the Pages list.
Proposals, agent execution, one-hour browser acceptance and 2,000 changing saves
remain separate work.

The native blank Word asset is serialized by the original engine separately from
the older checkpoint blank. The explicit binary qualification instructions are
in [the canonical Word creation test](../../internal/mooring/native_word_creation_linux_test.go).
Run `go test ./internal/mooring -run ^TestNativeCLIValidatorCanonicalWordCreation$`
with its pinned original engine.
The explicit run must pass; ordinary unit runs skip it when no binary is supplied.
It also verifies that the old lossy float promotion is still rejected.

The canonical Sheet asset is the qualified GridCraft CLI's native output, used
without model conversion. Follow [the canonical Sheet creation test's binary
qualification instructions](../../internal/mooring/native_grid_creation_linux_test.go),
then explicitly run
`go test ./internal/mooring -run ^TestNativeCLIValidatorCanonicalSheetCreation$`.
It must accept the actual creation bytes and reject an authored extension that
the engine would discard; an ordinary optional test skip is not qualification.
