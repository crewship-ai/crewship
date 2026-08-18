# PRD — Chat as a primary surface

**Status:** draft for review · **Author:** Pavel Srba · **Date:** 2026-08-12
**Scope:** make agent chat reachable, navigable and usable on a phone, using
code that is already written. Everything with a new data model is out.

**Companion:** `agent-ask-packs-and-document-intake.md` — prepared questions and
document intake. Step 7 here ships the cheap half of it; that PRD is the
follow-on, not a prerequisite.

**Wireframes:** four variants were drawn. The agent-tree variant is the
long-term direction and is **deliberately not in 1.0** — see §3.2.

> **Amended 2026-08-18, against `ef815a5f`.** Steps 1–7 have shipped, and this
> document has been brought back in line with the code that was written against
> it. Nothing is deleted to make the plan look clean: where a decision was
> reversed, both arguments are kept and the losing one is struck through, and
> where a claim was false it is corrected in place rather than removed. §10 is
> the behaviour this work introduced and §11 is what it discovered rather than
> built — §11 is the section most likely to save the next person time. Every
> claim added in this pass carries a file:line that was opened before it was
> written.

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

**How to read this section.** It is one decision with two corrections applied
on top of it, not three competing accounts. The list below describes **what is
on screen today**. Two of its bullets are the *outcome* of a reversal, and each
reversal is told once, in full, in §3.3.1 and §3.3.2 — the original argument,
what beat it, and what was removed. The history is kept because both reversals
were correct and both were expensive; a reader who only sees the end state will
propose the first version again.

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
- **Sessions under each agent, and nothing else.** Four folders — Sessions,
  Files, Asks, Memory — shipped and were deleted a day later (§3.3.1). An agent
  now expands straight to its threads: one step, not two.
- **Clicking an agent narrows the tree to it, and clicking it again widens it
  back out** — animated, local (`filterSlug`,
  `components/features/chat/chat-tree-sidebar.tsx:499`), and with no navigation
  in either direction. The route narrows nothing: `/chat/<slug>` lists every
  agent. Narrowing was a route first, and §3.3.2 is why it stopped being one.
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
- **No new endpoints and no new route.** The constraint held throughout: the
  static handler rewrites exactly one path level
  (`internal/api/static.go:203-217`, whose comment names the `/chat/a/b`
  misroute it was written to stop) and the slug is read from
  `window.location.pathname`. While the folders existed, the folder was
  `?folder=`, never `/chat/<agent>/<folder>` — and `sessions` was the default
  and was never serialised, so the URL `chatnotify` emits was untouched. The
  parameter is now dead and is scrubbed from a bookmarked URL rather than left
  to carry state nothing reads (§3.3.1).
- **Unchanged on a phone.** The drawer and the tab strip from Step 5 are what
  the tree steps aside for; `sessions-sidebar.tsx` survives as the drawer's
  contents and nothing else. It steps aside below **900 px**, not 768 — a real
  change in a band nobody had tested, and §10.1.

What the original cut got right and is still true: this is not a second browser
for an agent. It navigates conversations and nothing else. The first build of it
did try to be a browser, which is §3.3.1.

#### 3.3.1 Reversal one — the folders, built and deleted a day later

**What shipped first.** Each agent expanded into four folders: Sessions, Files,
Asks, Memory. Sessions listed that agent's threads; the other three replaced the
centre pane, composer included, so nothing dangled under a file list. Each was a
full surface with its own loading, empty and error states.

**Why it was wrong.** `Files` was on screen twice at once, in the tree on the left and in
the chat's own right rail, in the same view. The other two were the same
mistake one step further away: `Asks` is the agent's configuration tab and
`Memory` is the agent canvas. The rule the owner settled it with, which now
governs this surface:

> **left column** = navigation between objects — *where am I going*
> **right panel** = context of the object I have open — *what is here*
> **configuration** = the object's own settings page

By that rule Sessions is navigation and stays; the other three were already
where they belonged, and the tree was a fourth door onto rooms that had three.
"This is not a second browser for an agent" was true as a statement of intent
and false as a description of what shipped. Sunk cost is not a reason to keep a
surface that duplicates two others.

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

**Also salvaged: what the Memory pane found out.** It was written to refuse to
render a blank box, so every gap it hit had to be stated on screen — and that
enumeration outlived the pane. `crew:<crewId>/CREW.md` is read by
`memory-tab.tsx` and written by nothing at that path; `daily/` is recorded but
not enumerable; `lessons.md` has no row and no endpoint; `learned-<topic>.md`
has undiscoverable topic names; `pins.md` **is** reachable, contrary to the
brief that started the work. Those five lines became WP-9 of the work order —
where the first of them was then written up with the wrong cause. See the
correction there, and §11.4 below.

**Also changed, while the tree was open.** An agent with no sessions has no
chevron (an expander that opens onto nothing is a promise the tree cannot
keep), agents are ordered by most recent thread activity rather than
alphabetically, and the right rail's three icons carry the panel's name in the
tooltip, in the drawer's accessible name and in the open panel's own heading —
all read from one map, plus `aria-keyshortcuts` for the shortcut the tooltip
draws.

#### 3.3.2 Reversal two — focus was a route, and is now a filter

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
  filter is a `Show all N` button in the AGENTS section header
  (`chat-tree-sidebar.tsx:788`) — one piece of local state, no destination. A
  filter whose only off-switch is clicking the same row again strands anyone
  who has forgotten which row it was.
- A typed search still reaches past the narrowing, and still suspends it rather
  than clearing it.

**What replaced the click.** The click used to be how you started a
conversation with an agent that had none. That is the **Start a conversation**
row in the bullet above.

**And the swap is in place too.** Using that row on an agent this page does not
already have open no longer navigates either.
`app/(dashboard)/chat/[agentSlug]/chat-page-client.tsx` already bypasses the
Next router for the session — `history.pushState` (`:281`) plus a `popstate`
listener (`:299`), because a router-driven param change re-evaluates the layout
subtree and visibly remounts the dashboard chrome on static-export builds — and
the slug now goes through the same path: push the URL, move the local slug,
re-resolve the agent out of the roster already in memory (no request), and let
the panel follow. Opening ANOTHER agent's thread from the tree takes the same
path, carrying the thread. **The page no longer calls the router at all.**
`/chat` still navigates for it, because there is no ChatPanel on the index to
swap an agent under (O7).

The remount claim is the kind that is easy to assert and easy to be wrong
about, so it is pinned: the test captures the layout grid and sidebar DOM nodes
before the swap and asserts node **identity** after it. React reuses a DOM node
only when it does not unmount the subtree, so identity is the chrome tearing
down, in miniature. `ChatPanel` carries a key on the agent id, so a swap gets a
clean panel instead of inheriting the previous agent's state — which the route
change used to guarantee for free, and which is the one thing the cheaper
mechanism does not.

## 4. The steps

Seven steps. Each is independently shippable, independently revertable, and
ordered so that every step is useful even if the next one never happens. None
adds a table.

> **All seven have shipped** on `feat/chat-primary-surface`. The steps are left
> as written, with corrections marked in place where the implementation
> disagreed with the plan, because *what we thought we were building* is the
> half of a PRD that is expensive to reconstruct later. One claim they made
> collectively — that they add no endpoints — was wrong; §9.1.
>
> Step 7 says "one `suggested_prompts TEXT` column". A second column,
> `agents.ask_forms`, shipped immediately after it with the questionnaire
> sheet; that is the FORMS half of the companion PRD, staged the same way and
> described there.

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
  so chat and `/routines` are visibly one system. ~~**Chrome only; no facets,
  no tree.**~~ **Overruled: §3.3.** The chrome shipped first, exactly as
  written here, and the tree replaced it a few commits later when the flat list
  read as unfinished beside the pages it was borrowing chrome from.

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

**Do:** ~~wire `WithConversationSearch` at boot~~ — it has been supplied since
`d6ab6e9f`; see §1. Add a "Conversations" group to
`components/command-palette.tsx`. ~~The endpoint requires `agent_id` — fan out
across the user's agents client-side and merge, or extend the handler to accept
an empty agent scope. **Decide before starting** (O2).~~

**O2 is decided and built: the handler took the scope.** `agent_id` is now
optional (`internal/api/conversation_search.go:172-178`); absent means the
caller's whole workspace, and the agent set is resolved from the request
context rather than from the body. A client-side fan-out lost on two counts —
it would have issued one request per agent from a palette that queries on every
keystroke, and it could not have been made to say *how much* of the workspace
it searched. A searcher that cannot span agents answers an honest `503`
(`:240-246`) rather than narrowing silently to one agent, because returning a
fraction of the results with no indication is worse than refusing. Both scopes
run one query — `Store.Search` is a one-element call into `SearchAgents` — so
single-agent and workspace search cannot drift apart.

The scope carries a cap: 400 agents, the newest first
(`conversation_search.go:67,110`), which is the SQLite bound-parameter ceiling
and not a product choice. It is documented; it is not logged, and it should be
(WP-6).

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
**Unlocked by:** ~~approval rules that actually match chat runs
(`internal/policy/approval_mode.go:25-30`), **and** an expiry sweeper for
escalations~~ — **half of this is now built, and the other half was cited
wrongly.**

- *The escalation half shipped.* `EXPIRED` and `CANCELLED` are real states, the
  waiter decides expiry on the row's own deadline and a sweeper backstops it
  (`internal/api/escalation_lifecycle.go:17,78,141`), every transition goes
  through one CAS-guarded update so it and its journal entry happen exactly
  once, and the unanswered question got an answer in code rather than by
  omission: the run continues **with an explicit warning** — a mandatory field
  on the wire and `severity=warn` in the journal (`:85-90,177-195`). Two bugs
  fell out: expired escalations used to occupy a crew's escalation budget
  forever, and the inbox counted `status IN ('pending','open')` in lowercase
  against a column storing `PENDING`, so the badge was structurally zero
  (`internal/api/agent_inbox.go:95-115`).
- *The approval half was never in `approval_mode.go`.* Lines 25-30 of that file
  are a **comment**, and the comment says the default rule set does not match
  `agent_run`. A chat run does carry a mode and *is* gated
  (`internal/orchestrator/orchestrator_run.go:155-166`), but it passes only
  `agent_slug`, `agent_role` and `user_prompt`, while the three baked-in rules
  key on a tool-name regex (`internal/harbormaster/rules.go:52`), on
  `cost_estimate_usd`, and on `target`/`host`/`env`. The unlock condition is
  therefore not "rules that match" but **a rule shape that can match an agent
  run at all**, and the file that has to change is `harbormaster/rules.go`.

**Then build:** a card in the conversation subscribed to the session channel;
escalations already carry `chat_id` and already broadcast
(`internal/api/escalation_handler.go:272-284`), and the blocking protocol
(`internal/api/escalation_waiter.go:93`) already parks and resumes correctly.
~~**Also fix when touching this:** `internal/api/pipeline_trust.go:139-157`
grants and revokes with **no journal entry**.~~ **Done.** The substance was
right and the line range was not — the handlers are `GrantTrust` and
`RevokeTrust`, not the grant tail. Both now emit, carrying actor, scope, refs
and the definition hash, under `approval.trust_granted` /
`approval.trust_revoked` on purpose
(`internal/api/pipeline_trust.go:78,187,320`, `internal/journal/types.go:73-89`):
one filter has to show the one-off decision and the standing grant that
explains why later runs stopped asking.
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
(`internal/sidecar/routine_mcp.go:92`) do the authoring — plus a review step
before it becomes a cron job.

~~Three known defects to fix with it: `chat-panel.tsx:589` passes no
`workspaceId` so no server action ever appears; `SlashActionModal` is imported
by nothing;~~ **Both fixed.** The modal is imported at `chat-panel.tsx:34` and
rendered at `:849-856`, gated on the `workspaceId` from `useWorkspace()`
(`:123`) — and every action in the palette is now in exactly one of three
states: it works, it is visibly disabled with a stated reason, or it is not
rendered. The routine action is deliberately in the middle state, because the
transcript→DSL step does not exist and whether an agent-authored routine needs
a review boundary is an open product question rather than a wiring task. The
third defect stands: the payload posts `{name, cron_expr, timezone}` to an
endpoint that requires a target routine
(`internal/api/pipeline_schedules.go:560`).

~~Add `author_chat_id` to `userSaveRequest`
(`internal/api/pipelines_crud.go:547`) — the store already writes the column and
the sidecar already stamps it.~~ **This was written as a missing field and is
actually a decision.** `AuthorChatID` already exists on the *other* struct,
`internalSaveRequest` (`internal/api/pipelines_crud.go:519,527`), which is the
agent-mediated path — so the sidecar path already carries it and needs nothing.
`userSaveRequest`'s own doc comment (`:541-546`) says authorship is inferred
from the JWT **on purpose**. Adding the field means deciding that a
user-authored routine may declare its originating chat, which contradicts a
documented design choice. That is an owner call, not a patch.

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
`internal/api/slash_commands_handler.go:84-92` explains it is missing on
purpose, naming the sidecar loopback `/memory/write`
(`internal/sidecar/server.go:433`) as the only writer. The HITL verifier it
needs is fully written and reachable by construction but unreachable in
practice: `VerifyWrite` has one non-test caller, guarded by
`cfg.Verifier.Mode != VerifierOff` (`internal/memory/writer.go:154-155`), and
no shipping configuration sets `Verifier:` at all — see §11.3. Citations with
`file:line` need the indexer to stop discarding chunk line ranges: `Chunk`
carries `LineStart`/`LineEnd` and the insert binds only `chunk.File` and
`chunk.Content` (`internal/memory/index.go:121-123`), so they are computed and
thrown away.

**And the crew tier's history is a projection bug, not a missing writer.** The
memory panel reads version rows at `crew:<crewId>/CREW.md` while nothing writes
a row at that path; the writers exist and the watcher's parser cannot see the
shared directory. Full correction in the work order's WP-9 and in §11.4.
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
2. ~~**O2** — Step 6: fan out conversation search across agents client-side, or
   extend the handler to accept an empty agent scope?~~ **Answered: the
   handler took the scope.** See Step 6.
3. **O3** — Step 4: does `/chat` show recent threads, or the agent list first?
   Ship threads-first, measure, add a preference only if the data asks.
4. **O4** — Step 2: client-derived titles, or agent-generated ones? The latter
   reads better and costs a model call and a privacy conversation.
5. **O5** — Nav label: *Chat*, *Talk*, *Conversations* — and the Czech string.
6. **O6** — Does `/chat/[agentSlug]` stay a separate page, or become `/chat`
   with a preselected agent? Deep links from notifications must keep working
   either way (`internal/chatnotify/notify.go:155`).
7. ~~**O7** — One WebSocket per mounted panel today (`chat-panel.tsx:150-156`),
   separate from `RealtimeProvider`. Does `/chat` mount a panel at all before a
   thread is chosen?~~ **Answered: no**, and a test asserts the socket
   constructor is never called. §10.2. Revisit if the first message feels slow.
8. **O8** — Should the composer refuse a file up front when the crew that owns
   the output tree is stopped? Today it accepts the file and surfaces the
   server's `409` as a failed chip with a Retry. Refusing up front needs an
   endpoint that answers *is this agent's output tree writable* — no such
   contract exists, and "crew is running" is a proxy for it that is wrong in
   both directions (§10.3). This is the same question the code audit raised as
   its second owner question, and it is a backend contract before it is UI. It
   is also `agent-ask-packs-and-document-intake.md` O16 — one question, two
   documents; answer it in one place.
9. **O9** — When a user's question to an agent expires, the run now continues
   with an explicit warning (§6.1). That was decided in code rather than by an
   owner. It is the right default and it is still a default: is *continue with
   a warning* correct for every crew, or does a strict crew want the run to
   fail?

## 9. Testing standard

Per `CLAUDE.md`: test first, and a fix is red → green with the test failing on
current `main`. Two rules specific to this work:

- **Step 4's acceptance test runs against the static export**, not the dev
  server. The rewrite is the risk; a dev-server test would pass while production
  404s.
- **Step 5's camera attribute is verified on real hardware.** A headless
  assertion that `capture="environment"` is present in the DOM proves nothing
  about whether a phone opens the camera.

**What the first rule actually got.** Playwright runs against `pnpm dev` — the
exact configuration this rule warns would pass while production 404s — and no
Go job stages a real export, so a Go test would have `t.Skip`ped in every CI
run. What shipped instead is a CI step asserting `out/chat.html` and
`out/chat/_.html` exist immediately after the build (`.github/workflows/ci.yml:551`),
proven by deleting each and watching it fail. That is the cheapest honest check
and it is not a browser test; the rule above is still owed a real one.

**What now gates a merge, and what does not.** The flagship repair is no longer
nightly-only: `e2e/onboarding-wizard.spec.ts` — the spec that walks a new user's
first click, and the one that used to assert the 404 it was meant to kill — runs
on every pull request in ci.yml's `onboarding-journey` job, which stands up a
throwaway server on a never-bootstrapped database and drives it through
`playwright.fresh.config.ts`; it stays in `playwright.config.ts`'s `testIgnore`
for a precondition the main config cannot give it, not as rot, and
`scripts/onboarding-gate-test.sh` fails the build if that job is deleted, given
an `if:`, or moved back behind a schedule. It costs ~3.5 min on its own runner
and adds nothing to a PR's wall-clock, which is set by `Go Race` at ~24 min; the
nightly copy was removed rather than kept, so there is one definition to drift.
Folding it into `playwright-pr` was rejected on a fact, not a preference: that
job seeds its fixture with `crewship seed`, the wizard consumes the one-shot
bootstrap seed needs, and because the wizard bootstraps through the browser no
CLI token is ever written — seed's already-initialised fallback is
`requireAuth()` (`cmd/crewship/main.go:283`), which only reads a stored token and
never logs in, so seed would die on "not logged in" and take the PR browser gate
with it. The `out/chat.html` assertion added for rule 1 does run on pull requests
(the `frontend` job; ci.yml triggers on `pull_request`), and a third gate
predates both: `app/(onboarding)/onboarding/__tests__/dead-agent-routes.test.ts`
scans `app/`, `components/`, `hooks/`, `lib/` and `stores/` on every PR through
Vitest, so re-pointing the wizard at `/crews/agents/*` fails statically in
seconds. What still does **not** gate: nothing mechanically blocks a merge at all
— the active `main-branch-protection` ruleset carries only deletion,
non-fast-forward and linear-history rules and **no required status checks**, so
every check here, this one included, is enforced by the "never merge on red CI"
rule in `CLAUDE.md` and by a human reading the checks; the static scan cannot see
the wizard silently falling back to the dashboard when `GET /agents/<id>` yields
no slug (only the browser journey catches that); provider-backed chat send and
receive remain nightly; and seven specs sit in `testIgnore` for the deleted
`/crews/agents/*` and `/crews/new` families — all seven still reference those
routes and none was touched by this branch, so that bucket is still honest rot
and must not be widened to make a failure go away.

### 9.1 The endpoints this work added

~~Steps 1–7 add no endpoints.~~ ~~Steps 1–7 added one endpoint.~~ **The branch
added five.** The claim was wrong twice: first as written, then as amended by
hand, because the amendment corrected the count without noticing that the
sentence it was defending — *the remaining steps add no endpoint* — was the
part doing the damage. Four more arrived after the steps, out of the code audit
and the work order, and each one carried the house rule with it.

Every new endpoint gets a CLI command, and its acceptance test drives the
binary (`cmd/crewship/cli_route_contract_test.go` enforces the route-exists
half statically).

| Endpoint | Where from | CLI | route-roles |
|---|---|---|---|
| `PATCH /api/v1/agents/{agentId}/chats/{chatId}` | Step 2, session titles | `crewship chat rename` (`cmd_chat.go:654`) | `roleSelf` |
| `GET /api/v1/agents/{agentId}/chats/{chatId}/attachments` | WP-8(3) | `crewship chat attachments list` (`cmd_chat.go:382`) | — see below |
| `DELETE /api/v1/agents/{agentId}/chats/{chatId}/attachments/{attachmentId}` | WP-8(3) | `crewship chat attachments delete` (`cmd_chat.go:452`) | `roleCreate` |
| `POST /api/v1/escalations/{escalationId}/cancel` | WP-4(b) | `crewship escalation cancel` (`cmd_escalation.go:211`) | `roleCreate` |
| `POST /api/v1/escalations/sweep-expired` | WP-4(b) | `crewship escalation sweep-expired` (`cmd_escalation.go:263`) | `roleManage` |

Registrations: `internal/api/router_crews.go:348` and
`internal/api/router_orchestration.go:743,744,833,845`.

**The listing route has no `route-roles.txt` row, and that is correct.** The
manifest is generated from `Router.mutationRoutes` — the routes registered
through `authedMut` (`internal/api/route_roles_manifest_test.go:29-41`) — and
the listing is registered as `authed(wsCtx(...))`, authenticated and
workspace-scoped but not role-gated, like every other read. Worth stating
because "four rows for five endpoints" reads as an omission and is not one; the
test fails in both directions, so a missing row for a *mutating* route cannot
survive a run.

**One soft spot, recorded rather than fixed.** `crewship chat rename`'s
acceptance test drives the cobra `RunE` in-process against an `httptest` server
(`cmd_chat_rename_test.go:69`) rather than the built binary — it follows the
`chat` tree's own convention, and the whole tree is tested that way. The two
other commands this branch added do exec the binary
(`cmd_conversation_workspace_test.go:78`, `cmd_agent_ask_test.go:223`). Not a
defect and not worth a rewrite; named here so nobody later reads the
inconsistency as an oversight.

## 10. Shipped constraints

Behaviour this work introduced or discovered, which nothing in Steps 1–7
predicted and which a reader of the plan alone would get wrong.

### 10.1 The compact breakpoint on this page is 900 px, not 768

`useIsMobile` answers *is this a phone* and switches at 768
(`hooks/use-mobile.ts:3`). This surface asks a different question — *do two
panes fit* — and switches at 900 (`CHAT_TREE_BREAKPOINT`,
`components/features/chat/chat-tree-sidebar.tsx:170`, consumed through
`useChatCompactLayout` at `:178`). 240 px of session list beside a conversation
survived an 800 px window; 280 px of tree plus a status section does not.

Below the breakpoint the surface falls back to the shape Step 5 already built —
the session drawer and the chat/files/more tab strip — rather than to a
squeezed tree. That is safe because the panel's mobile mode is entirely
prop-driven, but it means the band **768–900 px now behaves as mobile and never
did before**, and nobody had tested it. The variable it lands in is still
called `isMobile`
(`app/(dashboard)/chat/[agentSlug]/chat-page-client.tsx:211`), which is worth
knowing before reading that file as though it were the global hook.

### 10.2 `/chat` mounts no panel, and therefore holds no socket

`ChatPanel` opens its own WebSocket on mount, separate from
`RealtimeProvider`. The index renders the sub-bar, the tree, recent threads and
the agent list, and no panel (`components/features/chat/chat-home.tsx:58-65`);
a test stubs the socket constructor and asserts it is never called. A landing
page has no business holding a live socket for a thread the user has not
chosen. This is O7 answered with *no*, and the answer has a second consequence:
`/chat` is the one route allowed to navigate when an agent is picked, because
there is no panel there to swap an agent under (§3.3.2).

### 10.3 A chat attachment needs a writable output tree — which usually means a running crew

Stated in an earlier draft, and in a commit message, as *chat attachments
require a running crew*. That is the common case, not the rule, and the
difference decides where the fix goes.

The upload never asks whether a crew is running. It writes host-side first and
answers `200` if that works (`internal/server/routes_files.go:221-236`). Only
`fs.ErrPermission` sends it through the container (`saveViaContainer`, `:307`),
and only there does a stopped crew become the `409` whose sentence names the
tree (`containerSaveErrorResponse`, `:614-624`); a deployment with no container
runtime at all gets `503` instead, because no retry would help.

The host write fails because the crew's trees are chowned to `1001:1001` when
the container is created (`internal/provider/docker/docker_container.go:1146-1154`)
while `crewshipd` runs as another uid. When that chown itself fails, the same
function falls back to `chmod 0777` (`:1196-1199`) — and after that a host
write succeeds with the crew stopped. A crew that was never provisioned has no
such owner either.

So: **an upload succeeds when the output tree is writable by the server, and
needs a running crew exactly when the crew runtime owns that tree.** The
consequence for the product is that the composer cannot honestly grey out the
paperclip on "crew is stopped" today — it has no endpoint that answers *is this
tree writable*. Deciding whether it should is O8, below, and it is a backend
contract before it is a UI change.

### 10.4 Two rules that look like style and are not

- **Filesystem permission checks use `errors.Is(err, fs.ErrPermission)`.**
  `os.IsPermission` does not follow a `%w` chain, so it returns false for the
  error `localfs` returns on `EACCES` and silently disarms the container-write
  fallback. A test pins the boolean, including the row where `os.IsPermission`
  is false for the same error.
- **A mock reproduces the server's contract, not the client's assumption.**
  See §11.2 — it is a defect class, not a coding-style note, and it certified
  two shipped bugs on this branch.

## 11. What this work discovered rather than built

Four defect classes recurred, each with more than one instance, and each one
cost more to find than to fix. They are here because the next person on this
surface will meet them again, and because three of the four are invisible to a
green test suite by construction.

### 11.1 A failure invisible to the user *and* to the tests

The house defect. The shape is always the same: something that could not be
answered is rendered as something that is not there.

- **`use-chat` dropped every WebSocket frame whose `type !== "chat_event"`** —
  including `access denied`. A send the channel authorizer refused left the
  optimistic user turn saying "…is thinking" forever, with nothing in the UI
  and nothing in the console. This is the single most expensive instance,
  because it hid the next one — a draft session that never became a row was
  denied at the socket, and the denial was the frame being dropped. Fixed: a
  top-level `error` frame now flushes the pending text and terminates the
  optimistic send with the server's reason (`hooks/use-chat.ts:1160-1179`),
  before the `type !== "chat_event"` gate that used to swallow it.
- **A failed fetch became `[]`.** `three-tier-files.tsx` swallowed a failed
  crew-scope fetch and drew "No shared crew files"; the same collapse had been
  copied into the tree's per-agent thread lists and into the page's own session
  fetch, where a `500` drew as *this agent has no conversations* in the
  product's primary navigation. All three now go through
  `components/features/chat/scope-fetch.tsx` so they cannot drift, a count that
  could not be read shows an em dash rather than a confident zero, and the
  error renders under the agent row without needing to expand it.
- **A crew folder opened onto nothing, permanently.** The crew scope drew the
  listing with `loadingDirs={new Set()}` and a toggle that only wrote to
  `expanded` — nothing ever fetched a directory's children, and
  `buildTopLevelTree` marks every directory `childrenLoaded: false`. The top
  level of `<crewId>/` is mostly directories, one per agent slug, so this was
  the common case: chevron turns, zero rows, no spinner, no error. It now
  fetches on expand through the same watcher shape the agent scope uses
  (`files/crew-files-scope.tsx`), and a directory whose fetch fails closes again
  with the status named rather than sitting open looking empty — the empty-crew
  lie one level down.
- **A file was read from one tree and would have been written to another.** The
  crew scope listed `/crews/{crewId}/files` (keys `<crewId>/…`) and handed the
  click to the *agent* editor, so every shared file toasted "Failed to load
  file": `proxy_files.go` rejects a `<crewId>/` path that is not under
  `<crewId>/<slug>/`. The read failing loudly was the mild half — a path that
  had passed that prefix test would have been saved into the agent's private
  tree. The editor now takes the tree as a required argument, stores it with the
  open file, and builds both URLs from that one stored value
  (`hooks/use-file-editor.ts`), so there is no code path left that can read one
  tree and write another.
- **The tree's fan-out cap does it on purpose.** An agent past the twelfth
  enters neither the thread map nor the error map
  (`chat-tree-sidebar.tsx:299`), so it misses the honest-failure path the same
  file implements and renders as `0` with no chevron — and filtering to it
  offers *Start a conversation* with an agent that may have a year of them.
  Documented in `docs/guides/chat-surface-limits.mdx`; the affordance is still
  owed (WP-6).
- **An upload that failed looked exactly like one that worked** — same chip,
  same name, same size, differing by a faint tint and nothing at all for a
  screen reader — while two failures collapsed into one toast because the
  toasts had no id. The user reasonably concluded one file had landed. Neither
  had.
- **A file that uploaded was never mentioned to the agent.** The bytes reached
  the container; the message went out as text alone. Uploading a file the
  recipient is never told about is worse than refusing the upload.

The rule that came out of it: decide, per swallowed error, whether the **user**
must know or a **developer** must, and make the two states distinguishable.
"We could not ask" and "there is nothing" must never render the same.

### 11.2 A test whose mock implemented the client's assumption

Not a weak test — an actively harmful one, because it certifies the bug and
raises the cost of finding it.

- **`session-on-first-send.test.tsx` mocked `ChatPanel` away and had the stub
  perform the POST**, against a fake server that answered the way the client
  assumed rather than the way `internal/api/proxy.go:263-286` answers. The real
  endpoint returns `200` with an empty message list for a chat that does not
  exist, so the client's existence probe said *the row is here* and no row was
  ever created. Chat sessions were not being persisted at all, and the suite
  was green. Six further suites carried an `ensureSession` mock returning
  `undefined` where the contract had become a boolean.
- **`e2e/onboarding-wizard.spec.ts` waited for `/crews/agents/`** — a route
  family deleted months earlier — so the one spec that walks a new user's first
  click **passed only while the bug was present**. Negative coverage is worse
  than no coverage: it converts a repair into a red build.
- **Two ask-sheet assertions encoded the shared-bucket bug**, seeding an
  unowned upload and expecting it to fill a field's value. They were rewritten
  to seed into the field, with the second field asserted empty.

The standard that replaced it: tests for these paths run the **real** panel and
composer against a fake server that reproduces the endpoint's precondition, and
a fix is not accepted until its test has been watched failing for the stated
reason.

The standard has a second half that is easier to skip. When a requested test
passes **before** the change, say so and keep it as a regression guard rather
than manufacturing a failure to make the work look larger. Three of the tests
requested for the upload-failure work passed on the unmodified branch — the
message half was already excluding failed uploads, and so were both attachment
gates in the ask sheet — so the allow-list that shipped is proven hardening on
two enumerated rows, not a fix for a live bug. Saying that is worth more than
the fix.

### 11.3 A guard that was unreachable rather than wrong

The most expensive class to diagnose, because every hypothesis about the guard
itself is false: it is correct, it is enabled, and it never runs.

- **The container-write fallback for attachments.** `crewSharedContainerPath`
  matched only `crews/<id>/shared/…`, so for an attachment key in the `/output`
  tree the function returned fifty lines above the fallback. The log said
  `file save failed` and never `file save via container failed` — which is what
  finally identified it. Both obvious hypotheses were wrong: the container was
  up, and `errors.Is(err, fs.ErrPermission)` did hold. Had it been reachable it
  would still have written to the wrong place, because the writer hardcoded
  `/crew/shared`.
- **The scan test for dead `/crews/agents/*` links.** It hunted the string
  `"/crews/agents/"` with a trailing slash, so the bare index slipped past it —
  and the toolbar's dead branch was gated on a regex written `/^\/crews\/agents\/…/`,
  escaped, so a substring scan could not see the very reference keeping the
  branch alive. An earlier version of the same test had a comment stripper that
  ran the block-comment regex first, so a line comment containing `/` and `*`
  swallowed the eighteen lines below it, blanking a dead `<Link>`. The file
  passed the check while shipping a 404.
- **The HITL memory verifier.** Fully written, and gated on
  `cfg.Verifier.Mode != VerifierOff` (`internal/memory/writer.go:154`). No
  shipping configuration sets `Verifier:` at all — both `WriteConfig{}`
  literals omit it (`internal/sidecar/memory_write.go:160-164`,
  `internal/api/memory_portability.go:434-437`). Reachable by construction,
  unreachable in practice.
- **The approval gate on chat runs.** A chat run does carry a mode and is
  gated with `Tool: "agent_run"`, and no baked-in rule can match it: the
  destructive-tool rule keys on
  `(?i)deploy|production|delete_.*|drop_.*|terminate_.*`
  (`internal/harbormaster/rules.go:52`) and the other two on
  `cost_estimate_usd` and `target`/`host`/`env`, none of which an agent run
  passes. The gate is live and cannot fire.

The tell in every case is a log line, or an assertion, that has **never** been
observed rather than one that is observed wrong. Worth asking of any guard
before trusting it: what would prove this ran?

### 11.4 A claim in a document everyone built on without opening the file

Every one of these was load-bearing. Each was carried between documents by
copying, and each survived because the next reader trusted the citation instead
of following it.

- **"The conversation-search route answers 503 unless wired."** It had been
  wired since `d6ab6e9f`; `NewRouter` has one production call site. What was
  missing was a caller and a workspace scope. Two documents planned a boot-time
  fix for a boot-time problem that did not exist.
- **"`pins.md` is unreachable."** It is reachable, and renders as a real tier.
- **"`waitpoint_trust_grants` makes the chat approval scope 80 % built."** The
  grant is keyed `(workspace, pipeline_id, step_id, definition_hash)`. Chat has
  no such scope key, so what was described as wiring is a new grant scope.
- **"`crew:<crewId>/CREW.md` has no writer at all"** (WP-9). It has two — the
  native dispatcher's `memory.write` with tier `CREW`
  (`internal/memory/tools.go:1156-1160`) and the sidecar's legacy
  `POST /memory/write` with `scope: "crew"`. The real defect is one directory
  level away and invisible from the writer: `parseMemoryPath` rejects anything
  that is not `crews/{id}/agents/{slug}/.memory/…`
  (`internal/memory/audit_watcher.go:449`), while the shared tier lives at
  `crews/{id}/shared/.memory/`, so a real write never produces the version row
  the panel reads. Acting on the claim as written would have added a **third**
  writer and left the bug in place. What makes this one instructive rather than
  careless: `crew:<id>/…` is a real convention with real producers — the
  consolidator writes `crew:<id>/pins.md` and `crew:<id>/learned-*.md`
  (`internal/consolidate/consolidator.go:707-712`) — so the panel extrapolated
  from a convention that happens not to cover this file. A claim can be
  perfectly reasonable and still be worth opening.
- **"Approval rules that actually match chat runs
  (`internal/policy/approval_mode.go:25-30`)"** — §6.1's unlock condition,
  below. Those lines are a *comment*, and the comment says the rules do not
  match. The thing that has to change is the rule shape in
  `internal/harbormaster/rules.go`, which neither document named.
- **"Steps 1–7 add no endpoints"** (§9.1). Wrong when written, and wrong again
  after it was corrected by hand.

The rule the work order opened with turned out to be the most valuable line in
it: *every claim carries a file:line, and a wrong claim is a deliverable rather
than a mistake.* The corrections above were found by three independent passes —
the audit corrected the work order, the contract sweep corrected the audit, and
an implementing agent corrected the sweep. None of the three was clean, and
the fourth pass found the disagreements between them.
