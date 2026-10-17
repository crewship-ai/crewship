"use client"

// /activity — one list of work and changes (Harbor 2, #3389).
//
//   panel      SecondaryPanel: search, Filter, Views with counts, Ledgers
//   top bar    Activity › {view}, the time range, Export
//   content    heading, summary line + Display, the table (grouped), Load more
//   Peek       the row's story on the right; its own page one click away
//
// The list is built from two sources and merged by time (lib/activity-feed):
// the chain index for WORK — a routine run and what it caused, agent work on
// an issue — and the journal for CHANGES somebody made. The journal stream
// keeps both live: a change is prepended as it lands, and a run event
// re-reads the chain index (debounced) so a run's row moves on its own.
//
// A run's own page (steps, inputs, output, the chain graph) is still a full
// page: "Open run" walks to it and the URL then reads `?walk=…`, which every
// older drill-down link also uses. Issues, routines, agents and crews are no
// longer pages of their own here; they narrow the list.

import * as React from "react"
import { useSearchParams } from "next/navigation"
import { Activity, ChevronLeft, ChevronRight, CircleDot, Download, ExternalLink, Filter, List, Workflow } from "lucide-react"
import Link from "next/link"

import { SubBar } from "@/components/layout/sub-bar"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { OfflineBanner, PageEmpty, PageError, PageLoading, PageNoResults, PageNotFound, StaleData } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Switch } from "@/components/ui/switch"
import { routineViewHref } from "@/components/features/routines/routine-navigation"
import { useAbilities } from "@/hooks/use-abilities"
import { useChains } from "@/hooks/use-chains"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalLookup } from "@/hooks/use-journal-lookup"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { useIsMobile } from "@/hooks/use-mobile"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { usePipelines } from "@/hooks/use-pipelines"
import {
  ACTIVITY_VIEWS,
  CHANGE_KIND_LABEL,
  EMPTY_FILTERS,
  GROUP_BY_LABEL,
  WORK_SOURCE_LABEL,
  WORK_STATUS_LABEL,
  activeFacetCount,
  bundleRepeats,
  changeEntryTypes,
  changeFromEntry,
  changeKindOf,
  changePasses,
  feedCsv,
  feedUrlParams,
  groupFeed,
  inRange,
  mergeFeed,
  parseFeedUrl,
  rangeBounds,
  rangeLabel,
  runHistory,
  viewCounts,
  viewOfStatus,
  workFromChain,
  workPasses,
  type ChangeItem,
  type ChangeKind,
  type FeedFilters,
  type FeedItem,
  type GroupBy,
  type TimeRange,
  type WorkItem,
  type WorkSource,
} from "@/lib/activity-feed"
import { runTone } from "@/lib/activity-run"
import { ACTIVITY_HOME, activityTrail, backFrom, currentStop, jumpTo, openStop, selectStop, type ActivityPath, type ActivityStop } from "@/lib/activity-selection"
import { NOISE_ENTRY_TYPES, shortId, type SpineLabels } from "@/lib/activity-stream"
import { activityUrlParams, parseActivityUrl } from "@/lib/activity-url"
import { useAppStore } from "@/lib/store"
import { toast } from "@/lib/toast"
import type { JournalEntry } from "@/lib/types/journal"
import { cn } from "@/lib/utils"

import { ActivityDetail } from "./activity-detail"
import { FeedTable, RunHistoryStrip, type AgentResolver } from "./activity-feed-table"
import { ActivityPanel, type LedgerSection } from "./activity-panel"
import { ActivityPeek } from "./activity-peek"
import { ActivityRange } from "./activity-range"
import { ActivityRunPage } from "./activity-run-page"
import { ActivityWorkPage } from "./activity-work-page"
import { WorkflowPage } from "./workflow-page"

/** The chain index the list reads: the server's maximum page. */
const CHAIN_LIMIT = 200
const PAGE = 50
const JOURNAL_PAGE = 300
const MAX_BUFFERED = 2_000

/** Stops that are a place of their own; every other kind narrows the list. */
const PAGE_KINDS = new Set(["workflow", "run"])

/** Journal events that move a work row: re-read the chain index on them. */
function movesWork(entryType: string): boolean {
  return /^(run|pipeline\.run|pipeline\.step|assignment|approval|peer\.escalation|keeper\.request)\b/.test(entryType)
}

interface InitialState {
  path: ActivityPath
  filters: FeedFilters
  group: GroupBy
  bundle: boolean
  range: TimeRange
  peek?: string
  entryId?: string
}

/**
 * Read the URL once. `?walk=` keeps its meaning for a run or a workflow (the
 * full page); a walk that ends on an issue, routine, agent or crew narrows
 * the list to that object instead, because those have no page here now.
 */
function readInitial(params: URLSearchParams): InitialState {
  const feed = parseFeedUrl(params)
  const legacy = parseActivityUrl(params)
  let path: ActivityPath = ACTIVITY_HOME
  const filters = { ...feed.filters }
  if (params.get("walk")) {
    const last = currentStop(legacy.path)
    if (last && PAGE_KINDS.has(last.kind)) path = legacy.path
    else if (last) applyStop(filters, last)
  }
  return { path, filters, group: feed.group, bundle: feed.bundle, range: feed.range, peek: feed.peek, entryId: legacy.entryId }
}

function applyStop(f: FeedFilters, s: ActivityStop): boolean {
  if (s.kind === "issue") f.issue = s.id
  else if (s.kind === "routine") f.routine = s.id
  else if (s.kind === "agent") f.agent = s.id
  else if (s.kind === "crew") f.crew = s.id
  else return false
  return true
}

/** Connection state as a word beside the counts. */
function LiveState({ status }: { status: string }) {
  const live = status === "connected"
  const polling = status === "polling"
  return (
    <span
      className={cn("inline-flex items-center gap-1.5 text-label", live ? "text-muted-foreground" : polling ? "text-warn" : "text-muted-foreground-soft")}
      title={live ? "Updates arrive as they happen" : polling ? "The stream is unavailable; checking every 5 s" : "Connecting to live updates"}
    >
      <span aria-hidden className={cn("size-[7px] rounded-full", live ? "animate-pulse bg-success motion-reduce:animate-none" : polling ? "bg-warn" : "bg-muted-foreground/40")} />
      {live ? "Live" : polling ? "Polling" : "Connecting…"}
    </span>
  )
}

function DisplayMenu({
  group,
  onGroup,
  bundle,
  onBundle,
}: {
  group: GroupBy
  onGroup: (g: GroupBy) => void
  bundle: boolean
  onBundle: (b: boolean) => void
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button size="sm" variant="outline">
          <List className="size-3.5" aria-hidden />
          Display
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[280px] p-2">
        <div className="px-1.5 pb-1.5 text-label font-semibold text-muted-foreground">Group by</div>
        <div role="group" aria-label="Group by" className="grid grid-cols-4 gap-0.5 rounded-md bg-muted p-0.5">
          {(Object.keys(GROUP_BY_LABEL) as GroupBy[]).map((g) => (
            <button
              key={g}
              type="button"
              aria-pressed={group === g}
              onClick={() => onGroup(g)}
              className={cn(
                "kit-tap h-[var(--control-xs)] rounded-sm text-label",
                group === g ? "bg-card font-semibold text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {GROUP_BY_LABEL[g]}
            </button>
          ))}
        </div>
        <label className="kit-tap mt-2 flex items-center justify-between gap-3 rounded-md border-t border-border px-1.5 pt-2.5 text-body">
          <span>Group repeated runs</span>
          <Switch checked={bundle} onCheckedChange={onBundle} aria-label="Group repeated runs" />
        </label>
      </PopoverContent>
    </Popover>
  )
}

export function ActivityStreamView({
  workspaceId,
  onOpenSection,
  journalLedgers = false,
  modeSegment,
}: {
  workspaceId: string
  /** Open one of the ledgers. See ActivityWorkspace. */
  onOpenSection?: (section: LedgerSection) => void
  /** OWNER / ADMIN: list the technical log and fleet runs under Ledgers (F-02). */
  journalLedgers?: boolean
  /** The Activity / Spend segment, first in the top bar (admins only). */
  modeSegment?: React.ReactNode
}) {
  const isMobile = useIsMobile()
  const lookup = useJournalLookup()
  const { role } = useAbilities()
  const openPanelSheet = useAppStore((s) => s.setSecondaryPanelOpen)

  const searchParams = useSearchParams()
  // eslint-disable-next-line react-hooks/exhaustive-deps -- read once at mount; Back/Forward is read by the popstate listener
  const initial = React.useMemo(() => readInitial(searchParams), [])

  const [path, setPath] = React.useState<ActivityPath>(initial.path)
  const [filters, setFiltersRaw] = React.useState<FeedFilters>(initial.filters)
  const [group, setGroup] = React.useState<GroupBy>(initial.group)
  const [bundle, setBundle] = React.useState(initial.bundle)
  const [range, setRange] = React.useState<TimeRange>(initial.range)
  const [peekId, setPeekId] = React.useState<string | null>(initial.peek ?? null)
  const [pendingEntryId, setPendingEntryId] = React.useState<string | undefined>(initial.entryId)
  const [record, setRecord] = React.useState<JournalEntry | null>(null)
  const [limit, setLimit] = React.useState(PAGE)
  const [debouncedQ, setDebouncedQ] = React.useState(initial.filters.q)

  // The clock the list is grouped against; a minute is fine-grained enough.
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(t)
  }, [])

  // Any narrowing returns to the list and to its first page.
  const setFilters = React.useCallback((f: FeedFilters) => {
    setFiltersRaw(f)
    setPath(ACTIVITY_HOME)
    setRecord(null)
  }, [])
  React.useEffect(() => setLimit(PAGE), [filters, group, bundle, range])

  React.useEffect(() => {
    const t = setTimeout(() => setDebouncedQ(filters.q.trim()), 280)
    return () => clearTimeout(t)
  }, [filters.q])

  /* ---------------- data ---------------- */

  const { pipelines } = usePipelines(workspaceId)
  const routineName = React.useCallback((slug: string) => pipelines.find((p) => p.slug === slug)?.name, [pipelines])
  const crewOfAgent = React.useCallback((id: string) => lookup.agents.get(id)?.crew_id ?? undefined, [lookup.agents])
  const filterCtx = React.useMemo(() => ({ routineName, crewOfAgent }), [routineName, crewOfAgent])

  const chainsQ = useChains(workspaceId, CHAIN_LIMIT)
  // Whether the index has answered once. Before its first request starts the
  // hook reads "not loading, no chains", which is not "this workspace is empty".
  const chainsAsked = React.useRef(false)
  const [chainsSettled, setChainsSettled] = React.useState(false)
  React.useEffect(() => {
    if (chainsQ.loading) chainsAsked.current = true
    else if (chainsAsked.current || chainsQ.chains.length > 0 || chainsQ.error) setChainsSettled(true)
  }, [chainsQ.loading, chainsQ.chains.length, chainsQ.error])
  const bounds = React.useMemo(() => rangeBounds(range, now), [range, now])

  // Changes only ever show in Everything without a work-only facet; the
  // journal is not asked otherwise.
  const changesWanted = !filters.status && !filters.source && filters.what !== "work"
  const changeKind: ChangeKind | null = filters.what && filters.what !== "work" && filters.what !== "changes" ? filters.what : null
  const journalParams = React.useMemo<Record<string, string | undefined>>(
    () => ({
      since: new Date(bounds.since).toISOString(),
      until: bounds.until != null ? new Date(bounds.until).toISOString() : undefined,
      entry_type: changeEntryTypes(changeKind).join(","),
      mission_id: filters.issue ?? undefined,
      q: debouncedQ || undefined,
    }),
    // `now` moves every minute; the window's start is fixed by the range until the range changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [range, changeKind, filters.issue, debouncedQ],
  )
  const journal = useJournalList({ workspaceId, params: journalParams, limit: JOURNAL_PAGE, maxEntries: MAX_BUFFERED, enabled: changesWanted })

  // One stream for both: a change is prepended, a run event re-reads the index.
  const refreshChainsRef = React.useRef(chainsQ.refresh)
  React.useEffect(() => {
    refreshChainsRef.current = chainsQ.refresh
  }, [chainsQ.refresh])
  const chainTimer = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  React.useEffect(() => () => {
    if (chainTimer.current) clearTimeout(chainTimer.current)
  }, [])
  const prependLive = journal.prependLive
  const onLiveEntry = React.useCallback(
    (e: JournalEntry) => {
      const kind = changeKindOf(e.entry_type)
      if (kind && changesWanted && (!changeKind || kind === changeKind) && (!filters.issue || e.mission_id === filters.issue)) prependLive(e)
      if (movesWork(e.entry_type)) {
        if (chainTimer.current) clearTimeout(chainTimer.current)
        chainTimer.current = setTimeout(() => void refreshChainsRef.current(), 1_500)
      }
    },
    [prependLive, changesWanted, changeKind, filters.issue],
  )
  const streamParams = React.useMemo(() => ({ exclude_entry_type: NOISE_ENTRY_TYPES.join(",") }), [])
  const stream = useJournalStream({ workspaceId, params: streamParams, enabled: true, onEntry: onLiveEntry })

  /* ---------------- rows ---------------- */

  const allWork = React.useMemo(
    () => chainsQ.chains.map((c) => workFromChain(c, { routineName })).filter((w) => w.active || inRange(w.ts, range, now)),
    [chainsQ.chains, routineName, range, now],
  )
  const issueLabel = React.useCallback((id: string) => {
    const m = lookup.missions.get(id)
    return m ? [m.identifier, m.title].filter(Boolean).join(" ") : undefined
  }, [lookup.missions])
  const agentName = React.useCallback((id?: string) => (id ? lookup.agents.get(id)?.name : undefined), [lookup.agents])
  const allChanges = React.useMemo(() => {
    if (!changesWanted) return []
    const out: ChangeItem[] = []
    for (const e of journal.entries) {
      const c = changeFromEntry(e, { agentName: (id) => agentName(id), issueLabel, routineName })
      if (c && inRange(c.ts, range, now)) out.push(c)
    }
    return out
  }, [changesWanted, journal.entries, agentName, issueLabel, routineName, range, now])

  const effective = React.useMemo(() => ({ ...filters, q: debouncedQ }), [filters, debouncedQ])
  const feed = React.useMemo(() => {
    const merged = mergeFeed(
      allWork.filter((w) => workPasses(w, effective, filterCtx)),
      allChanges.filter((c) => changePasses(c, effective, filterCtx)),
    )
    return bundle ? bundleRepeats(merged, now) : merged
  }, [allWork, allChanges, effective, filterCtx, bundle, now])
  const shown = React.useMemo(() => feed.slice(0, limit), [feed, limit])
  const groups = React.useMemo(() => groupFeed(shown, group, now, { routineName }), [shown, group, now, routineName])
  const counts = React.useMemo(
    () =>
      viewCounts(allWork, allChanges, effective, filterCtx, bundle ? (xs) => bundleRepeats(xs, now) : undefined),
    [allWork, allChanges, effective, filterCtx, bundle, now],
  )

  /* ---------------- panel options ---------------- */

  const panelOptions = React.useMemo(() => {
    const routines = new Map<string, number>()
    const agents = new Map<string, { name: string; n: number }>()
    const crews = new Map<string, number>()
    const sources = new Map<WorkSource, number>()
    for (const w of allWork) {
      if (w.routineSlug) routines.set(w.routineSlug, (routines.get(w.routineSlug) ?? 0) + 1)
      sources.set(w.source, (sources.get(w.source) ?? 0) + 1)
      const seen = new Set<string>()
      for (const a of w.agents) {
        agents.set(a.id, { name: a.name, n: (agents.get(a.id)?.n ?? 0) + 1 })
        const crew = crewOfAgent(a.id)
        if (crew && !seen.has(crew)) {
          seen.add(crew)
          crews.set(crew, (crews.get(crew) ?? 0) + 1)
        }
      }
    }
    // A link can name a routine, agent or crew with nothing in the range; it
    // stays pickable so the facet can be cleared where it was set.
    if (filters.routine && !routines.has(filters.routine)) routines.set(filters.routine, 0)
    if (filters.agent && !agents.has(filters.agent)) agents.set(filters.agent, { name: agentName(filters.agent) ?? shortId(filters.agent), n: 0 })
    if (filters.crew && !crews.has(filters.crew)) crews.set(filters.crew, 0)
    const byCount = <T extends { count: number; label: string }>(a: T, b: T) => b.count - a.count || a.label.localeCompare(b.label)
    return {
      routines: [...routines].map(([id, count]) => ({ id, label: routineName(id) ?? id, count })).sort(byCount),
      agents: [...agents].map(([id, a]) => ({ id, label: a.name, count: a.n })).sort(byCount),
      crews: [...crews].map(([id, count]) => ({ id, label: lookup.crews.get(id)?.name ?? shortId(id), count })).sort(byCount),
      sources: [...sources].map(([id, count]) => ({ id, count })).sort((a, b) => b.count - a.count),
    }
  }, [allWork, crewOfAgent, routineName, lookup.crews, agentName, filters.routine, filters.agent, filters.crew])

  const resolveAgent = React.useCallback<AgentResolver>(
    (id) => {
      const a = lookup.agents.get(id)
      return a ? { seed: a.avatar_seed || a.slug || a.id, style: a.avatar_style, name: a.name } : undefined
    },
    [lookup.agents],
  )

  /* ---------------- the Peek ---------------- */

  // J / K follow the rows in the order the table draws them.
  const flat = React.useMemo(() => groups.flatMap((g) => g.items), [groups])
  const findItem = React.useCallback(
    (id: string | null): FeedItem | null => {
      if (!id) return null
      for (const x of feed) {
        if (x.id === id) return x
        if (x.type === "work" && x.runs) {
          const r = x.runs.find((y) => y.id === id)
          if (r) return r
        }
      }
      const loose = allWork.find((w) => w.id === id)
      if (loose) return loose
      // Outside the range but in the index: still opens in the Peek.
      const c = chainsQ.chains.find((x) => x.origin === id)
      return c ? workFromChain(c, { routineName }) : null
    },
    [feed, allWork, chainsQ.chains, routineName],
  )
  const peekItem = findItem(peekId)

  // A run the link named that the index does not hold (older than the 200
  // newest workflows, or outside the range) opens as its own page instead.
  React.useEffect(() => {
    if (!peekId || peekItem || !chainsSettled || chainsQ.loading || journal.loading) return
    if (journal.entries.some((e) => e.id === peekId)) return
    setPath((p) => (p.stops.length === 0 ? selectStop({ kind: "run", id: peekId, label: peekId }) : p))
    setPeekId(null)
  }, [peekId, peekItem, chainsSettled, chainsQ.loading, journal.loading, journal.entries])

  // `?entry=<id>`: a change opens in the Peek, any other record as its page.
  React.useEffect(() => {
    if (!pendingEntryId || journal.loading) return
    const hit = journal.entries.find((e) => e.id === pendingEntryId)
    if (hit) {
      if (changeKindOf(hit.entry_type)) setPeekId(hit.id)
      else setRecord(hit)
    }
    setPendingEntryId(undefined)
  }, [pendingEntryId, journal.loading, journal.entries])

  const peekIndex = peekItem ? flat.findIndex((x) => x.id === peekItem.id) : -1
  const step = React.useCallback(
    (dir: 1 | -1) => {
      if (flat.length === 0) return
      const i = peekIndex < 0 ? (dir === 1 ? -1 : flat.length) : peekIndex
      const next = flat[Math.min(Math.max(i + dir, 0), flat.length - 1)]
      if (next) setPeekId(next.id)
    },
    [flat, peekIndex],
  )

  const openFull = React.useCallback((w: WorkItem) => {
    setPeekId(null)
    setPath(selectStop({ kind: "workflow", id: w.id, label: w.title }))
  }, [])

  /** A node clicked inside a run page: a run walks deeper, an object narrows the list. */
  const openNode = React.useCallback(
    (kind: string, ref: string) => {
      if (PAGE_KINDS.has(kind)) {
        setPath((p) => openStop(p, { kind, id: ref, label: kind === "run" ? (routineName(chainsQ.chains.find((c) => c.origin === ref)?.routine_slug ?? "") ?? shortId(ref)) : shortId(ref) }))
        return
      }
      const next = { ...EMPTY_FILTERS, status: null }
      if (applyStop(next, { kind, id: ref, label: ref })) {
        if (kind === "routine") setRange({ key: "30d" })
        setFilters(next)
      }
    },
    [chainsQ.chains, routineName, setFilters],
  )

  const showRoutine = React.useCallback(
    (slug: string, oneByOne = false) => {
      setPeekId(null)
      setFilters({ ...EMPTY_FILTERS, routine: slug })
      setRange({ key: "30d" })
      if (oneByOne) setBundle(false)
    },
    [setFilters],
  )
  const showIssue = React.useCallback(
    (id: string) => {
      setPeekId(null)
      setFilters({ ...EMPTY_FILTERS, issue: id })
    },
    [setFilters],
  )

  /* ---------------- the URL ---------------- */

  const url = React.useMemo(() => {
    if (path.stops.length > 0) return activityUrlParams({ path, lens: "workflows" }).toString()
    // A change in the Peek is written as its journal record, a run as ?run=.
    const peekIsChange = peekItem?.type === "change"
    const p = feedUrlParams({ filters, group, bundle, range, peek: peekIsChange ? undefined : (peekId ?? undefined) })
    if (record) p.set("entry", record.id)
    else if (peekIsChange) p.set("entry", peekItem.id)
    return p.toString()
  }, [path, filters, group, bundle, range, peekId, peekItem, record])
  const position = `${path.stops.map((s) => s.id).join("/")}|${peekId ?? ""}|${record?.id ?? ""}`
  const lastPosition = React.useRef(position)
  React.useEffect(() => {
    if (typeof window === "undefined") return
    const next = url ? `${window.location.pathname}?${url}` : window.location.pathname
    const moved = lastPosition.current !== position
    lastPosition.current = position
    if (window.location.pathname + window.location.search === next) return
    if (moved) window.history.pushState(null, "", next)
    else window.history.replaceState(null, "", next)
  }, [url, position])

  React.useEffect(() => {
    const onPop = () => {
      const s = readInitial(new URLSearchParams(window.location.search))
      lastPosition.current = `${s.path.stops.map((x) => x.id).join("/")}|${s.peek ?? ""}|${s.entryId ?? ""}`
      setPath(s.path)
      setFiltersRaw(s.filters)
      setGroup(s.group)
      setBundle(s.bundle)
      setRange(s.range)
      setPeekId(s.peek ?? null)
      setRecord(null)
      setPendingEntryId(s.entryId)
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])

  /* ---------------- keys ---------------- */

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || (el instanceof HTMLElement && el.isContentEditable)) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      // The Peek drives its own J / K / Enter / Esc while it is open.
      if (peekId || document.querySelector("[data-slot=peek]")) return
      if (e.key === "Escape") {
        if (record) setRecord(null)
        else if (path.stops.length > 0) setPath(backFrom)
        return
      }
      if (record || path.stops.length > 0) return
      if (e.key === "j" || e.key === "k") {
        e.preventDefault()
        step(e.key === "j" ? 1 : -1)
        return
      }
      const n = Number(e.key)
      if (n >= 1 && n <= ACTIVITY_VIEWS.length) {
        e.preventDefault()
        setFilters({ ...filters, status: ACTIVITY_VIEWS[n - 1].status })
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [peekId, record, path.stops.length, step, filters, setFilters])

  /* ---------------- what the page is ---------------- */

  const view = viewOfStatus(filters.status)
  const facets = activeFacetCount(filters)
  const issueObj = filters.issue ? { id: filters.issue, label: issueLabel(filters.issue) ?? lookup.missions.get(filters.issue)?.title ?? shortId(filters.issue), identifier: lookup.missions.get(filters.issue)?.identifier } : null
  const routineObj = filters.routine ? { slug: filters.routine, label: routineName(filters.routine) ?? filters.routine } : null
  const objectOnly = facets === 1 && (issueObj || routineObj)
  const title = issueObj
    ? issueObj.label
    : routineObj
      ? routineObj.label
      : filters.what === "changes"
        ? "Changes"
        : filters.what === "work"
          ? "Runs and agent work"
          : filters.what
            ? CHANGE_KIND_LABEL[filters.what]
            : filters.agent
              ? (agentName(filters.agent) ?? "Agent")
              : facets > 0
                ? "Filtered activity"
                : view && view !== "all"
                  ? ACTIVITY_VIEWS.find((v) => v.key === view)!.label
                  : filters.status
                    ? WORK_STATUS_LABEL[filters.status]
                    : "Everything"
  const description = issueObj
    ? "Everything about this issue: its work, the changes to it and what they caused"
    : routineObj
      ? "Everything about this routine: its runs, the changes to it and what they caused"
      : facets > 0
        ? `${facets} filter${facets === 1 ? "" : "s"} · ${rangeLabel(range).toLowerCase()}`
        : "Everything your crews and people did: runs, agent work, and every change to issues, routines, pages, crews and access"

  const filterWords = [
    filters.q && `“${filters.q}”`,
    filters.status && WORK_STATUS_LABEL[filters.status],
    filters.what && (filters.what === "work" ? "Runs and agent work" : filters.what === "changes" ? "Changes" : CHANGE_KIND_LABEL[filters.what]),
    routineObj && `Routine: ${routineObj.label}`,
    issueObj && `Issue: ${issueObj.label}`,
    filters.agent && `Who: ${agentName(filters.agent) ?? shortId(filters.agent)}`,
    filters.crew && `Crew: ${lookup.crews.get(filters.crew)?.name ?? shortId(filters.crew)}`,
    filters.source && `Started by: ${WORK_SOURCE_LABEL[filters.source]}`,
    rangeLabel(range),
  ].filter(Boolean)

  const nNeed = feed.filter((x) => x.type === "work" && x.status === "waiting").length
  const nFailed = feed.filter((x) => x.type === "work" && x.status === "failed").length
  const initialLoading = !chainsSettled && chainsQ.chains.length === 0 && (!changesWanted || journal.entries.length === 0)
  const workspaceEmpty =
    chainsSettled && !chainsQ.loading && !chainsQ.error && chainsQ.chains.length === 0 && !chainsQ.hasUnrecordedRuns && allChanges.length === 0 && !journal.loading && facets === 0 && !filters.q && !filters.status
  const offline = stream.status === "error"

  const routineRecords = usePipelineRunRecords(workspaceId, routineObj?.slug ?? null)
  const routineHistory = React.useMemo(() => runHistory(routineRecords.records, null), [routineRecords.records])

  const sentinel = React.useRef<HTMLDivElement | null>(null)
  React.useEffect(() => {
    const el = sentinel.current
    if (!el || typeof IntersectionObserver === "undefined") return
    const io = new IntersectionObserver((xs) => {
      if (xs.some((x) => x.isIntersecting)) setLimit((n) => (n < feed.length ? n + PAGE : n))
    })
    io.observe(el)
    return () => io.disconnect()
  }, [feed.length])

  function exportCsv() {
    const blob = new Blob([feedCsv(feed)], { type: "text/csv;charset=utf-8" })
    const href = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = href
    a.download = `activity-${new Date(now).toISOString().slice(0, 10)}.csv`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(href)
    toast.success(`Exported ${feed.length} ${feed.length === 1 ? "row" : "rows"} as CSV`)
  }

  const labels = React.useMemo<SpineLabels>(() => {
    const issues: Record<string, string> = {}
    lookup.missions.forEach((m, id) => {
      issues[id] = m.title.length > 30 ? `${m.title.slice(0, 29)}…` : m.title
    })
    return { issues }
  }, [lookup.missions])

  const stop = currentStop(path)
  const trail = activityTrail(path)
  const openChain = stop ? chainsQ.chains.find((c) => c.origin === stop.id) : undefined

  /* ---------------- render ---------------- */

  const listBody = (
    <>
      {chainsQ.error && (
        <PageError
          title="Could not load runs and agent work"
          description={changesWanted ? "Changes below still load. One part failed; the rest works." : "Retry to load the runs again."}
          detail={chainsQ.error}
          onRetry={() => void chainsQ.refresh()}
        />
      )}
      {changesWanted && journal.error && (
        <PageError
          title="Could not load changes"
          description="Runs and agent work below are up to date. One part failed; the rest works."
          detail={journal.error}
          onRetry={() => void journal.refresh()}
        />
      )}
      {initialLoading ? (
        <PageLoading rows={7} />
      ) : workspaceEmpty ? (
        <PageEmpty
          icon={Activity}
          title="No activity yet"
          description="Every routine run, agent task and change shows up here."
          primary={{ label: "Run a routine", href: "/routines", icon: Workflow }}
          cli="crewship activity"
        />
      ) : feed.length === 0 && !chainsQ.error ? (
        <PageNoResults
          filters={filterWords.join(" · ")}
          onClear={facets > 0 || filters.q || filters.status ? () => setFilters({ ...EMPTY_FILTERS }) : undefined}
          onWiden={range.key !== "30d" ? () => setRange({ key: "30d" }) : undefined}
          widenLabel="Show last 30 days"
        />
      ) : feed.length > 0 ? (
        <StaleData stale={offline}>
          <FeedTable groups={groups} now={now} selectedId={peekItem?.id ?? null} onSelect={(x) => setPeekId((cur) => (cur === x.id ? null : x.id))} resolveAgent={resolveAgent} />
        </StaleData>
      ) : null}
      {feed.length > 0 && (
        <div className="flex flex-wrap items-center gap-3 px-1">
          {feed.length > limit ? (
            <Button size="sm" variant="outline" onClick={() => setLimit((n) => n + PAGE)}>
              Load {Math.min(PAGE, feed.length - limit)} more
            </Button>
          ) : changesWanted && journal.nextCursor ? (
            <Button size="sm" variant="outline" disabled={journal.loadingMore} onClick={() => void journal.loadMore()}>
              Load older changes
            </Button>
          ) : null}
          <p className="text-label text-muted-foreground">
            Showing {Math.min(limit, feed.length)} of {feed.length} · {rangeLabel(range).toLowerCase()}
            {chainsQ.hasMore && ` · runs from the ${CHAIN_LIMIT} most recent workflows`}
            {chainsQ.hasUnrecordedRuns && " · older runs predate chain recording and are not listed"}
          </p>
          <div ref={sentinel} aria-hidden className="h-px w-full" />
        </div>
      )}
    </>
  )

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <SubBar
        icon={Activity}
        title="Activity"
        section={path.stops.length > 0 ? (stop?.label ?? "Run") : title}
        ariaLabel="Activity"
        // The list shows the view title as its own h1; the top bar's path
        // then stays plain text so the page keeps exactly one.
        heading={record != null || path.stops.length > 0}
        actions={
          <>
            {modeSegment}
            {modeSegment && <span aria-hidden className="mx-1 h-5 w-px bg-border" />}
            {!isMobile && <ActivityRange value={range} onChange={setRange} now={now} />}
            <Button size="icon-sm" variant="ghost" aria-label="Export CSV" title="Export CSV" onClick={exportCsv} disabled={feed.length === 0}>
              <Download className="size-4" aria-hidden />
            </Button>
          </>
        }
      />
      <div className="flex min-h-0 flex-1">
        <SecondaryPanel id="activity" label="Activity views">
          <ActivityPanel
            filters={filters}
            onFilters={setFilters}
            viewCounts={counts}
            routines={panelOptions.routines}
            agents={panelOptions.agents}
            crews={panelOptions.crews}
            sources={panelOptions.sources}
            onOpenLedger={onOpenSection}
            journalLedgers={journalLedgers}
          />
        </SecondaryPanel>

        <main className="min-h-0 min-w-0 flex-1 overflow-y-auto" aria-label="Activity">
          {offline && <OfflineBanner onRetry={stream.reconnect} />}

          {record ? (
            <div className="animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out">
              <ActivityDetail
                entry={record}
                workspaceId={workspaceId}
                labels={labels}
                agentName={agentName}
                crewName={(id) => (id ? lookup.crews.get(id)?.name : undefined)}
                crewMeta={(id) => {
                  const c = id ? lookup.crews.get(id) : undefined
                  return c ? { icon: c.icon, color: c.color } : undefined
                }}
                onBack={() => setRecord(null)}
                onSelectEntry={setRecord}
                onSpineClick={(l) => {
                  setRecord(null)
                  if (l.kind === "issue") showIssue(l.id)
                  else if (l.kind === "routine") showRoutine(l.id)
                  else if (l.kind === "run") setPath(selectStop({ kind: "run", id: l.id, label: l.label }))
                }}
              />
            </div>
          ) : path.stops.length > 0 ? (
            <div className="flex min-h-0 flex-col">
              <nav aria-label="Activity trail" className="flex shrink-0 items-center gap-1 border-b border-border bg-card/40 px-4 py-2 text-label">
                <Button size="sm" variant="ghost" onClick={() => setPath(ACTIVITY_HOME)}>
                  <ChevronLeft className="size-3.5" aria-hidden />
                  Back to activity
                </Button>
                {trail.truncated && <span className="text-muted-foreground">…</span>}
                {trail.crumbs
                  .filter((c) => c.depth > 0)
                  .map((c) => (
                    <React.Fragment key={c.depth}>
                      <ChevronRight aria-hidden className="size-3.5 shrink-0 text-muted-foreground-soft" />
                      <button
                        type="button"
                        onClick={() => setPath((p) => jumpTo(p, c.depth))}
                        aria-current={c.current ? "page" : undefined}
                        className={cn("kit-tap max-w-[26ch] truncate rounded px-1.5 py-0.5 hover:bg-muted", c.current ? "font-mono text-foreground" : "text-muted-foreground")}
                      >
                        {c.label}
                      </button>
                    </React.Fragment>
                  ))}
              </nav>
              <div key={stop ? `${stop.kind}:${stop.id}` : "page"} className="animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out">
                {stop?.kind === "run" ? (
                  <ActivityRunPage
                    key={stop.id}
                    workspaceId={workspaceId}
                    runId={stop.id}
                    chain={openChain}
                    routineName={openChain?.routine_slug ? routineName(openChain.routine_slug) : undefined}
                    onOpenNode={openNode}
                  />
                ) : openChain?.kind === "assignment" ? (
                  <ActivityWorkPage key={openChain.origin} workspaceId={workspaceId} chain={openChain} onOpenNode={openNode} />
                ) : openChain?.routine_slug ? (
                  <ActivityRunPage
                    key={openChain.origin}
                    workspaceId={workspaceId}
                    runId={openChain.origin}
                    chain={openChain}
                    routineName={routineName(openChain.routine_slug)}
                    onOpenNode={openNode}
                  />
                ) : openChain ? (
                  <WorkflowPage
                    workspaceId={workspaceId}
                    chain={openChain}
                    routineName={openChain.routine_slug ? routineName(openChain.routine_slug) : undefined}
                    onBack={() => setPath(backFrom)}
                    onOpenNode={openNode}
                  />
                ) : chainsQ.loading ? (
                  <PageLoading rows={5} />
                ) : (
                  <PageNotFound
                    what="run"
                    description="It is not among the most recent workflows, or it was removed. Its events are still in the Journal."
                    back={{ label: "Back to activity", onClick: () => setPath(ACTIVITY_HOME) }}
                  />
                )}
              </div>
            </div>
          ) : (
            <div className="mx-auto grid min-w-0 max-w-[1240px] gap-3.5 p-4 md:p-6">
              {isMobile && (
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    onClick={() => openPanelSheet(true)}
                    className="kit-tap inline-flex h-[var(--control-sm)] items-center gap-1.5 rounded-full border border-border bg-card px-3 text-label"
                  >
                    <Filter className="size-3.5" aria-hidden />
                    {title}
                  </button>
                  <ActivityRange value={range} onChange={setRange} now={now} compact />
                </div>
              )}

              <header className="flex flex-wrap items-center gap-3.5">
                {(issueObj || routineObj) && (
                  <span className="page-tile page-tile-40" aria-hidden>
                    {issueObj ? <CircleDot /> : <Workflow />}
                  </span>
                )}
                <div className="min-w-0 flex-1">
                  <h1 className="text-title font-semibold tracking-tight text-balance">{title}</h1>
                  <p className="mt-0.5 text-body text-muted-foreground">{description}</p>
                </div>
                {objectOnly && routineObj && (
                  <Button size="sm" variant="outline" asChild>
                    <Link href={routineViewHref(routineObj.slug, "definition")}>
                      <ExternalLink className="size-3.5" aria-hidden />
                      Open routine
                    </Link>
                  </Button>
                )}
                {objectOnly && issueObj?.identifier && (
                  <Button size="sm" variant="outline" asChild>
                    <Link href={`/issues/${encodeURIComponent(issueObj.identifier)}`}>
                      <ExternalLink className="size-3.5" aria-hidden />
                      Open issue
                    </Link>
                  </Button>
                )}
              </header>

              {!initialLoading && !workspaceEmpty && (
                <div className="flex flex-wrap items-center gap-2" data-testid="activity-summary">
                  <span className="text-body text-muted-foreground">
                    <b className="font-semibold text-foreground">{feed.length}</b> {feed.length === 1 ? "item" : "items"}
                    {nNeed > 0 && <b className="font-semibold text-warn"> · {nNeed} need a person</b>}
                    {nFailed > 0 && <b className="font-semibold text-destructive"> · {nFailed} could not finish</b>}
                  </span>
                  <LiveState status={stream.status} />
                  <span className="flex-1" />
                  <DisplayMenu group={group} onGroup={setGroup} bundle={bundle} onBundle={setBundle} />
                </div>
              )}

              {routineObj && routineHistory.bars.length > 0 && (
                <div className="flex items-start gap-3 rounded-xl border border-border bg-card px-3.5 py-3">
                  <span className="page-tile page-tile-40" aria-hidden>
                    <Workflow />
                  </span>
                  <div className="min-w-0 flex-1">
                    <b className="block font-semibold">Run history</b>
                    <small className="text-label text-muted-foreground">
                      Last {routineHistory.bars.length} runs of this routine
                      {routineHistory.successPct != null && ` · ${routineHistory.successPct} % completed`}
                    </small>
                    <RunHistoryStrip
                      bars={routineHistory.bars.map((b) => ({
                        id: b.id,
                        tone: runTone(b.status),
                        durationMs: b.durationMs,
                        label: `${new Date(b.startedAt).toLocaleString("en-GB")} · ${b.status}`,
                      }))}
                      onPick={(id) => setPeekId(id)}
                    />
                  </div>
                </div>
              )}

              {listBody}
            </div>
          )}
        </main>
      </div>

      <ActivityPeek
        workspaceId={workspaceId}
        item={record || path.stops.length > 0 ? null : peekItem}
        onClose={() => setPeekId(null)}
        onPrev={flat.length > 1 ? () => step(-1) : undefined}
        onNext={flat.length > 1 ? () => step(1) : undefined}
        now={now}
        role={role}
        routineName={routineName}
        onOpenFull={openFull}
        onPickRun={(id) => setPeekId(id)}
        onShowRoutine={showRoutine}
        onShowIssue={showIssue}
        onOpenRecord={(c) => {
          setPeekId(null)
          setRecord(c.entry)
        }}
        onChanged={() => void chainsQ.refresh()}
      />
    </div>
  )
}
