# Client dashboard — 2026-09-06

**Public implementation notes.** Retained for contributors because they
explain the dashboard's data and interaction choices. Read the dated updates
below before treating the original layout as current. The implementation is
in `app/(dashboard)/page.tsx` and
[`components/features/dashboard/`](../../components/features/dashboard/).

Scope: main dashboard, issue #2433. Reuses the existing theme,
SubBar, DashboardCard, AgentAvatar, CrewIcon, StatusPill and entityHref.

Order: attention → review/results and routine activity → crews → one agent
reporting section and Pages → collapsed system details. Real issue owners
and identifiers come from `/api/v1/issues`; separate REVIEW and DONE/COMPLETED
queries prevent recent completions from displacing all review work. Each
query is capped at four; the overview displays four review issues, two done
issues and up to two successful routines from the existing recent-run feed.
“All issues” and Activity lead to the complete lists. No demo output or
simulated completion is used in the production component.

Crew icons and colours come from crew configuration; agent faces use the
shared persisted-avatar component. Routine results without an invoking agent
use the routine icon rather than an invented person. Waiting/paused/queued
routines are shown separately from running routines. Fetch errors retain
available results, explain the partial state and offer retry. The reporting
window applies to the agent metrics, not to the results list.

Hover changes colour/border without moving crew cards. Existing section
entrances respect reduced motion; dashboard stagger is capped at 90 ms.
The live connection label is static. Routine details and issue review use
existing product routes; this change does not redesign their detail panels.

## Status 2026-09-15

Landed as #2434 (2026-09-06) and compacted by #2540 (2026-09-14, "the
dashboard fits one screen", #2539): the hero heading that repeated the
sub-bar is gone (`app/(dashboard)/page.tsx` carries an `sr-only` h1 only),
Results & review are one-line rows, **Your crews** is one row per crew in
the column beside them (`fleet-board.tsx`), the agent run summary is one
strip of four numbers (Completed / Success / P95 duration / Spend), the
run-volume chart is shorter, cards use 12 px padding. The order above still
holds: attention strip → Results & review beside Routines running now / Up
next / Your crews → Agent run summary and Run volume by crew, with the Pages
strip → collapsed System details. The phone layer (#2484, #2487) stacks the
same zones in one column. Everything in the paragraphs above about data
sources, error retention and motion described the September 15 release; the tile
*shapes* (cards, sparklines per crew) do not — see the rows-not-cards note in
#2540. The September 25 update in #2697 places active work in Results & review,
removes System details and the dashboard header counts/status/actions, and
keeps the title beside the sticky reporting-window selector.

## Status 2026-10-17 (Harbor 2, #3384)

The Dashboard follows the Harbor 2 shell. The reporting window (24h / 7d /
30d) is a segment in the top bar's page actions, rendered through `SubBar`;
the page's own sticky period bar is gone, and phones do not offer the
window (the page reports 24 h). The page opens with a greeting by local
time and one live line (date · things that need you · agents running · next
routine), leaving out parts that are zero. Order: Get started card (after
onboarding) → attention tiles (at most four, priority decisions → failures →
reviews → capacity → schedules, the rest on one overflow line; "Nothing needs
you" only when the inbox answered) → Results & review (All / For review / In
progress / Finished; running work is a group of In progress) beside Up next
and Your crews (a two-column grid) → the run summary strip (no icons; a
sparkline only under Completed, drawn from run volume) → Run volume and Pages.
Cards are `DashCard` (`components/features/dashboard/dash-card.tsx`): radius
10, no shadow, one grey link; an empty card is one line and a failed read
replaces only its own card with Retry. Loading is the kit's `PageLoading`;
a workspace without crews is one `PageEmpty`. Tablet (768–1279 px) shows the
tiles and figures 2 × 2. The Routines dashboard still uses the earlier widgets
(`legacy-overview.tsx`).
