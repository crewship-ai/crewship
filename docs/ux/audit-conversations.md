# UX audit — cluster A · Conversations (`/inbox-v2`, `/chat`)

Analysis only, 2026-09-03. Measured against `docs/ux/README.md` (the contract)
on a throwaway server built from this clone's tree (branch
`onboarding-client-redesign`, uncommitted state as of 13:00). No code was
changed. The next agent on this area starts from this file.

Screenshots: `/tmp/claude-1000/-srv-crewship-crewship-3/349a9290-4bee-46d8-b99b-fdc583e6ecbc/scratchpad/shots/`
(`base/` = demo seed only, `data/` and `fix/` = 100 inbox items + 30
conversations, `light/` = attempted light scheme). Every `report.json` next to
the PNGs carries the measured horizontal overflow, elements wider than the
viewport, sub-32px tap targets at 390px, and the identifiers found in page
text.

## 0. Method and assumptions

- Server: `crewship-shot start --no-docker` on `:8094` (8095–8099 were taken
  by other sessions), `CREWSHIP_DATA_DIR` set so the IPC socket does not
  collide with dev3, `CREWSHIP_INTERNAL_TOKEN` set explicitly so the internal
  API could be driven. Seed: `crewship seed` (3 crews, 7 agents, 15 issues,
  4 pages; **the seed also creates the Crewship Guide agent and one Guide
  conversation**, which is why `/chat` opens on `_crewship-setup-guide` even
  on a fresh workspace).
- CLI isolated with `CREWSHIP_CONFIG=<scratch>/cli-config.yaml`; `localhost`
  resolves to `::1` for the CLI while the server binds IPv4, so the server
  URL must be `127.0.0.1`.
- 100 inbox items = 100 escalations through `POST /api/v1/internal/escalations`
  (TEXT / CREDENTIAL / LINK, spread over 7 agents and 3 crews), 6 resolved
  with `crewship escalation resolve`, 3 cancelled, 8 marked read, plus 2
  guided ephemeral hires (`crewship policy set --level guided`, `crewship
  hire`) which produce a blocking waitpoint each and an approvals-queue row
  each. Final state: 103 inbox items, 94 in Needs action, 9 in History,
  2 approvals.
- 30 conversations = 29 `crewship chat create` + rename across 7 agents, 8
  ROUTINE-origin chats and 1 AGENT-origin chat through
  `POST /api/v1/internal/chats`, later 13 more on one agent (Riley) to test
  the per-agent cap.
- **Could not be produced without a runtime**: `message` items ("your agent
  replied", issue mentions, routine progress), `failed_run`,
  `schedule_missed`, `schedule_circuit_breaker_tripped`,
  `memory_consolidation`, mission signals. The Updates view therefore stayed
  empty; findings about it come from code.
- **Light theme could not be tested**: `app/layout.tsx` pins
  `<html className="dark">`; `prefers-color-scheme: light` renders dark.
  This is a cluster-D finding (§6).
- `crewship inbox resolve/archive` on an escalation returns 409 while the
  source exists (by design); the decision has to go through the source.

## 1. Inbox v2

### 1.1 What the screen is for

The one place a person finds out what is waiting on them: approvals
(waitpoints, approvals-queue rows), escalations, failed runs, missed
schedules, memory proposals, mission reviews; plus the updates that need no
decision and the record of what was decided. It merges three sources (inbox
items, approvals queue, missions) and dedupes the hire that arrives in two of
them.

Against README §1 (what needs me → what is happening → owned objects →
outcomes → related):

| §1 step | Present? | Screenshot |
|---|---|---|
| What needs me | Yes: "Needs action" list, oldest-deadline first. Rows carry no verb (Review / Approve / Install) — every row is a title and a sender. | `data/inbox-100-1440.png` |
| What is happening now | No. Nothing live, no pulsing dot, no "3 agents running". | — |
| State of owned objects | Partly: counts on the three views, facet counts in the popover. No crew or kind on the row. | `fix/inbox-filter-open-1440.png` |
| Outcomes | No. History is a list; no "12 decided this week", no median time-to-decide. | `data/inbox-100-history-1440.png` |
| Related objects | No (see 1.3). | — |

A screen that cannot answer step 1 must say so in one line; the empty inbox
is two blank panes ("Nothing here" / "Select an item to see its context and
actions.") — `base/inbox-empty-1440.png`, `base/inbox-empty-390.png`.

### 1.2 Dead ends (README §6)

| # | Checklist line | Finding | Evidence |
|---|---|---|---|
| D1 | Disabled primary states why | OK for approvals: "OWNER or ADMIN decides this" sits beside a disabled Approve (`inbox-v2-detail.tsx` ApprovalDetail, `inbox-detail.tsx` DecisionCard). **Not OK for the hire waitpoint**: "To deny, fire the agent from its crew page." — the crew page is not linked, and there is no Deny at all although the approvals-queue row it mirrors has one. | `data/inbox-100-hire-1440.png` |
| D2 | Empty pane names what will appear and one way to make it appear | **Fails everywhere.** Empty inbox: "Nothing here". Empty Updates while 94 items need action: "Nothing here". Empty reading pane: "Select an item…". None says "approvals, escalations, failed runs and missed schedules land here", none links to Routines/Crews, none shows last resolved items. | `base/inbox-empty-1440.png`, `fix/inbox-updates-empty-1440.png` |
| D3 | Errors keep input and offer retry | OK. Source failure → red strip "This inbox may be incomplete — … Retry" (`inbox-v2.tsx:255`). Approve failure → toast, comment textarea keeps its text. | code |
| D4 | Reload resumes where the person was | Partial. `?item=` is honoured and re-read on same-route navigation (`inbox-v2-deeplink.ts`). **View (Needs action / Updates / History), type/deadline/unread facets and search are not in the URL**; a reload from History with an item open lands back on Needs action with the item found by `request:<id>` fallback, filters gone. | code |
| D5 | Nothing internal leaks | **Fails on every detail.** Crew cell shows the crew cuid `cmtljgwic0009f1b3dd76` under the caption `crew_id`; Category shows `agents.escalation` under "derived from kind"; Subject caption `sender_name`, Arrived caption `created_at` (`inbox-detail.tsx` Definition renders a `field` line by design). Context card lists `Chat ID` / `Crew ID` cuids and `Escalation type TEXT`. Subject is the slug `riley`, not the name Riley. History rows and cards say `approve` / `reject` / `cancelled` verbatim (`resolved_action`). Role copy: "anyone in OWNER / ADMIN can decide this", "MANAGER+". Hire card: `devops-sre` template slug, `(template default)` as the model, `Agent ID` cuid. Approval detail (`inbox-v2-detail.tsx`) prints `row.kind.replaceAll("_"," ")` ("ephemeral hire") and `decided_by` as a raw user id in mono. The list title itself is `Agent escalation: <reason>` — a server-composed prefix that eats 130px of a 340px column. | `data/inbox-100-detail2-1440.png`, `data/inbox-100-history-detail-1440.png`, `data/inbox-100-hire-1440.png` |
| D6 | Mobile 390: no horizontal overflow, 44px targets, one column | Overflow **0px** on every inbox shot (list, detail, filter, history). One column: yes (list full width, detail replaces it with "← Back to inbox"). Targets: toolbar buttons 29px, view rows 28px, "Back to inbox" 29px, filter option rows ~27px — **all under 44px**; list rows are 50px (two lines) and pass. | `data/inbox-100-390.png`, `data/inbox-100-detail2-390.png`, `report.json` `small` |
| D7 | Copy says what it is | "Waitpoint", "Circuit breaker", "Approval gate", "Mission signal", "Memory proposal" as facet labels are engine vocabulary. "Access request" heading for a TEXT escalation that asks a yes/no question. Deadline facet says "No deadline" for all 92 escalations although the server expires them at `answer_deadline_at` (7 days) — the payload does not carry it, so the truthful answer is hidden. **A LINK escalation never shows the link**: the payload carries `chat_id, crew_id, escalation_type, reason` only, so the person approves "Need write access to the docs repo" without seeing the URL the agent asked to open. | `fix/inbox-search-click-1440.png` (LINK, no URL), `fix/inbox-filter-open-1440.png` |

Also under §6 spirit: the filter popover is clipped at 360px with no visible
scrollbar (`sidebar-kit.tsx:244`), so Deadline and State facets sit below a
hard cut ("DEADLINE" then a sliced "Any time") at both 1440 and 390 —
`fix/inbox-filter-open-1440.png`, `fix/inbox-filter-open-390.png`.

### 1.3 Missing cross-links (README §5: inbox item → run / routine / issue / credential / crew)

| Item kind (as produced here) | Links present | Links missing |
|---|---|---|
| escalation (TEXT/LINK/CREDENTIAL) | none in the row; detail has none either (no `chat_url`, no `issue_identifier`, no `pipeline_run_id` in payload, so `jumpFor` returns null) | the agent (`/crews?agent=<slug>`), the crew (`/crews/<id>` or `?crew=`), the chat it was raised in (`/chat/<slug>?session=<chat_id>` — the id IS in the payload), the link itself for LINK, the credential for CREDENTIAL |
| waitpoint (hire) | none | crew page (the copy tells you to go there), the approvals-queue twin, the template |
| approval (queue row) | "Open crew" and "Open mission" buttons exist in `ApprovalDetail` — but only for rows that are NOT suppressed as a hire duplicate, so on this data they never rendered | agent that asked (`agent_id` is on the row) |
| message with `chat_url` / `issue_identifier` (from code) | Open chat / Open ENG-1 | crew, run |
| failed_run with `pipeline_run_id` (from code) | Open run (`/activity?run=`) | routine, crew, agent |
| schedule_* (from code) | "Open routines" (the index, not the routine), Run now | the routine itself (`/routines?routine=<slug>`), its crew |
| grouped system incident | "Open System Health" → `/admin` | — |

Every row lacks the crew (colour dot + name) and the kind pill the contract
asks for. The Crew *cell* in the detail exists but shows an id and is not a
link.

### 1.4 Inconsistencies with README §2 anatomy (and §3 motion)

- **No page header.** The top bar reads only "Crewship"; there is no `SubBar`
  with icon, title, `94 need you · 9 decided`, live meta or a primary action.
  Chat at least has a breadcrumb; the inbox has no on-screen name at all
  (`data/inbox-100-1440.png`).
- **Empty states** are centred one-liners in the list column, not the
  `InlineEmpty` / `EmptyState` pattern (icon + what appears + action).
- **Status** is never a dot + word pill on a row; unread is a 6px blue dot
  with no word; urgency is a red time only when a deadline is within the hour.
  Outcome in History is a bare lowercase word.
- **Crew** never rendered with `CrewIcon` + colour dot; **Agent** avatars have
  no status dot; **Model** shown as `(template default)` text rather than
  `getModelLabel`.
- **Numbers**: view counts are plain spans, no `AnimatedNumber`,
  `tabular-nums` present. No `Sparkline` anywhere.
- **Detail cards** use `DetailCard` from `components/ui/detail` (12px radius,
  fine) but the 11px uppercase title + mono hint pattern is used
  inconsistently: "CONTEXT secrets masked" yes, decision card no.
- **Motion**: the reading pane staggers with `Appear` (reduced-motion aware).
  The list has no arrival flash, no `layout="position"` reorder, rows do not
  fade out on resolve — a decided row simply vanishes and the pane swaps to
  the confirmation card. The "Decision saved" confirmation is a 14-row-tall
  centred block, which is exactly the 150px centred block §2 forbids.
- Surfaces: list column is `bg-card` with `border-white/[0.06]` hairlines
  (the sidebar-kit dialect), detail is `bg-background` — consistent with
  Routines/Issues, which is what the file says it copies.

### 1.5 Scale (100 items)

- Rendering: 94 rows in one flat "Waiting for you" section, no priority /
  cap / fold, no grouping by crew, agent or kind, no "N more · K urgent"
  (`data/inbox-100-scrolled-1440.png`, full page). Sorted oldest-deadline
  first, then oldest-created first, so the oldest item is at the top and a
  fresh urgent one is at the bottom of 94.
- Every row reads "Agent escalation: Need the Grafana … / riley / 1m ago":
  with one producer the list is 94 near-identical lines; the distinguishing
  half of the title is what gets truncated at 340px.
- Search works (client-side, result count in the section header: "9"),
  facet counts are exact because the page loads **all** pages of active,
  resolved and approvals before rendering (`useInbox(..., {loadAll:true})`,
  approvals `limit: 200, loadAll`, missions paged by 100 with
  `include_tasks`). At 1 000 items that is >10 sequential requests before
  first paint and a 1 000-row DOM with no virtualisation. The file's own
  comment says the counts must move to a server GROUP BY the day this
  paginates.
- The right pane stays "Select an item…" no matter how many items wait; with
  94 waiting the pane should carry the triage summary (see P1-3).
- Hire twins are correctly suppressed (94 = 92 escalations + 2 waitpoints;
  the 2 approvals-queue rows are not double-listed).
- Mobile 390 with 94 rows: fine, 0px overflow, list scrolls.

## 2. Chat

### 2.1 What the screen is for

Talk to one named agent, and find the conversation you were in. Reached from
`/chat` (freshest conversation anywhere), `/chat/<slug>` (that agent's
freshest, or a draft) and `/chat/<slug>?session=<id>` (every notification and
CLI deep link). The column lists conversations by KIND (Direct / Routines /
Issues), the transcript pane carries the composer, the right rail opens
Files and Team.

Against §1:

| §1 step | Present? | Screenshot |
|---|---|---|
| What needs me | Unread badge per row and an "Unread only" facet. No "Riley is waiting for your answer" row, no link to the inbox item that caused the badge. | `data/chat-30-riley-1440.png` |
| What is happening now | Live dot on the avatar when the agent is RUNNING (from the WS `agent.status` event) — good. Nothing about *what* it is running. | code |
| Owned objects | The conversations. Grouped by day (Direct) or routine (Routines). | `fix/chat-routines-1440.png` |
| Outcomes | None. | — |
| Related | See 2.3. | — |

### 2.2 Dead ends (README §6)

| # | Checklist line | Finding | Evidence |
|---|---|---|---|
| C1 | Disabled primary states why | The "+" New conversation button is disabled when the roster is empty or failed, with no reason beside it (`conversations-sidebar.tsx:656`). Composer send button when disconnected: the `ReconnectBanner` explains, but only after a drop. | code |
| C2 | Empty pane names what will appear and how | The empty transcript says "Start a conversation / Send a message to Riley" with four generic chips ("Help me get started", "What can you do?"…) — **no crew, no role, no model, no skills, no credentials**, nothing that tells a client who they are about to talk to (brief's known finding, confirmed). Files tab: "No files in this session yet" (a session has no files; the agent does) and a "Workspace — soon" section, which is a permanent dead control. Team tab: "No conversations yet · Agent-to-agent conversations will appear here" with no way to make one appear. Issues scope: "No issue has started work here yet · Open Issues ↗" — this one is right. | `base/chat-riley-draft-1440.png`, `base/chat-riley-files-1440.png`, `base/chat-riley-team-1440.png`, `fix/chat-issues-1440.png` |
| C3 | Errors keep input and retry | OK: roster / fan-out failures render `ScopeFailure` with Retry, a refused session create leaves the draft in the composer and toasts, history 5xx keeps existing turns. | code |
| C4 | Reload resumes | OK for the conversation: `/chat/<slug>?session=` is written on every pick with `replaceState`; a session of another kind moves the scope by a one-shot probe. Scope, search and facets are not in the URL (acceptable — the session is the state). | code |
| C5 | Nothing internal leaks | **Breadcrumb shows the slug**: "Crews / riley / Chat", and on a fresh workspace "Crews / _crewship-setup-guide / Chat" because the seed's Guide conversation is the freshest (brief's known finding, confirmed on a clean seed). Header shows the session id `cmtljnyt` / `rt-1c0c0` in mono. Origin chip says "CLI" for chats the CLI created — correct but unexplained. Routine-step chats get **no** origin chip at all: `OriginChip` maps UI/CLI/WEBHOOK/CRON/AGENT and not ROUTINE (`chat-panel.tsx:1028`). The Guide agent (`_crewship-setup-guide`, a `kind=setup` crew hidden from `/crews`) appears in the roster, in the Agents facet and in the "Not started yet" strip. | `base/chat-empty-1440.png`, `fix/chat-routine-open-1440.png`, `fix/chat-filter-open-1440.png` |
| C6 | Mobile 390 | Document overflow **0px**. The suggestion-chip rail is a `w-max` strip that scrolls inside its own container (4 elements measured beyond the viewport, last chip cut to "Sh" with no scroll affordance). Targets: header buttons 26px, chips 29px, Chat/Files/More tabs 27px, camera/attach 26px — under 44px. The drawer is 280px over a dimmed transcript, closes via backdrop or the collapse button — fine. | `data/chat-30-390.png`, `data/chat-30-mobile-drawer-390.png`, `report.json` |
| C7 | Copy says what it is | A routine-step chat opened from the Routines scope says "Start a conversation · Send a message to Riley" — it is a run transcript, not something to start. "Not started yet" is a row of four unlabeled 16px avatars plus a truncated comma list; it is a button that opens the picker, but nothing says so. "CONVERSATIONS 44" immediately above "TODAY 44" is the same number twice. A single delegation gets its own group header ("Delegated: check the arm64 ru…") because any multi-row group turns headers on for all (`groupRowsByRoutine`). | `base/chat-riley-draft-1440.png`, `fix/chat-routine-open-1440.png`, `fix/chat-routines-1440.png` |

### 2.3 Missing cross-links (README §5: agent → crew, chat, runs, skills, credentials; chat → agent's crew, role, model, runs, files)

| From the chat surface | Present | Missing |
|---|---|---|
| Header / breadcrumb | "Crews" and the slug both link to `/crews?agent=<slug>` | crew name + colour (→ crew), role title, model (`getModelLabel`), status dot, "N skills · M credentials" (both counts are already on the `/agents/{id}` row the page fetches) |
| Empty transcript | avatar, name | role, crew, model, what it can do (skills), suggested prompts of the agent (only when configured) |
| Conversation row | agent name, time, unread count, live dot | crew, kind pill under Direct (all direct, fine), link to the issue / routine run a MISSION/ROUTINE chat belongs to |
| Routine-step chat | routine name as group header | link to the routine (`/routines?routine=`), to the run (`/activity?run=`), origin chip |
| Right rail | Files (agent + crew scopes), Team (peer messages) | Runs of this agent (`/activity?agent=`), Skills, Credentials, the inbox items this agent raised, "open in Crews" |
| Unread badge | count | the inbox item that produced it (`/inbox-v2?item=`) |

### 2.4 Inconsistencies with README §2 / §3

- **No `SubBar`.** The identity is the toolbar breadcrumb; no counts, no live
  meta, no primary action. Routines/Issues (the layouts this column copies)
  do carry one.
- **Agent** rendered with `AgentAvatar` but no status dot on the header or
  the empty state; the live dot in the list is green, the contract says
  RUNNING = blue with halo, idle = green.
- **Model** not shown anywhere on the surface (the `ModelPicker` in the
  composer folder is not mounted by `chat-panel.tsx`).
- **Status pills**: origin chip is a coloured 10px label (`bg-info/15`), not
  the `rounded-full border` pill; connection badge only renders on
  non-connected states (deliberate, fine).
- **Type scale**: rows use `text-[10px]`, `text-[9px]` and `text-[11px]`
  literals rather than `text-micro/label/body`.
- **Motion**: list rows stagger in with `motion.div` (0.018s, capped at 12)
  without a `useReducedMotion` guard (`conversations-sidebar.tsx:786`);
  no arrival flash for a new row; no `layout="position"` on reorder.
- Empty states are `ConversationEmptyState` (centred icon + two lines), the
  150px block §2 forbids; Files/Team empty states are the same shape.

### 2.5 Scale (30 conversations, 7 agents; then 19 on one agent)

- 30 conversations across 7 agents: fine at all three widths, grouped
  "Today 31", search narrows with a count, Routines groups by routine and
  collapses per routine (`fix/chat-routines-1440.png`).
- The client asks each agent for `limit=10` (`PER_AGENT_CHAT_LIMIT`) but the
  server returned all 19 of Riley's direct chats to `?limit=10` (verified
  with curl: 19 rows, `X-Chat-Kind-Counts: direct=19`). So today the column
  shows everything and the "eviction" the code protects against is not
  happening — but nothing on the surface would say so if it did: there is no
  "10 of 19 · Show all" and no pagination. Whichever side is right, the
  column needs a fold (P2-9).
- `AGENT_FANOUT_CAP = 12`: with more than 12 live agents, the 13th agent's
  conversations are never fetched and never listed — not in the list, not in
  "Not started yet" (absent ≠ empty). A workspace with 100 agents sees 12
  agents' threads and no hint that 88 exist. `/chat/<slug>` for such an agent
  works only because `ensureSlug` promotes it into the 12.
- 12 requests on every scope switch (`kind` is a fetch parameter), plus one
  per scope for counts — fine at 7 agents, 12 at the cap, and the kind counts
  are summed client-side.
- Mobile 390 with 44 rows: drawer scrolls, 0px overflow.

## 3. Prioritised improvements

P1 = blocks a client from deciding or finding something, P2 = confusing,
P3 = polish. Each with the concrete UI change. File pointers are where the
change lands; no code was touched.

### P1

1. **Inbox row = decision, not a log line.** Row layout: kind pill (Escalation
   / Approval / Failed run / Missed run, dot + word, tone by urgency),
   title without the server prefix ("Need the Grafana API key…"), second line
   `Riley · Ops ● · 2m · expires in 6d`, and a verb on the right (Review /
   Approve / Answer) instead of a bare timestamp. Crew from
   `payload.crew_id` resolved through the crews list the page already can
   fetch (`inbox-v2-explorer.tsx` EntryRow; strip the prefix in
   `inboxEntry`, or better, stop composing it in
   `internal/api/escalation_handler.go:235`).
2. **Detail identifies things, not ids.** Replace the four Definition cells
   with: Subject = `AgentAvatar` + Riley (link `/crews?agent=riley`), Crew =
   `CrewIcon` + Ops (link), Kind = "Escalation · needs a yes/no" (from
   `escalation_type`), Arrived/Expires = absolute + relative. Drop the
   `field` caption line entirely (`inbox-detail.tsx` Definition). Hide
   `chat_id`, `crew_id`, `agent_id`, `escalation_type` from the Context card
   (`CONTEXT_HIDE_KEYS`) and render them as links in a "Where this came from"
   row: Open chat (`/chat/<slug>?session=<chat_id>`), Open crew, Open run.
3. **The inbox is never blank.** Empty Needs action / Updates / History:
   one `InlineEmpty` line each — "Approvals, escalations, failed runs and
   missed schedules land here", with links to Routines and Crews, and the
   last 5 resolved items under it. Empty reading pane with N items waiting:
   a triage card — "94 waiting · 2 approvals · 12 older than a day · 3 crews",
   crew chips that set the (new) crew facet, and "Open oldest". Empty pane
   with 0 items: same card in its zero form, never "Select an item".
4. **LINK and CREDENTIAL escalations show what is being approved.** The
   server should put `metadata` (the URL) and the credential name into the
   inbox payload (`escalation_handler.go`); the decision card then renders
   the URL as a `Pill`/link above the buttons and the credential name as the
   existing `credential_name` chip. Until the payload carries it, the card
   must say "The link the agent asked to open is only visible in the chat →
   Open chat" rather than nothing.
5. **Hire waitpoint gets a Deny and a link.** Render Deny next to Approve
   hire (the approvals-queue twin has `/decide` with `denied`; the suppressed
   row's id is in `payload.approval_id`), and make "its crew page" a link.
   Show Template as a name ("DevOps / SRE") and Model via `getModelLabel`
   or "Uses the template's model" (`kind-actions.tsx` waitpoint branch,
   `inbox-detail.tsx` MessageBody).
6. **Chat header says who you are talking to.** A one-row agent strip under
   the toolbar (or a `SubBar`): `AgentAvatar` with status dot, name, role
   title, `CrewIcon` + crew name (link), model label, `3 skills · 1
   credential` (links to `/crews?agent=<slug>` tabs), origin chip. The empty
   transcript reuses the same strip enlarged plus "What Riley can do" from
   the agent's skills, replacing the generic four chips when the agent has
   none configured (`chat-panel.tsx` desktop header and
   `ConversationEmptyState`; data is already in `ChatClientAgent` +
   `_count` on `/agents`).
7. **Breadcrumb shows the name, not the slug, and never the Guide's slug.**
   `app-toolbar.tsx:179` resolves `chatAgentSlug` to the roster name (the
   chat page already has it; publish it through `useAppStore.breadcrumbs`
   the way orchestration does). Agents whose crew is `kind=setup` are
   labelled "Crewship Guide" and excluded from the roster, the facet and
   "Not started yet" unless the Guide conversation is the one open.

### P2

8. **Fold 94 into priority · cap · fold.** Sections in Needs action:
   "Expiring" (deadline < 24h), "Approvals", "Escalations by crew" (each crew
   collapsible with its count), then "N more". Sort newest-first within a
   section; the oldest-first order today buries what just arrived. Add a
   Crew facet to the popover (real field `payload.crew_id`).
9. **Conversation column fold.** Per agent under Direct: 6 rows then
   "N more with Riley · Show all"; when the fan-out is capped, a footer row
   "88 more agents · Search to find them" that runs the picker. Team the
   server `limit` with the client's (today the server ignores `limit=10`;
   decide which is right in `internal/api/agent_chats.go` and test it).
10. **Routine-step chats read as runs.** Origin chip for ROUTINE (and label
    "Routine" / "Scheduled" / "Webhook" instead of "Hook"), empty-state copy
    "This step has not written anything yet" instead of "Start a
    conversation", and a row link to the routine (`/routines?routine=`) and
    the run. Suppress the group header when a group has one member.
11. **Filter popover scrolls visibly or grows.** `max-h-[360px]` clips the
    Deadline and State facets; either raise it to fit the nine types plus
    two facets (~480px) or show a scrollbar and a fade (`sidebar-kit.tsx:244`,
    shared primitive — note it in `docs/ux/CHANGELOG.md`).
12. **Words for outcomes and roles.** History rows and cards: "Approved by
    Demo" / "Rejected" / "Expired unanswered" / "Withdrawn" via
    `OUTCOME_LABEL` (exists, unused here) plus the decider's name; roles as
    "an owner or admin decides this". Facet labels: "Approval", "Question
    from an agent", "Failed run", "Missed run", "Paused schedule", "Memory
    proposal", "Issue review".
13. **Deadline truth for escalations.** Carry `answer_deadline_at` into the
    inbox payload as `timeout_at` so the Deadline facet and the row
    countdown stop saying "No deadline" for something that expires.
14. **"Not started yet" becomes a list.** Six `AgentAvatar` + name + role
    rows with a "Start" verb, folded after six ("4 more"). Tap target 44px on
    phones.
15. **View and filters in the URL** (`?view=history&type=approval&q=…`) so a
    reload or a shared link resumes; keep `?item=`.
16. **Page headers.** Both screens get a `SubBar`: Inbox — icon, "Inbox",
    "94 need you · 0 updates · 9 decided", live dot when the WS is connected,
    primary "Mark all read" (Updates), secondary "Open approvals". Chat —
    icon, "Chat", "44 conversations · 2 live", primary "New conversation".

### P3

17. Contract motion: arrival flash on a new inbox row and a new conversation
    row; `layout="position"` on reorder; rows fade out on resolve; wrap the
    column stagger in `useReducedMotion`; replace the full-height "Decision
    saved" block with an inline success strip above the next item.
18. Tap targets at 390: toolbar buttons, view rows, chips and tab strip to
    44px (sidebar-kit `SidebarRow`/`SidebarToolbar`, `AskRail`, chat mobile
    tabs). Give the chip rail a fade or arrows so it reads as scrollable.
19. Type scale literals (`text-[9px]`, `text-[10px]`, `text-[11px]`) →
    `text-micro` / `text-label`; origin chip → the standard pill.
20. Remove the "Workspace · soon" files section until it exists; rename
    "No files in this session yet" to "Riley has not written any files yet".
21. Session id chips in the chat header: keep behind a "Copy link" action,
    not on screen.
22. History outcomes column: split "Decisions" into decided-by-a-person vs
    decided-by-the-system (already derived by `isArchivedNotDecided`, but
    "Archived" is the wrong word for "cancelled").
23. Team tab: link each peer message to the delegation chat (`/chat/<from>?session=`)
    and say how one arises ("an agent delegating with @mention or the
    delegate tool").

## 4. Measurements

| Shot | Width | Horizontal overflow | Elements past viewport | Sub-32px targets |
|---|---|---|---|---|
| inbox-100 | 1440 / 820 / 390 | 0 / 0 / 0 px | 0 | 7 at 390 (toolbar 29px, view rows 28px) |
| inbox-100-detail2 | 1440 / 820 / 390 | 0 / 0 / 0 px | 0 | 6 at 390 (Back 29px, Approve/Reject 29px) |
| inbox-100-history | 1440 / 390 | 0 / 0 px | 0 | 7 at 390 |
| chat-30 / chat-30-riley | 1440 / 820 / 390 | 0 / 0 / 0 px | 4 at 390, all inside the scrolling chip rail | 12 at 390 (header 26px, chips 29px, tabs 27px) |
| chat-30-mobile-drawer | 390 | 0 px | 4 (same rail behind the drawer) | 12 |

Identifiers found in page text (cuids): 2 per open inbox item (crew id, chat
id), 2 on the hire item (agent id, crew id); `_crewship-setup-guide` in the
chat breadcrumb on a fresh seed. Console: repeated 404s on `/chat` (static
export path probes) and 409s on the inbox (mark-read on rows whose source
owns the state) — neither surfaces to the user.

## 5. What was verified vs read

Verified on screen: everything with a screenshot path above. Read from code
only: Updates grouping (`groupAdvisories`), message/failed_run/schedule
actions (`kind-actions.tsx`), missions merge, the approvals-detail links,
error paths (C3, D3), the fan-out cap. One false alarm worth recording:
clicking a filtered inbox row 600 ms after typing opened a different item
in my first run; with a 1.5 s settle the right item opened
(`fix/inbox-search-click-1440.png`), so it was the script racing the
filter, not a selection bug.

## 6. For the integrator (cluster D)

- `/inbox` (v1, `components/features/inbox/inbox-list.tsx`) still ships and
  is still linked from the dashboard (`dashboard-overview.tsx:256`,
  `bridge-strip.tsx:172`, `/inbox?kind=failed_run`) and from
  `crewship open inbox` (`cmd_open.go:98`), while the nav and the bell go to
  `/inbox-v2`. Two inboxes with different detail panes is a product
  inconsistency, not a cluster-A one. Decide which survives and redirect.
- `app/layout.tsx` pins `className="dark"`; there is no light theme to read
  colours against, so README §2's light rule cannot be checked by anyone.
- `sidebar-kit` rows and toolbar are 28–29px everywhere (Issues, Routines,
  Crews, Pages, Inbox, Chat); the 44px mobile rule has to be fixed once, in
  the kit.
- `SidebarFilterPopover` clips at 360px for any page with more than ~8
  facet options.
