# Native Craft sessions, leases, agent tasks and whole-document API

The separate whole-native WordCraft/GridCraft profile is experimental and off by
default. `craft_native.enabled: true` or `CREWSHIP_CRAFT_NATIVE_ENABLED=true`
enables its human session and EditLease HTTP API. It requires a Linux boot-aware
clock. A qualified Word profile is advertised as `wordcraft_native` by the authenticated
workspace document status endpoint. New Doc then creates a fresh empty personal
Page, initializes its native Word model and releases the initial lease before
opening the full editor. An ambiguous initialization is never blindly replayed. Native format metadata on
Page reads reopens an existing native Page through that same fresh admission.
A lost creation-release response cannot erase or reinitialize its confirmed data.
This does not convert existing documents or qualify the complete editor
acceptance. Agent proposals are described under "Agent proposals" below.

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
| GET | `/api/v1/pages/{slug}/craft/availability` | No Craft admission. | `{enabled: boolean, can_edit: boolean, preview_enabled: boolean, agents_enabled: boolean}`, 200. |
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
| POST | `/api/v1/pages/{slug}/craft/document/reconcile` | Exact original snapshot binding and hash; no blind retry. Current write rights and the original admission; the original lease may have expired or moved. | Original pinned commit receipt,200; never an arbitrary latest head. |
| POST | `/api/v1/pages/{slug}/craft/document/restore` | Current binding and targetVersion. | New committed version and node durability, 200. |
| GET | `/api/v1/pages/{slug}/craft/proposals` | Craft admission header; Page read. | `{proposals: [...]}` open and decided agent proposals, 200. |
| GET | `/api/v1/pages/{slug}/craft/proposals/{proposalID}` | Craft admission header; `ncp_` id. | `{proposal, bytes, content_hash, engine_pin}` candidate, 200; 404 when absent. |
| POST | `/api/v1/pages/{slug}/craft/proposals/{proposalID}/accept` | `{generation: "1"}` of this admission's lease; Page write. | `{proposal, receipt}` with the new committed version, 200; 409 `stale` when the content changed after base. |
| POST | `/api/v1/pages/{slug}/craft/proposals/{proposalID}/reject` | `{}`; Page write, no lease. | Rejected proposal, 200; 409 when already decided. |

Except availability, agent task controls, initialization and initial session creation, send `X-Craft-Session` as well
as ordinary authentication. Session creation returns `session_token`, the distinct
public `session_id`, `application_id`, and `page_id`. Keep the token only in the
mounted host closure, never in editor messages, local drafts, URLs or public DTOs.
The editor host and its lease controls share one admission; separate admissions
represent separate connections even for the same human.

A reply lost during an outage longer than the lease stays provable: reconcile
checks Page write rights and that the caller is the original admission, not
that its lease is still live. When the editor loses write, it reconciles the
unconfirmed attempt once before retiring; a proven commit and an identical
fresh capture leave no recovery draft, anything else stays offered for
comparison. The expired generation still cannot save.

Renew/release require `{generation: "1"}`. Handoff requires
`{generation: "1", request_id: "public-request-id"}`. Other lease mutations use
`{}`. Generations are canonical positive decimal **strings**, never JavaScript
numbers. Before the first acquisition, the status generation is `null`.

A lease status has `holder: {actor_id, display_name, kind} | null`, `mine`,
`generation`, `expires_at`, `can_write`, `can_edit`, `acquired_at`,
`handoff_requested_kind`, `handoff_requested_by`, and `can_stop_agent`. An agent holder also has public `agent_id` and `run_id`; its `kind` is `agent`. Requests are an **array**
of `{request_id, actor_id, display_name, kind}`, visible to the holder. Current
editor admissions are human-only; an agent uses the separate confirmed task binding below. `acquired_at` is presently `null`: the lease does not
persist acquisition wall time, and renewal time must not impersonate it.

Competing acquisition and stale generations return 409. A handoff advances the
server generation; a late renew or write from the old connection is rejected.
Neither a display name nor a public request ID establishes write authority.

## Shared previews and edit capability

`can_edit` is current original-credential and Page edit authority, independent of
whether someone else holds the lease. `can_write` additionally requires this
connection to hold custody. Observers receive no takeover, handoff, publish or
agent controls. An older response without `can_edit: true` grants no UI edit
capability. Server mutations continue to recheck the live authority and lease.

`GET /api/v1/pages/{slug}/craft/preview` uses ordinary human authentication and
`workspace_id`, without a Craft admission. It accepts canonical Read or Comment
visibility and returns only the current shared native version: `version`, base64
`bytes`, `applicationId`, `enginePin`, `editorBundle` and `editorURL`. The live
credential, Page rights, captured authority epochs and immutable native profile
are checked in the same protected transaction as the shared pin. No private
latest version, history, quota, admission or custody metadata is returned. A
missing shared pin returns 404, denied access 403 and disabled/unqualified
profiles 503; there is no fallback to the latest bytes. `preview_enabled` in
availability advertises this separately from the existing native observer path.
The default editor entry requires both `enabled === true` and `can_edit === true`
before mounting the editor host. `can_edit` checks current original-credential
and Page write authority in the same transaction as the other availability
fields. Read and Comment instead mount the shared preview only when
`preview_enabled === true`, without opening a Craft session; missing or truthy
nonboolean edit capabilities grant no host access. The legacy explicit Read
observer API remains compatible, while Comment alone never opens an editor
admission or receives private latest native bytes.

### Revocation of a shared preview (push)

The preview has no event stream of its own. When a grant on a native Craft page
is added or removed (`PUT`/`DELETE /api/v1/pages/{slug}/grants`), the server,
after the grant transaction commits, sends one `page.access_recheck` frame with
the payload `{ "page_id": "<id>" }` to each person the changed grant names: the
user, the members of a named crew, or the workspace members for a workspace
grant (agents are not viewers). It travels on that person's own identity channel
`user:{userId}`, which only that person may subscribe to, so nobody receives a
signal about another user's rights or about a page that is not theirs. The frame
carries no content, no level and no names; it only means "re-check".

An open preview that receives a frame for its own `page_id` (and on
`realtime.reconnected`) immediately repeats the ordinary `craft/preview` read.
On 403/404 or any failed read it removes the frame, closes the editor port and
shows "Sdílený náhled není dostupný". A frame for another page is ignored. If a
read is already in flight, one more read runs after it, so a result that began
before the commit cannot keep the content. The 30 s timer and the focus/online
rereads stay as the fallback for every change that sends no frame (workspace
member removal, role or ownership changes, a missed frame). The frame cannot
grant or reveal anything: the answer always comes from the server's own
authorisation of the read. PRD row `A.craft.read-revoked` expects the removal
within about 5 s.

`POST /api/v1/pages/{slug}/craft/document/share` requires ordinary human auth and
`X-Craft-Session`. Its strict JSON request contains `workspaceId`, `pageId`,
`applicationId`, `enginePin`, `sessionId`, `generation`, `baseVersion` and
`targetVersion` (canonical positive decimal strings for generations/versions).
It publishes an existing committed version with current write authority, the
current lease generation and latest-version CAS inside the same transaction.
The same transaction records the original human actor, selected version and
Page in the authorization audit and journal. Unsupported or failed audit writes
roll back publication. It cannot create content or grant access. Rejected publication preserves both
content and the prior shared pin. Success returns `{version, durability:"node",
issuedBy:"crewship", phase:"shared"}`; it is a preview publication receipt,
not acknowledgement of an unsaved native buffer. Clients must first confirm the
exact current native snapshot and must not retry an ambiguous publication.

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
must not trigger local erasure. An admission captures the author-relevant
generations of its own human and its own Page (`mooring_authority_generations`
`user` and `page` rows, installed at admission if absent). Any change whose
recipients include that person or that Page (their grants, crew or workspace
membership, folder ACL, account status, the Page's owner, audience, folder or
panels) retires the old admission, including a revoke-then-restore cycle. The
broad workspace delivery epoch (`workspaces.authority_revision`) is not part of
this fence: creating another Page, changing a grant for someone else on another
Page or adding a member does not retire a live editor. Current Page rights,
membership revision and the original credential are still checked live on
every operation and stream heartbeat. A fresh read admission is separate from
reacquisition.

Original expired or revoked credentials never regain an old admission. Explicit
reauthorization accepts only a separately authenticated newer expiry for the same
still-live identity and epoch. It never extends an already exhausted boot-time
cap or adopts another source identity.

The browser obtains a fresh cookie before initial admission and proactively refreshes
it while the document remains mounted. A successful same-identity reauthorization
replaces the metadata GET with a newly authenticated connection, retaining the
private admission, lease generation, editor and local journal. The previous HTTP
request still expires at its original JWT horizon. A short, bounded retry of a
409 metadata GET only waits for the previous connection to close; saves, custody
mutations and ambiguous outcomes are never replayed. Bearer credentials do not
enter the cookie-refresh flow.

A transport reset, clean SSE EOF or temporary HTTP failure reconnects only
metadata after current same-admission reauthorization. At most eight recovery
attempts back off from 100 ms to five seconds. A 409 while the server still holds
an old stream after transport loss uses the same exponential budget, allowing
several seconds for cleanup. Planned credential rotation keeps its separate
short handoff grace. A valid lease on a stream shorter than five seconds does
not reset the recovery budget. Invalid metadata, authorization
refusal and explicit session retirement are terminal. Exhaustion uses the
existing draft-retaining disconnection path. Closing the client cancels both
the stream and a coalesced periodic/recovery authorization. No save, custody
mutation or new admission is replayed by this recovery.

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

The native-only operator environment overrides are
`CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR`,
`CREWSHIP_CRAFT_NATIVE_WORD_ENGINE_REVISION`,
`CREWSHIP_CRAFT_NATIVE_WORD_CLI_PATH`,
`CREWSHIP_CRAFT_NATIVE_WORD_CLI_SHA256`,
`CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_BUNDLE` and
`CREWSHIP_CRAFT_NATIVE_WORD_EDITOR_URL`. The corresponding Sheet overrides are
`CREWSHIP_CRAFT_NATIVE_GRID_ENGINE_REVISION`,
`CREWSHIP_CRAFT_NATIVE_GRID_CLI_PATH`,
`CREWSHIP_CRAFT_NATIVE_GRID_CLI_SHA256`,
`CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_BUNDLE` and
`CREWSHIP_CRAFT_NATIVE_GRID_EDITOR_URL`.
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
`level`, `retention`). Quota byte counters are strings. `level` is `none`,
`near` (from 80%), `critical` (from 95%) or `full` (at the limit) so the editor
can warn before saving fails. Quota exhaustion returns 413;
conflict or stale custody 409; invalid native data 422. Existing versions remain
available. Generic denied writes retain the local draft; only proven read loss
requires its erasure.

## Native agent tasks

`craft_native.agents_enabled` or `CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED=true`
separately enables this pilot. Both native documents and agent tasks are off by
default. Enabling the human editor alone grants no agent access. The operator
must configure the qualified original CLI and the supported private Claude Code
runtime with its current sidecar. Unsupported or unavailable runtimes refuse
admission before accepting work.

An authenticated human with current Page write access may send
`POST /api/v1/pages/{slug}/craft/tasks?workspace_id=…` with exactly
`{chat_id, message_id, whole_document: true}`. The message must be that human's
persisted original user message in the actual registered agent's chat. The host
captures original credential/authority epochs, the whole-document consent and
immutable work input. A confirmed original provider exec and matching current
work attempt are required before the actual run credential can access the
native document. An ordinary run, a database row, an old grant, or a claimed
actor/mandate is insufficient. Repeating the same message/Page/agent cannot
launch another task; a new task needs a new original message and consent.

The normal human CLI uses the same explicit consent and Page authority:

```sh
crewship craft task start @PAGE --chat-id CHAT --message-id MESSAGE --whole-document --workspace WORKSPACE
crewship craft task status @PAGE WORK_ID --workspace WORKSPACE
crewship craft task stop @PAGE --workspace WORKSPACE
```

`craft task start` requires a true `--whole-document`; it never supplies consent
by default. These human commands do not use the native editor session or agent
credentials. Stop reports the actual cancellation/termination fields, rather
than assuming the process has stopped. Mutations are not replayed on failure.

The accepted response is `{work_id, status: "queued"}`, HTTP 202. It is not proof
of execution or a save. The same original human can inspect
`GET /api/v1/pages/{slug}/craft/tasks/{workID}?workspace_id=…` under fresh current
Page write rights. This read-only projection contains actual bounded tool
outcomes and `last_committed_receipt`, never prompt or command bodies, native document
bytes or credentials. `outcome` is `saved` once the run has a committed receipt,
or `proposal` with `proposal_id` when it recorded a proposal instead; it is
absent while the run has neither. The last receipt is explicitly historical; an uncertain
or in-flight later attempt is not Saved. Append `craft_task_id=<work_id>` to the
normal native editor URL to inspect this task in the editor. That selector is
not authority and never starts or resumes a run.

The task's original agent calls the fixed sidecar MCP tools `craft_read`,
`craft_acquire`, `craft_renew`, `craft_apply` and `craft_release`. They forward the
actual run credential and crew IPC identity to the fixed private native-task
RPC endpoint. Requests cannot select a Page, actor,
run, executable or profile. The host binds every call to the one captured task
and rechecks current original rights and epochs at the final SQL writer.

Acquisition uses the same EditLease as the human editor. A human holder returns
409 with `holder_kind: "human"`; no engine mutation or version is published.
Generations and base versions remain canonical positive decimal strings.
Apply requires the current `generation`, exact `base_version`, a new bounded
`request_id` and structured `commands`. The original engine performs the edit;
the existing NativeStore validates the complete returned native document and
publishes through the same lease/CAS/VersionStore path. There is one publication
attempt. An exact repeated request only reconciles its original snapshot; it
never reruns the engine. A changed packet with the same ID conflicts, and a
historical receipt cannot clear another attempt's uncertainty or downgrade the
last confirmed version.

The initial command surface is deliberately narrow: Word supports
`caret.docStart`, `caret.docEnd` and plaintext `text.insert`; Sheet supports
scalar/literal `cell.set`. Word `text` and Sheet `cell.get` provide bounded
original-engine reads. File/system commands and formulas supplied through this
initial agent tool are refused. This does not restrict the human editor's
original feature surface. Executables are operator-pinned and rehashed before
executing the same file descriptor; document paths are private host-generated
files, with no shell interpretation or inherited host credentials.

The normal fifteen-second lease is unchanged. A live authenticated apply renews
its exact lease every five seconds only while that bounded request remains
alive. Between calls the agent must explicitly renew. The holder is
`agent:<actual-agent-id>` with public session correlation
`native-agent:<actual-run-id>`; the immutable version retains that actor and run
alongside the captured work/run provenance. Detailed original consent auditing
also depends on the existing work/run ledger retention. No human session is
borrowed and no separate version ledger is introduced.

Only the original human, with fresh current Page write rights, can send
`POST /api/v1/pages/{slug}/craft/tasks/cancel` with `{}`. The host retires only
that registered agent's exact lease and permanently closes the grant under the
SQL writer before acknowledging. It cannot force a human holder. The response
reports `grant_revoked`, `lease_retired`, `cancel_requested`, `stopped` and
`stop_pending`; physical process termination is not assumed from cancellation.
The human editor exposes Stop only when `can_stop_agent` is true. A stopped
process is successful only with retired custody and either a qualified actual
Node receipt (`native_outcome: "saved"`) or a proposal this run recorded
(`native_outcome: "proposal"`, `native_proposal_id`); a run with neither fails
with "native agent finished without a committed native receipt", and uncertain
outcomes remain distinct.

This pilot retains at most 128 captured task entries per process and at most 64
apply IDs per task. It refuses further work instead of evicting uncertain
authority. The private status timeline retains the last 64 actual completed tool
responses and is explicitly `current-host-only`; a restart cannot reconstruct a
grant or its timeline from SQL. Durable native versions and original receipt
metadata remain in VersionStore. Existing documents are never migrated.

## Current implementation boundary

The backend has direct-handler and real-Router two-human regression coverage.
Whole-document routes require an already explicit native Page and a trusted
qualified profile; only the explicit owner-only native initializer may create
that profile on an existing empty Page. Page creation, full editor mounting,
proposals and agent execution,
one-hour browser acceptance and 2,000 changing saves are separate integration
capabilities; coverage of one does not establish the others.

New Doc offers Word and Sheet only when their independent `wordcraft_native` and
`gridcraft_native` status capabilities are literal true. These capabilities
advertise live, qualified profiles without enabling the older Docs engine.
The owner-only native initializer receives the selected original engine's
canonical blank bytes for a fresh Page. Creation releases only its exact lease
generation; an ambiguous release can reopen the confirmed document with fresh
read-only admission, while an ambiguous initialize is never repeated. Existing
native Pages retain their application descriptor when opened from the Pages list.

The native blank Word asset is serialized by the original engine separately from
the older checkpoint blank. For explicit qualification, set
`CRAFT_QUALIFICATION_ENGINE` and `CRAFT_QUALIFICATION_SHA256` to the original
pinned Word executable and hash, then run
`crewship-heavy go test -tags craft_native_engine_qualification ./internal/mooring -run ^TestNativeCLIValidatorCanonicalWordCreation$`.
The run must execute and pass, including refusal of the old lossy float promotion.

The canonical Sheet asset is the qualified GridCraft CLI's native output, used
without model conversion. Set the same two qualification inputs to the original
pinned Grid executable and hash, then run it separately:
`crewship-heavy go test -tags craft_native_engine_qualification ./internal/mooring -run ^TestNativeCLIValidatorCanonicalSheetCreation$`.
It must accept the actual creation bytes and reject an authored extension that
the engine would discard. Both are qualification-only inputs, not server
configuration. These explicit runs fail when inputs are absent; ordinary unit
builds exclude them. Default CI is not evidence that either engine executed.

The human and agent transport boundaries have targeted API, original-engine,
private-process, retirement, cancellation and receipt regressions. Live editor
and genuine dispatched-agent acceptance must still qualify the exact release;
unit provider/model fixtures are not live agent evidence. Complete
visible-editor feature acceptance remains separate work.

## Agent proposals

When a person holds the lease, an agent never waits for, steals or retries it.
`craft_propose` runs the same allowlisted original-engine commands over the
exact `base_version` the agent read and stores the complete candidate in
`craft_native_proposals`; the document, its versions and the lease are
unchanged. At most 16 proposals per document may be open. Before counting, a
new proposal marks every open proposal whose base content differs from the
current head `stale` (decided by `system:superseded`; rows are kept), because
such a proposal can no longer be accepted. A proposal over an older version with
identical content stays open, since accept still applies it on the head. The stored idempotency key includes the authenticated run. A different run of
the same agent has a separate request namespace and cannot adopt its earlier
candidate; the display author remains the agent identity.

Apply and propose requests share a limit of 64 admitted change attempts per
run; reaching it returns 429 before another engine invocation. Exact retries
consume no further slot and never rerun the engine, including after the document
advances. Changing commands under the same request identifier conflicts. An
unknown save or proposal commit blocks new changes with 503 until that exact
request is reconciled. Only its pending candidate is retained for this purpose;
completed or definitely refused candidates do not accumulate in memory.

All retained proposal content, including rejected, stale and accepted rows,
counts toward its own per-document proposal allowance (64 MiB by default,
`NativeStoreConfig.MaxProposalBytes` for trusted host configuration). This
allowance is separate from human version history: filling it refuses additional
proposals while leaving the human's available history allowance intact. Accept
charges the newly saved version to version history and keeps the candidate in
the proposal allowance. Reject never replenishes either retained candidate space
or the human's version history.

Accounting uses exact bytes for older rows restored as TEXT and reserves decision
metadata up front. Native BLOB lengths use SQLite's metadata-only length path;
Read, View and Save do not scan retained candidate payloads. Insertion and the
proposal budget check share the authorized writer transaction; a refusal rolls
back the new candidate without deleting any confirmed candidate or version.
These are document allowances; aggregate workspace/disk reservations remain a
separate storage requirement.

A proposal is a successful task outcome: the work item and run succeed with
`native_outcome: "proposal"` and `native_proposal_id`, and the task status shows
`outcome: "proposal"` with `proposal_id`. Only a proposal recorded by this run's
own session counts. A different run may reuse a client `request_id`; it records
its own proposal instead of adopting an earlier run's candidate.

A person with an admitted session reviews them (`GET .../craft/proposals`,
candidate bytes at `GET .../craft/proposals/{id}`) and decides once. Accept is
the person's own fenced save: Page write rights, the immutable profile, this
admission's lease generation, base == latest and the open state are checked and
the version and decision commit in one transaction. The editor saves the
holder's state before accepting; versions that only repeat the base bytes do not
count as a change, and the proposal applies on the current head. If the document
content moved on, the proposal becomes `stale` and nothing is applied; it is
never rebased over changed content.
Reject needs write rights but no lease: any co-editor with current Page write
rights may reject a proposal while another person holds the lease, and the
document is never changed. Reading the list and a candidate uses the same
session-bound read gate as the document, so a Read-only admission can review
but not decide; Comment-only access is not admitted at all. A proposal id is
resolved only within the admitted page and workspace; another page's id
answers 404. The agent cannot decide its own proposal.

The bounded proposal review list puts all open proposals before decided history,
then orders each group by creation time. Newer decisions cannot hide a pending
proposal; the per-document open-proposal cap remains 16 and the list cap 50.
