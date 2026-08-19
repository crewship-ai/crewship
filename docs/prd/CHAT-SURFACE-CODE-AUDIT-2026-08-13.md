# Chat surface code audit — 2026-08-13

**Status:** independent code audit for review; not an implementation plan or a
claim that the findings are already fixed.
**Audience:** product owner and a second coding agent asked to verify, dispute,
and turn the findings into red tests and scoped work.
**Repository snapshot:** branch `feat/chat-primary-surface`, commit `12837d8b`,
plus the uncommitted worktree changes listed in §1. The commit hash alone does
not reproduce the inspected tree.
**Primary companions:** `CODEX-WORK-ORDER-CHAT-1-0.md`,
`chat-as-a-primary-surface.md`, and
`agent-ask-packs-and-document-intake.md`.

---

## 0. Executive verdict

Crewship is not missing a chat-shaped screen. It already has many of the
visible primitives expected of a current AI chat product: streamed answers,
reasoning and tool blocks, stop, edit/resend, regenerate, search, export,
attachments, camera input, per-agent prompts and forms, an artifact side pane,
delegation events, and run/cost data elsewhere in the product.

The release risk is that these primitives do not yet form one trustworthy
workflow:

```text
conversation
  -> durable inputs
  -> agent run and tool/delegation timeline
  -> approvals and user decisions
  -> sources and memory
  -> versioned outputs
  -> optional reusable routine
```

Several controls either do nothing, show data that is not durable, or imply a
stronger contract than the backend provides. Adding more competitor-shaped
features before fixing those seams would increase the amount of UI that can
mislead a user.

The recommended product direction is therefore **not** “clone ChatGPT or
Claude.” It is: keep Crewship's Workspace → Crew → Agent model and make chat the
truthful control surface for orchestration. Consumer-chat features should be
adapted only when they support that model.

The highest-cost defects found by this audit are:

1. a file cannot be attached to the locally created first-message draft;
2. two different uploads with the same filename can produce metadata that no
   longer identifies the stored bytes;
3. the artifact editor fetches a directory-list endpoint as though it were a
   file-download endpoint;
4. questionnaire upload fields are not independent;
5. questionnaire answers and their provenance are not durable structured data;
6. several command-palette/slash actions are visible but have no effective
   handler;
7. delegation, approvals, memory, and artifacts are represented as disconnected
   fragments rather than a durable conversation/run timeline.

## 1. Method and limits

Three parallel read-only passes inspected:

- attachments, files, and the artifact pane;
- ask forms, intake, rendering, and manifest round-tripping;
- chat orchestration, delegation, approvals, routines, memory, and commands.

The audit compared the implementation to the three companion documents and
followed server handlers rather than inferring behaviour from component names.
No files were changed during the inspection itself. This document is the first
new audit artifact.

The inspected working tree already contained these unrelated or preceding
changes and they have been preserved:

```text
M  components/features/chat/__tests__/assistant-turn.test.tsx
M  components/features/chat/assistant-turn.tsx
M  docs/prd/agent-ask-packs-and-document-intake.md
M  docs/prd/chat-as-a-primary-surface.md
M  hooks/__tests__/use-chat-failure-surfacing.test.ts
M  hooks/use-chat.ts
M  internal/manifest/client.go
M  internal/manifest/export.go
M  internal/manifest/plan.go
M  internal/manifest/schema.go
M  internal/manifest/validate.go
?? internal/manifest/agent_ask_forms_test.go
```

This was a static code audit, not a production-usage study. It did **not**:

- run a provider-backed browser conversation end to end;
- prove behaviour against a running crew or after reconnect/reload;
- inspect real workspace invocation counts or product telemetry;
- perform a fresh, sourced market-parity survey of current ChatGPT/Claude
  releases.

References below are snapshot locations, not substitutes for tests. The second
reviewer should open the code because line numbers will move.

## 2. What is already present

The codebase has enough implemented surface that a rewrite is not justified.
The following should be repaired and connected rather than rebuilt:

- a primary `/chat` route with agent/session navigation and mobile treatment;
- local draft sessions created server-side on the first send;
- streamed assistant turns, reasoning/tool presentation, stop, regenerate,
  edit/resend, copy, and error presentation;
- conversation search and export;
- per-agent `suggested_prompts` and `ask_forms`;
- attachment upload, camera capture, and attachment paths in the prompt;
- a right rail with file/memory/activity concepts;
- backend journal, run, trace, assignment, escalation, approval, policy, budget,
  and cost concepts;
- routine authoring initiated from `/routines` through a fresh chat;
- an agent-side `save_routine` capability;
- a crew memory write tier and a file-backed memory system.

The gap is mainly contract integrity and integration, not absence of primitives.

## 3. Confirmed P0 findings

### P0.1 — A cold/new chat cannot accept an attachment

`chat-page-client.tsx:396-426` deliberately creates only a local draft ID when
an agent has no existing sessions. The chat row is created by `ChatPanel` on
first send. The upload handler, however, queries `chats` before accepting bytes
and returns `404 Chat not found` when the row does not exist
(`internal/api/proxy_attachments.go:57-63`). The attachment path does not first
call the session-creation path.

This contradicts the user-visible model: attaching is an ordinary composer
action, but the very first conversation accepts text before it accepts a file.

**Required red test:** open an agent with zero chats, attach a file before
typing or sending, and assert that upload succeeds and the eventual first
message references the same durable attachment.

### P0.2 — Filename is a mutable storage identity

The blob path is constructed as:

```text
attachments/<chatId>/<sanitised filename>
```

at `internal/api/proxy_attachments.go:108-124`. Uploading different bytes under
the same filename overwrites that path before metadata insertion. Metadata is
deduplicated by chat, SHA, and filename, so different SHA values can produce
two rows pointing at the same `storage_key` while only the last bytes remain.

Consequences:

- old metadata can claim a checksum for bytes that no longer exist;
- a message's path does not identify the version the agent read;
- provenance and deletion cannot be made reliable on top of the current key.

**Required red test:** upload `evidence.pdf` with bytes A and then bytes B in
one chat; prove that every returned attachment identity still resolves to its
own checksum. The current path model should fail that assertion.

### P0.3 — Stored bytes can exist without authoritative metadata

`recordChatAttachment` is explicitly best-effort and runs after the byte write
(`internal/api/proxy_attachments.go:139-164,220-258`). A failed insert is logged
but the endpoint still returns success. Chat attachment blobs are also outside
the content-addressed reclaim mechanism described in the same handler.

The metadata row therefore cannot be the authority for listing, provenance,
or garbage collection, while the path is not immutable enough to be that
authority either.

This requires a lifecycle decision, not only a retry: either make metadata and
blob publication one recoverable operation, or define and implement an
explicit reconciliation process.

### P0.4 — ArtifactPane calls the list API as a download API

`components/features/chat/artifact/artifact-pane.tsx:69-85` requests:

```text
GET /api/v1/agents/<id>/files?workspace_id=<id>&path=<path>
```

and reads the response as text. `AgentFiles` ignores `path` and returns the
file listing (`internal/api/proxy_files.go:43-69`). File content has a separate
route:

```text
GET /api/v1/agents/<id>/files/download
```

registered in `internal/api/router_orchestration.go:733-734` and implemented by
`AgentFileDownload`.

The editor can consequently receive JSON directory data as editable file
content. Its save action makes this potentially destructive.

**Required red test:** open a known text artifact, assert exact file bytes are
loaded, edit once, and assert no directory-list JSON can be written back.

### P0.5 — Multiple file/photo form fields are one shared bucket

`ask-form-sheet.tsx:118-140` reads one attachment list keyed only by session.
Every file/photo field maps to the same `attachmentPaths`; the component even
selects only the first upload field to display chips. Submit validation at
`:171-187` therefore treats any uploaded file as satisfying every required
upload field. Rendering sends the full path list to each field.

A form asking for both “contract” and “identity photo” cannot tell which file
answers which question, and one file satisfies both requirements.

**Required red test:** define two required upload fields, upload one file into
the first, and assert that the second remains invalid and receives no path.

### P0.6 — Form submission and provenance are not durable structured data

Form values are local component state. Composer persistence retains `modelId`
and `drafts`, not attachments (`stores/composer-store.ts:103-107`). Provenance
is a bounded in-memory map keyed by rendered message content
(`components/features/chat/asks/ask-provenance.ts`). Reloading loses the form
identity and field/value relationship even if the rendered plain-text message
survives.

Content is not a safe identity: two identical rendered submissions can collide,
and provenance can be recorded before a send is durably confirmed.

This blocks reliable audit, editing, analytics, and later automation. The
conversation should retain ordinary readable text, but also store a structured
submission envelope such as:

```json
{
  "form_id": "...",
  "form_version": 3,
  "values": {"purpose": "..."},
  "field_attachment_ids": {"contract": ["att_..."]},
  "rendered_text": "..."
}
```

### P0.7 — Ask-form constraints are stronger in schema than at submission

The form definition model admits constraints and richer types, but the UI
submission path primarily checks `required`. The audit did not find complete
submit-time enforcement of `min`, `max`, `pattern`, or `multiple`. Unknown
field types can degrade to ordinary text input. That is especially unsafe if a
definition uses a secret-like type: a user may reasonably believe it has
special handling while it is rendered into an ordinary durable chat message.

**Required tests:** each supported constraint must reject a violating value at
submit; every unknown type must fail closed; secret values must never enter
message text, preview, logs, telemetry, or provenance.

### P0.8 — Visible command actions can be no-ops

The command/slash surfaces expose client actions including branch, search,
export, and run-task. The inspected `ChatPanel` action path handles only a
subset such as regenerate/clear. Server-provided actions also need workspace
scope and an action callback that are not consistently supplied by the live
chat composition.

A command that closes the palette without an effect is the same class of
truthfulness defect as a clickable-looking `<span>`.

**Required contract test:** enumerate every visible action and assert exactly
one of: it performs its advertised effect, it is visibly disabled with a
reason, or it is not rendered.

## 4. Orchestration and governance findings

### 4.1 Delegation is fragmented into transient text

The backend emits assignment, queue, and peer-query concepts. The chat client
handles only a subset of assignment events and turns them into generic system
rows. Peer-query and queued/unqueued events are ignored. Richer delegated-task
cards depend on CLI/harness text conventions that ordinary agent profiles do
not expose.

The result is neither a dependable group conversation nor a run tree. A reload
can lose the user's understanding of who delegated what, which subrun is still
active, and where its output went.

**Recommendation:** model delegation as durable typed events linked to
`chat_id`, parent run, child run/assignment, source agent, target agent, status,
timestamps, cost, and outputs. Render that same model live and after reload.

### 4.2 Approvals and escalations are not one user-decision model

Harbormaster approvals and sidecar escalations have separate paths. The known
debts from the work order remain material:

- trust-grant mutation lacks the comparable durable journal event;
- the agent waits 300 seconds for an escalation, then gives up, while the
  database row can remain `PENDING` forever;
- chat does not present a complete escalation lifecycle.

The product needs an explicit state machine such as `PENDING → ANSWERED |
REJECTED | EXPIRED | CANCELLED`, with actor, scope, deadline, and journal event
for every transition. Timeout behaviour must be a policy decision, not a
silent client fallback.

### 4.3 Progress and tool/source evidence are flattened

Run, journal, trace, tool, source/snippet/line/score, and cost data exist in
different layers, but chat often reduces them to generic blocks or text. A
modern orchestration chat should let the user answer:

- what is running now;
- which tool or agent is responsible;
- what is waiting for me;
- which sources support the answer;
- what it cost;
- which durable outputs were produced.

This does not require exposing hidden chain-of-thought. It requires typed,
auditable operational events and useful source metadata.

### 4.4 “Artifact” is currently a mutable file tab, not an artifact registry

The Zustand tab list and agent-files API provide convenient editing, but no
durable artifact identity, version history, content hash, producing run,
message, agent, source attachments, or publish/download provenance. Calling it
an artifact pane creates expectations the data model cannot meet.

Keep the file editor if useful, but reserve “artifact” for a managed output
record with immutable versions and explicit lineage.

### 4.5 Memory exists on disk without a dependable UI projection

The work order's WP-9 claim that `CREW.md` has no writer is false. The
`memory.write` capability supports the crew tier and resolves it to `CREW.md`.
The actual gap is downstream projection: MCP writes do not necessarily create
the `memory_versions`/journal records read by the panel, and the host audit
watcher's path parser covers agent `.memory` paths but not the shared
`shared/.memory` path.

The panel can therefore show an empty history while a real shared memory file
exists. Fixing this by adding another writer would duplicate capability and
leave the projection bug intact.

## 5. False or stale claims found in the work order and PRDs

These are findings, not editorial details. Each changes what an implementer
would build.

### 5.1 “CREW.md has no writer” — false

There is a crew-tier `memory.write` path. Rewrite WP-9 around missing audit and
version projection for shared memory, and test a real write through to what the
panel reads.

### 5.2 “Do not build routine from conversation; it is deferred” — stale

Routine authoring is partially shipped. `RoutineCreateDialog` opens a fresh
chat with an authoring goal, and the agent has native `save_routine`. The flow
tests immediate persistence. What is missing is the promised preview/confirm
boundary before mutation.

The correct question is not whether to add a routine-from-chat button. It is
whether all agent-mediated routine creation must produce a reviewable draft
before save.

### 5.3 “author_chat_id is a later follow-up” — stale for one path

The agent-mediated sidecar save path already sends/models `author_chat_id`.
Audit other creation paths for parity instead of re-adding the field.

### 5.4 “Chat attachments require a running crew” — not a stable invariant

The write first goes through the host/IPC storage path and can succeed without
a running container. Container write is a permission fallback. Whether a
stopped crew blocks upload depends on storage permissions and deployment state,
so the UI cannot truthfully use “crew running” as the sole precondition without
a deliberate backend contract.

### 5.5 “Composer persists drafts” — misleading on the live chat path

The store supports persisted draft values, but attachments are excluded from
`partialize`, and the audit did not find the chat flow consistently hydrating
and updating the stored text draft. Document capability and wired behaviour
separately.

### 5.6 “Attachment path/lifecycle is already correct” — false

Cold-chat upload, mutable same-name paths, best-effort metadata, and absent
canonical lifecycle disprove this. Generic file APIs exist, but that is not a
coherent attachment list/delete/version/garbage-collection contract.

### 5.7 `ask_forms` manifest round-trip — only partially complete

The current worktree adds significant manifest support, but two boundaries
need explicit tests:

- standalone resource kind `Agent` does not model the same form surface as the
  nested crew agent path;
- clearing can fail to converge if an empty value is omitted while a literal
  `ask_forms: "[]"` is normalised by the server to `NULL`, causing perpetual
  plan drift.

Do not mark WP-3 complete from a configured-value happy path alone.

## 6. Product fit: what to adopt, adapt, or defer

This is a product-fit recommendation based on Crewship's architecture, not a
claim of exhaustive competitor parity as of the audit date.

### Adopt natively

These make Crewship's existing value proposition stronger:

- reliable streaming, reconnect, cancellation, and explicit failed states;
- durable attachment and questionnaire identity;
- a typed run/tool/delegation/cost timeline;
- a persistent “Needs you” approval/question queue in chat;
- citations and source inspection with run provenance;
- managed, versioned outputs linked to their producing run;
- draft → review → confirm for creating routines or other durable resources;
- scoped and audited memory with a truthful history projection;
- keyboard access, mobile parity, and no silent result caps.

### Adapt to Crewship rather than copy literally

- **Projects:** Workspace/Crew/Agent already provide stronger scope. Do not add
  a competing project hierarchy; add chat views and filters over the existing
  one.
- **Canvas/artifacts:** make it a run-bound, versioned output workspace, not an
  unscoped scratchpad.
- **Conversation branching:** represent alternatives as run/message variants
  with shared ancestry and explicit output lineage.
- **Group chat:** use typed delegation and RBAC. Do not simulate agents as
  human chat participants using only formatted text.
- **Model picker:** keep the ordinary path policy-driven; expose provider/model
  overrides as an advanced control with cost/capability consequences.
- **Questionnaires:** use them as structured agent intake that also renders a
  readable message, not as a second unrelated forms product.

### Defer unless product evidence changes

- voice-first and image-generation playground features;
- anonymous public share links;
- a personality/GPT marketplace disconnected from crews and permissions;
- another top-level “Project” abstraction;
- raw hidden chain-of-thought display;
- UI that works only for harness-specific builtins unavailable to standard
  agent profiles;
- a consumer-style carousel of model brands as the primary mental model.

## 7. Target data flow

The smallest coherent architecture is:

```text
Browser
  -> create/ensure chat identity
  -> upload immutable attachment
       attachment_id + content hash + logical filename + lifecycle state
  -> submit message or form
       readable text
       + structured metadata
       + field-scoped attachment_ids
  -> materialise run inputs
       collision-free, read-only runtime paths
  -> agent run
       typed tool/delegation/approval/source events
  -> publish outputs
       artifact_id + version + hash + run/chat/message/agent provenance
```

Three concepts must remain distinct:

| Concept | Purpose | Identity/lifecycle |
|---|---|---|
| Attachment | User input to a conversation/run | Immutable ID and content; explicit reference/GC state |
| Runtime file | Materialised path visible inside one execution | Derived, replaceable, not the provenance authority |
| Artifact/document | Durable produced or curated output | Versioned record with creator/run/source lineage |

Paths are useful handles for an agent, but they must not be the sole durable
identity of uploaded evidence or generated output.

## 8. Recommended order of work

Order is by cost of leaving the defect, with telemetry placed before another
broad rollout but after immediate data-integrity repairs.

1. **Add a provider-backed, merge-blocking chat journey.** Cover first send,
   stream completion, reconnect/error, and persisted reload. This is the gate
   through which the following fixes must be proven.
2. **Remove truthful-UI P0s.** Fix cold attachment, artifact download, shared
   form upload fields, and every visible no-op action. Temporarily hide a
   control if its real contract is not ready.
3. **Make governance a durable state machine.** Expiry, answer semantics,
   audit actor/scope, and a persistent “Needs you” surface.
4. **Introduce canonical attachments and structured form submissions.** Do
   this before adding document intake, because intake otherwise inherits the
   same identity and provenance defects.
5. **Create one durable orchestration timeline.** Connect parent/child runs,
   delegation, tools, wait states, cost, sources, and outputs.
6. **Repair memory and artifact projections.** Shared memory write → journal →
   version history → panel; produced file → managed artifact version.
7. **Complete the silent-failure sweep.** Every swallowed error becomes a
   visible user state or an intentional, observable developer event.
8. **Repair PR-gating test integrity.** Provider-backed browser coverage and
   the repaired onboarding flow must run on the path that blocks merges.
9. **Instrument the resulting funnel before wider release.** The pre-chip
   baseline is already lost; establish a named post-repair baseline for chip,
   form, attachment, session, search, approval, and routine-review events.
10. **Ship one intake vertical slice only after the primitives are sound.** A
    real uploaded document should retain source identity through extraction,
    clarification, memory/write decision, and output. Defer a pack library
    until repeated use proves the need.

Mapping to the existing work order:

- WP-1 stays high and broadens to truthfulness/observable-state contracts.
- WP-2 remains mandatory but follows immediate corruption/data-loss risks.
- WP-3 is not complete until clear and standalone-agent convergence are pinned.
- WP-4 remains high and should become one governance-state package.
- WP-5 appears addressed in the current dirty worktree; verify, do not rebuild.
- WP-6 and WP-7 remain release-readiness work.
- WP-8 must be rewritten around canonical identity, not only three edge cases.
- WP-9 must be rewritten because its “no writer” premise is false.

## 9. Minimum acceptance suite

The next implementation should not be accepted without tests that prove these
behaviours at their real boundary:

1. Cold chat: attach before first send; reload; agent reads the same bytes.
2. Same filename, different bytes: both immutable identities resolve to their
   recorded hashes; no silent overwrite.
3. Attachment metadata failure: no successful orphan is left without a
   recoverable state.
4. Two upload fields: one file cannot satisfy or populate both.
5. Ask constraints: required/min/max/pattern/multiple fail correctly; unknown
   and secret types fail closed.
6. Structured form: reload preserves form ID/version, values, field attachment
   IDs, and rendered text without content-key collisions.
7. Artifact: open uses the download endpoint, shows exact bytes, saves a new
   version, and retains producing-run provenance.
8. Commands: every rendered action works or is explicitly disabled/absent.
9. Delegation: live and reloaded views show the same parent/child state and
   terminal result.
10. Escalation: timeout produces `EXPIRED`; agent behaviour is explicit; trust
    grant and revoke carry actor/scope journal records.
11. Crew memory: a real `memory.write` to crew scope appears in the version and
    journal projection consumed by the panel.
12. Routine authoring: no durable routine mutation occurs before review and
    explicit confirmation.
13. Navigation caps: a thirteenth agent cannot silently disappear.
14. Mobile: attach, form, approval, delegation state, and outputs remain
    operable at the chat breakpoint.
15. Provider-backed E2E: real server contract, real socket frames, persisted
    messages, explicit denied/error terminal state.

For every test, first demonstrate that it fails for the stated reason. A mock
that implements the client's assumption instead of the server's response is
not evidence.

## 10. Questions the owner must still answer

These cannot be settled by static code inspection:

1. Are `0 runs` on the observed routines page representative of real product
   use or only of `dev3`? If representative, activation outranks broad chat
   expansion.
2. Should an attachment be accepted while a crew is stopped? The answer must
   become one backend capability contract, not a permission-dependent accident.
3. On an expired user question, does the run fail, pause durably, use a defined
   default, or continue with an explicit warning?
4. Which mutations require preview/confirm: routines only, or all durable
   resources created through chat?
5. Which chat and run events are retained, for how long, and who may inspect
   sources, costs, tool inputs, and artifacts?

## 11. Handoff prompt for a second coding agent

Use this verbatim if helpful:

> Treat `docs/prd/CHAT-SURFACE-CODE-AUDIT-2026-08-13.md` as hypotheses, not
> truth. Verify or disprove each P0 directly against the current code and real
> handler contracts. For every confirmed P0, write a red test that fails for
> the claimed reason before proposing a fix. Do not add new chat features
> first. Separate safe short repairs (wrong endpoint, hidden/no-op controls)
> from canonical data-model work (immutable attachments, structured form
> submissions, durable orchestration events, versioned artifacts). Rewrite
> WP-8, WP-9, and the routine-from-conversation section where their premises
> are false or stale. Propose the smallest provider-backed browser E2E that can
> block a merge. In your report, list every claim in this audit that was wrong;
> disproving it is a successful audit result.

## 12. Completion definition for this audit

This document is complete as an **audit artifact** when a second reviewer can:

- reproduce or disprove each confirmed finding;
- see which companion claims must be corrected before implementation;
- distinguish immediate UI fixes from data-model changes;
- decide which modern-chat patterns fit Crewship's orchestration model;
- turn the priority order into separately claimable issues.

It does not declare the chat surface release-ready.
