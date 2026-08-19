# Work order — chat surface, release 1.0

**Audience:** an external coding agent (OpenAI Codex or equivalent) working this
repository unattended.
**Baseline:** branch `feat/chat-primary-surface`, 18 commits ahead of `main`.
**Companions:** `chat-as-a-primary-surface.md` (the plan),
`agent-ask-packs-and-document-intake.md` (questionnaires and intake).
**Deliverable:** working code **and** amendments to those two PRDs. The
documents are as much the product as the code, and they are currently behind it.

---

## 0. The one rule

> **A test you cannot break on purpose is not evidence.**

This branch has already produced three tests that passed while the feature was
broken:

- `session-on-first-send.test.tsx` mocked `ChatPanel` away and had the stub
  perform the POST, so the code that was broken never ran — against a fake
  server that answered the way the client *assumed* rather than the way
  `internal/api/proxy.go:263-286` actually answers. Chat sessions were not being
  persisted at all, and the suite was green.
- `e2e/onboarding-wizard.spec.ts` waited for `/crews/agents/`, a route deleted
  months ago, so it passed **only while the bug was present**.
- The `os.IsPermission` guard question: nothing pinned `errors.Is(err,
  fs.ErrPermission)` for the error `localfs` actually returns, and the whole
  container-write fallback hangs on that single boolean.

So for every change below: write the test, watch it fail for the *right reason*,
then fix. When a test passes before your change, say so and keep it as a
regression guard — do not manufacture a failure to make the work look larger.
Three such honest declarations already exist on this branch; match that standard.

## 1. Distrust this document

Several claims in the two PRDs turned out to be false when checked:

- "the conversation-search route answers 503 unless wired" — it had been wired
  since `d6ab6e9f`; what was missing was a caller.
- "`pins.md` is unreachable" — it is reachable.
- "`waitpoint_trust_grants` makes the chat approval scope 80 % built" — the
  grant is keyed to a routine step; chat has no such scope.

Every claim below carries a file:line. **Open it before you act on it.** If a
claim is wrong, the finding *is* the deliverable — report it and stop that
package rather than building on a false premise.

Verification commands:

```bash
export CREWSHIP_SERVER=http://localhost:8082
npx vitest run                                   # 429 files / 5097 tests, green
npx tsc --noEmit                                 # clean
go build ./...                                   # clean
go test ./internal/api/... -timeout 40m          # ~10 min; the default 10m timeout is NOT enough
sqlite3 crewship.db "SELECT id,title,origin FROM chats ORDER BY created_at DESC LIMIT 5;"
grep -o '"msg":"file save[^"]*"[^}]*' /tmp/crewship-2-go.log | tail
```

`go test ./...` with default flags will report a **false FAIL** on
`internal/api`: the package needs more than the per-package 10-minute default.
The Makefile uses `-timeout 40m` for this reason.

## 2. What already shipped on this branch

So you do not rebuild it: `/chat` route and both navigations; session titles
(`chats.title` was never written before); sessions created by sending rather
than by arriving; ⌘K over the existing BM25 conversation search, widened to
workspace scope; per-agent `suggested_prompts` and `ask_forms` with a Go+TS
renderer pinned to one golden fixture; the questionnaire sheet in the composer;
the shared `sidebar-kit` tree with a local agent filter; mobile layout and
camera; attachment paths reaching the agent in the message; and the fix for
attachments never being saved at all on a provisioned crew.

---

## 3. Work packages

Ordered by **cost of leaving them**, not by effort.

### WP-1 — Silent failure is the house defect. Find the rest.

Four failures on this branch were invisible to the user *and* to the tests, and
they were invisible for the same reason: a `catch {}`, a swallowed frame, or an
error mapped to an empty result.

Known instances, all verified:

| Where | What it swallows |
|---|---|
| `hooks/use-chat.ts:1160` | every WebSocket frame whose `type !== "chat_event"` — including `access denied`, which is why a denied send showed "…is thinking" forever |
| `chat-panel.tsx` `ensureSession` (pre-fix) | `catch { /* ignore */ }` around the row create |
| `three-tier-files.tsx:67` (deleted) and its twin in the tree fan-out | a failed fetch became `[]`, rendering as "nothing here" |
| the auto-title PATCH | silent by design, which is correct — but it was silent about a 404 caused by a *missing row*, which is not |

**Task.** Sweep the chat surface and the API client layer for the same shape:
`catch {}` with no state change, `r.ok ? … : []`, `?? []` over a failed fetch,
and any mapping that makes "we could not ask" indistinguishable from "there is
nothing". For each: decide whether the user must know, and if not, whether a
developer must (a log, a metric). Produce a list even where you do not change
the code.

**Acceptance:** every instance is either fixed, or listed with a one-line
justification for staying silent. A test for each fixed one, asserting the
distinguishable state — not just the absence of a crash.

### WP-2 — Telemetry was lost, not deferred

`agent-ask-packs-and-document-intake.md` §12 says: *instrument before shipping
so the comparison exists.* Chips shipped per-agent with **no instrumentation at
all** (grep `ask_chip_` → nothing), so the PRD's "≥ 35 % of sessions start from
a chip" is now unmeasurable against a baseline that no longer exists.

The staging note deferred the *library*. It did not defer the *measurement*, and
that distinction is the reason this is a work package rather than a footnote.

**Task.** Find how this product emits product telemetry today (start from the
journal — `internal/journal/types.go` — and from whatever the frontend already
reports; do not invent a second mechanism). Then instrument, at minimum:
chip shown / clicked, form opened / submitted / abandoned (with the last-touched
field, which is where a bad form shows itself), attachment uploaded / failed,
session titled, ⌘K conversation hit opened.

**Acceptance:** a named event per interaction, documented in the PRD, and one
honest paragraph in §12 saying the pre-change baseline is gone and what we will
compare against instead.

### WP-3 — `ask_forms` will be dropped by `crewship apply`

`suggested_prompts` had exactly this bug and it was fixed on this branch
(`internal/manifest/{schema,export,plan,validate}.go`, plus a follow-up PATCH
because `POST /api/v1/agents` does not model the column and `readJSON` ignores
unknown keys). **`ask_forms` shipped immediately afterwards with the same gap.**

`buildAgentPostCreateBody` in `internal/manifest/plan.go` was deliberately left
as the single hook for exactly this.

**Task.** Same treatment: spec field, exporter, body builder, diff, create
follow-up, validation mirroring the Go caps in `internal/askforms`.

**Acceptance:** an export → apply round-trip onto a *fresh* server preserves
both columns byte-for-byte, including the empty case producing no spurious
diff. Table-driven, red first.

### WP-4 — Two audit debts that predate this branch and are still open

Both were verified twice, most recently against the current tree.

**(a) Trust grants emit no journal entry.** `internal/api/pipeline_trust.go`
grants and revokes with only a `broadcastInboxUpdated`, while every comparable
decision emits one (`internal/harbormaster/store_mutate.go:222-277`,
`AfterDecide`). It is the only decision in the system with no audit trail, and
it is the mechanism a future "always allow" in chat would be built on.

**(b) Escalations never expire.** The agent-side wait gives up after 300 s
(`internal/sidecar/query.go:326`) and proceeds *without an answer*; the row
stays `PENDING` forever. The only `UPDATE escalations SET status` sites are the
human resolve and a narrow credential auto-resolve. There is no `EXPIRED` state.

**Task.** (a) emit on grant and revoke, matching the `AfterDecide` shape
(actor, scope refs, payload). (b) add the sweeper, and decide what the agent
should be told — proceeding silently after a timeout is a product decision
nobody has made explicitly.

**Acceptance:** for (a), a test that fails if the emit is removed — asserting
the entry's actor and refs, not a count. For (b), the state machine's timeout
transition, and an explicit answer in the PRD for what the agent does when its
question expires.

### WP-5 — `AskUserCard` renders a lie

`components/features/chat/assistant-turn.tsx:138-171` renders an agent's
question as option pills that **look clickable and are `<span cursor-default>`
with no handler**. The tool behind them, `AskUserQuestion`, is not granted to
agents at all — `internal/orchestrator/tool_profiles.go:12` names it as a
harness builtin with no Crewship backing, and `tool_profiles_test.go:23` asserts
it is absent from every profile.

**Task.** Either wire it to the escalation protocol that genuinely works
(`internal/sidecar/query.go:201` → `internal/api/escalation_waiter.go:93`, which
already carries `chat_id` and already broadcasts on the session channel), or
delete the card. Do not leave a control that cannot be operated.

**Acceptance:** if wired — an answer reaches the waiting agent, proven end to
end. If deleted — nothing renders those parts as interactive, and the PRD
records why.

### WP-6 — Undocumented silent caps

`chat-tree-sidebar.tsx` caps the fan-out at **12 agents × 10 chats → 25 rows**;
`internal/api/conversation_search.go` caps workspace search at **400 agents**.
A workspace with thirteen agents silently loses threads from its primary
navigation, and nothing on screen or in the docs says so.

**Task.** Decide per cap: raise it, paginate, or say so on screen. The PRD's own
rule is *no silent caps — log what was dropped*, and none of these do.

**Acceptance:** each cap is documented, and where it can bite, visible.

> **Corrected 2026-08-18 — half of this premise was wrong, and the wrong half
> is worth more than the right one.**
>
> "None of these do" swept in every cap on the surface. Opened one at a time,
> most of them already refuse loudly and name the offender, and a `400` that
> says *which* prompt was too long is not a silent cap at all:
>
> | Cap | Value | Boundary behaviour | Verdict |
> |---|---|---|---|
> | Chat attachment size | 25 MB | `400 invalid multipart form or file too large (max 25MB)` — `internal/api/proxy_attachments.go:115-118`; the browser rejects and names the file first (`composer/attachment-zone.tsx:20`) | **Not silent** |
> | Suggested prompts / length | 8 / 120 runes | `400`, naming the line: *"prompt 3 exceeds 120 characters (it has 137)"* — `internal/api/agents_suggested_prompts.go:25,29,55-56` | **Not silent** |
> | Ask forms / fields / label / template | 4 / 6 / 48 / 2000 | `400` naming the form and the placeholder; nothing is written — `internal/askforms/forms.go:43,47,51,53,225-226` | **Not silent** |
> | Workspace search | 400 agents | Newest 400 searched, the rest omitted — `internal/api/conversation_search.go:67,110` | **Documented, unlogged** |
> | Tree fan-out | 12 agents × 10 chats → 25 rows | Nothing — `chat-tree-sidebar.tsx:212,215,218`, sliced at `:299` | **Genuinely silent** |
>
> So the documentation debt is **one cap wide, not four**. The 400-agent cap was
> already documented twice on this branch (`docs/api-reference/conversations.mdx`,
> `docs/guides/conversation-search.mdx`); what it still lacks is an operator log
> line, because a user cannot act on it and an operator asking "why did search
> not find it" absolutely must be able to.
>
> The fan-out is the one that needs a screen affordance, and it is worse than
> "loses threads". An agent past the twelfth enters neither `threadsByAgent`
> **nor** `threadErrors`, so it misses the honest-failure path the same file
> already implements: a failed fetch renders an em dash and a `ScopeFailure`
> panel saying *this is not an empty history*, while the thirteenth agent
> renders a confident `0` with no chevron, and filtering to it offers **Start a
> conversation** with an agent that may have a year of them. Same "we could not
> ask" → "there is nothing" collapse WP-1 names as the house defect, in the
> product's primary navigation.
>
> Documented since, in `docs/guides/chat-surface-limits.mdx`. The affordance is
> still owed.

### WP-7 — Test integrity

- `e2e/onboarding-wizard.spec.ts` is repaired but runs **only** in a nightly /
  on-demand job, never on a PR. The flagship repair of this branch is therefore
  unguarded on the path that would catch a regression.
- Eight further specs referencing the deleted `/crews/agents/*` family sit in
  `playwright.config.ts`'s `testIgnore` and the nightly DRIFT bucket. That
  bucket exists to keep the rot visible; decide deliberately whether each spec
  is repaired or deleted, and do not "fix" them by widening the ignore list.
- CI asserts `out/chat.html` exists after the build. That is the cheapest
  honest check and it was proven by deleting the file — but it is not a browser
  test. §9 of the chat PRD asks for one against the built export.

**Task.** Make the onboarding flow's assertion run somewhere that blocks a
merge, or state in the PRD that it does not and why that is acceptable.

### WP-8 — Attachments: three unfinished edges

1. **A chat attachment now requires a running crew** (409 with an actionable
   message). That is a real behaviour constraint introduced by the fix, and it
   deserves a product decision rather than a status code: should the composer
   refuse the file up front when the crew is stopped?
2. **`stores/composer-store.ts` persists drafts but not attachments** — a reload
   keeps the text and loses the file references, and those references are now
   the only way the agent hears about the file (`lib/attachment-message.ts`).
3. **No listing or delete endpoint for chat attachments.** The blobs live
   outside the content-addressed tree and outside the reclaim machinery
   (`internal/api/attachments_gc.go` says so deliberately).

> **Corrected 2026-08-18. All three premises moved; two of them were wrong when
> written.** Kept rather than deleted, because each was wrong in a way a reader
> can learn from.
>
> **(1) is not an invariant, it is a consequence of who owns a directory.**
> I wrote "requires a running crew" into a commit message and the sentence
> travelled into two PRDs. The upload does not ask whether a crew is running.
> It writes host-side first (`internal/server/routes_files.go:221-236`), and
> only an `fs.ErrPermission` sends it through the container
> (`saveViaContainer`, `:307`), where a stopped crew becomes the `409`
> (`containerSaveErrorResponse`, `:614-624`). The host write fails because
> `fixBindMountOwnership` chowns the crew's trees to `1001:1001` at container
> creation (`internal/provider/docker/docker_container.go:1146-1154`) while
> `crewshipd` runs as another uid — and when that chown itself fails, the same
> function falls back to `chmod 0777` (`:1196-1199`), after which a host write
> succeeds with the crew stopped. A crew that was never provisioned has no such
> owner either. So the truthful statement is: **an upload needs a writable
> output tree; when the crew runtime owns that tree, that means a running
> crew.** The product decision (should the composer refuse up front?) still
> stands, and it now has a precondition the frontend cannot read from any
> endpoint — which is the actual work.
>
> **(2) is right about attachments and wrong about drafts.** `partialize`
> really does persist `drafts` (`stores/composer-store.ts:209`) and really does
> drop the `File` handles (`:32-35`). But **`setDraft` has no caller anywhere in
> the app** — the composer holds its text in `useState`
> (`components/features/chat/composer/chat-composer.tsx:107`) and only ever
> calls `clearDraft` (`:117,147`). The persisted draft map is written by nothing
> and cleared on send. So a reload loses the text *and* the file references;
> "keeps the text" describes a capability the store has and the chat path never
> uses. Fixing (2) is two jobs, not one, and the cheaper job was invisible
> because the store's type signature implied it was already done.
>
> **(3) is stale — it shipped.** `GET …/chats/{chatId}/attachments` and
> `DELETE …/attachments/{attachmentId}` are registered at
> `internal/api/router_orchestration.go:743-744`, with
> `crewship chat attachments list` / `delete` (`cmd/crewship/cmd_chat.go:382,452`)
> and doc sections in `docs/api-reference/agents.mdx`. The blob layout changed
> underneath it: the storage key is now
> `attachments/<chatId>/<attachmentId>/<filename>`, so an upload owns its own
> directory and a delete unlinks its own bytes with nothing to arbitrate
> (`chatAttachmentUnlinkTarget`, `internal/api/proxy_attachments.go:410-416`).
> The reclaim exclusion in `internal/api/attachments_gc.go` now needs a *new*
> justification: it was excluded because nothing could delete these blobs, and
> something can.

### WP-9 — The memory surface tells the truth about itself; the older one does not

The chat panes built and then deleted on this branch established which memory
tiers are actually readable. What remains: **`crew:<crewId>/CREW.md` has no
writer at all**, so the existing panel at
`components/features/crews/agent-canvas-tabs/memory-tab.tsx` queries a path
nothing ever writes and renders permanent emptiness as though the crew shares
nothing. `parseMemoryPath` (`internal/memory/audit_watcher.go:449`) matches only
`crews/{id}/agents/{slug}/.memory/…`, while CREW.md lives at
`{base}/crews/{id}/shared/.memory/` (`internal/orchestrator/memory_persona.go:61`).

**Task.** Either give that tier a writer, or make the panel say what is missing.
`daily/`, `lessons.md` and `learned-*.md` have related gaps — enumerate them
honestly in the PRD rather than fixing them blind.

> **Corrected 2026-08-18 — the premise is false, and the task it implies would
> have made things worse.**
>
> `CREW.md` has a writer. Two, in fact. The native dispatcher's `memory.write`
> takes tier `CREW` and resolves it to `CrewMemoryDir/CREW.md`
> (`internal/memory/tools.go:1156-1160`, with the crew cap at `:66`), and the
> legacy sidecar route `POST /memory/write` accepts `scope: "crew"` with
> `CREW.md` in its allowlist (`internal/sidecar/memory_write.go:20-38,66-69`).
> Building a third writer — the obvious reading of "give that tier a writer" —
> would have duplicated a capability and left the real defect untouched.
>
> **The real defect is the projection.** The panel reads version rows at the
> path `crew:<crewId>/CREW.md`
> (`components/features/crews/agent-canvas-tabs/memory-tab.tsx:388`). Nothing
> in the tree ever writes a row at **that** path — which is the trap, because
> the `crew:` prefix is a real convention with real producers: the consolidator
> writes `crew:<crewID>/pins.md` and `crew:<crewID>/learned-*.md`
> (`canonicalAuditPath`, `internal/consolidate/consolidator.go:707-712`). The
> panel's guess was a reasonable extrapolation from a convention that happens
> not to cover this file, which is exactly why nobody checked it.
>
> The audit watcher is the only producer of direct-write version rows, and
> `parseMemoryPath` requires the shape `crews/{id}/agents/{slug}/.memory/…` —
> `parts[2] != "agents"` is a hard reject
> (`internal/memory/audit_watcher.go:449`) — while the shared tier lives at
> `crews/{id}/shared/.memory/CREW.md`
> (`internal/orchestrator/memory_persona.go:61-63`). Its canonical path is also
> built as `agent:{slug}/{rel}` (`:471-473`), a shape `crew:` can never match.
> So a real crew-scoped write lands on disk, is read back correctly by the
> agent, and is invisible to every version and journal surface.
>
> **Rewritten task.** Make the shared tier parse: extend `parseMemoryPath` to
> the `shared/.memory` shape and emit `crew:{crewID}/{rel}`, then prove a real
> `memory.write` with `tier: "CREW"` reaches the rows `memory-tab.tsx` reads.
> The acceptance test is end-to-end through the writer, not a unit test of the
> parser — a parser test would pass against a path no writer produces, which is
> the same mistake one level down.
>
> **In flight as this correction was written.** `internal/memory/projection.go`
> and a shared-tier watcher test are in the working tree at `ef815a5f`,
> unfinished by anyone who signed this. The correction above is a reading of
> the committed code; check the tree before acting on it.
>
> The three related gaps stand as written and were enumerated on the branch:
> `daily/YYYY-MM-DD.md` is recorded but not enumerable (the member endpoint
> takes one exact path; prefix listing is admin-only), `lessons.md` has no row
> and no endpoint because `parseMemoryPath` does not map it at all, and
> `learned-<topic>.md` is recorded with undiscoverable topic names. Also
> corrected while here: `pins.md` **is** reachable, contrary to the brief that
> started this — with the caveat that the consolidator skips recording when
> `BlobRoot` is empty, so the file can exist on disk with no row.

---

## 4. The PRD amendments — a required deliverable, not a courtesy

The two PRDs are behind the code in specific, listed ways. Produce a diff for
each, containing at least:

> **Delivered 2026-08-18**, against `ef815a5f`. Item 1 below undercounted: the
> branch added **five** endpoints, not one. Steps 1–7 added exactly one (the
> chat rename); the audit follow-ups added four more — the two attachment
> lifecycle routes from WP-8(3) and the two escalation routes from WP-4(b).
> Every one has a CLI command; the four mutating ones have a `route-roles.txt`
> row and the read-only listing correctly has none, because the manifest covers
> `mutationRoutes` (`internal/api/route_roles_manifest_test.go:29-41`). See
> `chat-as-a-primary-surface.md` §9.

**`chat-as-a-primary-surface.md`**
1. §4 Step 4 claims "Steps 1–7 add no endpoints". They added one:
   `PATCH /api/v1/agents/{agentId}/chats/{chatId}`. Correct the claim and record
   that the CLI pairing and route-roles manifest were honoured.
2. §3.3 has been amended twice by hand (folders removed; focus became a filter).
   Fold the history into one coherent section — the current text reads as an
   argument with itself.
3. Record the **behaviour constraints** introduced: a chat attachment requires a
   running crew; the compact breakpoint on this page is 900 px, not the global
   768; `/chat` mounts no panel and therefore no socket.
4. Add a section for what this branch **discovered rather than built**: the
   silent-failure class in WP-1, the `os.IsPermission` trap, the mock-disagrees-
   with-server class. These are the findings most likely to be re-encountered.

**`agent-ask-packs-and-document-intake.md`**
1. §7's example uses `{{currency}}`; the shipped renderer reserves
   `<field>_currency` because the bare form cannot survive save-time validation
   and collides with two money fields. The staging note records this — promote
   it into §7 itself, where a reader will look.
2. §12 telemetry: see WP-2. Say plainly that the baseline is gone.
3. §8 (intake) is untouched and remains the largest unbuilt piece. Restate its
   entry condition: it needs `intake_documents`, the two sidecar tools and a
   memory write path, and it should not start until someone actually files
   documents.
4. §5.6 was added by the upload-failure work. Check it against what shipped.

## 5. Do not

- Do not make `GET /chats/{id}/messages` return 404 for an unknown chat to
  "simplify" the client. It is a documented public response with three CLI
  callers (`cmd_history.go`, `cmd_export.go`, `cmd_recap.go`) and an OpenAPI
  contract. The client was wrong; it has been fixed.
- Do not replace `errors.Is(err, fs.ErrPermission)` with `os.IsPermission` — it
  returns false through a `%w` chain and would silently disarm the container
  write fallback. There is a test pinning this; read its comment.
- Do not introduce a deeper chat route (`/chat/<agent>/<thing>`). The static
  export rewrites exactly one path level (`internal/api/static.go:196-217`).
- Do not build the pack library, the intake pipeline, the decision card, the
  document preview or the routine-from-conversation button. Each is deferred
  **with a stated unlock condition** in §6 of the chat PRD. If you believe a
  condition is met, say so and stop — that is a decision for the owner.
- Do not delete a test to make a failure go away. Rewrite it to pin the new
  behaviour and say in your report which ones you changed and why.

## 6. How to report

Per work package: the claim you verified and how; what you changed; the test
that failed first and its failure message; what you deliberately did not do.

State plainly which claims in this document turned out to be wrong. That
section is the most valuable thing you will write, because this document was
assembled by the same process that produced the defects it lists.
