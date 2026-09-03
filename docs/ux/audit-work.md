# Audit — cluster B · Work & execution (`/issues`, `/routines`, `/activity`, `/journal`)

Written 2026-09-03 against `docs/ux/README.md` (§1–§6). Analysis only: no code
was changed, nothing was deployed. Every finding carries a `file:line` or a
screenshot path so the next agent on this cluster can start from here.

## 0. Method and assumptions

- **Tree audited:** `/srv/crewship/crewship_3` at `3fa36df5` plus the
  uncommitted dashboard/onboarding work on `onboarding-client-redesign`. None of
  the cluster-B files are modified in that tree, so the code read is `main`.
- **Throwaway server:** the dev3 binary (`/tmp/crewship-3-dev`, built 12:50
  from a `web/out` newer than every file under `app/` and `components/`) was
  copied and started with the brief's env on **port 8097, not 8096** — Caddy
  already listens on 8090 and 8096 on this box. `CREWSHIP_DATA_DIR` had to be
  set or the instance dies on the shared `/tmp/crewship.sock`. Start script and
  pidfile: `$SCRATCH/start.sh`, `$SCRATCH/srv/pid`; it is still running so
  screenshots can be re-taken (`kill $(cat $SCRATCH/srv/pid)` to stop it).
- **Data:** `crewship seed` (demo@crewship.ai / password123): 3 crews, 7
  agents, 15 issues, 7 projects, 39 routines, 4 pages. No Docker, so crew
  provisioning failed and **no agent ever ran**; the only run in the workspace
  is the seeder's `page-watch` routine run (`run_cmtljjya400015a96ecf0`). This
  matters for the "one timeline" question: the issue → run leg could only be
  audited from code and the API, not from a live run.
- **Scale:** 1 000 issues (`crewship issue create`, 35 s) and 100 routines
  (`crewship routine save`, 4 s) added through the CLI. `crewship routine init`
  output cannot be saved as-is — its `agent_slug: "your-agent"` is refused with
  422 — so a per-crew copy with a real agent slug was used.
- **Screenshots:** `$SCRATCH = /tmp/claude-1000/-srv-crewship-crewship-3/a90a9b51-281f-4bf4-8469-e5ce7af3879f/scratchpad`.
  `$SCRATCH/shots/baseline/<page>-<width>.png` is demo data;
  `$SCRATCH/shots/scale/<page>-<width>.png` is after the bulk load. Each has a
  `.txt` sibling with the page's `innerText`, and `overflow.tsv` with the
  measured horizontal overflow. Widths 1440 / 820 / 390, dark theme.
- **Overflow, measured** (max element right edge minus viewport; page
  `scrollWidth` never exceeded the viewport, so these are *contained* scrollers):

  | Page | 1440 | 820 | 390 |
  |---|---|---|---|
  | `/issues` board and list | 382 px | 1 002 px | 1 088 px |
  | `/routines?slug=…` detail | 199 px | 274 px | 112 px |
  | `/journal?tab=runs` | 0 | 0 | 9 px |
  | everything else | 0 | 0 | 0 |

- Three read-only sub-audits (routines, activity, journal + issue detail) were
  run over the full component trees; their `file:line` citations are folded in
  below and were spot-checked against the files I read myself
  (`orchestration-layout.tsx`, the issues views, `issue_handler_runs.go`,
  `sub-bar.tsx`, `dashboard-overview.tsx`).

## 1. The question: does "one timeline" hold?

**No.** A client cannot follow an issue into its runs into the journal and back
without a dead end. The chain breaks in four places, two of them in the API:

1. **Issue → its runs.** The issue detail shows exactly one run
   (`issue-card-detail.tsx:474` renders `runs[0]`; the other 99 the endpoint
   returns are discarded) and one link, `Open full trace` →
   `/activity?mission=<id>` (`:947-953`). The run rows the API returns carry an
   *assignment* id, status, agent **name**, task, timing
   (`internal/api/issue_handler_runs.go:14-24`) — no agent id, no trace id, no
   run id — so no row can link anywhere. The bottom-drawer Runs tab on `/issues`
   is a bare table of the same rows (`bottom-panel/runs-tab.tsx:84-118`) with
   raw status text and no click-through.
2. **Run → the journal.** `/activity`'s run drill-down embeds the run's steps
   but has **no link to `/journal`** — the "Chain" card offers *Copy trace id*,
   a clipboard write (`activity-detail.tsx:353`). Its only outbound links are a
   bare `/journal` on the overview (`activity-overview.tsx:367`), the routine
   (`routine-runs-page.tsx:152`), the issue (`drill-downs.tsx:135`), and
   `/agents/<id>` (`drill-downs.tsx:397`) — **which is a dead route**
   (`app/(dashboard)/agents/page.tsx:23` redirects to `/crews`; there is no
   `[id]` segment). Screenshot: `shots/scale/activity-pipeline-and-run-1440.png`
   (the run page; the h1 is the raw run id and the Result card is raw JSON).
3. **Journal → anything.** The timeline row has **zero hyperlinks**: crew is an
   icon (`logs-list.tsx:275-292`), agent is an avatar (`:218-268`), the expanded
   detail's `agent_id` / `crew_id` / `trace_id` are buttons that re-filter the
   same page (`:488-495`), and `mission_id` is plain text (`:473`). The Runs tab
   links a run only to `/journal?tab=timeline&trace_id=` (`runs-view.tsx:574`)
   and the agent to `/crews?agent=` (`:94-95`); its run DTO has **no
   mission/issue field** (`internal/api/runs.go:28-99`; the aggregate never
   projects `mission_id`, `internal/journal/runs.go:183-196`), so run → issue is
   impossible from the journal. `grep /activity components/features/journal`
   returns nothing: journal → activity does not exist. Screenshots:
   `shots/baseline/journal-1440.png`, `journal-runs-1440.png`.
4. **Back to the issue from the journal.** Even if a link existed,
   `/journal?mission_id=` is not a URL key (`hooks/use-journal-url-state.ts:56-71`)
   and renders the unfiltered timeline. The issue's own `trace_id` is written
   at creation (`issue_handler_create.go:236`) but never selected by
   `issueSelectQuery()` (`issue_handler.go:394-418`), so the client-side
   `issue.trace_id` is `undefined`.

Two more breaks on the routine side of the same story:

- The three "runs" counters disagree on demo data: Routines says **1 run**,
  Activity's rail says **1**, the journal Runs tab says **No runs yet** (it
  aggregates `run.*` agent-run entries, not pipeline runs). A client sees three
  answers to "did anything run?". Screenshots `shots/baseline/routines-1440.png`,
  `activity-1440.png`, `journal-runs-1440.png`.
- Two run-id shapes leak into links. The dashboard, routines overview and inbox
  all link `/activity?run=<id>`; when that id comes from the journal-backed
  `pipelines/{slug}/runs` endpoint it is `j_…`, and `/activity` renders "This
  run's record is not loaded" (`shots/baseline/activity-run-1440.png`). With the
  pipeline-run id `run_…` the page works only when `?pipeline=` is also passed;
  `?run=run_…` alone shows the same empty state
  (`shots/scale/activity-pipelinerun-1440.txt`). And after the 1 000-issue load
  the run page's own journal section says "No activity matches these filters …
  holds 300 events" because the 300-event window no longer contains the run
  (`shots/scale/activity-pipeline-and-run-1440.png`, bottom).

What *does* hold: `/issues/<id>` ↔ `/issues?issue=<id>` (URL is the state,
reload and Back work, deep links to issues outside the loaded 100 render —
`shots/scale/issue-ENG-300-inline-1440.png`); routine detail → last run →
activity run page (`/activity?pipeline=<slug>&run=<id>`,
`routine-card-detail.tsx:790-794`); journal Runs row → journal trace; and
`/journal` itself is fully URL-driven.

## 2. `/issues`

**Purpose.** The board/list of the workspace's issues with a left explorer
(projects, issues, filters), a full-width detail when one is selected, and a
bottom drawer (Activity / Runs / Changes / Comments / Spec / Docker) scoped to
the selected issue. `app/(dashboard)/issues/page.tsx` → `OrchestrationPageShell`
→ `OrchestrationLayout mode="issues"` (`components/features/orchestration/`).
Screenshots: `shots/baseline/issues-{1440,820,390}.png`,
`issue-ENG-1-page-1440.png`, `issue-ENG-1-page-390.png`.

It is the strongest screen in the cluster: `SubBar` with icon + title + count +
one soft primary (`orchestration-layout.tsx:569-589`), status chips with live
counts (`issues-status-chips.tsx`), shared filter state between explorer and
chips (`use-filtered-issues.ts`), URL-held selection (`use-issue-detail.ts`),
keyboard shortcuts, sticky per-user view mode, bulk edit that keeps the
selection on refusal (`issues-list-view.tsx:449-480`), and a detail that uses
`Appear`, `StatStrip`, `Pill` with icon + word and status-aware empty copy.

### Dead ends (§6)

| # | Where | Finding | Evidence |
|---|---|---|---|
| I-1 | header count | `N issues` in the SubBar is `missions.length` from a `limit=50` call (`orchestration-page-shell.tsx:34`, `orchestration-layout.tsx:572`), the chips count the `limit=100` issues call (`:338`). With 1 015 issues the header says **50**, the chips **100**, the explorer **100**. | `shots/scale/issues-1440.png` |
| I-2 | board | The board is a horizontal scroller of five fixed 280 px columns (`issues-board-view.tsx:1453`). At 1440 with the explorer open the **Done column is off-screen** (382 px hidden); at 820 only 2½ columns show; at 390 it is 1 088 px wide. §6 asks for one-column stacks at 390. | `shots/baseline/issues-{1440,820,390}.png`, `overflow.tsv` |
| I-3 | empty board / list | Board: "No issues yet … Create an issue" is a centred 50 vh block (`issues-board-view.tsx:1424-1443`); list: "No issues found / Create your first issue to get started." with **no action** (`issues-list-view.tsx:536-545`); explorer: "No issues found" with no clear-filters (`unified-explorer.tsx:296-303`); filtered-empty board columns say "No issues" per column — four different empties for one state. | code |
| I-4 | list filtered to zero | With a search that matches nothing the board renders five empty columns and no "0 results, clear search"; the list shows "Create your first issue" even though issues exist. | `shots/scale/issues-search-1440.png` (search matched 10; the four empty columns read "No issues") |
| I-5 | fetch errors | Every list fetch swallows errors (`orchestration-page-shell.tsx:41-46`, `orchestration-layout.tsx:335-369`): a 500 renders "No issues yet". In the detail, all six sub-resources fall back to `[]` (`issue-detail-surface.tsx:170-183`), so a failed comments call renders "Nobody has said anything about this issue yet." No retry anywhere. | code |
| I-6 | unknown identifier | `/issues/ENG-9999` renders one grey sentence, "Issue not found", with no back link, retry or search (`issue-detail-surface.tsx:627-633`); the breadcrumb keeps the bad id. | `shots/baseline/issue-missing-1440.png` |
| I-7 | bottom drawer | Six unlabeled icons on mobile (`orchestration-layout.tsx:1069`); "Select an issue to inspect its {tab}" as the empty pane; Runs tab shows raw `run.status` text (`bottom-panel/runs-tab.tsx:104`) and "Failed to load: HTTP 500" with no retry (`:79`); Docker tab is developer tooling on a client screen. | `shots/baseline/issues-390.png` (row of icons at the bottom) |
| I-8 | leaks | Metadata band prints the raw cuid (`issue-card-detail.tsx:824`), the latest-run card's subtitle is the raw assignment id (`:932`) and its title lower-cases the enum, `Last run · in_progress` (`:931`); History rows print `a.action.replace(/_/g," ")` and raw `details` (`:1061-1064`); `via` shows `user_api` (`:822`). | code |
| I-9 | mobile 390 | Explorer becomes an overlay, fine; but the collapsed-explorer button floats over the board toggle (`orchestration-layout.tsx:606-613`); the detail's rich-text toolbar wraps into three rows of 20 icons (`shots/baseline/issue-ENG-1-page-390.png`). Drawer tab targets are 28 px tall (`:1058`). | screenshots |
| I-10 | disabled without reason | Bulk Status/Priority buttons and the create modal's submit have no stated reason when inert; `Run routine` disappears instead of disabling (`issue-card-detail.tsx:554-557`, acceptable because the Routine card explains). | code |

### Missing cross-links (§5: issue → crew, agent, runs, journal trace, comments)

| Link | Status | Where |
|---|---|---|
| → crew | **plain text ×4**; `crew_slug` is in the payload (`issue_handler.go:395`) | `issue-card-detail.tsx:273, 509, 541, 823` |
| → agent working it | **plain text**, avatar without status dot | `:279-287, 526-539` |
| → runs | one run, one link to `/activity?mission=`; 99 rows discarded; rows unlinkable (no run/trace id in DTO) | `:474, 947`; `issue_handler_runs.go:14-24` |
| → journal trace | **absent**; `/journal` cannot filter by mission anyway | see §1 |
| → routine | editable mode shows a picker with no link (`:561-567`); read-only chip links `/routines?routine=` (`:574`) — **wrong param**, `/routines` reads `?slug=` (`routines-layout.tsx:82`) | `shots/baseline/routine-page-watch-1440.png` is what that link lands on: the unfiltered overview |
| → comments, project, related issues, PRs | present | `:726-778, 645, 386, 410` |
| explorer/board rows → crew, agent | crew name is a filter facet, not a link; agent avatar is decorative | `unified-explorer.tsx:275-295`, `issue-card.tsx` |

Link inventory of `/issues/ENG-1` (every `<a href>` outside the nav):
`shots/scale/issue-ENG-1-links.txt` — three related issues and "open" project.
Nothing else.

### Anatomy (§2)

- Uses `SubBar`, `StatusIcon` + word, `AgentAvatar`, `Appear`, `StatStrip`,
  `Pill`, `DetailCard`, sticky rail. Good.
- Not used: `CrewIcon` for the crew (text only), `AnimatedNumber` (StatStrip
  values are strings), `Sparkline` (project progress is a div), `EmptyState`
  from `components/layout`, `InlineEmpty`, `AlertDialog` (relation and PR
  removal delete without confirmation — arguably fine).
- The detail page (`/issues/<id>`) hand-rolls its chrome bar instead of
  `SubBar` (`issue-page-client.tsx:62-95`); the inline variant has a second,
  different back-bar (`orchestration-layout.tsx:736-752`).
- The "running" pulse on the run timeline ignores the stream status it already
  subscribes to (`run-activity-timeline.tsx:130, 225-229`) — pulses while
  offline.
- Dead files still in the folder: `activity-feed.tsx` (has the human
  `actionLabel` map the History card should use), `property-row.tsx`,
  `assignee-picker.tsx` (type only), `issues-toolbar.tsx` (the old toolbar
  with a `New Issue` button; the live strip is `issues-toolbar-strip.tsx`).

### Scale (1 000 issues)

- **Hard cap 100, undisclosed.** Board, list and explorer show the newest 100
  (`orchestration-layout.tsx:338`); no "N more", no pagination, no server
  search: the search box filters the 100 loaded rows
  (`use-filtered-issues.ts`), so "Load issue 99" finds 10 of 11 matches and a
  client cannot find an older issue at all except through ⌘K.
  `shots/scale/issues-1440.png`, `issues-search-1440.png`.
- Status chips count the 100, not the workspace (`All 100`), so "how many are
  in progress" is wrong by construction.
- No priority-cap-fold: `Backlog 100` is one column of 100 cards; there is no
  "needs attention first" ordering (overdue, failed, review) — the board is
  status columns in server order.
- List view at scale is fine visually (`shots/scale/issues-listmode-1440.png`)
  but is a plain table (§1.3: "never a bare table when an entity has an icon,
  a colour and a status") — it has status icon + word, but no crew icon, and
  no avatar.
- History is capped at 50 server-side with no marker (`issue_handler_workflow.go:160`);
  comments are uncapped and unvirtualised (`issue_handler_comments.go:29-38`).
- Each issue open is ~13 requests (`issue-detail-surface.tsx:174-183` +
  roster + milestones + pipelines + automations); `usePipelines` loads all
  139 routines for one picker.

## 3. `/routines`

**Purpose.** Overview (KPIs, catalog donut, firing next, recent runs, waiting
on you, 7-day outcomes, recently failing) or one routine's detail (identity,
stat strip, definition graph + editor, last run, triggers/versions, access,
runs). `app/(dashboard)/routines/page.tsx` → `RoutinesLayout`
(`components/features/routines/`). Screenshots: `shots/baseline/routines-{1440,390}.png`,
`shots/scale/routines-1440.png`, `routine-slug-page-watch-1440.png`,
`routine-page-watch-full-1440.png`, `routine-slug-load-{1440,390}.png`.

This is the screen that already speaks the dashboard's language: `SubBar`
with `N routines · M runs`, `DashboardCard` for all six cards, `KpiCard`,
`StatusDonut`, `Appear` stagger within the cap, geometry-true skeletons,
`CrewIcon` everywhere, and the "Waiting on you" card that answers §1.1.

### Dead ends (§6)

| # | Where | Finding | Evidence |
|---|---|---|---|
| R-1 | URL | **The URL is not the state.** The page reads `?slug=` once (`routines-layout.tsx:82-86`) and never writes it; selecting a routine leaves the URL at `/routines`, reload returns to the overview, Back leaves the page, and no filter, tab, editor or run selection is shareable. | `shots/scale/routine-slug-page-watch-1440.png` is only reachable by typing the URL |
| R-2 | inbound links | Two callers link `?routine=` (`dashboard-overview.tsx:460`, `issue-card-detail.tsx:574`), which the page ignores → unfiltered overview. Canonical is `lib/routine-href.ts` → `?slug=`. | `shots/baseline/routine-page-watch-1440.png` (= overview) |
| R-3 | empty panes | All overview empties are a centred `py-7` block with no action prop (`routines-overview.tsx:512-519`): "No schedule is due. Open a routine and add one under Triggers." names a control three levels deep, as prose; "Nothing is waiting on a decision." and "Nothing ran in the last 7 days." name nothing; explorer "No routines found" has no clear-filters (`routines-explorer.tsx:530`); detail "This routine hasn't been invoked yet." has no Run action (`routine-card-detail.tsx:719-721`). | `shots/baseline/routines-1440.png` |
| R-4 | errors | No retry on the list (`routines-overview.tsx:177-181`, message is the raw `pipelines list: 500`), the detail (`routines-detail-panel.tsx:572-576`, `fetch routine: 404`), the runs tab or schedules tab. Import keeps input but shows `Error: 422: <server body>`. | code |
| R-5 | disabled without reason | Import (`routines-layout.tsx:452`), Run and Cancel (reason only in a `title` tooltip, `routines-detail-panel.tsx:629-674`), Rename/Duplicate, Create. | code |
| R-6 | irreversible | Disable, Reject, Delete schedule use `window.confirm` (`routines-detail-panel.tsx:331,334`, `routine-schedules-tab.tsx:80`), not `AlertDialog`, and none says where to recover. | code |
| R-7 | leaks | Raw run ids as row titles and card subtitles (`routine-card-detail.tsx:1043, 750`; `routines-overview.tsx:609`), `Last Run · COMPLETED` (enum, upper-cased), `0% agent steps`, `page.write` chips, `DSL version`, `chain 2`, "waterfall", "wake gate", "Agentless · token-zero", a dock tab literally labelled **YAML**, cron strings and a raw JSON textarea in the schedule form. Full table in the sub-audit; the visible ones on demo data are in `shots/scale/routine-slug-page-watch-1440.png`. | screenshot |
| R-8 | mobile | The detail's React-Flow definition canvas is a 380 px-tall pan/zoom surface at 390 (`routine-card-detail.tsx:403-449`); the bottom dock is a 320 px drawer that mounts on every selection (`bottom-panel/index.tsx:80`); KPI grid has no one-column fallback (`routines-overview.tsx:185`); 112 px contained overflow inside the detail. Overview itself stacks well (`shots/baseline/routines-390.png`). | `shots/scale/routine-slug-load-390.png` |
| R-9 | donut | Clicking the "Disabled" slice maps to `filter: "all"` (`lib/routines-overview.ts:367`); on mobile the sidebar it filters is collapsed so the click has no visible effect. | code |

### Missing cross-links (§5: routine → crew, schedules, last run, pages it produces)

| Link | Status | Where |
|---|---|---|
| → crew | **missing**; the agent chip links bare `/crews` (`routine-card-detail.tsx:305-312`); `author_crew_id` is fetched and never rendered | code |
| → schedules | in-page only, no per-schedule link to its runs | `:796-844` |
| → last run | present: `/activity?pipeline=<slug>&run=<id>` | `:762-794`; `shots/scale/routine-page-watch-links.txt` |
| → pages it produces | **missing entirely** — `page-watch` writes two panels and the detail never names the page | screenshot shows `WRITES TO CREWSHIP · page.write`, no link |
| run row → agent / crew / issue | none, although `PipelineRun` carries `invoking_crew_id`, `invoking_agent_id`, `issue_identifier` (`hooks/use-pipeline-runs.ts:43-51`) | `routines-overview.tsx:329-369`, `routine-card-detail.tsx:1029-1068` |
| → journal | none | — |

### Anatomy (§2)

- Uses correctly: `SubBar` (+ one soft primary), `DashboardCard`, `KpiCard`,
  `StatusDonut`, `CrewIcon`, `Appear`, `StatStrip`, `Pill`, skeletons.
- Hand-rolled: pills with a dot only in the detail-panel hero (everything else
  is `Pill` = colour + word, no dot); the overview's own `Empty`; agent avatars
  as `background-image` spans (`routines-explorer.tsx:283-287, 500-506`);
  `LastRunCard` bypasses `DetailCard`; raw `<button>`s for Approve/Deny, the
  Triggers/Versions switch, Manage, Edit code; three separate status-colour
  functions.
- Not used: `AnimatedNumber`, `Sparkline` (the trend is a recharts bar chart
  with axes and grid, the opposite of §2), `LiveDot` (`animate-pulse` on bare
  spans regardless of realtime state), `getModelLabel`, `AlertDialog`, the
  SubBar `meta` slot (no live indicator at all).
- §1 order: KPIs first, "Waiting on you" fourth.
- Dead code in the folder: `routine-overview-tab.tsx`, `routines-filter-sidebar.tsx`
  (which had the only "{n} of {N}" result count), `live-run-row.tsx` belongs to
  the header bell.

### Scale (139 routines)

- The explorer renders all 139 rows, unsorted by attention, no cap/fold, no
  "of N" beside the search (`routines-explorer.tsx:375-521`); search works
  ("Load routine 9" → 11, `shots/scale/routines-search-1440.png`) but the count
  is a bare section badge.
- KPIs are computed over the last **100 runs**, not a time window
  (`hooks/use-pipeline-runs.ts:74`), so "Success · 7d" silently becomes
  "success over the last 100 runs" once routines are on schedules. Nothing on
  screen says so.
- Two full run feeds poll at once (provider `limit=200` + overview
  `limit=100`), the whole catalog refetches on every run completion
  (`hooks/use-pipelines.ts:125-132`), schedules are fetched workspace-wide three
  times, and `RoutineReachCard` fires two requests per agent row.
- "Waiting on you" is uncapped (`routines-overview.tsx:390-421`).
- Overview at 139 is otherwise unchanged and fine (`shots/scale/routines-1440.png`).

## 4. `/activity`

**Purpose.** "What is happening, and where do I look": a 280 px rail of
lenses (Workflows / Issues / Agents / Routines) with status segments, an
overview (Running now / Waiting on you / Failures / Spend, Open asks, What is
broken, Latest activity), and in-page drill-downs for a workflow, a routine's
runs, a run, an issue, an agent, or a single event.
`app/(dashboard)/activity/page.tsx` → `ActivityStreamView`
(`components/features/activity-stream/`). Screenshots:
`shots/baseline/activity-{1440,390}.png`, `activity-run-1440.png`,
`shots/scale/activity-1440.png`, `activity-pipeline-and-run-1440.png`.

### Dead ends (§6)

| # | Where | Finding | Evidence |
|---|---|---|---|
| A-1 | URL | Reads `?mission`, `?pipeline`, `?run`, `?status` **once at mount** (`activity-stream-view.tsx:159`, empty deps) and **never writes**. Every lens, scope, drill-down and opened event is React state: reload loses the walk, Back leaves the page, nothing is shareable. Escape and j/k are the undocumented substitutes (`:531-543`). | code |
| A-2 | deep link | `?run=<j_…>` (the id the routines API and dashboard emit) → "This run's record is not loaded / The run's own row is fetched per routine, and this run was opened without one." No action. Same for `?run=<run_…>` without `?pipeline=`. | `shots/baseline/activity-run-1440.png` |
| A-3 | dead link | `Agent page ↗` → `/agents/<id>` has no route (`drill-downs.tsx:397`; `app/(dashboard)/agents/page.tsx:23` redirects `/agents` → `/crews`). | code |
| A-4 | retry | `TopologyCard`'s "Try again" calls `setChain(null)` which is not in the effect deps (`topology-card.tsx:96, 124`) — it does nothing, on both the workflow page and the event detail. `useChains` errors are never destructured (`activity-stream-view.tsx:293-297`) so a failed index renders "No workflows yet". Missions and chain fetch failures are swallowed into "nothing here" copy (`:219-234`, `activity-detail.tsx:129-131`). | code |
| A-5 | empty panes | Seventeen distinct empty sentences (sub-audit lists them all); only three carry an action. The routine runs page's "This routine has not run yet." has no Run; the issue drill-down's "open the issue for its own history" names a button that is only rendered when the list is non-empty (`drill-downs.tsx:133, 156-160`). The rail's "Nothing composed a process in this window. 1 run happened on their own — they are under Routines." is the default state on demo data and links nowhere. | `shots/baseline/activity-1440.png` |
| A-6 | leaks | The run drill-down's `<h1>` is the raw run id, the Result card is a JSON dump, Status/Trigger are raw enums (`drill-downs.tsx:267-284`); the event detail's sub-header is the raw `entry_type` and the Record card dumps payload JSON (`activity-detail.tsx:209, 388-418`); "keeper request" in the Open-asks copy; "chain", "walk", "topology", "lens", "workflow", "(loaded window)", "(graph only)" throughout; every feed row's spine chip is `#<5 chars>` of a cuid. The feed on demo data reads "routine run run_cmtljjya400015a96ecf0 pushed watch/entry". | `shots/scale/activity-pipeline-and-run-1440.png`, `shots/baseline/activity-1440.png` |
| A-7 | mobile | At 390 the rail opens **over** the overview with no scrim (`shots/baseline/activity-390.png`); the SubBar's event count is hidden below `sm`; workflow rows carry 130 px of fixed gutters plus indent (`workflow-page.tsx:555, 571, 626`); rows are 40 px or less. | screenshot |
| A-8 | KPI tiles | Tiles are buttons when > 0 and divs at 0 with no reason (`activity-overview.tsx:243-263`); Spend is never clickable; card actions vanish at zero. | code |

### Missing cross-links (§5: run / journal entry → agent, crew, issue, routine)

The whole screen has **four** outbound `href`s (`activity-overview.tsx:367`
bare `/journal`; `routine-runs-page.tsx:152` routine; `drill-downs.tsx:135`
issue; `drill-downs.tsx:397` dead agent route). Link inventory:
`shots/scale/activity-links.txt`, `activity-runid-links.txt` — nav only.

| From the run drill-down | Status |
|---|---|
| → agent | absent (`PipelineRunRecord` has no agent field) |
| → crew | absent |
| → issue | absent; `PipelineRun.issue_identifier` exists on the wire and is unused |
| → routine | plain `Pill` with the slug (`drill-downs.tsx:284`) |
| → journal trace | absent; "Copy trace id" only |

Feed-row actor and crew names are spans (`feed-row.tsx:123-124`); the spine
chips look like links (`role="link"`) but set a client filter. The workflow
page routes every click to an in-page card and never to `/issues/<id>`,
`/crews`, or `/routines?slug=` although it holds `routine_slug` and
`issues[].identifier` (`hooks/use-chains.ts:25, 75`).

### Anatomy (§2)

- Uses: `SubBar` (title + `N events · past 24 hours`; live badge in `actions`
  not `meta`), `DashboardCard`, `KpiCard`, `CrewIcon`, `AgentAvatar` (no
  status dot), `Appear`.
- Hand-rolled: two private `Empty` components plus ad-hoc centred blocks
  (`activity-overview.tsx:104`, `lens-overviews.tsx:57`, …); bare status dots
  with no word everywhere (`feed-row.tsx:111-117`, `activity-sidebar.tsx:374`,
  `workflow-page.tsx:286`); `LiveBadge` instead of `LiveDot`; spinners
  instead of skeletons inside the view.
- Not used: `AnimatedNumber`, `Sparkline` (the only chart appears when the
  window spans ≥ 2 days, so at the default 24 h there is no trend at all),
  `getModelLabel`, `layout="position"`, arrival flash, `components/layout/empty-state`.
- Live indicator is honest (EventSource state, `hooks/use-journal-stream.ts`)
  but pulses in the degraded/polling state too, and `gapDetected` /
  `reconnect` are discarded — no way to force a reconnect (by design,
  `activity-stream-view.tsx:736-741`).
- `components/features/activity/runs-view.tsx` is **not reachable** from
  `/activity` (only from the legacy `/issues` shell's hidden tab) and its header
  comment says it is the `/activity` default; `run-activity-timeline.tsx` is
  reachable (run drill-down, issue detail).

### Scale (300-event window, 1 000 issues)

- The rail's chain index is **25, not a fold** (`hooks/use-chains.ts:139`), no
  load-more; every count on all three lens dashboards describes those 25 while
  being presented as "of N in the catalogue" (`lens-overviews.tsx:371-376,
  513-517`). The only disclosure is a footer line in the rail.
- After the bulk load the window is 300 events (`PAGE_SIZE`,
  `activity-stream-view.tsx:119`), "Latest activity" is 1 000 `created:` rows
  and stale-panel warnings (`shots/scale/activity-1440.png`), and the routine
  run's own journal falls out of the window (§1). The issues facet is
  `limit=200` with no disclosure (`:221`).
- `RoutineRunsPage` is capped at 50 and its header prints "50 runs recorded"
  as if total (`routine-runs-page.tsx:160`). The chain walk is fetched twice per
  workflow page; opening one feed row fires four requests.

## 5. `/journal`

**Purpose.** The records centre: Timeline (Grafana-style event log with
resources strip, histogram, severity/type chips, stats rail), Runs (agent-run
aggregates: live strip, KPIs, breakdowns, paginated table), Spend (admin).
`app/(dashboard)/journal/page.tsx` + `components/features/logs/` +
`components/features/journal/`. Screenshots: `shots/baseline/journal-{1440,390}.png`,
`journal-runs-{1440,390}.png`, `journal-spend-1440.png`,
`shots/scale/journal-crew-filter-1440.png`.

It is the best-engineered screen for URL state (every filter is a URL key,
`hooks/use-journal-url-state.ts`), stream honesty (`StreamStatusBadge` is
clickable and names the real error, `page.tsx:777-840`), result counts
(`visible 89 / 89`), and a cap that is disclosed ("Showing first 5,000").
It is also the least client-facing: it is an operator log console.

### Dead ends (§6)

| # | Where | Finding | Evidence |
|---|---|---|---|
| J-1 | timeline rows | The type column **is the raw entry type**: `page.panel.updated`, `pipeline.step.completed`, `mission.created`, `container.metrics` (`logs-list.tsx:376-378`); `TYPE_PILL_LABEL` exists and is documented as no longer used (`lib/journal-style.ts:385-474`). Summaries read "routine run run_cmtljjya400015a96ecf0 pushed watch/entry" and "demo@crewship.ai pushed from-outside/build". Group chips are lowercase internals (`keeper`, `mission`, `audit`). | `shots/baseline/journal-1440.png` |
| J-2 | Runs tab | Says "No runs yet / Agent executions across all crews will show up here in real time" while Routines and Activity report one run (§1). The empty state is a `py-12` centred block inside a `SettingsCard` and says "No runs yet" even when filters are the cause (`runs-view.tsx:524-541`). Error has no retry (`:510-514`); insights failures render as "No data yet". | `shots/baseline/journal-runs-1440.png` |
| J-3 | no actions | The SubBar has no `actions` at all; nothing on the page creates anything, which is right, but the empty timeline ("Once the crew runs, events will land here in real time.", `logs-panel.tsx:544-547`) names no CLI/UI action. | code |
| J-4 | leaks | Expanded row prints five raw cuids and a payload JSON dump (`logs-list.tsx:458-534`); Spend's by-agent table prints raw `agent_id` / `crew_id` and its chart series are labelled by cuid (`journal-spend-view.tsx:81-85, 186-187`) although `useJournalLookup` resolves both; search placeholder teaches `agent:viktor severity:error` syntax. Severity in the row is a 3 px colour bar with only an `sr-only` word (`logs-list.tsx:348-350`) — colour alone. | `shots/baseline/journal-1440.png` |
| J-5 | mobile 390 | The stats rail is an inline `280px` grid column with no breakpoint (`logs-panel.tsx:442-445`) and the row grid is 373 px of fixed columns (`logs-list.tsx:68`): at 390 the timeline shows **timestamps only**, the toolbar takes 60 % of the screen, and the resources popover is `w-[480px]`. Runs rows use an unconditional 584 px grid inside `overflow-hidden` — clipped, not scrollable (`runs-view.tsx:596, 695`; `page.tsx:626`). | `shots/baseline/journal-390.png`, `journal-runs-390.png` |
| J-6 | disabled | Resources cells are `disabled={!hasData}` with no reason (`resources-strip.tsx:162-167`). Saved-views' "Filter something to save a view" is the pattern to copy. | code |
| J-7 | histogram selection | The one filter not in the URL; visibly narrows the list and is lost on reload (`logs-panel.tsx:221`). | code |
| J-8 | stale copy | The page comment says Spend is behind a "Soon" badge and mentions `quartermaster`; no tab is locked (`page.tsx:81-98`). | code |

### Missing cross-links (§5: run / journal entry → agent, crew, issue, routine)

| | Timeline row | Runs tab | Spend tab |
|---|---|---|---|
| → agent | icon only, no link (`logs-list.tsx:218-268`) | `/crews?agent=<slug>` (`runs-view.tsx:94-95`) | raw id text |
| → crew | icon only (`:275-292`) | plain text (`:608`, `:889`) | raw id text |
| → issue | `mission_id` plain text (`:473`) | **impossible**: not in the run DTO (`internal/api/runs.go:28-99`) | absent |
| → routine | raw `pipeline.*` type text | absent | `/routines?slug=` — right param, but "Top runs" rows link the routine, never the run (`journal-spend-view.tsx:222`; backend sets `Label=slug`, `internal/journal/spend.go:257-266`) |
| → activity | **none anywhere** | none | none |

Link inventory of `/journal`: `shots/scale/journal-links.txt` — nav only.
`journal-entry-card.tsx`, the one component that does link crew and agent
(`/crews?crew=`, `/crews?agent=`), is not rendered on `/journal` and its issue
link goes to `/missions/<id>`, a route that does not exist (`:318`).

### Anatomy (§2)

- Uses: `SubBar` (description is `89 loaded`, not `N · M`), `CrewIcon`,
  `StatusBadge` + dot on run rows, type chips dot + word, result count,
  honest stream badge.
- Hand-rolled: `SettingsCard` and bespoke bordered divs instead of
  `DashboardCard` (two different radii between Runs and Spend); `Image` agent
  avatars instead of `AgentAvatar` (`logs-list.tsx:236-249`); own `Spark`
  SVG instead of `Sparkline`; `shortModel()` instead of `getModelLabel`
  (`runs-view.tsx:631`); a second "Live" pill driven by the pause toggle, not
  the connection (`logs-toolbar.tsx:243-253`); `auto-updating` pulse on the
  Runs strip regardless of socket state (`runs-view.tsx:717-724`).
- Not used: `Appear`, `AnimatedNumber`, `LiveDot`, `EmptyState`,
  `InlineEmpty`, the SubBar `actions` slot.

### Scale (361 events after the load, 5 000 cap)

- Timeline: cap disclosed, page size 500, eager pagination, one lookup fetch
  for names — the best scale story in the cluster. Two EventSources per open
  tab (timeline + resources strip); the anomaly badge scans all 5 000 entries
  every 30 s.
- Runs: 25 per page with "Showing a–b of N" (good); breakdown cards
  `slice(0, 6)` with no "N more" and no "Other" (`runs-view.tsx:851, 886`).
- Spend: top-5 chart with no "Other"; the by-agent table is unbounded.
- The crew filter works at scale (`shots/scale/journal-crew-filter-1440.png`,
  361 loaded, `visible 361 / 361`).

## 6. Inconsistencies with §2 — what Activity and Journal should copy

Issues and Routines are the two screens a client can read. The other two are
operator consoles. Concretely:

| Copy this from Issues / Routines | Into Activity | Into Journal |
|---|---|---|
| **URL is the state** (`use-issue-detail.ts` pushState selection; `/journal`'s own `use-journal-url-state`) | every lens, scope, drill-down and opened event becomes a query key; Back closes, reload resumes, links share | histogram bucket selection |
| **SubBar with `N things · M things`, one soft primary, `meta` for live** (`orchestration-layout.tsx:569`, `routines-layout.tsx:168`) | move the live badge to `meta`; keep the count visible at 390 | replace `89 loaded` with `N events · M runs · window`; live badge in `meta` (already), add an `Export` secondary |
| **Status = icon + word** (`StatusIcon` + `statusLabel`, `StatusBadge` on journal runs) | replace every bare dot (`feed-row`, sidebar rows, workflow runs) with a pill; humanise `run.status`, `triggered_via`, `entry_type` | severity bar → pill; `entry_type` → the `TYPE_PILL_LABEL` map that already exists |
| **Entity rows are cards/lists with icon + colour + status, never a bare table** (`issue-card.tsx`, routines explorer rows) | the run drill-down header: routine icon + name, not a raw id | the Runs table rows are fine; the Timeline row needs a name column (agent + crew names, not glyphs) |
| **`DashboardCard` + `InlineEmpty` with an action** (`routines-overview.tsx` cards, `dashboard-overview.tsx:815`) | replace the two private `Empty`s and the centred blocks | replace `SettingsCard` and the bespoke bordered divs; give "No runs yet" the CLI action |
| **Dead-end copy names the fix** (issue detail's "No routine bound. Starting this issue hands it to Alex directly.") | "This run's record is not loaded" → link to the routine; "This routine has not run yet." → Run button | "No journal entries" → `crewship routine run <slug>` / Run a routine link |
| **Routines' "Waiting on you" card as §1.1** | Activity has it (Open asks) — put it first, above the KPIs | Runs tab: put the failed/running strip first |
| **Explorer counts + filter popover** (`unified-explorer.tsx`, `routines-explorer.tsx`) | the rail already does this; add "of N" and a fold | — |

And two things Issues and Routines should copy from Journal: the honest,
clickable stream badge (`page.tsx:777-840`) instead of always-on pulses, and
"visible N / M" beside every search.

## 7. Prioritised list

P1 blocks a client, P2 confuses, P3 polish. Each line names the UI change.

### P1

| # | Screen | Change |
|---|---|---|
| 1 | `/issues` | **Server-side list, count and search.** Header count from a `count` field (or the same call) instead of the `limit=50` missions list; board/list/explorer paged or "N more · K need attention · Show all"; search hits the API. Today a client with > 100 issues cannot see or find the rest (I-1, scale). |
| 2 | `/routines` | **Write the URL.** `?slug=` on select (pushState like `use-issue-detail.ts`), plus `?tab`/`?run` in the detail; accept `?routine=` as an alias or fix the two callers (`dashboard-overview.tsx:460`, `issue-card-detail.tsx:574`) to `routineHref()`. Today the dashboard's "Up next" and the issue's routine chip both dead-end (R-1, R-2). |
| 3 | `/activity` | **Write the URL** (lens, scope, path, selected event) so Back closes and reload resumes; **fix `/agents/<id>`** → `/crews?agent=<slug>` (needs the slug in the run/chain payload); make `?run=` resolve a bare run id (look up the routine from the run, or accept the journal `j_` id) so the dashboard and routine links land on a run, not on "record is not loaded" (A-1, A-2, A-3). |
| 4 | `/issues` + API | **Issue → runs → journal.** Return `run_id`/`trace_id` and `agent_slug` from `issues/{id}/runs`; render the run list (not only `runs[0]`) with each row linking `/activity?pipeline=…&run=…`; add `mission_id` to the journal URL keys and a "Journal for this issue →" link; project `mission_id` onto `/api/v1/runs` so a journal run can name its issue. This is the §1 answer. |
| 5 | `/journal`, `/issues`, `/activity` | **Stop rendering fetch failures as empty states.** Distinguish "could not load" (retry, keep input) from "nothing here" in `issue-detail-surface.tsx:170`, `orchestration-page-shell.tsx:41`, `activity-stream-view.tsx:219`, `activity-detail.tsx:129`, `topology-card.tsx:124` (retry is a no-op), `runs-view.tsx:510`. |
| 6 | `/journal` | **Mobile timeline and runs.** Collapse the stats rail and switch the row grid to a two-line layout below `md`; make the Runs rows scroll or stack instead of clipping at 584 px (J-5). |
| 7 | `/issues` | **Board at 390 and 1440.** One column per status stacked at < `md`; at ≥ `lg` fit the five columns to the viewport (min-width 220, no fixed 280) so Done is not hidden behind a scroller (I-2). |

### P2

| # | Screen | Change |
|---|---|---|
| 8 | all four | **One runs count.** Routines' `M runs`, Activity's rail count and Journal's Runs tab must agree or say what they count ("1 routine run · 0 agent runs"). |
| 9 | `/journal` | **Humanise the timeline row**: type pill from `TYPE_PILL_LABEL`, summary without ids ("Page watch pushed the Watch page"), agent + crew as names that link `/crews?agent=` / `/crews?crew=`, `mission_id` as an issue link, severity as a pill; hide the payload dump behind "Raw record". |
| 10 | `/activity` | **Run page header** = routine icon + name + status pill; Result card rendered per producer kind (a page write → "Wrote panel *entry* of page *Watch* →") with the JSON behind a toggle; "Steps" first, journal feed scoped by `trace_id` not by window; links to routine, crew, agent, issue, journal. |
| 11 | `/activity` | **Fold the rail**: "Newest 25 workflows · Show all" with load-more; sort failing first inside a lens; carry the "these counts describe 25" caveat onto the lens dashboards or remove the "of N in the catalogue" ratios. |
| 12 | `/routines` | Crew link on the identity card (`author_crew_id` is already fetched); "Pages it writes" card from the definition's `page.write` targets; run rows show agent / issue / crew chips that link (data is in `PipelineRun`); "No cron triggers" → an **Add schedule** button in place; window-based KPIs ("last 100 runs" disclosed until then). |
| 13 | `/issues` | Crew and assignee as links (`crew_slug` is in the payload; expose `assignee_slug`); `CrewIcon` beside the crew; the History card via `actionLabel()` from the dead `activity-feed.tsx`; "History 50" → "latest 50 · show all"; the 404 page gets Back to issues + search. |
| 14 | `/routines` | Replace `window.confirm` with `AlertDialog` naming what is lost; put the disabled reason beside Run / Cancel / Import instead of in `title`. |
| 15 | `/routines`, `/activity`, `/journal` | Kill the vocabulary in visible copy: DSL, YAML tab, waterfall, wake gate, chain, topology, walk, lens, keeper request, agentless · token-zero, `page.write` chips, `0% agent steps`, cron strings in the schedule form (keep an "Advanced" reveal). |
| 16 | `/activity` | At 390 open with the rail closed, or give the overlay a scrim and a close affordance; show the event count in the SubBar at every width. |
| 17 | `/issues` | Bottom drawer: label the tabs at 390, drop Docker/Spec from the client build, make the Runs tab rows link to the run and show status as a pill. |

### P3

| # | Screen | Change |
|---|---|---|
| 18 | `/routines`, `/activity`, `/journal` | `AnimatedNumber` on KPIs; `Sparkline` (one hue, no axes) instead of the recharts bar chart and the hand-rolled `Spark`; `AgentAvatar` with status dot instead of `background-image` spans and `Image` tags. |
| 19 | `/activity`, `/issues`, `/routines` | Pulse only on a connected stream: consume `status` from `useJournalStream` in `run-activity-timeline.tsx`, `routines-explorer.tsx`, `logs-toolbar.tsx`; drop the pulse in the polling state in `LiveBadge`. |
| 20 | `/issues` detail | Use `SubBar` for the chrome bar; keep `?from=` when navigating issue → related issue; collapse the editor toolbar at 390; de-duplicate `Appear` orders 4 and 9. |
| 21 | `/journal` | Resolve agent/crew ids in Spend through `useJournalLookup`; route "Top runs" rows to `/activity?run=`; add "N more · Other" to the 6-row breakdowns; put the histogram bucket in the URL. |
| 22 | folder hygiene | Delete or wire: `issues/activity-feed.tsx`, `property-row.tsx`, `issues-toolbar.tsx`, `routines/routine-overview-tab.tsx`, `routines-filter-sidebar.tsx`, `activity/runs-view.tsx` (unreachable from `/activity`; its header comment is false), `journal-entry-card.tsx` (`/missions/<id>` link). Stale comments: `routines/page.tsx:7-11`, `journal/page.tsx:91-98`. |

## 8. Things not verified here

- No agent run existed (no Docker), so the issue → run leg was judged from
  `issue_handler_runs.go`, `bottom-panel/runs-tab.tsx`, `issue-card-detail.tsx`
  and the `/api/v1/runs` DTO, not from a rendered run row. The first agent to
  work on P1-4 should reproduce on dev2 with a real run.
- Light theme was not screenshotted.
- The Spend tab was screenshotted only with zero cost data.
- The sub-audits' remaining `file:line` citations (about 200) were not each
  re-opened; the ones that drive a P1/P2 line above were.
