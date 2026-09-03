# Crewship UI/UX contract

The rules every screen follows, so that four people (or four agents) working on
four areas at once produce ONE product. Written 2026-09-03 after the dashboard
and onboarding redesign; those two screens are the reference implementation.
If a rule here and a screen disagree, the screen is wrong.

## 1. What a screen is for

Every screen answers, top to bottom, in this order:

1. **What needs me** — approvals, escalations, failures, gaps. Each row carries a
   verb (Review, Inspect, Install, Answer), never a bare chevron.
2. **What is happening now** — live, with a pulsing dot only when realtime is
   actually connected.
3. **State of the objects this screen owns** — cards or a dense list, never a
   bare table when an entity has an icon, a colour and a status.
4. **Outcomes** — numbers with a sparkline and the window they cover.
5. **Related objects** — the cross-links in §5.

A screen that cannot answer 1 says so in one line, not with an empty pane.

## 2. Anatomy (reuse, do not reinvent)

| Piece | Component | Rule |
|---|---|---|
| Page header | `SubBar` (`components/layout/sub-bar`) | icon, title, `N things · M things`, live meta, primary + secondary action |
| Card | `DashboardCard` | 11px uppercase title, mono hint, `→` action link |
| Empty state inside a card | `InlineEmpty` (`components/ui/inline-empty`) | ONE line, an icon, an action. Never a 150px centred block |
| Empty page | `EmptyState` (`components/layout/empty-state`) | says what will appear here and the CLI/UI action that creates it |
| Status | `StatusPill` (`components/ui/status-pill`) + `formatStatus` (`lib/format-status`) | tones: success / blue / warn / danger / muted / purple. Never a colour alone: dot + word. No local pill maps |
| Crew | `CrewIcon` + name + colour dot | colour may be a palette id OR a hex — use `crewColor` / `crewColorHex` |
| Agent | `AgentAvatar` with status dot | RUNNING blue with halo, ERROR red, idle green |
| Model | `getModelLabel` | ids come from `config/models.json`, never typed in a component |
| Numbers | `AnimatedNumber` | count up on mount; `tabular-nums` always |
| Trend | `Sparkline` (`components/ui/sparkline`) | draws on mount, one hue, no axes |
| Cross-link | `entityHref()` (`lib/entity-links`) | every link to a crew, agent, issue, routine, run, page, credential, chat goes through it |
| Long list | `usePagedList` (`hooks/use-paged-list`) | `?limit&offset` + `X-Total-Count`; show `N of TOTAL` and a Show-more |
| Disabled primary button | a one-line reason beside it | onboarding's `blocking reason` pattern |
| Irreversible action | `AlertDialog` that says what is lost and where to recover | Skip setup, delete, nuke |

Type scale: `text-micro` / `text-label` / `text-body`; mono for ids, times,
counts. Radii: cards 12px, chips 6px, pills full. Surfaces: `bg-card` on
`bg-background`, borders `border-border/60`. Dark theme is the one the app runs
today (`app/layout.tsx` pins `dark`); design for it. Whether light is
supported is PLAN.md decision D5 — until it is decided, do not spend time on
light-theme readability, and do not paint colours that would be impossible
there either.

## 3. Motion (all under `useReducedMotion`)

- Sections enter with `Appear` staggered by `order` (0.045s apart, max 9).
- Live things pulse (`LiveDot`); nothing else loops.
- A row that ARRIVES flashes primary at 16% and fades over ~2s (attention strip).
- Lists reorder with `layout="position"`; rows leave by fading, not collapsing.
- Cards lift 2–3px on hover, spring 420/32. No scale on hover.
- Skeletons match the final geometry, so nothing jumps when data lands.

## 4. Scale

Design for 1 and for 100 of everything: 100 crews, 100 routines, 1 000
issues. The pattern is **priority, cap, fold**: what needs a person first,
then the busiest, a fixed number of full cards (6), the rest as a dense list
behind "N more · K need attention · Show all". Charts stack at most 8 series
plus "Other". Lists with search get a result count.

## 5. Cross-links — the map every screen honours

| From | Always links to |
|---|---|
| Crew | its chat, its agents, its routines, its issues, its pages, its credentials, its spend, its journal |
| Agent | its crew, chat with it, its runs, its skills, its credentials |
| Issue | its crew, the agent working it, its runs, its journal trace, comments |
| Routine | its crew, its schedules, last run, the pages it produces |
| Page | the crew that owns it, the producers (agent/routine) behind each panel |
| Inbox item | the object that raised it (run, routine, issue, credential) and the crew |
| Run / journal entry | agent, crew, issue, routine |
| Credential | crews and agents that hold it, integrations that need it |

Pages are the most independent object; still, every panel names its owner and
producer, and both are links.

## 6. Dead ends (the checklist for every PR)

- [ ] Every disabled primary action states why, next to itself.
- [ ] Every empty pane names what will appear there and one way to make it appear.
- [ ] Every error keeps the person's input and offers a retry.
- [ ] A reload mid-task resumes where the person was (URL is the state).
- [ ] Nothing internal leaks: no `_crewship-setup-guide` slugs, no cuids, no raw
      status enums in copy (`IN_PROGRESS` → "In progress").
- [ ] Mobile 390px: no horizontal overflow, 44px targets, one-column stacks.
- [ ] Copy says what it is: an account API key is not a CLI token; "$0.00" is not
      "not metered"; "Available" is not "unknown".

## 7. Process for one area

1. Read this file. Read the screen's code and every hook it uses.
2. Seed a throwaway server (`docs/ux/agent-brief.md` §Verification) and
   screenshot desktop 1440, tablet 820 and phone 390 with demo data; also with
   100 of the main object.
3. Write `docs/ux/audit-<area>.md`: what the screen is for, every dead end from
   §6, every missing cross-link from §5, what is inconsistent with §2.
4. Implement in the order: dead ends → cross-links → anatomy → motion.
5. Tests first for every pure derivation; Vitest for render behaviour that has
   a defect behind it; Playwright only for a flow.
6. Docs ship with the change (`docs/guides/<area>.mdx`).
7. Screenshots before/after in the PR. One PR per area; never touch another
   area's files except through a shared component in `components/ui`.
