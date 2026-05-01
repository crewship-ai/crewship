# Dashboard redesign — Mission Control

## Context

Current `app/(dashboard)/page.tsx` is functional but tabular-heavy: 7 stat cards (no trends) + Recent Missions table + Agents table + realtime-streamed Container Resources table. The user wants the dashboard to become a true **Mission Control** — a single page that answers two questions at a glance:

1. **What needs me right now?** (approvals, escalations, keeper requests, reviews, mentions)
2. **Is my fleet healthy and productive?** (throughput, costs, agent utilisation, container load, mission pipeline)

Charts matter here. The current dashboard has zero trend data — you can't tell if cost is rising, if throughput is falling, or which agents are idle.

The `New Agent` button is removed from the dashboard toolbar. Creation lives in `/agents/new`; the dashboard is for control, not creation.

Wireframe: `wireframes/dashboard.html` (open in browser, ticks live via JS).

## Chart library decision — shadcn/ui charts + Recharts v3

I researched the current React chart landscape via Context7. Candidates evaluated:

| Library | Fit | Verdict |
|---|---|---|
| **shadcn/ui charts** (Recharts v3 wrapper) | Already in the shadcn ecosystem. Uses CSS variables `var(--chart-1..5)` for theme, `ChartContainer` + `ChartTooltip` + `ChartLegend` primitives, declarative. Covers bar, line, area, pie, radial, radar, composed. | ✅ **Primary** |
| **Tremor** | Pre-built dashboard blocks but brings its own design system; would collide with shadcn. | ❌ Collision |
| **Nivo** | Prettiest defaults but larger bundle, not theme-variable aware, d3-based. | ❌ Bundle cost |
| **Victory** | Older API, weaker TypeScript ergonomics for Next 15. | ❌ |
| **@visx/heatmap** | The only good React heatmap library. | 🟡 **Optional** — can also fake with a Tailwind grid of divs (~50 lines) |
| **Recharts Treemap / RadialBar / RadialBarChart** | Already in Recharts. | ✅ Free |
| **React Flow** | Already in the project (`workflow-graph.tsx`). | ✅ Reuse if we add crew/agent graph |
| **@tanstack/react-table** | Already in the shadcn stack. | ✅ Reuse for rich tables |

**Decision:** Recharts v3 via shadcn/ui wrappers. Heatmap as a Tailwind grid of divs (no extra dep). No new top-level chart dependency. The shadcn `components/ui/chart.tsx` primitive gets added once via `pnpm dlx shadcn@latest add chart` and then every chart reuses the same `ChartContainer` + `ChartConfig` pattern for consistent theming.

## Layout — 7 rows, top-to-bottom priority

Matches `wireframes/dashboard.html` exactly. The row order is deliberate: highest-urgency content at the top, trends in the middle, reference material at the bottom.

```text
┌─ Toolbar ────────────────────────────────────────────────────────────┐
│ Overview · My Work(11) · Costs · Insights · Activity          ⌁ Ask │
│                                                  🔍 Search · ● Live │
├─ ACTION CENTER ──────────────────────────────────────────────────────┤
│ ⚡ Waiting for you:  !3 Escalations  ⏳2 Keeper  ⏳4 Reviews  …      │
├─ KPI strip · 6 sparkline cards ──────────────────────────────────────┤
│ Agents · Running · Active missions · Open issues · Cost · Success%  │
├─ Throughput (2fr)  ·  Mission status donut (1fr) ───────────────────┤
│ Stacked bar 24h by crew           6-segment donut w/ legend         │
├─ Cost burn (2fr)   ·  Top-cost missions (1fr) ──────────────────────┤
│ Stacked area 7d by model         horizontal bars · top 6 missions  │
├─ Container resources (full width, live) ────────────────────────────┤
│ ENG/DEV/QUA/RES rows with sparklines, CPU%, memory bar, status     │
├─ Agent heatmap (1.5fr) · Crew radial (1fr) · Projects (1fr) ───────┤
│ 12×24 activity grid  · 4 radial health  · 5 project progress bars │
├─ Activity feed (1.5fr) · Inbox (1fr) · Captain (1fr) ──────────────┤
│ Live ticker      · urgent items list · prompt box + suggestions    │
├─ Recent missions compact table (full width) ────────────────────────┤
│ ID · Title · Progress · Status · Cost · When                       │
└──────────────────────────────────────────────────────────────────────┘
```

## Data sources — what exists vs what needs a new endpoint

| Widget | Source | Status |
|---|---|---|
| Action Center chips | `/api/v1/escalations`, `/api/v1/admin/keeper/requests`, `/api/v1/missions?status=REVIEW`, `/api/v1/proposals`, `/api/v1/mentions` | 🟢 all exist except `mentions` endpoint (new, 40 LOC) |
| KPI: Agents, Running | `/api/v1/agents` | 🟢 exists |
| KPI: Active/Total missions | `/api/v1/mission-metrics` | 🟢 exists |
| KPI: Open issues | `/api/v1/issues?status=BACKLOG,TODO,IN_PROGRESS,REVIEW` | 🟢 exists |
| KPI: Cost 24h | `/api/v1/mission-metrics` (`total_cost_24h`) | 🟢 exists |
| KPI: Success rate | derived from `runs` stats | 🟢 exists |
| KPI **sparklines** (trend series) | `/api/v1/metrics/timeseries` | 🟢 endpoint exists — see "Metrics backend" below |
| Mission status donut | `/api/v1/missions` grouped client-side | 🟢 exists |
| **Issue throughput 24h stacked by crew** | `/api/v1/metrics/timeseries?metric=issues_closed&group_by=crew` | 🟢 exists |
| **Cost burn 7d stacked by model** | `/api/v1/metrics/timeseries?metric=cost_usd&window=7d&group_by=model` | 🟢 exists |
| Top-cost missions | `/api/v1/missions?sort=cost&limit=6` | 🟡 sort param missing, ~10 LOC to add |
| Container resources (live) | `container.stats` WS event | 🟢 **already streaming in current dashboard** |
| Agent heatmap | `/api/v1/metrics/timeseries?metric=runs_count&group_by=agent` | 🟡 endpoint exists; needs `group_by=agent` added (~15 LOC) |
| Crew health radial | `/api/v1/crews` + `/api/v1/agents` grouped | 🟢 exists (client aggregate) |
| Projects progress | `/api/v1/projects` (has `issue_count` + completion) | 🟢 exists |
| Activity feed | WS events `mission.updated`, `task.updated`, `run.*`, `issue.*` | 🟢 exists, just subscribe |
| Inbox | same as Action Center | 🟢 exists |
| Captain | `/api/v1/captain/chat` | 🟢 exists |
| Recent missions table | `/api/v1/missions?limit=5&sort=updated_at` | 🟢 exists |

### Metrics backend: extend the existing timeseries endpoint

`GET /api/v1/metrics/timeseries` is already shipped (`internal/api/metrics_handler.go`, `internal/api/router_routes.go`). It returns zero-filled bucket sequences and supports the metric set this dashboard needs:

```http
GET /api/v1/metrics/timeseries?metric=<name>&window=<24h|7d|30d>&bucket=<15m|1h|1d>&group_by=<crew|model|status|none>

Response: { metric, window, bucket, group_by,
  buckets: [{ts: "2026-04-10T13:00:00Z", series: {"eng": 3, "dev": 2, "qua": 1, "res": 0}}, ...],
  series_labels: { ... } }
```

Already supported:
- `metric`: `issues_closed`, `cost_usd`, `runs_count`, `active_missions` — covers KPI sparklines + throughput + cost burn out of the box.
- `group_by`: `crew`, `model`, `status`, `none`.
- `window`: `24h`, `7d`, `30d`. `bucket`: `15m`, `1h`, `1d`.

Gap to close for this redesign:
- `group_by=agent` is not currently in the handler's `validGroupBy` set. Adding it requires the validator entry plus an `agent_id`-keyed aggregation branch alongside the existing crew/model branches — roughly 15 LOC plus a query for the `runs` table. The agent heatmap is the only widget that needs it; everything else uses the values already wired.

So this row in the dashboard plan is "wire the frontend" not "build a new backend endpoint" — except for that single 15-LOC `group_by=agent` extension.

## Phased implementation

### Phase 1 — Foundations (no new backend, ~1 session)
1. Install shadcn chart primitive: `pnpm dlx shadcn@latest add chart`
2. Remove `New Agent` button from dashboard toolbar
3. Add Action Center banner — pure layout, fed from existing endpoints
4. Redesign KPI strip — 6 cards with **static mock sparklines** (placeholder until metrics endpoint lands)
5. Add tab nav to dashboard (Overview · My Work · Costs · Insights · Activity) — only Overview tab implemented, others empty shells

**Outcome:** layout matches wireframe, Action Center works, KPI strip looks right but sparklines are constant.

### Phase 2 — Real trend charts (mostly frontend wiring, ~1 session)
1. Wire KPI sparklines to `GET /api/v1/metrics/timeseries` (already shipped) — last 24 data points per card
2. Add **Issue throughput** stacked bar chart (Recharts `BarChart` + `stackId`) using `metric=issues_closed&group_by=crew`
3. Add **Mission status** donut (Recharts `PieChart`)
4. Add **Cost burn** stacked area chart (Recharts `AreaChart` + gradients) using `metric=cost_usd&group_by=model`
5. Add **Top cost missions** horizontal bar (Recharts `BarChart` layout="vertical") — needs the small `sort=cost` param add on `/missions`
6. Extend `metrics_handler.go` to accept `group_by=agent` (heatmap dependency) — single validator entry + agent-keyed aggregation branch

**Outcome:** all 4 trend charts populated from real data.

### Phase 3 — Live + rich widgets (~1 session)
1. Move existing Container Resources tile up into new layout; add per-crew sparkline (30 points, 1Hz from `container.stats` WS)
2. Add **Agent activity heatmap** (12 agents × 24 hours, Tailwind grid of divs colored by intensity)
3. Add **Crew health radial** (Recharts `RadialBarChart`, 4 segments)
4. Add **Active projects** with progress bars
5. Move activity feed into new layout; add status pill + ID column; keep existing WS subscriptions
6. Add **Inbox** tile — consolidates escalations/keeper/proposals/mentions into 5 rows
7. Add **Captain** tile — prompt box + 4 clickable suggestion pills

**Outcome:** dashboard is visually complete; everything updates live; parity with wireframe.

### Phase 4 — Polish & empty states
1. Loading skeletons per widget (not one big skeleton)
2. Empty states for every widget ("No active missions", "No escalations — you're clear ✓")
3. Click-through targets: every chip, row, and bar becomes a link
4. Keyboard: `⌘K` opens search, `⌘/` opens Captain, number keys `1..5` switch tabs
5. Persistence: remember last-viewed tab in localStorage
6. Responsive breakpoints: <1280 px collapses 3-col rows to 2-col, <768 px stacks everything

## Critical files

### To modify
- `app/(dashboard)/page.tsx` — full rewrite against new layout
- `components/providers.tsx` — unchanged (RealtimeProvider already mounted in `(dashboard)/layout.tsx`)

### To create
- `components/ui/chart.tsx` — installed by shadcn CLI
- `components/features/dashboard/action-center.tsx` — Action Center banner
- `components/features/dashboard/kpi-card.tsx` — single KPI sparkline card (reused ×6)
- `components/features/dashboard/throughput-chart.tsx` — stacked bar chart
- `components/features/dashboard/status-donut.tsx` — mission status pie
- `components/features/dashboard/cost-burn-chart.tsx` — stacked area
- `components/features/dashboard/top-missions-chart.tsx` — horizontal bars
- `components/features/dashboard/container-resources-tile.tsx` — live CPU/mem per crew
- `components/features/dashboard/agent-heatmap.tsx` — 12×24 grid
- `components/features/dashboard/crew-radial.tsx` — RadialBarChart per crew
- `components/features/dashboard/project-progress.tsx` — progress bar list
- `components/features/dashboard/activity-feed.tsx` — live ticker (extracts existing logic)
- `components/features/dashboard/inbox-tile.tsx` — consolidated urgent items
- `components/features/dashboard/captain-tile.tsx` — prompt + suggestions
- `components/features/dashboard/recent-missions-table.tsx` — compact missions list

### To extend
- `internal/api/metrics_handler.go` — add `group_by=agent` (only gap on the existing handler)

### Reused
- `hooks/use-realtime.tsx` — WS subscriptions already exist
- `lib/types/mission.ts` — Mission, IssueStatus types
- `components/ui/*` — shadcn primitives

## Verification

1. `pnpm lint && pnpm build` clean
2. Dashboard loads under 500 ms on devserver
3. Trigger `./crewship issue start X` → Action Center and Activity Feed and throughput chart all update within 1 s without refresh
4. Block/unblock backend → Live pill turns red → Offline, returns green on reconnect
5. Each KPI card's sparkline has at least 24 data points populated
6. Cost burn chart's last data point is within 1 min of now
7. Heatmap shows non-zero cells for agents that ran tasks today

## Open questions for the user

1. **Tab scope:** are `My Work`, `Costs`, `Insights`, `Activity` tabs in scope now or Phase 2+? (I'd ship only `Overview` first, stub the others.)
2. **Captain inline:** should the Captain tile actually POST to `/api/v1/captain/chat` on click, or navigate to the full Captain page? (Inline is nicer but more work.)
3. **Time zone:** the timeseries endpoint should bucket in user's local TZ or UTC? (Recommend UTC + client-side conversion, simpler cache semantics.)
4. **Persistence budget:** do we need historical metrics beyond 30 days on the free tier? (Affects whether we write to a separate `metrics` table or compute on-the-fly.)
