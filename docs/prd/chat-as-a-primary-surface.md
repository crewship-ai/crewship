# PRD — Chat as a primary surface

**Status:** draft for review · **Author:** Pavel Srba · **Date:** 2026-08-12
**Scope:** make agent chat reachable, navigable and usable on a phone, using
code that is already written. Everything with a new data model is out.

**Companion:** `agent-ask-packs-and-document-intake.md` — prepared questions and
document intake. Step 7 here ships the cheap half of it; that PRD is the
follow-on, not a prerequisite.

**Wireframes:** four variants were drawn. The agent-tree variant is the
long-term direction and is **deliberately not in 1.0** — see §3.2.

---

## 0. The question that outranks this document

On `crewship-dev3` the routines page reads: **38 routines · 0 runs · never
invoked 38 · nothing scheduled · nothing ran in the last 7 days.**

If that is the state of the *product* and not just of a freshly seeded dev
clone, then the most expensive problem in Crewship is that the core loop never
starts — and improving chat is polishing the entrance to a building nobody has
walked through.

**Step 0, before any work below:** check invocation counts on a real workspace.

```
export CREWSHIP_SERVER=http://localhost:8082
/tmp/crewship-2-dev routine list --json | jq '[.[] | {slug, invocation_count}]'
```

- **If routines do run in real use** → this PRD proceeds as written.
- **If they do not** → this PRD is paused after Step 3 (the bug fixes), and the
  effort moves to activation. Nothing in Steps 1–3 is wasted either way; they
  are repairs.

This is not a formality. It is the one finding that could invalidate the rest,
so it is answered first.

## 1. The problem

Chat is not buried. As far as navigation is concerned it is **absent**.

- No `Chat` entry in either nav — desktop `navSections`
  (`components/layout/app-sidebar.tsx:35-69`), mobile `mobileNavSections`
  (`components/layout/app-toolbar.tsx:48-78`).
- No `/chat` route. Only `/chat/[agentSlug]`; a bare `/chat` 404s under static
  export, documented at `hooks/use-active-runs.ts:38-44`.
- **The onboarding wizard's final redirect targets a deleted route**
  (`app/(onboarding)/onboarding/page.tsx:440` → `/crews/agents/[id]/chat`; same
  dead target at `components/features/dashboard/welcome-checklist.tsx:88`, and
  two more in the chat page's own menu at `chat-page-client.tsx:450,456`). A new
  user's first attempt to reach their agent lands on nothing.
- Sessions are titled `Untitled session` forever, though `chats.title` has
  existed since the first migration
  (`internal/database/migrate_consts_v01_init.go:166`). Nothing writes it.
- ⌘K indexes entities and **not one message** (`components/command-palette.tsx:364-393`),
  while a BM25 conversation search sits unused in the backend
  (`internal/api/conversation_search.go:32-90`).
  **Corrected during implementation:** an earlier draft of this PRD said the
  route answers 503 unless wired. It does not — `WithConversationSearch` has
  been supplied at boot since `d6ab6e9f` (`internal/server/server.go:810-816`),
  and `NewRouter` has one production call site. What was missing was a *caller*
  and a *workspace scope*: the handler required an `agent_id`, which a global
  search cannot supply. The 503 branch is real but only reachable in a server
  constructed without the store, and there is now a test pinning both halves so
  the claim cannot drift again.
- `ChatPanel` has a **complete mobile mode with zero callers** — `mobilePanel`
  (`components/features/chat/chat-panel.tsx:76`, branches at `:445-514`). The
  live page hardcodes `gridTemplateColumns: "240px 1fr"` and is the only layout
  that never calls `useIsMobile()` (`chat-page-client.tsx:481-484`).
- Visiting `/chat/<slug>` can **create a session by arriving**
  (`ensureSession`, `chat-page-client.tsx:241-280`).

Every line above is a repair or a connection. That is the shape of this PRD.

## 2. Goals

- G1 — Chat is reachable in one click, on desktop and phone.
- G2 — A list of conversations is navigable, i.e. threads have names.
- G3 — Existing search is usable from the keyboard.
- G4 — No new tables, no new tenancy, no new security surface.
- G5 — Nothing here forecloses the deferred work in §5 and §6.

## 3. Scope

### 3.1 In

Steps 1–7 in §4. All of it is wiring, repair, or one column write.

### 3.2 Out, and why

Each of these was in an earlier draft and was cut on the merits. The reason is
recorded so it does not get re-argued from scratch.

| Cut | Why |
|---|---|
| ~~**Agent tree with Files / Memory folders**~~ **— REVERSED, see §3.3** | The cut read: `/crews` already browses an agent's memory, files and config, and a second IA for the same object is the disease this codebase has. That argument was about duplicating `/crews`. It missed the argument that decided it: every other surface in the product (`/routines`, `/issues`) uses this sidebar shape, and the flat list looked unfinished beside them. |
| **Document preview pane** | Requires serving agent-authored content inline (`internal/server/routes_files.go:179-181` is `attachment` + `octet-stream` **on purpose**). That is the riskiest work in the whole plan — stored XSS if done casually — for a case where Download already works. §6.3. |
| **Inline decision card** | The default rule set does not match `agent_run` (`internal/policy/approval_mode.go:25-30`), so approvals in chat essentially do not fire today. Building UI for a non-event is zero usage. Fix the policy first, then the card. §6.1. |
| **"Allow for 1 hour / always"** | Earlier draft claimed this was 80 % built. It is not: `waitpoint_trust_grants` is keyed `(workspace, pipeline_id, step_id, definition_hash)`. Chat has no such scope key, so this needs a **new grant scope** — design work, not wiring. |
| **Routine from this conversation** | The registered slash action is real (`internal/api/slash_commands_handler.go:60`) but the transcript→DSL step, which is the whole brain of it, does not exist anywhere. And more ways to create routines does not help a catalogue at 0 runs (§0). §6.2. |
| **Space / project switcher** | A switcher that only filters implies an isolation boundary it does not provide. A padlock icon that is not a padlock is worse than no icon. §6.4. |
| **Ask-pack library** | The workspace library with per-agent bindings is the right long-term model and is over-built for 1.0. Step 7 ships one textarea instead: 90 % of the value, 5 % of the cost, no migration. |

### 3.3 Back in: the agent tree

Overruled by the owner and built. The reason is consistency, not features: the
flat 240px session list was the only in-page sidebar in the product that was
not the shared shape, and `/chat` had a *second*, different left column of its
own — two left columns for one surface, neither of them the one `/routines` and
`/issues` use.

What shipped:

- **One column, `components/features/chat/chat-tree-sidebar.tsx`**, rendered by
  both routes and assembled from `components/layout/sidebar-kit` (280px, 44px
  collapsed; toolbar with search + filter + collapse; section headers with
  counts; `SidebarRow` selection, so the accent bar is the tokenised one).
- **STATUS facets** — All · Unread · Running · Done, counted from data that was
  actually fetched: `unread_count` and `ended_at` from `GET /agents/{id}/chats`,
  `status` from `GET /agents`. There is no **Needs you** facet: neither endpoint
  can answer it (escalations are a different read), and a facet whose number is
  a guess is worse than one that is not there. It arrives with the work in §6.1,
  which is where the escalation read belongs anyway.
- **Folders per agent** — **removed one day after they landed.** `Files`
  appeared twice in one view: in the tree and in the right rail beside it. The
  rule that settled it — left column navigates between objects, right panel
  holds the open object's context, configuration belongs to the object's own
  settings page — leaves only Sessions in the tree. Asks lives in the agent's
  config tab, Memory on the agent canvas, Files in the rail.
- ~~**Focus follows the route.**~~ **REVERSED, see §3.3.2.** `/chat/<slug>`
  briefly listed that agent alone, and clicking an agent was what put you
  there. Narrowing the tree is a local filter now, and the route narrows
  nothing.
- **Clicking an agent narrows the tree to it, and clicking it again widens it
  back out** — animated, local, and with no navigation in either direction.
  Expansion keeps its own controls (the chevron, and ←/→ on the row).
- **An agent with no conversations can still be talked to.** The row used to
  only toggle the disclosure, so an agent nobody had messaged was inert — no
  threads, no chevron, nothing on click — on the one surface whose job is to
  make starting a conversation easy. Since the plain click is the filter, this
  is an explicit **Start a conversation** row under a narrowed agent that has
  none. It is a `SidebarRow`, so it is tabbable and answers Enter/Space, and it
  POSTs nothing: a click is not a message.
- **STATUS facets count the agent the filter is on**, and the header says
  whose. A workspace-wide count over a list that can only show one agent's
  threads is a promise the click cannot keep. (The scope was the ROUTE's agent
  in the first cut; it is the filter's now, because the filter is the only
  thing that narrows the column.)
- **No new endpoints and no new route.** The folder is `?folder=`, never
  `/chat/<agent>/<folder>`: the static handler rewrites exactly one path level
  and the slug is read from `window.location.pathname`.
- **Unchanged on a phone.** The drawer and the tab strip from Step 5 are what
  the tree steps aside for; `sessions-sidebar.tsx` survives as the drawer's
  contents and nothing else.

What the original cut got right and is still true: this is not a second browser
for an agent. It navigates conversations, and the Files/Asks/Memory panes are
views onto what `/crews` administers, not a second place to administer it.

#### 3.3.1 Reverted: three of the four folders

`Files`, `Asks` and `Memory` are gone from the tree, one day after they landed.
The bullet above stands as written — this is what happened to it.

**Why.** `Files` was on screen twice at once, in the tree on the left and in
the chat's own right rail, in the same view. The other two were the same
mistake one step further away: `Asks` is the agent's configuration tab and
`Memory` is the agent canvas. The rule the owner settled it with, which now
governs this surface:

> **left column** = navigation between objects — *where am I going*
> **right panel** = context of the object I have open — *what is here*
> **configuration** = the object's own settings page

By that rule Sessions is navigation and stays; the other three were already
where they belonged, and the tree was a fourth door onto rooms that had three.
The paragraph above — "this is not a second browser for an agent" — was true as
a statement of intent and false as a description of what shipped. Sunk cost is
not a reason to keep a surface that duplicates two others.

**What was removed.** `components/features/chat/panes/` entirely (the three
panes, the shared `pane-shell`, the barrel and their tests), the `FolderPane`
branch of the chat page, `CHAT_FOLDERS` / `ChatFolder` / `folderHref` /
`onOpenFolder` / `activeFolder`, and `files/three-tier-files.tsx`, which the
Files pane was the only caller of. An agent now expands straight to its
threads: one step, not two.

**`?folder=` is dead, and does not linger.** It was a query parameter and not a
path segment, so nothing 404s — but a URL bookmarked while it worked would
otherwise keep carrying state nothing reads. The page scrubs it with
`replaceState` on mount and opens the conversation (any `?session=` in the same
URL is still honoured).

**Salvaged.** The pane distinguished three states the right rail did not:
loading, a failed fetch with a retry, and "we could not ask" as against "there
is nothing". That honesty moved into the right rail's Files tab before the pane
was deleted (`files/crew-files-scope.tsx`), which also fixed the lie it
replaced: the crew scope used to swallow a failed fetch into `[]` and render
"No shared crew files". The same treatment now covers the tree's own per-agent
thread lists, which had the identical defect in the product's primary
navigation — a 500 drew as "this agent has no conversations". Both go through
`components/features/chat/scope-fetch.tsx` so they cannot drift.

**Also changed, while the tree was open.** An agent with no sessions has no
chevron (an expander that opens onto nothing is a promise the tree cannot
keep), agents are ordered by most recent thread activity rather than
alphabetically, and the right rail's three icons carry the panel's name in the
tooltip, in the drawer's accessible name and in the open panel's own heading —
all read from one map, plus `aria-keyshortcuts` for the shortcut the tooltip
draws.

#### 3.3.2 Reversed: focus was a route, and is now a filter

The bullet above stands as written — this is what happened to it.

**What shipped first.** Clicking an agent in the tree navigated to
`/chat/<slug>`, and being on that route was what made the tree list that agent
alone. It was chosen for exactly one reason: the URL already said which agent
was in play, so narrowing cost no new state, survived a reload and travelled in
a shared link.

**Why it was wrong.** The whole page reloads. Every agent you looked at was a
route change, so the dashboard chrome tore down and rebuilt to change one name
in a list. The owner, watching it:

> clicking Riley hides the other agents; clicking Riley again brings them back,
> animated. It should be a filter.

Trading state for a page transition is a bad trade in one direction only, and
this was that direction: nobody can feel a `useState`, and everybody feels a
transition. "No new state" was an argument about the code, not about the
product.

**What it is now.** `filterSlug`, local to `chat-tree-sidebar.tsx`.

- Clicking an agent narrows the tree to it and unfolds it; clicking the same
  row again widens it back out. No router, no URL write, no fetch.
- Branches enter and leave under `spring.smooth` inside an
  `AnimatePresence mode="popLayout"`, so the agent that stays springs up into
  the gap instead of waiting on a height collapse. `prefers-reduced-motion`
  gets no layout animation, no travel and a zero-length transition.
- `activeAgentSlug` selects and auto-expands, and narrows nothing.
  `/chat/<slug>` lists every agent until the reader says otherwise.
- The **All agents** row is gone with the navigation it undid. Clearing the
  filter is a `Show all N` button in the AGENTS section header — one piece of
  local state, no destination.
- A typed search still reaches past the narrowing, and still suspends it rather
  than clearing it.

**What replaced the click.** The click used to be how you started a
conversation with an agent that had none. That is the **Start a conversation**
row in the bullet above.

**And the swap is in place too.** Using that row on an agent this page does not
already have open no longer navigates either. `chat-page-client.tsx` already
bypasses the Next router for the session — `history.pushState` plus a
`popstate` listener, because a router-driven param change re-evaluates the
layout subtree and visibly remounts the dashboard chrome on static-export
builds — and the slug now goes through the same path: push the URL, move the
local slug, re-resolve the agent out of the roster already in memory (no
request), and let the panel follow. Opening ANOTHER agent's thread from the
tree takes the same path, carrying the thread. **The page no longer calls the
router at all.** `/chat` still navigates for it, because there is no ChatPanel
on the index to swap an agent under (O7).

## 4. The steps

Seven steps. Each is independently shippable, independently revertable, and
ordered so that every step is useful even if the next one never happens. None
adds a table.

---

### Step 1 — Repair the dead paths

**Why:** a new user's first click after onboarding hits a 404 today. This is a
bug, not a feature, and it outranks everything else in this document.

**Do:** point `onboarding/page.tsx:440` and `welcome-checklist.tsx:88` at
`/chat/<agentSlug>`; fix the two dead menu targets in `chat-page-client.tsx:450,456`;
make `overview-tab.tsx:343` carry `?session=` like `agent-canvas-cards.tsx:29`
already does; delete the orphaned `components/features/agents/{sessions,settings,workspace,logs,tools}`
page clients that no route imports.

**Done when:** completing onboarding lands on a working chat; no route in the
app links to `/crews/agents/*`; the orphans are gone.

**Test:** Playwright — finish onboarding, assert a composer is on screen.
Vitest — a link-target test that fails if any component references a removed route.

**Size:** hours.

---

### Step 2 — Session titles

**Why:** every list of conversations built after this depends on threads having
names. `chats.title` exists and is never written.

**Do:** on the first user message of an untitled session, write a title —
client-derived, first ~60 characters, trimmed at a word boundary. Server-side
generation is out (§7, O4).

**Done when:** a new session shows its first message as its name in the sidebar,
after reload, and in the inbox item that `internal/chatnotify/notify.go` emits.

**Test:** Go table-driven over the derivation (empty, whitespace-only, emoji,
very long, a message that is only an attachment); Vitest for the rename
appearing without a refetch.

**Size:** hours.

---

### Step 3 — Stop creating sessions by arriving

**Why:** `ensureSession` (`chat-page-client.tsx:241-280`) can POST a session
merely because a page was opened. With chat one click from everywhere (Step 4)
this fires constantly and fills the list with empty threads.

**Do:** create on first send, not on mount. Keep the `?prompt=` handoff path,
which legitimately wants a fresh session.

**Done when:** opening chat and navigating away creates nothing — **and the
first send creates exactly one row.** Both halves, or this step trades a
sidebar full of empty threads for conversations that are never saved at all.

**Watch out:** `GET /api/v1/chats/{id}/messages` answers **200 with an empty
message list** for a chat that does not exist (`internal/api/proxy.go`,
`ChatMessages`; the CLI's `history --prompts`, `export` and `recap` read the
same endpoint and rely on it). It is therefore not a probe for existence, and
the first implementation of this step read it as one — the create was skipped
for every draft session, and with no `chats` row the WS channel authorizer
(`internal/ws/channel_auth.go`, `isSessionOwner`) refused the send outright:
nothing persisted, nothing titled, no error on screen. The panel now confirms
the row rather than inferring it — it creates it, or it has loaded real
messages for it — and the redundant create for a row that already exists is an
`INSERT OR IGNORE` that writes nothing.

**Test:** Go — POST count on the chats endpoint across a mount/unmount cycle.
Vitest — the first send POSTs, against a mocked server that answers the
history GET exactly as `proxy.go` does.

**Size:** hours. **Steps 1–3 are the "even if §0 says stop" set.**

---

### Step 4 — `/chat` exists and is in the navigation

**Why:** the product has no front door to its agents.

**Do:**
- Add `app/(dashboard)/chat/page.tsx`. **Do not** introduce a deeper route:
  the Go static rewrite handles one level (`internal/api/static.go:196-217`) and
  the slug is parsed from `window.location.pathname`
  (`chat-page-client.tsx:37-45`). Session stays a query parameter.
- `/chat` renders: the agent list, and beneath it recent threads across agents
  (`GET /agents` + per-agent `GET /agents/{id}/chats`, merged client-side and
  sorted by recency). No new endpoint.
- Add the nav entry to both `navSections` and `mobileNavSections`.
- The left column of `/chat/<slug>` keeps today's session list. It gets the
  shared chrome from `components/layout/sidebar-kit.tsx` — `SidebarSearch`,
  `SidebarSection` with counts, `SidebarRow` with the tokenised accent bar —
  so chat and `/routines` are visibly one system. **Chrome only; no facets, no
  tree.**

**Done when:** `/chat` resolves in a production static export, the nav entry is
present on both breakpoints, and a thread opens in two clicks from anywhere.

**Test:** Playwright on a built export (not dev) — nav → `/chat` → open thread →
reload → same thread. This must run against the real static build; the dev
server does not exercise the rewrite.

**Size:** ~2 days. **Risk:** the static export path is the one place this plan
can surprise us. Do it early.

---

### Step 5 — Mobile

**Why:** the mobile chat is written and unreachable. This is the cheapest large
improvement available.

**Do:** pass `mobilePanel` from the page; add the `useIsMobile()` branch that
every other layout has; add `accept="image/*" capture="environment"` to the
composer's file input on mobile viewports.

**Done when:** on a 390px viewport the sidebar is not a fixed 240px column, the
composer is reachable above the keyboard, and the camera button opens the
camera on a real phone.

**Test:** Playwright mobile viewport; manual check on one iOS and one Android
device — the capture attribute cannot be verified in a headless browser, and
pretending otherwise is how this ships broken.

**Size:** ~2 days.

---

### Step 6 — ⌘K finds conversations

**Why:** searching what was said is the second most repeated action in any tool
with history, and the backend is done.

**Do:** wire `WithConversationSearch` at boot (`internal/api/router_options.go:283-296`);
add a "Conversations" group to `components/command-palette.tsx`. The endpoint
requires `agent_id` (`internal/api/conversation_search.go:32-90`) — fan out
across the user's agents client-side and merge, or extend the handler to accept
an empty agent scope. **Decide before starting** (O2).

**Done when:** ⌘K on a phrase from a week-old conversation opens that thread.

**Test:** Go — BM25 ranking table, workspace scoping, a cross-tenant 404;
Vitest — the palette group renders and navigates.

**Size:** ~2 days, plus the O2 decision.

---

### Step 7 — Prepared questions, the cheap version

**Why:** chips are the highest-frequency new surface, because they replace
typing rather than adding a step. Today they are hardcoded per role
(`lib/agent-suggestions.ts:6-47`) and identical for every agent.

**Do:** one `suggested_prompts TEXT` column on `agents` (one question per line,
max 8, 120 chars each). Edited in a textarea in agent settings. `getSuggestions`
(`lib/agent-suggestions.ts:49-53`) prefers the agent's list and falls back to
today's role packs when empty, so nothing regresses for an unconfigured agent.

**Explicitly not:** a pack library, bindings, forms, templates or an API
surface. Those are the companion PRD, and they wait for a second user who wants
to share a pack.

**Done when:** two agents in one workspace show different chips, and an agent
with none shows today's defaults.

**Test:** Go — column read/write, cap enforcement, empty-line handling;
Vitest — precedence and fallback.

**Size:** ~1 day. One column is a migration but not a model; if even that is too
much, the same value can be had from `system_prompt` conventions — but a column
is honest and searchable, and this is where I would spend the one migration.

---

## 5. Order and why it is this order

1–3 are repairs and stand alone. 4 is the only structurally risky step (static
export) and therefore comes before the work that depends on it. 5 and 6 are
independent of each other and can run in parallel. 7 can ship any time after 2.

If time runs out at any point, the product is still better than before, and no
half-built surface is left behind. That is the property the earlier draft did
not have.

## 6. Deferred, with the condition that unlocks each

Not "later" — each of these has a trigger. When the trigger is true, schedule it.

### 6.1 Inline decision card
**Unlocked by:** approval rules that actually match chat runs
(`internal/policy/approval_mode.go:25-30`), **and** an expiry sweeper for
escalations — today a PENDING escalation lives forever while the agent gives up
after 300 s and continues without an answer (`internal/sidecar/query.go:339-366`).
**Then build:** a card in the conversation subscribed to the session channel;
escalations already carry `chat_id` and already broadcast
(`internal/api/escalation_handler.go:272-284`), and the blocking protocol
(`internal/api/escalation_waiter.go:93`) already parks and resumes correctly.
**Also fix when touching this:** `internal/api/pipeline_trust.go:139-157` grants
and revokes with **no journal entry**, while every comparable decision emits one
(`internal/harbormaster/store_mutate.go:222-277`). That is an audit gap today,
independent of any UI.
**Note:** `AskUserCard` (`assistant-turn.tsx:138-171`) renders options as
non-clickable spans for a tool agents are not even granted
(`internal/orchestrator/tool_profiles.go:12`). Fix it or delete it — it
currently renders a lie. **Resolved:** the special card was deleted. A legacy
or imported `AskUserQuestion` tool call renders only as an ordinary,
non-interactive tool record; no choices are presented as controls.

### 6.2 Routine from this conversation
**Unlocked by:** §0 answering that routines do get used.
**Then build:** the agent-mediated path — a prepared prompt into the session so
the bundled `routine-author` skill and `save_routine`
(`internal/sidecar/routine_mcp.go:90`) do the authoring — plus a review step
before it becomes a cron job. Three known defects to fix with it:
`chat-panel.tsx:589` passes no `workspaceId` so no server action ever appears;
`SlashActionModal` is imported by nothing; the payload posts
`{name, cron_expr, timezone}` to an endpoint that requires a target routine
(`internal/api/pipeline_schedules.go:560`). Add `author_chat_id` to
`userSaveRequest` (`internal/api/pipelines_crud.go:547`) — the store already
writes the column and the sidecar already stamps it
(`internal/sidecar/pipelines.go:174`).

### 6.3 Document preview
**Unlocked by:** a decision on where agent-authored content is rendered.
**Then build:** an inline-disposition endpoint with a sniffed, allowlisted
content type, rendered in a `sandbox`ed iframe **without** `allow-same-origin`
or from a separate origin, `nosniff`, no cookies. Phase 1 images/text/pdf,
phase 2 docx/xlsx, always an honest "preview not available → download".
Telemetry from `preview_unsupported{ext}` is the only sane input to phase 2.

### 6.4 Space / sharing
**Unlocked by:** the owner deciding whether they want a *filter* or an
*isolation boundary*. These are a week and a quarter respectively.
**If a filter:** build it on `chats.visibility` (v118) plus `chat_participants`,
which already model private and shared. **Do not** call it Project — `projects`
is a portfolio object with status, health and target dates
(`internal/database/migrate_consts_v33_v41.go:169-189`).
**If a boundary:** it is a real container and every `workspace_id`-keyed query
is in scope. Say so out loud before starting.

### 6.5 "Remember this" and citations
`POST /api/v1/memory/write` does not exist, and
`internal/api/slash_commands_handler.go:84-91` explains it is missing on
purpose. The HITL verifier it needs is fully written and entirely unwired
(`internal/memory/verifier.go:44-56`). Citations with `file:line` need the
indexer to stop discarding chunk line ranges (`internal/memory/index.go:121-126`).
**Cheap interim, worth doing any time:** render `memory.search` results as a
source list instead of a `Result (N chars)` blob — the file name is already on
the wire.

## 7. Forward compatibility: group threads with agents

Wanted, deferred, and **not to be foreclosed**. The groundwork exists:
`chats.visibility` and `chat_participants` (v118), author attribution already
resolved (`chat-panel.tsx:245-274` → `turn-renderer.tsx:47`), `@mention`
autocomplete, participant-shaped notification fan-out
(`internal/chatnotify/notify.go`), and `POST /chats/{id}/participants` with no
frontend caller. Two things block it, neither of them UI: `chats.agent_id` is
single-valued and NOT NULL, and `chat_participants` knows only `user_id`.

**Binding on the steps above — the free half only:**
- Do not name new components or props "the agent of this thread". Assume
  "user or *an* agent" wherever authorship is rendered.
- Do not add constraints to `chat_participants` that assume a human.
- Keep the mention endpoint's shape able to return agents.

**Explicitly not binding:** routing every read through a participant list while
that list is always length one. That is indirection for a deferred feature; the
eventual migration touches those call sites anyway.

**Two consequences to design for when it is built, because they are expensive
to retrofit:** a mention must wake **only** the mentioned agent, not every agent
in the thread; and an agent added to a thread reads everything said before it
arrived — that join must appear in the transcript as a visible event, not a
silent dropdown.

## 8. Open questions

1. **O1** — §0: are the 0 runs a dev-clone artefact or the product's real state?
   Everything else is downstream of this.
2. **O2** — Step 6: fan out conversation search across agents client-side, or
   extend the handler to accept an empty agent scope?
3. **O3** — Step 4: does `/chat` show recent threads, or the agent list first?
   Ship threads-first, measure, add a preference only if the data asks.
4. **O4** — Step 2: client-derived titles, or agent-generated ones? The latter
   reads better and costs a model call and a privacy conversation.
5. **O5** — Nav label: *Chat*, *Talk*, *Conversations* — and the Czech string.
6. **O6** — Does `/chat/[agentSlug]` stay a separate page, or become `/chat`
   with a preselected agent? Deep links from notifications must keep working
   either way (`internal/chatnotify/notify.go:155`).
7. **O7** — One WebSocket per mounted panel today (`chat-panel.tsx:150-156`),
   separate from `RealtimeProvider`. Does `/chat` mount a panel at all before a
   thread is chosen? Ship "no", revisit if the first message feels slow.

## 9. Testing standard

Per `CLAUDE.md`: test first, and a fix is red → green with the test failing on
current `main`. Two rules specific to this work:

- **Step 4's acceptance test runs against the static export**, not the dev
  server. The rewrite is the risk; a dev-server test would pass while production
  404s.
- **Step 5's camera attribute is verified on real hardware.** A headless
  assertion that `capture="environment"` is present in the DOM proves nothing
  about whether a phone opens the camera.

Every new endpoint gets a CLI command and the acceptance test drives the binary
(`cli_route_contract_test.go` enforces the pairing). Steps 1–7 added one
endpoint, `PATCH /api/v1/agents/{agentId}/chats/{chatId}`, for session titles;
its CLI pairing and route-roles manifest were added with it. The remaining
steps add no endpoint, so the rule applies again to work from §6 onward.

## 10. Shipped constraints and implementation findings

- Chat attachments require a running crew. A stopped crew returns 409 because
  the sidecar is the writer for the agent-visible output directory.
- This page switches to its compact layout below 900 px, not at the global
  768 px mobile breakpoint. At `/chat` no conversation panel is mounted before
  a thread is chosen, so the route opens no chat WebSocket merely by arriving.
- Silent failure is a recurring defect class on this surface: a rejected
  top-level WebSocket frame was previously discarded because it was not a
  `chat_event`, leaving the optimistic user turn thinking forever. Such frames
  now terminate the pending send and render the server's reason.
- Filesystem permission checks must use `errors.Is(err, fs.ErrPermission)`.
  `os.IsPermission` does not recognize the wrapped error returned by `localfs`
  and silently disables the container-write fallback.
- A mock must reproduce the server contract, not the client's assumption.
  The first-send test once had its `ChatPanel` stub perform the POST and used a
  fake history response to infer row existence; the real server returns an
  empty message list for an unknown chat, so the passing test bypassed the
  broken implementation.
