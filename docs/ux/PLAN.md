# UI/UX programme — consolidated plan (2026-09-03)

Sources: `audit-conversations.md` (A), `audit-work.md` (B), `audit-fleet.md`
(C), and the dashboard/onboarding work already on `onboarding-client-redesign`
(D). Analysis only; nothing below is implemented. Numbers in brackets are the
audit's own item numbers.

## 0. What every audit found independently — fix once, system-wide

These appeared in all three clusters and in the dashboard. They are the reason
a per-page polish pass would not hold; they go first, as shared work.

| # | Theme | Evidence | Fix (one place) |
|---|---|---|---|
| S1 | **The 100-row ceiling.** List APIs return 100 rows; pages never pass a limit and count what they got. | C1: "100 crews" with 103, deep links 404 past the cap. B1: "50 issues" with 1 015, search covers 100. A8: server ignores `limit=10`, client caps 12 agents silently. | Backend: `count` + cursor paging on `/crews`, `/agents`, `/credentials`, `/missions`, `/agent_chats`; respect `limit`. Frontend: one `usePagedList` hook; SubBar counts from `count`. |
| S2 | **Objects do not link to each other.** | A3/A6 (inbox → agent/crew/chat/run; chat → crew/skills), B4 (issue → runs → journal broken in the DTO), C10–C12 (crew canvas links to nothing; credential rows plain text). | One `entityHref()` map (`lib/entity-links.ts`) for crew, agent, issue, routine, run, page, credential, chat; DTOs carry the slugs the links need (`agent_slug`, `run_id`, `trace_id`, `mission_id`). |
| S3 | **Status is spelled 13 ways.** | C14: 13 local pill maps; B: dots without words; A: raw `approve/reject`, `TEXT/LINK`. | `components/ui/status-pill.tsx` (dot + word, five tones) and `formatStatus()`; delete the local maps. |
| S4 | **URL is not the state.** | B2/B3 (routines, activity never write the URL; dashboard links land unfiltered), C19 (`?tab`, `?dock`, `?id`), A (inbox facets). | Every screen: `?slug/?tab/?run/?q` written on change, read on mount; Back closes the detail. |
| S5 | **Internal words leak.** | A7 `_crewship-setup-guide` in breadcrumb and roster; C leaks PRD refs, cuids, enums, `harbormaster`; B leaks DSL/YAML/waterfall/chain/lens. | Hide `_`-prefixed slugs and `kind=setup` crews everywhere except the Guide chat; copy review per screen against README §6. |
| S6 | **Fetch failures render as "nothing here".** | B5, C17, A (Updates). | Error state with Retry that keeps input, distinct from empty, in every list shell. |
| S7 | **Mobile is unfinished below `md`.** | A2 (tap targets 26–29px in the sidebar kit), B6/B7 (journal, issues board), C5/C7 (roster grid clips, integrations rail). | Fix the shared `sidebar-kit` targets to 44px once; per screen: one-column stacks, overlay drawers with a scrim. |
| S8 | **Destructive actions use `window.confirm`.** | C18, B14. | `AlertDialog` naming what is lost and where to recover (onboarding Skip is the reference). |

## 1. Per-cluster P1s (blocks a client) — after §0

**A · Inbox + Chat** — `audit-conversations.md` §3
- A1 Inbox row is a decision: kind pill, title without server prefix, crew + agent + deadline, a verb.
- A2 Detail shows names and links, not `crew_id`/cuids; "Where this came from" row (chat, crew, run).
- A3 Inbox is never blank: inline empties with what lands here; a triage card when items wait.
- A4 LINK / CREDENTIAL escalations show what is being approved (payload change in `escalation_handler.go`).
- A5 Hire waitpoint gets Deny and a real crew link.
- A6 Chat header says who you talk to: avatar, role, crew, model, skills, origin chip.
- A7 Breadcrumb shows the name; the Guide never leaks its slug.

**B · Issues + Routines + Activity + Journal** — `audit-work.md` §7
- B1 Server-side count/search/paging on issues (part of S1).
- B2/B3 Routines and Activity write the URL; `/agents/<id>` dead route fixed; `?run=` accepts a bare id.
- B4 Issue → runs → journal: DTO carries `run_id`, `trace_id`, `agent_slug`; run list, not `runs[0]`; `mission_id` in journal (part of S2).
- B5 Failures are not empties (S6).
- B6/B7 Journal and issues board at 390 and 1440.

**C · Crews + Agents + Skills + Credentials + Integrations** — `audit-fleet.md` §6
- C1 Limits and true totals (S1).
- C2 Crew canvas "Needs you" strip with Install / Connect / Build / Inspect; fix Integrations reading fields the API does not send.
- C3 Agent "Waiting on your decision" banner gets its buttons.
- C4 Agent Issues cell → `/issues?assignee=` (404 today).
- C5/C7 Roster and Integrations on mobile.
- C6 Guide hidden from the fleet roster (S5).
- C8 Crew-scoped MCP servers visible in live Integrations.

**D · Dashboard, Pages, Settings, Admin, top bar**
- D1 Pages: "never produced" links to the producer that should fill it; owner and producer are links from the overview.
- D2 Settings → Sessions: group by device, show last activity, confirm "Sign out everywhere else".
- D3 Top bar "100 need rebuild" needs a path to act on it in bulk.
- D4 `/inbox` v1 still ships and is linked from the dashboard and `crewship open inbox` while nav uses v2 (A10) — retire one.
- D5 Light theme is pinned off in `app/layout.tsx` (A9); decide whether light is supported before anyone designs for it.

## 2. Suggested waves

1. **Wave 0 — foundations (one agent, sequential, ~3 days):** S1 backend paging and counts, S2 `entityHref` + DTO slugs, S3 status pill, S7 sidebar-kit tap targets, S8 dialog. Each ships with tests and lands before wave 1 starts, because every cluster consumes them.
2. **Wave 1 — clusters in parallel (three agents, own worktrees, one PR per cluster):** the P1 lists above in the order dead ends → cross-links → anatomy → motion, on top of wave 0.
3. **Wave 2 — P2/P3 and integration:** per-cluster P2s, then one consistency pass by the integrator across all screens against README §2/§3, then deploy to dev3 as one state.

## 3. Decisions for Pavel

1. S1 is backend work and the largest single item; do we take it now or cap the UI honestly ("100 of 103 · Show all") until paging lands?
2. Inbox v1 vs v2: which one survives (D4)?
3. Light theme: supported or not (D5)?
4. Order of wave 1: A (client-facing decisions) first, or C (the fleet is what a client buys)?

## 4. Design canvases (visual proposals, 2026-09-03)

| Cluster | Canvas |
|---|---|
| Dashboard (reference) | https://claude.ai/code/artifact/6190f31b-6a67-49d5-98f5-60ff8f4bb98c |
| A · Inbox + Chat | https://claude.ai/code/artifact/4f04a55c-2210-4cfb-b307-b8ac1856b4a0 |
| B · Issues, Routines, Activity, Journal | https://claude.ai/code/artifact/15ecf4bd-5af6-4708-a830-e9f0d2477cca |
| C · Crews, Agents, Credentials, Integrations | https://claude.ai/code/artifact/48e72792-333a-4b7e-97fe-d55312df072d |
| D · Pages, Settings, Admin | https://claude.ai/code/artifact/2096b4ff-1d85-4468-8e84-293be3c8587b |

All static mockups in the app's tokens with sticky notes keyed to the audit
items. Sample states beyond the seed (errors, running agents, 100+ objects) are
invented to show the design and are labelled as such in each canvas's notes.
