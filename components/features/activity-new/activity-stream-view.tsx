"use client"

// /activity-new — the shell: SubBar, rail, and whichever main surface the
// current scope calls for.
//
// Two modes, one page:
//
//   scope = all   → the overview dashboard. What is running, what is stuck
//                   on a person, what broke, what it cost, is today normal.
//   scope = one   → that bucket as a list, still in cards, so "3 failed"
//                   is a place you can go rather than a number you read.
//
// Selecting any row replaces both with the activity's own page: the run's
// node graph, the chain under its trace id, and the raw record. Not a side
// panel — see activity-detail.tsx for why.

import * as React from "react"
import { Activity, ArrowLeft, ChevronRight, FilterX } from "lucide-react"

import { SubBar } from "@/components/layout/sub-bar"
import { SidebarActiveChip, SidebarActiveChips, SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Appear, EmptyState } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalLookup } from "@/hooks/use-journal-lookup"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { useIsMobile } from "@/hooks/use-mobile"
import { usePipelines } from "@/hooks/use-pipelines"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { apiFetch } from "@/lib/api-fetch"
import {
  ACTIVE_ENTRY_TYPES,
  ACTIVITY_SCOPES,
  NOISE_ENTRY_TYPES,
  narrowToFocus,
  scopeOf,
  shortId,
  sourceEntryTypes,
  sourceMeta,
  type SpineLabels,
  type SpineLink,
} from "@/lib/activity-stream"
import {
  ACTIVITY_HOME,
  activitySurface,
  activityTrail,
  backFrom,
  currentStop,
  jumpTo,
  openStop,
  selectStop,
  stopMatcher,
  workflowAnchor,
  workflowLabel,
  type ActivityPath,
  type ActivityStop,
} from "@/lib/activity-selection"
import type { JournalEntry } from "@/lib/types/journal"
import { cn } from "@/lib/utils"

import {
  ActivitySidebar,
  EMPTY_FACETS,
  TIME_RANGES,
  type FacetState,
  type SidebarIssue,
  type SidebarRoutine,
} from "./activity-sidebar"
import { useChains } from "@/hooks/use-chains"
import type { LensKey } from "@/lib/activity-lenses"
import { ActivityOverview, iconFor } from "./activity-overview"
import { ActivityDetail } from "./activity-detail"
import { WorkflowPage } from "./workflow-page"
import { AgentsOverview, IssuesOverview, RoutinesLensOverview } from "./lens-overviews"
import { RoutineRunsPage } from "./routine-runs-page"
import { AgentDrillDown, IssueDrillDown, RunDrillDown } from "./drill-downs"
import { FeedRow } from "./feed-row"

/** Connection state as a word, not a button. */
function LiveBadge({ status }: { status: string }) {
  const connected = status === "connected"
  const degraded = status === "polling"
  return (
    <span
      title={
        connected
          ? "Streaming over SSE"
          : degraded
            ? "The stream is unavailable; falling back to polling every 5s"
            : "Connecting to the activity stream"
      }
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md px-2 py-1 font-mono text-[10px] uppercase tracking-wider",
        connected && "text-success",
        degraded && "text-warn",
        !connected && !degraded && "text-muted-foreground-soft",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          connected && "bg-success animate-pulse",
          degraded && "bg-warn animate-pulse",
          !connected && !degraded && "bg-muted-foreground/40",
        )}
      />
      {connected ? "live" : degraded ? "polling" : "connecting"}
    </span>
  )
}

const MAX_BUFFERED = 2_000
const PAGE_SIZE = 300

export function ActivityStreamView({ workspaceId }: { workspaceId: string }) {
  const isMobile = useIsMobile()
  const lookup = useJournalLookup()

  const [facets, setFacets] = React.useState<FacetState>(EMPTY_FACETS)
  const [search, setSearch] = React.useState("")
  const [debouncedSearch, setDebouncedSearch] = React.useState("")
  const [pinned, setPinned] = React.useState<SpineLink | null>(null)
  const [selected, setSelected] = React.useState<JournalEntry | null>(null)
  const [railCollapsed, setRailCollapsed] = React.useState(false)
  // Which catalogue the rail lists. Owned HERE rather than inside the rail so
  // it survives a drill-down: walking into an agent out of a chain graph and
  // pressing back must land the reader on the list they left, not reset them to
  // Workflows.
  const [lens, setLens] = React.useState<LensKey>("workflows")

  // Where the reader is — ONE value, whether they picked a workflow out of the
  // rail, focused an issue, or walked down into a node of a chain.
  //
  // It was two independent states: an EntityFocus for the rail and a
  // selectedChain for the graph. Neither cleared the other, so the chips could
  // read "routine: Normalize dates to ISO 8601" over a card still drawing the
  // on-close-file-followup chain — two answers to "what am I looking at", and
  // the graph was the stale one. Making them notify each other would have kept
  // two sources of truth, which is the shape of that bug; there is one, and
  // the rail's highlight, the query's narrowing, the main column, the trail
  // and the chip are all derived from it in lib/activity-selection.
  //
  // The path is that same one value grown a memory: its LAST stop is the
  // selection, the ones before it are only there so back has somewhere to go.
  const [path, setPath] = React.useState<ActivityPath>(ACTIVITY_HOME)
  const stop = React.useMemo(() => currentStop(path), [path])
  const surface = React.useMemo(() => activitySurface(stop), [stop])
  const focus = surface.focus
  const trail = React.useMemo(() => activityTrail(path), [path])

  const goBack = React.useCallback(() => setPath(backFrom), [])

  // The rail follows the workflow the reader is INSIDE, not the stop they are
  // standing on, so the highlight does not blink off the moment they open a
  // node of it.
  const railChain = React.useMemo(() => workflowAnchor(path), [path])

  // A node has no server-side expression — an agent id is a column but a run
  // or an assignment id lives inside the payload — so it narrows the loaded
  // window here, the same way a pinned crumb does.
  const nodeMatch = React.useMemo(
    () => (surface.node ? stopMatcher(surface.node) : null),
    [surface.node],
  )

  // Entities for the rail. The journal lookup already carries crews, agents
  // and missions; routines come from the pipelines list so the rows can show
  // the same icon + colour the Routines rail does.
  const { pipelines } = usePipelines(workspaceId)
  // Only the Routines lens reads these; the hook is cheap and shared, and
  // gating the fetch on the lens would make the card empty on first paint
  // every time the reader switches to it.
  const { schedules } = usePipelineSchedules(workspaceId)
  const [issues, setIssues] = React.useState<SidebarIssue[]>([])
  React.useEffect(() => {
    let cancelled = false
    apiFetch(`/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId)}&limit=200`)
      .then(async (r) => (r.ok ? await r.json() : []))
      .then((d: unknown) => {
        if (cancelled) return
        const arr = Array.isArray(d) ? d : ((d as { missions?: unknown[] })?.missions ?? [])
        setIssues(arr as SidebarIssue[])
      })
      .catch(() => {
        if (!cancelled) setIssues([])
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  React.useEffect(() => {
    const t = setTimeout(() => setDebouncedSearch(search.trim()), 280)
    return () => clearTimeout(t)
  }, [search])

  const range = TIME_RANGES.find((r) => r.key === facets.range) ?? TIME_RANGES[1]

  // Server-side wherever the endpoint can express it: the journal already
  // filters on entry_type, severity, crew_ids, agent_ids, since, q and
  // mission_id, so the page filters the whole table, not the page it holds.
  const params = React.useMemo<Record<string, string | undefined>>(() => {
    const entryTypes =
      facets.scope === "active"
        ? ACTIVE_ENTRY_TYPES
        : facets.scope === "waiting"
          ? sourceEntryTypes("human")
          : facets.sources.length > 0
            ? facets.sources.flatMap((s) => sourceEntryTypes(s))
            : []

    const severities =
      facets.scope === "failed" ? ["error"] : facets.severities.length > 0 ? facets.severities : []

    return {
      since: new Date(Date.now() - range.ms).toISOString(),
      entry_type: entryTypes.length > 0 ? entryTypes.join(",") : undefined,
      severity: severities.length > 0 ? severities.join(",") : undefined,
      // Per-minute container/exec telemetry is excluded unless asked for.
      // Without this the eight most recent rows are eight CPU samples — a
      // feed that is live and says nothing.
      exclude_entry_type: facets.showTelemetry ? undefined : NOISE_ENTRY_TYPES.join(","),
      crew_ids: facets.crewIDs.length > 0 ? facets.crewIDs.join(",") : undefined,
      agent_ids: facets.agentIDs.length > 0 ? facets.agentIDs.join(",") : undefined,
      // An issue focus IS expressible server-side, so it narrows the whole
      // table rather than the loaded page.
      mission_id: focus?.kind === "issue" ? focus.id : pinned?.kind === "issue" ? pinned.id : undefined,
      crew_id: focus?.kind === "crew" ? focus.id : undefined,
      q: debouncedSearch || undefined,
    }
  }, [facets, pinned, focus, range.ms, debouncedSearch])

  const { entries, loading, loadingMore, error, nextCursor, refresh, loadMore, prependLive } =
    useJournalList({ workspaceId, params, limit: PAGE_SIZE, maxEntries: MAX_BUFFERED })

  // The workflow index. Independent of the journal query on purpose: the rail
  // must answer "where can I go" even when the current filters answer nothing,
  // and tying it to the same window is what made the rail collapse to a single
  // row the moment an issue was focused.
  const { chains, hasUnrecordedRuns: chainsHaveUnrecorded } = useChains(workspaceId)

  // Picking a workflow is a selection like any other, so it goes through the
  // same setter — which is what makes it impossible for the graph to outlive
  // the thing that opened it. The label is resolved here, at the click, rather
  // than looked up again at render: a heading read from a list that has since
  // refreshed is the same class of staleness this whole change removes.
  const selectChain = React.useCallback(
    (origin: string | null) => {
      if (!origin) {
        setPath(ACTIVITY_HOME)
        return
      }
      setPath(
        selectStop({
          kind: "workflow",
          id: origin,
          label: workflowLabel(chains.find((c) => c.origin === origin)),
        }),
      )
    },
    [chains],
  )

  /** The chain the workflow column draws, or undefined once the index has moved on. */
  const openChain = React.useMemo(
    () => (surface.chainAnchor ? chains.find((c) => c.origin === surface.chainAnchor) : undefined),
    [chains, surface.chainAnchor],
  )

  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params,
    enabled: true,
    onEntry: prependLive,
  })

  // Routine / run / step ids live inside the payload, which the API cannot
  // filter on — so a crumb of those kinds narrows here, over the loaded
  // window, and the chip says so rather than implying a full-table match.
  // Narrowed by the pinned crumb and the entity focus, but NOT by the status
  // facet — this is the set the rail's status counts are taken over, so each
  // count answers "how many would I get if I also clicked this".
  const focusScoped = React.useMemo(() => {
    let out = entries
    if (pinned && pinned.kind !== "issue") {
      out = out.filter((e) => {
        const bag = { ...(e.payload ?? {}), ...(e.refs ?? {}) }
        if (pinned.kind === "routine") {
          return bag["pipeline_slug"] === pinned.id || bag["routine_slug"] === pinned.id
        }
        if (pinned.kind === "run") return bag["run_id"] === pinned.id
        return bag["step_id"] === pinned.id || bag["step"] === pinned.id
      })
    }
    // The node the reader walked into, if any. Same reason as the crumb: an
    // assignment or a run id is payload, not a column.
    if (nodeMatch) out = out.filter(nodeMatch)
    // A routine focus cannot go to the server: the slug lives inside the
    // payload, which the journal does not index. Narrowed here, over the
    // loaded window, and the chip says so.
    return narrowToFocus(out, focus)
  }, [entries, pinned, focus, nodeMatch])

  // What the feed shows and the cards count: the focused set with the status
  // facet applied on top.
  //
  // The split matters because the two were computed from different windows
  // before. The cards were built from the focused set while the rail's counts
  // came from the whole loaded window, so a screen focused on one routine read
  // "FAILED 0 — nothing broke" beside a rail reading "Failed 9" — one screen,
  // two answers to "did anything break", and the reassuring one was wrong.
  const visible = React.useMemo(() => {
    // `done` has no server-side expression (it is "everything else"), so it
    // is the one scope narrowed client-side.
    if (facets.scope === "done") return focusScoped.filter((e) => scopeOf(e) === "done")
    return focusScoped
  }, [focusScoped, facets.scope])

  const crewCounts = React.useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of focusScoped) if (e.crew_id) c[e.crew_id] = (c[e.crew_id] ?? 0) + 1
    return c
  }, [focusScoped])

  const issueCounts = React.useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of entries) if (e.mission_id) c[e.mission_id] = (c[e.mission_id] ?? 0) + 1
    return c
  }, [entries])

  const routineCounts = React.useMemo(() => {
    const c: Record<string, number> = {}
    for (const e of entries) {
      const bag = { ...(e.payload ?? {}), ...(e.refs ?? {}) }
      const slug = bag["pipeline_slug"] ?? bag["routine_slug"]
      if (typeof slug === "string" && slug) c[slug] = (c[slug] ?? 0) + 1
    }
    return c
  }, [entries])

  // Status and priority for the issues the Issues lens draws. The chain index
  // carries neither — ChainIssueRef is id, identifier, title and `created` — so
  // without this an issue in Activity wears a generic dot while the same issue
  // one page away wears its status glyph.
  const issueMeta = React.useMemo(
    () => new Map(issues.map((i) => [i.id, { status: i.status, priority: i.priority }])),
    [issues],
  )

  const routines = React.useMemo<SidebarRoutine[]>(
    () =>
      pipelines.map((p) => ({
        id: p.id,
        slug: p.slug,
        name: p.name,
        icon: (p as { icon?: string | null }).icon ?? null,
        color: (p as { color?: string | null }).color ?? null,
        invocation_count: p.invocation_count,
        last_invocation_status: p.last_invocation_status ?? null,
      })),
    [pipelines],
  )

  const labels = React.useMemo<SpineLabels>(() => {
    const issues: Record<string, string> = {}
    lookup.missions.forEach((m, id) => {
      issues[id] = m.title.length > 30 ? `${m.title.slice(0, 29)}…` : m.title
    })
    return { issues }
  }, [lookup.missions])

  const crews = React.useMemo(
    () => [...lookup.crews.values()].map((c) => ({ id: c.id, name: c.name, icon: c.icon, color: c.color })),
    [lookup.crews],
  )
  const agents = React.useMemo(
    () => [...lookup.agents.values()].map((a) => ({ id: a.id, name: a.name, crew_id: a.crew_id })),
    [lookup.agents],
  )

  const agentName = React.useCallback(
    (id?: string) => (id ? lookup.agents.get(id)?.name : undefined),
    [lookup.agents],
  )
  const crewName = React.useCallback(
    (id?: string) => (id ? lookup.crews.get(id)?.name : undefined),
    [lookup.crews],
  )
  const crewMeta = React.useCallback(
    (id?: string) => {
      const c = id ? lookup.crews.get(id) : undefined
      return c ? { icon: c.icon, color: c.color } : undefined
    },
    [lookup.crews],
  )

  // A node clicked in a chain graph arrives as "kind:ref" and nothing else —
  // the walker's payload is deliberately thin. The name is resolved HERE, at
  // the click, from what the page already holds, for the same reason the
  // workflow's label is: a breadcrumb that reads "agent: agt_01H8…" tells the
  // reader nothing about where they are, and re-reading the name at render
  // time from a list that has since refreshed is the staleness this whole
  // change removes.
  const resolveStop = React.useCallback(
    (kind: string, ref: string): ActivityStop => {
      if (kind === "agent") return { kind, id: ref, label: lookup.agents.get(ref)?.name ?? shortId(ref) }
      if (kind === "crew") return { kind, id: ref, label: lookup.crews.get(ref)?.name ?? shortId(ref) }
      if (kind === "issue") return { kind, id: ref, label: labels.issues?.[ref] ?? shortId(ref) }
      if (kind === "routine") {
        // The graph refs a routine by id on some nodes and by slug on others;
        // the journal only ever carries the slug, so the stop is keyed on the
        // slug or it narrows to nothing.
        const r = routines.find((x) => x.id === ref || x.slug === ref)
        return { kind, id: r?.slug ?? ref, label: r?.name ?? ref }
      }
      return { kind, id: ref, label: shortId(ref) }
    },
    [lookup.agents, lookup.crews, labels.issues, routines],
  )

  /** Walk one level down. Bounded and loop-collapsing — see openStop. */
  const openNode = React.useCallback(
    (kind: string, ref: string) => setPath((p) => openStop(p, resolveStop(kind, ref))),
    [resolveStop],
  )

  // Keyboard: a surface you can only drive with a mouse is one you abandon
  // on the second screenful.
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) return
      if (e.key === "Escape") {
        // An opened record first, then one stop back up the walk. Escape is
        // the key readers press to undo the last thing they opened, and until
        // now it stopped working the moment they were already at the top.
        if (selected) setSelected(null)
        else setPath(backFrom)
        return
      }
      if (e.key !== "j" && e.key !== "k") return
      e.preventDefault()
      const i = selected ? visible.findIndex((x) => x.id === selected.id) : -1
      const next = e.key === "j" ? Math.min(i + 1, visible.length - 1) : Math.max(i - 1, 0)
      if (visible[next]) setSelected(visible[next])
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [visible, selected])

  // Every chip carries whether it NARROWS the feed. All of them do except a
  // workflow, which re-points the graph: the journal has no chain_origin
  // column, so a chain is not expressible as a filter over journal rows. The
  // distinction is not cosmetic — the empty-result banner counts filters, and
  // a chip that filters nothing must not be counted there.
  const chips: { label: string; narrows: boolean; onClear: () => void; position?: boolean }[] = []
  if (debouncedSearch)
    chips.push({ label: `“${debouncedSearch}”`, narrows: true, onClear: () => setSearch("") })
  if (pinned) {
    chips.push({
      label:
        pinned.kind === "issue" ? `issue: ${pinned.label}` : `${pinned.kind}: ${pinned.label} (loaded window)`,
      narrows: true,
      onClear: () => setPinned(null),
    })
  }
  // The selection is one chip, because it is one selection — but it is the
  // reader's POSITION, and the trail above already names it and is the way
  // back out of it, so it is not drawn a second time as a removable filter.
  // It is still counted: an issue focus or a node really does narrow the
  // window, and the empty-result banner's "none satisfies all N filters"
  // would be a lie if the filter doing the narrowing went uncounted.
  if (surface.chip) {
    chips.push({ ...surface.chip, position: true, onClear: () => setPath(ACTIVITY_HOME) })
  }
  if (facets.range !== "24h") {
    chips.push({ label: range.label, narrows: true, onClear: () => setFacets({ ...facets, range: "24h" }) })
  }
  for (const s of facets.sources) {
    chips.push({
      label: sourceMeta(s).label,
      narrows: true,
      onClear: () => setFacets({ ...facets, sources: facets.sources.filter((x) => x !== s) }),
    })
  }
  for (const s of facets.severities) {
    chips.push({
      label: s,
      narrows: true,
      onClear: () => setFacets({ ...facets, severities: facets.severities.filter((x) => x !== s) }),
    })
  }
  for (const id of facets.crewIDs) {
    chips.push({
      label: lookup.crews.get(id)?.name ?? id,
      narrows: true,
      onClear: () => setFacets({ ...facets, crewIDs: facets.crewIDs.filter((x) => x !== id) }),
    })
  }
  for (const id of facets.agentIDs) {
    chips.push({
      label: lookup.agents.get(id)?.name ?? id,
      narrows: true,
      onClear: () => setFacets({ ...facets, agentIDs: facets.agentIDs.filter((x) => x !== id) }),
    })
  }

  const narrowingChips = chips.filter((c) => c.narrows).length

  // The window holds events and this question returns none of them. Told
  // apart from a genuinely quiet system, because the overview's copy for zero
  // is reassurance — "nothing broke", "all clear", "Nothing has failed. Nice."
  // — and every word of it is false when the answer is simply unasked.
  const emptyByFilters = narrowingChips > 0 && visible.length === 0 && entries.length > 0

  // One click out. Clearing them one crumb at a time is the state a reader is
  // already lost in.
  //
  // NOT a loop over each chip's onClear. Every facet chip's closure captures
  // the SAME `facets` from this render and spreads it, so calling them in
  // sequence makes each one compute from the original state and the last write
  // wins — "clear all filters" left every earlier facet in place, which is the
  // exact opposite of what the button promises and is invisible unless two
  // facets are on at once. One write, from the known-empty state.
  const clearAllChips = React.useCallback(() => {
    setSearch("")
    setPinned(null)
    setPath(ACTIVITY_HOME)
    setFacets((f) => ({ ...EMPTY_FACETS, scope: f.scope }))
  }, [])

  const scopeMeta = ACTIVITY_SCOPES.find((s) => s.key === facets.scope)

  // One boolean, so the overview and a workflow cannot both be true. They were
  // stacked: picking a workflow re-pointed the graph at the top and left the
  // global overview under it, unchanged — same "56 events · past 24 hours ·
  // every crew, agent, routine and issue in one place" for every workflow, two
  // screenshots identical below the graph. One selection, one column.
  // A routine picked out of the Routines lens. Its own surface, because thirty
  // runs of one routine is a different question from any chain: the workflow
  // page answers "what did this one run cause", and this answers "which of the
  // thirty was the one at ten past two".
  const openRoutineSlug =
    stop?.kind === "routine" && lens === "routines" ? stop.id : null

  // A row in a lens leads to that THING, not to the feed narrowed by its id.
  // Every kind landed on the feed before, which for an issue meant a card
  // headed "Activity · 0 in past 24 hours" over the words "Nothing here" — a
  // mission's own events do not carry the entry types that query asks for, so
  // the one kind with the most to show showed nothing.
  const openIssue = stop?.kind === "issue" ? stop : null
  const openAgent = stop?.kind === "agent" ? stop : null
  const openRun = stop?.kind === "run" ? stop : null

  const overviewShown =
    !loading &&
    !error &&
    !emptyByFilters &&
    surface.main === "overview" &&
    facets.scope === "all" &&
    lens === "workflows"

  // Each lens owns the column. They were four tabs over one Overview: the same
  // four KPIs and the same "what is broken" whichever was pressed, which is a
  // control that changes what is SELECTED without changing what is SHOWN — the
  // same defect as a graph left pointing at the previous chain.
  //
  // `stop == null` is load-bearing and was missing. An issue is an ENTITY kind,
  // so activitySurface answers "overview" for it exactly as it does for no
  // selection at all — which meant clicking an issue row in the Issues lens
  // re-rendered the same Issues dashboard, unchanged, over a breadcrumb that
  // said the reader had gone somewhere. A row that leads back to the page it is
  // on is a dead end wearing a link's clothes.
  //
  // An agent row never had the bug because "agent" is not an entity kind and
  // fell through to the narrowed feed. Requiring no selection here puts issues
  // and routines on that same path, so every lens row leads somewhere for the
  // same reason.
  const lensOverviewShown =
    !loading &&
    !error &&
    !emptyByFilters &&
    surface.main === "overview" &&
    stop == null &&
    lens !== "workflows"
  // The feed as a list: one scope bucket, or one node of a chain. Same shape,
  // different heading — a bucket is "everything that failed", a node is
  // "everything this agent touched" — so it is one branch, not two that can
  // both draw.
  const listShown =
    !loading &&
    !error &&
    !emptyByFilters &&
    surface.main !== "workflow" &&
    !overviewShown &&
    !lensOverviewShown &&
    openRoutineSlug == null &&
    openIssue == null &&
    openAgent == null &&
    openRun == null
  const listTitle = surface.node ? surface.node.label : (scopeMeta?.label ?? "Activity")
  const listCaption = surface.node
    ? `${visible.length.toLocaleString()} ${visible.length === 1 ? "event" : "events"} mentioning this ${
        surface.node.kind
      } in ${range.label.toLowerCase()}`
    : `${visible.length.toLocaleString()} in ${range.label.toLowerCase()}`
  const filterChips = chips.filter((c) => !c.position)

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar
        icon={Activity}
        title="Activity"
        // The header names the reader's position, not a fixed page. In a
        // workflow the event count is not what is on screen — it counted the
        // whole window while the column showed one chain, which is the same
        // lie the stacked overview told underneath it.
        section={stop ? stop.label : scopeMeta?.label}
        ariaLabel="Activity"
        description={
          surface.main === "workflow" ? (
            openChain ? (
              <>
                {openChain.runs.toLocaleString()} {openChain.runs === 1 ? "run" : "runs"} · started by{" "}
                {openChain.started_by}
              </>
            ) : (
              <>workflow</>
            )
          ) : (
            <>
              {visible.length.toLocaleString()} {visible.length === 1 ? "event" : "events"} ·{" "}
              {range.label.toLowerCase()}
            </>
          )
        }
        actions={
          // No Pause, no Refresh. This surface is always live: the journal
          // pushes over SSE and falls back to polling on its own, so a
          // Refresh button would only ever advertise a staleness that is
          // not there, and a Pause is a way to be shown old numbers by
          // accident. The status word next to the count is the whole
          // control surface.
          <LiveBadge status={streamStatus} />
        }
      />

      <div className="relative flex min-h-0 flex-1 overflow-hidden">
        {isMobile && !railCollapsed && (
          <button
            type="button"
            aria-label="Close filters"
            onClick={() => setRailCollapsed(true)}
            className="absolute inset-0 z-20 bg-black/50"
          />
        )}

        <aside
          className={cn(
            "shrink-0 overflow-hidden border-r border-white/[0.06] bg-card transition-all",
            railCollapsed ? "w-9" : "w-[280px]",
            isMobile && !railCollapsed && "absolute inset-y-0 left-0 z-30 shadow-2xl",
          )}
        >
          {railCollapsed ? (
            <div className="flex h-full flex-col items-center pt-1.5">
              <SidebarCollapseButton collapsed onToggle={() => setRailCollapsed(false)} />
            </div>
          ) : (
            <ActivitySidebar
              chains={chains}
              chainsHaveUnrecorded={chainsHaveUnrecorded}
              selectedChain={railChain}
              onSelectChain={selectChain}
              search={search}
              onSearchChange={setSearch}
              facets={facets}
              onChange={setFacets}
              crews={crews}
              agents={agents}
              issues={issues}
              routines={routines}
              crewCounts={crewCounts}
              issueCounts={issueCounts}
              routineCounts={routineCounts}
              focus={focus}
              // The rail chooses which walk you are on, so it starts a new
              // path rather than pushing onto the current one.
              onFocus={(f) => setPath(selectStop(f))}
              lens={lens}
              onLens={setLens}
              // Same rule as onFocus: a row in the rail is where a walk BEGINS.
              onOpenEntity={(kind, id, label) => setPath(selectStop({ kind, id, label }))}
              onToggleCollapse={() => setRailCollapsed(true)}
            />
          )}
        </aside>

        <div className="flex min-w-0 flex-1 flex-col">
          {/* An opened activity takes the whole content area. It was a 340px
              right panel first, and an execution graph does not fit in a
              column — the shape of a run IS the information. */}
          {selected ? (
            <div className="min-h-0 flex-1 overflow-y-auto">
              <ActivityDetail
                entry={selected}
                workspaceId={workspaceId}
                labels={labels}
                agentName={agentName}
                crewName={crewName}
                crewMeta={crewMeta}
                onBack={() => setSelected(null)}
                onSelectEntry={setSelected}
                onSpineClick={(l) => {
                  setSelected(null)
                  setPinned(l)
                }}
              />
            </div>
          ) : (
          <div className="flex min-h-0 min-w-0 flex-col">
            {/* Where you are, and every place you came through to get here.
                Rendered whenever the reader has left the overview, because a
                column that can be four levels deep and cannot say which level
                it is on is a place people stop trusting. */}
            {path.stops.length > 0 && (
              <nav
                aria-label="Activity trail"
                className="flex shrink-0 items-center gap-1 border-b border-white/[0.06] px-3 py-1.5 text-xs"
              >
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-6 gap-1 px-1.5 text-xs text-muted-foreground"
                  onClick={goBack}
                >
                  <ArrowLeft className="h-3.5 w-3.5" />
                  Back
                </Button>
                <span aria-hidden className="mx-1 h-3.5 w-px bg-white/10" />
                {trail.crumbs.map((c, i) => (
                  <React.Fragment key={c.depth}>
                    {i > 0 && <ChevronRight aria-hidden className="h-3 w-3 shrink-0 text-muted-foreground/50" />}
                    {/* The walk is longer than the trail: stops fell off the
                        front at the depth cap, and a breadcrumb that quietly
                        began in the middle would claim the reader started
                        there. */}
                    {i === 1 && trail.truncated && (
                      <span className="text-muted-foreground/60" title="Earlier stops were dropped">
                        …
                      </span>
                    )}
                    <button
                      type="button"
                      onClick={() => setPath((p) => jumpTo(p, c.depth))}
                      aria-current={c.current ? "page" : undefined}
                      className={cn(
                        "max-w-[22ch] truncate rounded px-1.5 py-0.5 hover:bg-white/[0.06]",
                        c.current ? "font-medium text-foreground" : "text-muted-foreground",
                      )}
                    >
                      {c.label}
                    </button>
                  </React.Fragment>
                ))}
              </nav>
            )}

            {/* Filters, not position — the trail owns position. Hidden in a
                workflow, where none of them describes what is on screen. */}
            {filterChips.length > 0 && surface.main !== "workflow" && (
              <SidebarActiveChips className="shrink-0 border-b border-white/[0.06] px-4 py-1.5">
                {filterChips.map((c) => (
                  <SidebarActiveChip key={c.label} onRemove={c.onClear}>
                    {c.label}
                  </SidebarActiveChip>
                ))}
              </SidebarActiveChips>
            )}

            <div className="min-h-0 flex-1 overflow-y-auto">
              {/* A routine out of the Routines lens: its runs, by the hour.
                  Placed before every other branch because it is a whole
                  surface, not a narrowing of the feed — the same reason the
                  workflow page is. */}
              {openIssue && (
                <IssueDrillDown
                  workspaceId={workspaceId}
                  identifier={openIssue.label}
                  chains={chains}
                  onOpenWorkflow={selectChain}
                />
              )}

              {openAgent && (
                <AgentDrillDown
                  workspaceId={workspaceId}
                  agentID={openAgent.id}
                  name={openAgent.label}
                  chains={chains}
                  onOpenWorkflow={selectChain}
                />
              )}

              {openRun && (
                <RunDrillDown
                  workspaceId={workspaceId}
                  runID={openRun.id}
                  // The routine is whichever one the reader walked through to
                  // get here — a run is opened from its routine's list or from
                  // a workflow, and both leave that stop on the path.
                  routineSlug={
                    path.stops.find((s) => s.kind === "routine")?.id ??
                    chains.find((c) => c.origin === workflowAnchor(path))?.routine_slug
                  }
                />
              )}

              {openRoutineSlug && (
                <RoutineRunsPage
                  workspaceId={workspaceId}
                  slug={openRoutineSlug}
                  label={stop?.label ?? openRoutineSlug}
                  routine={routines.find((r) => r.slug === openRoutineSlug)}
                  onOpenRun={(runID) => openNode("run", runID)}
                />
              )}

              {openRoutineSlug == null && openIssue == null && openAgent == null && openRun == null && surface.main !== "workflow" && loading && entries.length === 0 && (
                <div className="flex items-center justify-center gap-2 py-20 text-xs text-muted-foreground">
                  <Spinner className="h-3.5 w-3.5" /> Loading activity…
                </div>
              )}

              {surface.main !== "workflow" && error && !loading && (
                <div className="px-6 py-14">
                  <EmptyState
                    icon={Activity}
                    title="Could not load activity"
                    description={error}
                    action={
                      <Button size="sm" variant="outline" onClick={() => void refresh()}>
                        Try again
                      </Button>
                    }
                  />
                </div>
              )}

              {/* A filter combination that matches nothing must SAY so.
                  Two crumbs can intersect to the empty set — a run pinned from
                  the spine plus an issue focused in the rail is one click
                  away — and the overview then renders a wall of reassuring
                  zeros: "nothing broke", "all clear", "Nothing has failed.
                  Nice." Every one of those is false; the truth is that nothing
                  was ASKED. The window has events, this question has none. */}
              {/* A picked workflow IS the column — the payoff of the whole
                  trace layer: one picture that crosses from the rule that
                  started it, through the routine runs, into the agent work
                  those dispatched. It used to be glued above the global
                  overview, which then answered a question nobody asked; the
                  overview is gone here, not pushed down. */}
              {surface.main === "workflow" &&
                (openChain ? (
                  <WorkflowPage
                    workspaceId={workspaceId}
                    chain={openChain}
                    routineName={routines.find((r) => r.slug === openChain.routine_slug)?.name}
                    onBack={goBack}
                    onOpenNode={openNode}
                  />
                ) : (
                  // Reachable: the chains index is fetched once and not
                  // streamed, so a row can be picked and then swept from the
                  // list. Saying so beats a graph-shaped hole.
                  <div className="px-6 py-14">
                    <EmptyState
                      icon={Activity}
                      title="This workflow is no longer in the index"
                      description={`“${surface.chainLabel ?? "It"}” was picked from a list that has since moved on. The runs it holds are still in the activity feed.`}
                      action={
                        <Button size="sm" variant="outline" onClick={goBack}>
                          Back
                        </Button>
                      }
                    />
                  </div>
                ))}

              {surface.main !== "workflow" && !loading && !error && emptyByFilters && (
                <div className="px-6 py-14">
                  <EmptyState
                    icon={FilterX}
                    title="No activity matches these filters"
                    description={`The loaded window holds ${entries.length.toLocaleString()} ${
                      entries.length === 1 ? "event" : "events"
                    }, and none of them satisfies all ${narrowingChips} filter${
                      narrowingChips === 1 ? "" : "s"
                    } at once. This is an empty question, not a quiet system.`}
                    action={
                      <Button size="sm" variant="outline" onClick={clearAllChips}>
                        Clear {narrowingChips === 1 ? "the filter" : "all filters"}
                      </Button>
                    }
                  />
                </div>
              )}

              {lensOverviewShown && lens === "issues" && (
                <IssuesOverview
                  chains={chains}
                  rangeLabel={range.label}
                  issueMeta={issueMeta}
                  onOpenEntity={(kind, id, label) => setPath(selectStop({ kind, id, label }))}
                  onOpenWorkflow={selectChain}
                />
              )}

              {lensOverviewShown && lens === "agents" && (
                <AgentsOverview
                  chains={chains}
                  rangeLabel={range.label}
                  hiredCount={agents.length}
                  onOpenEntity={(kind, id, label) => setPath(selectStop({ kind, id, label }))}
                  onOpenWorkflow={selectChain}
                />
              )}

              {lensOverviewShown && lens === "routines" && (
                <RoutinesLensOverview
                  chains={chains}
                  routines={routines}
                  rangeLabel={range.label}
                  catalogueCount={pipelines.length}
                  schedules={schedules}
                  onOpenRoutine={(slug, label) => setPath(selectStop({ kind: "routine", id: slug, label }))}
                />
              )}

              {overviewShown && (
                <ActivityOverview
                  entries={visible}
                  rangeLabel={range.label}
                  labels={labels}
                  agentName={agentName}
                  crewName={crewName}
                  crewMeta={crewMeta}
                  selectedID={undefined}
                  onSelect={setSelected}
                  onSpineClick={setPinned}
                  onScope={(s) => setFacets({ ...facets, scope: s })}
                />
              )}

              {listShown && (
                <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
                  <Appear order={0}>
                    <div>
                      <h1 className="text-lg font-semibold tracking-tight">{listTitle}</h1>
                      <p className="text-xs text-muted-foreground">{listCaption}</p>
                    </div>
                  </Appear>
                  <Appear order={1}>
                    <DashboardCard title={listTitle} icon={Activity} hint={`${visible.length}`}>
                      {visible.length === 0 ? (
                        <div className="py-8">
                          <EmptyState
                            icon={Activity}
                            title="Nothing here"
                            description={
                              surface.node
                                ? `Nothing in the loaded window mentions this ${surface.node.kind}. Widen the range, or go back.`
                                : "No event in this window matches. Widen the range or clear a filter."
                            }
                            action={
                              surface.node ? (
                                <Button size="sm" variant="outline" onClick={goBack}>
                                  Back
                                </Button>
                              ) : (
                                <Button size="sm" variant="outline" onClick={() => setFacets(EMPTY_FACETS)}>
                                  Reset
                                </Button>
                              )
                            }
                          />
                        </div>
                      ) : (
                        <div className="flex flex-col">
                          {visible.map((e) => (
                            <FeedRow
                              key={e.id}
                              entry={e}
                              icon={iconFor(e)}
                              labels={labels}
                              actorName={agentName(e.agent_id)}
                              crewName={crewName(e.crew_id)}
                              agentId={e.agent_id}
                              crewIcon={crewMeta(e.crew_id)?.icon}
                              crewColor={crewMeta(e.crew_id)?.color}
                              selected={false}
                              onSelect={() => setSelected(e)}
                              onSpineClick={setPinned}
                            />
                          ))}
                        </div>
                      )}
                    </DashboardCard>
                  </Appear>
                  {nextCursor && (
                    <div className="flex justify-center">
                      <Button size="sm" variant="outline" disabled={loadingMore} onClick={() => void loadMore()}>
                        {loadingMore && <Spinner className="mr-1.5 h-3 w-3" />}
                        Load older
                      </Button>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
          )}
        </div>
      </div>
    </div>
  )
}
