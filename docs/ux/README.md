# Crewship UI/UX contract

Related public implementation notes: [dashboard](dashboard-client-overview.md),
[inbox](inbox-client-overview.md) and
[routines operator console](routines-operator-console-contract-2026-09-15.md).
Their dated design/validation records are not fresh verification results.

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
| Saving typed-in values | `PageSaveProvider` + `PageSaveBar` (`components/ui/page-save-bar`); cards register through `useDirtyForm` + `SaveFooter`, `SettingsSaveBar` or `usePageSave` | see "Saving" below |

### Saving

One rule for every screen in Crewship that edits typed-in values — Settings,
Admin and nested pages today; crew settings, routines and every other editor
adopt the same bar, never a Save of their own:

| Control | How it saves |
|---|---|
| Switch | at once; on failure it flips back and the error is a toast |
| Text, number, select, picker | ONE floating bar, bottom centre of the content pane: "N unsaved changes · Discard · Save" (⌘S / Ctrl+S). Never a Save button inside a card |
| A change across several workspaces | the same bar; its Save opens the dry-run preview, the confirm applies |
| An issue's fields (title, description, status, priority, assignee, dates, project, milestone, routine, labels) | the same bar, one PATCH for all of them; a status of Done, Cancelled, Duplicate or Review, or a new assignee, asks first and names the automations it starts. Start, Stop, review verbs, comments and links stay instant actions |
| Delete and other irreversible actions | its own button in the Danger zone and an `AlertDialog` |

Leaving with edits pending (section nav, a link, reload) asks "Leave with N
unsaved changes?" — Stay / Discard / Save and leave. A failed save is a toast
in the bottom-right corner that stays until dismissed, offers Retry and keeps
the edits; errors never sit inside a card. Success is quiet: the bar says
"Saved" for a moment. On a phone the bar docks above the tab bar. The bar
wears the success toast's green (`--save-bar-*` tokens in `app/globals.css`)
with a slow halo and a pinging dot (both off under reduced motion): pending
edits are one step from done and must not be overlooked.

Adopting it on a new screen:

1. Wrap the page in `PageSaveProvider` (outside its section nav, so the nav
   can ask before switching) and put `PageSaveBar` once inside a `relative`
   box around the scrolling content; give the scroller ~6rem bottom padding.
   `DrillPage` already does both.
2. Cards register their edits: `useDirtyForm` + `SaveFooter count={form.dirtyCount}`
   (the card's own strip disappears inside a provider), `SettingsSaveBar` for
   a list of drafts, or `usePageSave({ label, count, save, discard })` for
   anything else. `save` rejects on failure; the bar shows the toast.
3. Navigation that swaps the page's content without a link goes through
   `usePageSaveGuard()`.

Type scale: `text-micro` / `text-label` / `text-body`; mono only for machine
text (ids, times, counts, durations). Section labels use the `eyebrow`
utility.

### Harbor: where the look lives

The design language is Harbor (the marketing site's), expressed only through
tokens and a handful of utilities, so it can be changed in one place.

| What | Where | Rule |
|---|---|---|
| Light / dark palettes | `app/globals.css` (`:root`, `.dark`) | dark is the default; both are held to AA by `lib/__tests__/theme-contrast.test.ts` |
| Accent (brand) colour | `app/styles/accents.css` + `lib/theme/accents.ts` | one light and one dark block per accent; components never name an accent colour, they use `primary` / `primary-hover` / `primary-strong` |
| Theme + accent pickers | `components/layout/theme-switcher.tsx` (profile menu) | per browser; accent painted before first paint by `ACCENT_BOOT_SCRIPT` |
| Selected row | `.row-selected` (via `ListRow` / `SidebarRow`, `lib/interaction.ts`) | tint + hairline + straight 3px bar; never a `border-left` on a rounded row |
| Status chip | `StatusPill` and the `--chip-*-bg/fg` tokens | the only place a row shows severity |
| Icon in a box | `icon-tile` utility (`ConceptIcon variant="chip"`) | tint via `--ic`; neutral unless the icon itself is the status |
| Cards | `rounded-[20px] border bg-card`, hover `lift` | no resting shadow |
| Deep panel | `panel-surface` | heroes, terminals, graph canvases |
| Nested page | `DrillPage` (`components/layout/drill-page`) | a big sub-area (Settings › Crew links, › Audit log) gets its own route; its side panel REPLACES the parent's and starts with "← Parent", then the sidebar-kit toolbar (search, Filter, collapse) and collapsible `DrillNavSection`s whose values carry counts. Never a third column, never a nested page inside a nested page; on a phone the panel becomes a bottom sheet behind a "Filters" button, and view tabs stay on the page |

**One colour per row.** Severity is carried by the status pill. Icon tiles,
avatars and titles in the same row stay neutral, so the one red or amber on a
list is the thing to look at. The exception is identity: an entity that has
its own colour — a crew (`CrewIcon`) or a routine (`RoutineGlyph`) — is always
drawn in it, everywhere it appears. That colour names the thing; it never
carries status. Red is for what broke (failed run); amber for
what waits on a person (paused, needs a tool, approval).

**Adding an accent:** a light and a dark block in `accents.css`, a row in
`ACCENTS`. Never a status hue (green, red, amber). The tests fail if the two
files disagree or any pair drops below AA.

### Admin › Backups

Backups is a nested page, `/admin/backups` (`DrillPage`), like Security and
People. Its side panel holds the **Scope** (Whole instance, or Selected
workspaces with the same multi-select as Security, greyed out on the
instance-only Storage and Keys & alerts), Overview with its attention count,
**Runs** as facets (All, Failed, Incomplete, Manual, Pinned), **Plans** as rows
with "+ New plan", Recovery (New restore, Restore history, Drills) and the two
instance settings. Section, facet, plan, run and scope live in the URL
(`?section=&status=&plan=&run=&scope=&ws=`); an old `?tab=backups` link lands
here. Proof is always three levels (checksum, contents checked, test restore)
and a partial result reads as partial, in the warn tone — the server's verdict,
never one recomputed in the browser. Data retention is not a backup setting: it
stays in the Admin console. Sections live in `components/features/admin/backups/`;
`?demo=1` (never in a production build) draws them from `__fixtures__`.

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

Follow [CONTRIBUTING](../../CONTRIBUTING.md) for claims, branches and review.

1. Read this contract and the screen's code and hooks.
2. Build the UI and server, then use `scripts/throwaway-server.sh` for an
   isolated screenshot server; see the [script catalog](../../scripts/README.md).
   Check desktop 1440px, tablet 820px and phone 390px with representative data.
3. Keep internal audits, work allocation and before/after evidence in
   [private working context](../development/private-context.md). Publish only
   the documentation, reproducible tests and review evidence needed by public
   contributors. Do not start a new `docs/ux/audit-*.md` session log.
4. Fix dead ends, cross-links, shared anatomy and motion in that order.
5. Add regressions for changed behaviour and update the user-facing guide.
6. Describe shared primitive changes in the PR and the relevant public contract.
