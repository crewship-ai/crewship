"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import type { JournalEntry } from "@/lib/types/journal"
import { groupOf, type EntryGroup, GROUP_ORDER } from "@/lib/journal-style"
import { LogsToolbar, type SeverityFilter, type SeverityCounts, type ScopeControl } from "./logs-toolbar"
import { LogsTypeChips } from "./logs-type-chips"
import { LogsHistogram, type BucketRange } from "./logs-histogram"
import { LogsList } from "./logs-list"
import { LogsStatsRail } from "./logs-stats-rail"
import type { TimeRange } from "./time-range-picker"

interface LogsPanelProps {
  entries: JournalEntry[]

  // ---- Optional server-driven filters / actions ----
  /** When provided, renders a time-range picker in the toolbar. */
  timeRange?: TimeRange
  onTimeRangeChange?: (r: TimeRange) => void

  /** When provided, renders a crew select in the toolbar. */
  crewScope?: ScopeControl
  /** When provided, renders an agent select in the toolbar. */
  agentScope?: ScopeControl

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

  /** Pagination — called when the user scrolls to the bottom of the list. */
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
}

/**
 * Grafana Explore-style log viewer.
 *
 * Owns the local UI state (search input, severity, muted groups, view
 * toggles) and derives every downstream slice from `entries`. Server
 * filters (time range, crew/agent, full-text search) are wired via
 * optional callbacks so the parent page can keep the source-of-truth
 * for what's actually fetched.
 */
export function LogsPanel({
  entries,
  timeRange,
  onTimeRangeChange,
  crewScope,
  agentScope,
  onServerSearch,
  onRefresh,
  loading,
  hasMore,
  loadingMore,
  onLoadMore,
}: LogsPanelProps) {
  const [query, setQuery] = useState("")
  const [severity, setSeverity] = useState<SeverityFilter>("all")
  const [muted, setMuted] = useState<Set<EntryGroup>>(new Set())
  const [live, setLive] = useState(true)
  const [wrap, setWrap] = useState(false)
  const [newestFirst, setNewestFirst] = useState(true)
  const [dedup, setDedup] = useState(false)
  // Histogram drill-down — when set, narrows the rendered list to the
  // clicked bucket. Reset whenever the active time range changes so a
  // stale bucket from a previous window doesn't silently filter
  // everything out.
  const [bucket, setBucket] = useState<BucketRange | null>(null)
  useEffect(() => {
    setBucket(null)
  }, [timeRange])

  // Debounce the search → server callback. 300 ms matches the prior
  // JournalFilters debounce and keeps typing latency invisible.
  useEffect(() => {
    if (!onServerSearch) return
    const t = setTimeout(() => onServerSearch(query), 300)
    return () => clearTimeout(t)
  }, [query, onServerSearch])

  const matcher = useMemo(() => buildMatcher(query), [query])

  // Stage 1: severity + search filter (used for type-chip counts).
  const sevSearchFiltered = useMemo(() => {
    if (severity === "all" && !matcher) return entries
    return entries.filter((e) => {
      if (severity !== "all" && e.severity !== severity) return false
      if (matcher && !matcher(e)) return false
      return true
    })
  }, [entries, severity, matcher])

  const groupCounts = useMemo(() => {
    const c: Record<EntryGroup, number> = Object.fromEntries(
      GROUP_ORDER.map((g) => [g, 0]),
    ) as Record<EntryGroup, number>
    for (const e of sevSearchFiltered) {
      c[groupOf(e.entry_type)] += 1
    }
    return c
  }, [sevSearchFiltered])

  // Stage 2: apply muted groups.
  const filtered = useMemo(() => {
    if (muted.size === 0) return sevSearchFiltered
    return sevSearchFiltered.filter((e) => !muted.has(groupOf(e.entry_type)))
  }, [sevSearchFiltered, muted])

  // Stage 2.5: histogram bucket narrowing. Histogram still receives
  // `filtered` (the unconstrained set) so the selected bucket is
  // visible in context — only the list/stats see the narrower slice.
  const bucketed = useMemo(() => {
    if (!bucket) return filtered
    return filtered.filter((e) => {
      const ts = new Date(e.ts).getTime()
      return ts >= bucket.fromMs && ts < bucket.toMs
    })
  }, [filtered, bucket])

  // Stage 3: order + dedup.
  const ordered = useMemo(() => {
    const arr = bucketed.slice()
    arr.sort((a, b) => {
      const t = new Date(b.ts).getTime() - new Date(a.ts).getTime()
      return newestFirst ? t : -t
    })
    if (!dedup) return arr
    const out: JournalEntry[] = []
    for (const e of arr) {
      const prev = out[out.length - 1]
      if (prev && prev.entry_type === e.entry_type && prev.summary === e.summary) continue
      out.push(e)
    }
    return out
  }, [bucketed, newestFirst, dedup])

  const severityCounts = useMemo<SeverityCounts>(() => {
    const c: SeverityCounts = { all: 0, info: 0, notice: 0, warn: 0, error: 0 }
    const base = matcher ? entries.filter(matcher) : entries
    c.all = base.length
    for (const e of base) {
      const s = e.severity
      if (s === "info" || s === "notice" || s === "warn" || s === "error") c[s] += 1
    }
    return c
  }, [entries, matcher])

  const onToggleGroup = useCallback((g: EntryGroup) => {
    setMuted((prev) => {
      const next = new Set(prev)
      if (next.has(g)) next.delete(g)
      else next.add(g)
      return next
    })
  }, [])

  const onResetGroups = useCallback(() => setMuted(new Set()), [])

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

  const hasAnyEntries = entries.length > 0
  const hasFilters =
    severity !== "all" || muted.size > 0 || query.trim().length > 0

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

  return (
    <div className="flex flex-col h-full min-h-0">
      <LogsToolbar
        query={query}
        onQueryChange={setQuery}
        severity={severity}
        onSeverityChange={setSeverity}
        counts={severityCounts}
        visibleCount={ordered.length}
        totalCount={entries.length}
        live={live}
        onLiveToggle={() => setLive((v) => !v)}
        wrap={wrap}
        onWrapToggle={() => setWrap((v) => !v)}
        newestFirst={newestFirst}
        onNewestToggle={() => setNewestFirst((v) => !v)}
        dedup={dedup}
        onDedupToggle={() => setDedup((v) => !v)}
        onExport={onExport}
        timeRange={timeRange}
        onTimeRangeChange={onTimeRangeChange}
        crewScope={crewScope}
        agentScope={agentScope}
        onRefresh={onRefresh}
        loading={loading}
      />
      <LogsTypeChips
        counts={groupCounts}
        muted={muted}
        onToggle={onToggleGroup}
        onResetAll={onResetGroups}
      />
      <LogsHistogram
        entries={filtered}
        timeRange={timeRange}
        selected={bucket}
        onSelect={setBucket}
      />
      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "minmax(0,1fr) 280px" }}>
        <div className="border-r border-border/50 min-h-0 overflow-hidden flex flex-col">
          {ordered.length === 0 ? (
            <EmptyState
              hasAnyEntries={hasAnyEntries}
              hasFilters={hasFilters}
              loading={Boolean(loading)}
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
                />
              </div>
              {(loadingMore || (hasMore === false && ordered.length > 0)) && (
                <div className="shrink-0 px-3 py-2 text-center text-[10px] font-mono uppercase tracking-wider text-muted-foreground/60 border-t border-border/40">
                  {loadingMore ? "Loading older entries…" : "End of journal"}
                </div>
              )}
            </>
          )}
        </div>
        <LogsStatsRail entries={ordered} />
      </div>
    </div>
  )
}

function EmptyState({
  hasAnyEntries,
  hasFilters,
  loading,
}: {
  hasAnyEntries: boolean
  hasFilters: boolean
  loading: boolean
}) {
  if (loading) {
    return (
      <div className="h-full flex items-center justify-center text-[12px] text-muted-foreground/60">
        Loading entries…
      </div>
    )
  }
  if (!hasAnyEntries) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-1 text-center px-6">
        <div className="text-[12px] text-foreground/80">No journal entries</div>
        <div className="text-[11px] text-muted-foreground/70 max-w-sm">
          Once the crew runs, events will land here in real time.
        </div>
      </div>
    )
  }
  if (hasFilters) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-1 text-center px-6">
        <div className="text-[12px] text-foreground/80">No entries match the current filters</div>
        <div className="text-[11px] text-muted-foreground/70 max-w-sm">
          Adjust severity, type chips, or clear the search to widen the view.
        </div>
      </div>
    )
  }
  return (
    <div className="h-full flex items-center justify-center text-[12px] text-muted-foreground/60 italic">
      No log entries.
    </div>
  )
}

/**
 * Build a predicate from the search string. Supports:
 *   /pattern/        → case-insensitive regex on summary + entry_type
 *   path:/some/path  → substring match on payload.path
 *   foo bar          → all-tokens substring match on summary + entry_type
 */
function buildMatcher(q: string): ((e: JournalEntry) => boolean) | null {
  const trimmed = q.trim()
  if (!trimmed) return null

  const rx = trimmed.match(/^\/(.+)\/([imsx]*)$/)
  if (rx) {
    try {
      const re = new RegExp(rx[1], rx[2] || "i")
      return (e) => re.test(e.summary || "") || re.test(e.entry_type)
    } catch {
      // fall through to plain text on invalid regex
    }
  }

  const tokens = trimmed.split(/\s+/)
  const kv: Array<[string, string]> = []
  const free: string[] = []
  for (const t of tokens) {
    const m = t.match(/^([a-z_]+):(.+)$/i)
    if (m) kv.push([m[1].toLowerCase(), m[2].toLowerCase()])
    else free.push(t.toLowerCase())
  }

  return (e) => {
    const hay = `${e.summary || ""} ${e.entry_type}`.toLowerCase()
    for (const tok of free) {
      if (!hay.includes(tok)) return false
    }
    if (kv.length > 0) {
      for (const [k, v] of kv) {
        const value = readField(e, k)
        if (!value || !String(value).toLowerCase().includes(v)) return false
      }
    }
    return true
  }
}

function readField(e: JournalEntry, k: string): unknown {
  switch (k) {
    case "type": return e.entry_type
    case "sev": case "severity": return e.severity
    case "agent": case "agent_id": return e.agent_id
    case "crew": case "crew_id": return e.crew_id
    case "mission": case "mission_id": return e.mission_id
    case "trace": case "trace_id": return e.trace_id
    default: return e.payload ? e.payload[k] : undefined
  }
}
