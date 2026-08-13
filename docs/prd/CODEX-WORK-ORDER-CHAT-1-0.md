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

---

## 4. The PRD amendments — a required deliverable, not a courtesy

The two PRDs are behind the code in specific, listed ways. Produce a diff for
each, containing at least:

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
