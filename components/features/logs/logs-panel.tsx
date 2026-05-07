"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { AlertCircle, ChevronLeft, ChevronRight } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"
import { type EntryGroup } from "@/lib/journal-style"
import { annotateEntries, filterEntries, type AnnotatedEntry } from "@/lib/journal-perf"
import { buildMatcher } from "@/lib/log-search"
import { useUserPreference } from "@/hooks/use-user-preference"
import { LogsToolbar, type SeverityFilter, type ScopeControl } from "./logs-toolbar"
import { LogsTypeChips } from "./logs-type-chips"
import { LogsHistogram } from "./logs-histogram"
import type { BucketRange } from "./logs-histogram"
import { LogsList } from "./logs-list"
import { LogsStatsRail } from "./logs-stats-rail"
import type { TimeRange, CustomRange } from "./time-range-picker"
import type { RefreshRate } from "./refresh-rate-picker"

interface LogsPanelProps {
  entries: JournalEntry[]

  // ---- Optional server-driven filters / actions ----
  /** When provided, renders a time-range picker in the toolbar. */
  timeRange?: TimeRange
  onTimeRangeChange?: (r: TimeRange) => void
  customRange?: CustomRange | null
  onCustomRangeChange?: (r: CustomRange) => void

  /** When provided, renders a crew select in the toolbar. */
  crewScope?: ScopeControl
  /** When provided, renders an agent select in the toolbar. */
  agentScope?: ScopeControl

  /** id → display name lookup for resolving UUIDs in the stats rail. */
  agentLookup?: Record<string, string>

  /** Render the admin-only Network card in the stats rail. */
  showNetworkCard?: boolean

  /**
   * Severity filter — controlled. When provided, the toolbar's
   * severity row drives this state via `onSeverityChange` instead of
   * the panel's internal state. Used by /journal so the parent can
   * forward severity to the server-side `severity=` query param,
   * fixing the silent-zero-results bug when client-side filtering
   * runs on top of the 5,000-row buffer cap.
   */
  severity?: SeverityFilter
  onSeverityChange?: (s: SeverityFilter) => void
  /**
   * Muted (clicked-off) groups — controlled when provided. Same
   * server-forwarding rationale as `severity`: muting a group should
   * remove its rows from the SQL result, not just narrow the
   * already-loaded buffer.
   */
  muted?: Set<EntryGroup>
  onMutedChange?: (next: Set<EntryGroup>) => void

  /**
   * Called (debounced) when the user types in the search box. Lets the
   * parent forward the query to the backend's full-text search so the
   * filter sees more than the currently-loaded chunk. Client-side
   * narrowing of the rendered list still runs on top of this.
   */
  onServerSearch?: (q: string) => void

  /** Refresh handler — shows a button + spinner state. */
  onRefresh?: () => void
  /** Mark the panel as loading (spinner on the refresh button). */
  loading?: boolean
  /** Surface a fetch error inline at the top of the list area. */
  error?: string | null

  /** Auto-refresh cadence — when provided, renders a picker in the toolbar. */
  refreshRate?: RefreshRate
  onRefreshRateChange?: (r: RefreshRate) => void

  /**
   * Live-tail state — controlled by the parent so it can also pause the
   * SSE prepend. When omitted, defaults to true and is treated as a
   * scroll-follow flag only (legacy crows-nest behaviour).
   */
  live?: boolean
  onLiveChange?: (live: boolean) => void

  /**
   * Pagination state. The journal page now eager-loads all entries in
   * the active time range, so `onLoadMore` is rarely passed from
   * there — it's still supported for surfaces that want
   * scroll-triggered pagination (e.g. /crows-nest before its merge).
   */
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  /**
   * When set, the loaded buffer hit this cap and older entries beyond
   * it weren't fetched. Footer renders a hint nudging the user to
   * narrow the time range.
   */
  cappedAt?: number

  /**
   * Trace deeplink — when set, the toolbar renders a clear pill so
   * the user knows they're focused on a single run. Calling
   * `onClearTraceId` removes the focus and returns to the full
   * timeline.
   */
  traceId?: string
  onClearTraceId?: () => void

  /**
   * Detail-row jump handlers passed through to LogsList. Wire each one
   * to the corresponding setter on the page (setTraceId, setAgentId,
   * setCrewId) so clicking an ID in an expanded entry zooms the
   * filter to that entity in one tap.
   */
  onSelectTrace?: (traceId: string) => void
  onSelectAgent?: (agentId: string) => void
  onSelectCrew?: (crewId: string) => void
}

/**
 * Grafana Explore-style log viewer.
 *
 * Owns local UI state (search input, severity, muted groups, view toggles,
 * histogram drill-down, expanded rows) and derives every downstream slice
 * from `entries` in a single pass — see `lib/journal-perf.ts`. Server
 * filters (time range, crew/agent, FTS5 search) are wired via optional
 * callbacks so the parent page keeps the source-of-truth for what's
 * actually fetched.
 */
export function LogsPanel({
  entries,
  timeRange,
  onTimeRangeChange,
  customRange,
  onCustomRangeChange,
  crewScope,
  agentScope,
  agentLookup,
  showNetworkCard,
  severity: severityProp,
  onSeverityChange,
  muted: mutedProp,
  onMutedChange,
  onServerSearch,
  onRefresh,
  loading,
  error,
  refreshRate,
  onRefreshRateChange,
  live: liveProp,
  onLiveChange,
  hasMore,
  loadingMore,
  onLoadMore,
  cappedAt,
  traceId,
  onClearTraceId,
  onSelectTrace,
  onSelectAgent,
  onSelectCrew,
}: LogsPanelProps) {
  const [query, setQuery] = useState("")
  // Severity + muted are controlled when the parent passes both the
  // value and the setter. Otherwise we keep local state for legacy
  // surfaces (older standalone uses of LogsPanel).
  const [internalSeverity, setInternalSeverity] = useState<SeverityFilter>("all")
  const [internalMuted, setInternalMuted] = useState<Set<EntryGroup>>(new Set())
  const severity = severityProp ?? internalSeverity
  const muted = mutedProp ?? internalMuted
  const setSeverity = useCallback((next: SeverityFilter) => {
    if (onSeverityChange) onSeverityChange(next)
    else setInternalSeverity(next)
  }, [onSeverityChange])
  const setMuted = useCallback((updater: Set<EntryGroup> | ((prev: Set<EntryGroup>) => Set<EntryGroup>)) => {
    if (onMutedChange) {
      const next = typeof updater === "function" ? updater(muted) : updater
      onMutedChange(next)
      return
    }
    setInternalMuted(updater as React.SetStateAction<Set<EntryGroup>>)
  }, [onMutedChange, muted])
  const [internalLive, setInternalLive] = useState(true)
  // View toggles persist per-user via /api/v1/me/preferences. The
  // localStorage cache means first-paint never flashes the default;
  // server sync keeps the choice consistent across devices.
  const [wrap, setWrap] = useUserPreference<boolean>("journal.timeline.wrap", false)
  const [newestFirst, setNewestFirst] = useUserPreference<boolean>(
    "journal.timeline.newestFirst",
    true,
  )
  const [dedup, setDedup] = useUserPreference<boolean>("journal.timeline.dedup", false)
  const [statsCollapsed, setStatsCollapsed] = useUserPreference<boolean>(
    "journal.timeline.statsCollapsed",
    false,
  )
  // Histogram bucket selection — client-side narrowing of the loaded
  // entries. Kept separate from `timeRange`/`customRange` because
  // changing those triggers a backend re-fetch (slow); a bucket
  // filter is an instant client-side narrow.
  const [bucket, setBucket] = useState<BucketRange | null>(null)
  // Stale buckets from a previous time range would silently filter
  // every entry. Reset whenever the active time window changes.
  useEffect(() => {
    setBucket(null)
  }, [timeRange, customRange])
  const searchInputRef = useRef<HTMLInputElement>(null)

  // `live` is controlled when the parent passes both the value and a
  // setter — that's the journal/crows-nest case where SSE prepend needs
  // to know to pause. Without those, fall back to internal scroll-follow.
  const live = liveProp ?? internalLive
  const onLiveToggle = useCallback(() => {
    if (onLiveChange) onLiveChange(!live)
    else setInternalLive((v) => !v)
  }, [live, onLiveChange])

  // Debounced server search — 300 ms keeps typing latency invisible.
  useEffect(() => {
    if (!onServerSearch) return
    const t = setTimeout(() => onServerSearch(query), 300)
    return () => clearTimeout(t)
  }, [query, onServerSearch])

  // Pre-attach _tsMs once. Cheap to call repeatedly: the helper
  // short-circuits on entries that already carry the field.
  const annotated = useMemo<AnnotatedEntry[]>(() => annotateEntries(entries), [entries])
  const matcher = useMemo(() => buildMatcher(query), [query])

  // One pass for severity counts, group counts, filtered, bucketed.
  const stage = useMemo(
    () => filterEntries(annotated, { severity, matcher, muted, bucket }),
    [annotated, severity, matcher, muted, bucket],
  )

  // Sort + optional adjacent dedup happen on the bucketed slice — these
  // are list-presentation concerns, kept out of `filterEntries` so the
  // histogram still gets the unsorted, un-deduped stream.
  const ordered = useMemo<AnnotatedEntry[]>(() => {
    const arr = stage.bucketed.slice()
    arr.sort((a, b) => (newestFirst ? b._tsMs - a._tsMs : a._tsMs - b._tsMs))
    if (!dedup) return arr
    const out: AnnotatedEntry[] = []
    for (const e of arr) {
      const prev = out[out.length - 1]
      if (prev && prev.entry_type === e.entry_type && prev.summary === e.summary) continue
      out.push(e)
    }
    return out
  }, [stage.bucketed, newestFirst, dedup])

  const onToggleGroup = useCallback((g: EntryGroup) => {
    setMuted((prev) => {
      const next = new Set(prev)
      if (next.has(g)) next.delete(g)
      else next.add(g)
      return next
    })
  }, [setMuted])

  const onResetGroups = useCallback(() => setMuted(new Set()), [setMuted])

  const onClearAllFilters = useCallback(() => {
    setQuery("")
    setSeverity("all")
    setMuted(new Set())
    setBucket(null)
  }, [setSeverity, setMuted])

  const onExport = useCallback(() => {
    const blob = new Blob([JSON.stringify(ordered, null, 2)], { type: "application/json" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `crewship-logs-${new Date().toISOString().slice(0, 19).replace(/[:.]/g, "-")}.json`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  }, [ordered])

  // Pagination guard — don't fire onLoadMore while one is in flight.
  const loadMoreInFlight = useRef(false)
  useEffect(() => {
    if (!loadingMore) loadMoreInFlight.current = false
  }, [loadingMore])
  const handleEndReached = useCallback(() => {
    if (!onLoadMore || !hasMore || loadingMore || loadMoreInFlight.current) return
    loadMoreInFlight.current = true
    onLoadMore()
  }, [onLoadMore, hasMore, loadingMore])

  // Keyboard shortcuts at the panel level. `/` focuses search, Esc
  // clears all filters (or blurs the input if it has focus), Cmd+L
  // toggles live tail. Bypassed when the user is typing in another
  // input outside the panel.
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      const target = ev.target as HTMLElement | null
      const inEditable =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        (target?.isContentEditable ?? false)

      if (ev.key === "/" && !inEditable) {
        ev.preventDefault()
        searchInputRef.current?.focus()
        searchInputRef.current?.select()
        return
      }
      if (ev.key === "Escape") {
        if (target === searchInputRef.current) {
          if (query) {
            setQuery("")
          } else {
            ;(target as HTMLInputElement).blur()
          }
          return
        }
        if (!inEditable) {
          onClearAllFilters()
        }
        return
      }
      if ((ev.key === "l" || ev.key === "L") && (ev.metaKey || ev.ctrlKey) && !inEditable) {
        ev.preventDefault()
        onLiveToggle()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [query, onClearAllFilters, onLiveToggle])

  const hasAnyEntries = entries.length > 0
  const hasFilters =
    severity !== "all" || muted.size > 0 || query.trim().length > 0 || bucket !== null
  const visibleCount = ordered.length
  const totalCount = entries.length

  return (
    <div className="flex flex-col h-full min-h-0">
      <LogsToolbar
        query={query}
        onQueryChange={setQuery}
        searchInputRef={searchInputRef}
        severity={severity}
        onSeverityChange={setSeverity}
        counts={stage.sevCounts}
        visibleCount={visibleCount}
        totalCount={totalCount}
        live={live}
        onLiveToggle={onLiveToggle}
        wrap={wrap}
        onWrapToggle={() => setWrap(!wrap)}
        newestFirst={newestFirst}
        onNewestToggle={() => setNewestFirst(!newestFirst)}
        dedup={dedup}
        onDedupToggle={() => setDedup(!dedup)}
        onExport={onExport}
        timeRange={timeRange}
        onTimeRangeChange={onTimeRangeChange}
        customRange={customRange}
        onCustomRangeChange={onCustomRangeChange}
        crewScope={crewScope}
        agentScope={agentScope}
        onRefresh={onRefresh}
        loading={loading}
        bucketFilter={bucket}
        onClearBucketFilter={() => setBucket(null)}
        refreshRate={refreshRate}
        onRefreshRateChange={onRefreshRateChange}
        traceId={traceId}
        onClearTraceId={onClearTraceId}
      />
      <LogsTypeChips
        counts={stage.groupCounts}
        muted={muted}
        onToggle={onToggleGroup}
        onResetAll={onResetGroups}
      />
      <LogsHistogram
        entries={stage.filtered}
        timeRange={timeRange}
        customRange={customRange ?? null}
        selected={bucket}
        onSelect={setBucket}
      />
      <AnimatePresence>
        {error && (
          <motion.div
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            exit={{ opacity: 0, height: 0 }}
            transition={{ duration: 0.2, ease: "easeOut" }}
            className="px-3 py-2 border-b border-border/50 bg-red-500/10 text-red-300 text-[11px] flex items-center gap-2 shrink-0 overflow-hidden"
          >
            <AlertCircle className="h-3.5 w-3.5 shrink-0" />
            <span>{error}</span>
            {onRefresh && (
              <button
                type="button"
                onClick={onRefresh}
                className="ml-auto underline-offset-2 hover:underline"
              >
                Retry
              </button>
            )}
          </motion.div>
        )}
      </AnimatePresence>
      <div
        className="flex-1 min-h-0 grid"
        style={{ gridTemplateColumns: statsCollapsed ? "minmax(0,1fr) 28px" : "minmax(0,1fr) 280px" }}
      >
        <div className="border-r border-border/50 min-h-0 overflow-hidden flex flex-col">
          {visibleCount === 0 ? (
            <EmptyState
              hasAnyEntries={hasAnyEntries}
              hasFilters={hasFilters}
              loading={Boolean(loading)}
              onClearFilters={hasFilters ? onClearAllFilters : undefined}
            />
          ) : (
            <>
              <div className="flex-1 min-h-0">
                <LogsList
                  entries={ordered}
                  wrap={wrap}
                  followTail={live}
                  newestFirst={newestFirst}
                  onEndReached={handleEndReached}
                  onSelectTrace={onSelectTrace}
                  onSelectAgent={onSelectAgent}
                  onSelectCrew={onSelectCrew}
                />
              </div>
              {(loadingMore || (hasMore === false && visibleCount > 0) || cappedAt) && (
                <div className="shrink-0 px-3 py-2 text-center text-[10px] font-mono uppercase tracking-wider text-muted-foreground/60 border-t border-border/40">
                  {loadingMore
                    ? "Loading more…"
                    : cappedAt
                      ? `Showing first ${cappedAt.toLocaleString()} — narrow the time range to see more`
                      : "End of journal"}
                </div>
              )}
            </>
          )}
        </div>
        {statsCollapsed ? (
          <button
            type="button"
            onClick={() => setStatsCollapsed(false)}
            className="border-l border-border/50 bg-background hover:bg-card flex items-center justify-center text-muted-foreground hover:text-foreground"
            title="Expand stats rail"
            aria-label="Expand stats rail"
          >
            <ChevronLeft className="h-3.5 w-3.5" />
          </button>
        ) : (
          <div className="relative">
            <button
              type="button"
              onClick={() => setStatsCollapsed(true)}
              className="absolute top-2 left-2 z-10 h-5 w-5 inline-flex items-center justify-center rounded border border-border/60 bg-card text-muted-foreground hover:text-foreground hover:bg-accent/40"
              title="Collapse stats rail"
              aria-label="Collapse stats rail"
            >
              <ChevronRight className="h-3 w-3" />
            </button>
            <LogsStatsRail
              entries={ordered}
              agentLookup={agentLookup}
              showNetworkCard={showNetworkCard}
            />
          </div>
        )}
      </div>
    </div>
  )
}

function EmptyState({
  hasAnyEntries,
  hasFilters,
  loading,
  onClearFilters,
}: {
  hasAnyEntries: boolean
  hasFilters: boolean
  loading: boolean
  onClearFilters?: () => void
}) {
  if (loading) {
    return (
      <motion.div
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: 0.2 }}
        className="h-full flex items-center justify-center text-[12px] text-muted-foreground/60"
      >
        Loading entries…
      </motion.div>
    )
  }
  if (!hasAnyEntries) {
    return (
      <motion.div
        initial={{ opacity: 0, y: 6 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.25, ease: "easeOut" }}
        className="h-full flex flex-col items-center justify-center gap-1 text-center px-6"
      >
        <div className="text-[12px] text-foreground/80">No journal entries</div>
        <div className="text-[11px] text-muted-foreground/70 max-w-sm">
          Once the crew runs, events will land here in real time.
        </div>
      </motion.div>
    )
  }
  if (hasFilters) {
    return (
      <motion.div
        initial={{ opacity: 0, y: 6 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.25, ease: "easeOut" }}
        className="h-full flex flex-col items-center justify-center gap-2 text-center px-6"
      >
        <div className="text-[12px] text-foreground/80">No entries match the current filters</div>
        <div className="text-[11px] text-muted-foreground/70 max-w-sm">
          Adjust severity, type chips, search, or histogram selection to widen the view.
        </div>
        {onClearFilters && (
          <motion.button
            type="button"
            onClick={onClearFilters}
            whileHover={{ scale: 1.04 }}
            whileTap={{ scale: 0.96 }}
            className="mt-1 inline-flex items-center gap-1 h-6 px-2 rounded border border-sky-500/40 bg-sky-500/10 text-[10px] text-sky-300 hover:bg-sky-500/20"
          >
            Clear all filters
            <span className="opacity-60 font-mono">Esc</span>
          </motion.button>
        )}
      </motion.div>
    )
  }
  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.2 }}
      className="h-full flex items-center justify-center text-[12px] text-muted-foreground/60 italic"
    >
      No log entries.
    </motion.div>
  )
}
