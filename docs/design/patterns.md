# Crewship UI Layout Patterns

Canonical layout vocabulary for dashboard pages. The reference implementation
is `/orchestration` (`app/(dashboard)/orchestration/page.tsx` +
`components/features/orchestration/orchestration-layout.tsx`).

New pages must pick one of the five patterns below. Deviations require sign-off
and a short note in the PR explaining why none of the canonical patterns fit.

## Vocabulary

- `Card`, `CardHeader`, `CardTitle`, `CardContent` — wrap every content section.
  Do **not** use custom `<section className="rounded-xl border ...">` divs.
- `Badge variant="outline"` — status pills, counts, identifier chips.
- `Button` variants — `outline`, `ghost`, `primary/10`-tinted utility buttons.
- `text-muted-foreground` — all secondary copy.
- `text-body font-medium text-foreground/80` — primary h1 in top strips.
- `text-[10px] uppercase tracking-wider text-muted-foreground` — section headers inside cards.
- `h-9` — top strip / tab-bar height.
- `h-7 px-2.5 text-xs` — utility buttons inside strips.
- Colors: `border-border/60`, `bg-card`, `bg-background`. Never hex.

---

## 1. 3-panel master-detail

**Used by**: `/orchestration`, `/approvals`, `/crows-nest/[crewId]`.

```
+---------------------------------------------------------------+
| top strip (h-9)  [tabs]                        [actions]      |
+----------+--------------------------------------+-------------+
| left     | center content                       | right panel |
| sidebar  |                                      | (Sheet-like |
| 300 px   |                                      |  detail)    |
| collapse |                                      | 360 px      |
| → 48 px  |                                      |             |
|          |                                      |             |
+----------+--------------------------------------+-------------+
| bottom drawer (h-8 → 240 px)                                  |
+---------------------------------------------------------------+
```

**Grid template (desktop)**

```tsx
gridTemplateColumns: `${leftCollapsed ? "48px" : "300px"} 1fr ${showRightPanel ? "360px" : "0px"}`
```

**Components**

- Outer container: `<div className="flex flex-col h-[calc(100vh-48px)] bg-background">`
- Top strip: custom `<div className="... h-9 bg-card border-b border-border/60 ...">`
  with tab buttons styled `border-b-2` for active state.
- Left sidebar: `<div className="border-r border-border/60 bg-card">` with collapse
  toggle using `<PanelLeftClose/>` / `<PanelLeftOpen/>` icons.
- Right panel: conditional `<motion.div>` or shadcn `<Sheet>` on mobile.
- Bottom drawer: `<motion.div>` with `animate={{ height: drawerOpen ? 240 : 32 }}`.

**When to pick this pattern**

Anytime a list → detail interaction needs to coexist with a persistent filter
rail *and* an optional telemetry drawer. Most "operator" surfaces qualify.

---

## 2. Sidebar + main

**Used by**: `/journal`, `/paymaster`, any future "content + filters" page.

```
+------------------+--------------------------------+
| filter rail      | main content                   |
| 240 px           |                                |
|                  |  +--------------------------+  |
|  filters…        |  | Card                     |  |
|                  |  |   CardHeader             |  |
|                  |  |   CardContent            |  |
|                  |  +--------------------------+  |
|                  |                                |
+------------------+--------------------------------+
```

**Grid template**

```tsx
className="grid grid-cols-1 lg:grid-cols-[240px_1fr]"
```

For `/journal`, the filter rail is on the **right** (flex-row-reverse semantics
via `flex flex-col lg:flex-row`) so the timeline gets reading priority. Both
placements are valid; pick based on information density.

**Components**

- Optional `h-9` top strip with breadcrumbs + refresh.
- Sidebar: `<aside className="border-r border-border/60 bg-card ...">`; sections
  titled with `text-[10px] uppercase tracking-wider text-muted-foreground/80`.
- Main: each section wrapped in `<Card>`.

**When to pick this pattern**

Content view that benefits from persistent filtering but has no natural
detail-panel interaction (the "list items" themselves aren't clickable into a
detail). Example: journal timeline, spend dashboards.

---

## 3. Top strip + grid cards

**Used by**: `/eval`, `/fleet` (dashboard variant), single-page admin views.

```
+---------------------------------------------------+
| top strip (h-9)                         [actions] |
+---------------------------------------------------+
|                                                   |
|  +-------+  +-------+  +-------+   KPI row        |
|  | card  |  | card  |  | card  |                  |
|  +-------+  +-------+  +-------+                  |
|                                                   |
|  +----------------------------+   wide card       |
|  | Card  Recent runs          |                   |
|  |       (table)              |                   |
|  +----------------------------+                   |
|                                                   |
+---------------------------------------------------+
```

**Grid templates**

- KPI band: `grid grid-cols-2 lg:grid-cols-3 gap-3` (or `lg:grid-cols-4`).
- Content cards: `grid grid-cols-1 lg:grid-cols-2 gap-3` or stacked in a `space-y-4` stack.

**Components**

- Top strip: `<div className="flex items-center h-9 bg-card border-b border-border/60 px-3 gap-2">`.
- KPI cards: dedicated components (`MetricCard`, `StatCard`) — keep them
  compact so four fit on a 1024 px viewport.
- Detail drawer for row clicks: shadcn `<Sheet>` on the right
  (`className="sm:max-w-xl w-full"`).

**When to pick this pattern**

Analytics or status dashboards where there's no filter rail and no
master-detail flow — everything is read-only summary plus optional drill-down.

---

## 4. Card list + inline detail

**Used by**: `/agents`, `/crews/[id]` landing, `/skills`.

```
+-----------------------------------------------+
| breadcrumb strip (h-9)              [actions] |
+-----------------------------------------------+
|                                               |
|  +-----------------------------------------+  |
|  | Card — summary row         [Edit] [>]   |  |
|  +-----------------------------------------+  |
|  +-----------------------------------------+  |
|  | Card — summary row         [Edit] [>]   |  |
|  | ▼ expanded detail                       |  |
|  +-----------------------------------------+  |
|  +-----------------------------------------+  |
|  | Card — summary row         [Edit] [>]   |  |
|  +-----------------------------------------+  |
|                                               |
+-----------------------------------------------+
```

**Recommended components**

- `<Card>` per row; CardContent uses `flex items-center gap-3`.
- Expand/collapse: `<Collapsible>` from shadcn, toggled by a ghost icon button.
- Stick to a single-column stack: `space-y-2` between cards, `grid gap-2 sm:grid-cols-2 lg:grid-cols-3` for smaller, tile-shaped cards.

**Minimum classes**

- Row card: `className="py-4"`, CardContent `className="flex items-center gap-3 px-4"`.
- Row heading: `text-sm font-medium truncate`.
- Row metadata: `text-[11px] text-muted-foreground font-mono truncate`.

**When to pick this pattern**

Entity lists where the user typically scans identifiers and occasionally dives
in to see one thing in detail without wanting to leave the list.

---

## 5. Detail page with breadcrumbs

**Used by**: `/missions/[id]/timeline`, `/orchestration/issues/[identifier]`, `/agents/[slug]`.

```
+---------------------------------------------------+
| [Back] Orchestration > Missions > Timeline [ID]   |
+---------------------------------------------------+
|                                                   |
|          +-----------------------------+          |
|          | Card  (max-w-3xl/4xl mx-auto|          |
|          |        px-6 py-6 space-y-6) |          |
|          |                             |          |
|          |  title (editable)           |          |
|          |  description                |          |
|          |  ──── Separator             |          |
|          |  comments / content         |          |
|          |  ──── Separator             |          |
|          |  activity / timeline        |          |
|          +-----------------------------+          |
|                                                   |
+---------------------------------------------------+
                                         +------------+
                                         | Sheet      |
                                         | detail     |
                                         | (sm:max-xl)|
                                         +------------+
```

**Required scaffolding**

```tsx
<div className="h-[calc(100vh-48px)] flex flex-col bg-background">
  {/* h-9 breadcrumb top bar */}
  <div className="flex items-center h-9 bg-card border-b border-border/60 px-3 gap-2">
    <Button variant="ghost" size="sm" className="h-7 px-2 text-xs">
      <ArrowLeft className="h-3 w-3 mr-1" /> Back
    </Button>
    <nav className="flex items-center gap-1 text-xs text-muted-foreground">
      <Link href="/parent">Parent</Link>
      <ChevronRight className="h-3 w-3 opacity-60" />
      <span className="text-foreground/80 font-medium">Current</span>
    </nav>
    <Badge variant="outline" className="text-[10px] font-mono">ID</Badge>
    <div className="flex-1" />
    {/* actions */}
  </div>

  {/* centered content */}
  <div className="flex-1 overflow-y-auto">
    <div className="max-w-4xl mx-auto px-4 md:px-6 py-5 space-y-4">
      {/* Cards */}
    </div>
  </div>

  {/* Sheet for sub-detail (checkpoint, run, revision) */}
  <Sheet>…</Sheet>
</div>
```

**When to pick this pattern**

Single-entity deep view. The user has navigated to *one specific thing*.
Secondary detail (one-level-deeper records — a checkpoint inside a timeline, a
run inside an eval) opens in a Sheet rather than navigating away.

---

## Common pitfalls

- **Using `<section className="rounded-xl border ...">` instead of `<Card>`.**
  Card wrappers are load-bearing for dark-mode, focus rings, and spacing.
  Grep for `rounded-xl border border-border/60 bg-card` — every hit should
  become a `<Card>`.
- **Forgetting `overflow-hidden` on parent grid containers.** Sidebars and
  drawers animate their `width`/`height`; without `overflow-hidden` they
  cause horizontal page scroll during transitions.
- **Hard-coded colors.** Use palette tokens (`border-border/60`, `bg-card`,
  `text-muted-foreground`). The exception is status badges, which have a
  tight palette documented in `status-badge.tsx`.
- **Custom `h-*` values for strips.** The strip is always `h-9`. Buttons inside
  are `h-7`. This is intentional so strips feel consistent across routes.
