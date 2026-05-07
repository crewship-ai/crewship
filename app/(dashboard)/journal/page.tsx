"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import {
  Activity,
  BookOpen,
  DollarSign,
  ListOrdered,
  Lock,
  Radio,
  RadioTower,
  Zap,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspace } from "@/hooks/use-workspace"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { RunsView } from "@/components/features/journal/runs-view"
import { LogsPanel } from "@/components/features/logs/logs-panel"
import { ResourcesStrip } from "@/components/features/logs/resources-strip"
import { sinceFromTimeRange, type CustomRange, type TimeRange } from "@/components/features/logs/time-range-picker"
import { refreshRateMs, type RefreshRate } from "@/components/features/logs/refresh-rate-picker"
import type { ScopeOption, SeverityFilter } from "@/components/features/logs/logs-toolbar"
import { GROUP_ORDER, type EntryGroup } from "@/lib/journal-style"
import { entryTypesForGroups } from "@/lib/journal-groups"
import { parseStructuredQuery } from "@/lib/log-search"

/**
 * Cap on entries kept in memory. Generous enough to hold a full
 * Grafana-style "show me everything in the time range" window for
 * busy workspaces; small enough to keep the filter chain + virtuoso
 * + histogram + stats rail all responsive on a laptop.
 */
const JOURNAL_MAX_ENTRIES = 5000

interface CrewSummary {
  id: string
  name: string
  icon?: string | null
  color?: string | null
  avatar_style?: string | null
}
interface AgentSummary {
  id: string
  name: string
  crew_id?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { avatar_style?: string | null } | null
}

type JournalTab = "timeline" | "runs" | "spend"

interface TabDef {
  id: JournalTab
  label: string
  icon: typeof ListOrdered
  /** When true, only OWNER/ADMIN see the tab. */
  adminOnly?: boolean
  /**
   * Locked tabs render with a "Soon" badge and a lock icon, and the
   * click handler is a no-op so activeTab never lands on them.
   */
  locked?: boolean
}

const ALL_TABS: TabDef[] = [
  { id: "timeline", label: "Timeline", icon: ListOrdered },
  { id: "runs", label: "Runs", icon: Activity },
  { id: "spend", label: "Spend", icon: DollarSign, adminOnly: true, locked: true },
]

/**
 * Crew Journal — workspace-wide records center.
 *
 *   - Timeline: runtime events from `journal_entries` (Grafana-style)
 *   - Runs:     agent run aggregates derived from journal `run.*` entries
 *   - Spend:    cost ledger surface — currently locked behind a "Soon"
 *               badge until LLM-cost attribution is prioritized.
 *
 * Audit log moved to `/settings?tab=audit` (admin compliance view).
 * Eval / quartermaster replay surface was removed; the backend emit
 * machinery in `internal/quartermaster/` is preserved as a nice-to-have
 * to revisit when there are >=2 production missions worth comparing.
 *
 * Per-tab RBAC hides admin-only tabs entirely from non-admins so they
 * never see a "click here for 403" affordance.
 */
export default function JournalPage() {
  const searchParams = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { role, loading: rolesLoading } = useAbilities()
  const isAdmin = role === "OWNER" || role === "ADMIN"

  // Visible tabs depends on role. Admin-only tabs are filtered out for
  // non-admins. The deeplink defaults to timeline if the user lacks
  // access to the requested tab.
  const visibleTabs = useMemo<TabDef[]>(
    () => ALL_TABS.filter((t) => !t.adminOnly || isAdmin),
    [isAdmin],
  )

  // Filter state — lifted from the page so URL/deeplink hydration is
  // trivial and so the LogsPanel toolbar can drive the backend query.
  const initialTimeRange = useMemo<TimeRange>(() => {
    const t = searchParams.get("time")
    return (t === "5m" || t === "15m" || t === "1h" || t === "24h" || t === "7d" || t === "30d" || t === "all" || t === "custom")
      ? (t as TimeRange)
      : "24h"
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const initialCustomRange = useMemo<CustomRange | null>(() => {
    const f = Number(searchParams.get("from"))
    const t = Number(searchParams.get("to"))
    if (Number.isFinite(f) && Number.isFinite(t) && t > f) return { fromMs: f, toMs: t }
    return null
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const [timeRange, setTimeRange] = useState<TimeRange>(initialTimeRange)
  const [customRange, setCustomRange] = useState<CustomRange | null>(initialCustomRange)
  const [crewId, setCrewId] = useState<string>(() => searchParams.get("crew_id") ?? "")
  const [agentId, setAgentId] = useState<string>(() => searchParams.get("agent_id") ?? "")
  const [traceId, setTraceId] = useState<string>(() => searchParams.get("trace_id") ?? "")
  const [serverQuery, setServerQuery] = useState<string>("")
  // Severity + muted-groups are LIFTED out of LogsPanel so we can mirror
  // them as server-side filters. The previous client-only filtering
  // silently dropped matches when the 5,000-entry buffer cap kicked in
  // — muting "container" might leave zero events visible because the
  // server already returned the most recent 5k container.metrics rows.
  const initialSeverity = useMemo<SeverityFilter>(() => {
    const s = searchParams.get("severity")
    return (s === "info" || s === "notice" || s === "warn" || s === "error" ? s : "all") as SeverityFilter
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const initialMuted = useMemo<Set<EntryGroup>>(() => {
    const raw = searchParams.get("mute")
    if (!raw) return new Set<EntryGroup>()
    const set = new Set<EntryGroup>()
    for (const g of raw.split(",")) {
      const trimmed = g.trim() as EntryGroup
      if ((GROUP_ORDER as readonly string[]).includes(trimmed)) set.add(trimmed)
    }
    return set
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const [severity, setSeverity] = useState<SeverityFilter>(initialSeverity)
  const [muted, setMuted] = useState<Set<EntryGroup>>(initialMuted)
  const [live, setLive] = useState(true)
  // Auto-refresh cadence — defaults to "live" (SSE-driven, no polling)
  // so we don't load the backend with redundant requests when a
  // working stream is already pushing events. Pollers (5s/10s/…) are
  // additive on top of SSE for users who want a hard freshness floor.
  const [refreshRate, setRefreshRate] = useState<RefreshRate>("live")

  // Initial tab from `?tab=`. Unknown / unauthorized values fall back
  // to timeline so a stale bookmark can never surface a 403.
  const initialTab = useMemo<JournalTab>(() => {
    const t = searchParams.get("tab")
    const valid = ALL_TABS.some((tab) => tab.id === t)
    if (!valid) return "timeline"
    return t as JournalTab
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const [activeTab, setActiveTab] = useState<JournalTab>(initialTab)

  // If admin permissions resolve to non-admin OR a deeplink lands on a
  // locked tab, demote back to timeline. Locked check fires regardless
  // of role so even admins can't snap into an unimplemented surface via
  // a stale bookmark.
  useEffect(() => {
    if (rolesLoading) return
    const tabDef = ALL_TABS.find((t) => t.id === activeTab)
    if (!tabDef || tabDef.locked || (tabDef.adminOnly && !isAdmin)) {
      setActiveTab("timeline")
    }
  }, [activeTab, isAdmin, rolesLoading])

  // Crew + agent options for the toolbar selects.
  const [crews, setCrews] = useState<ScopeOption[]>([])
  const [agents, setAgents] = useState<ScopeOption[]>([])

  useEffect(() => {
    if (!workspaceId) {
      setCrews([])
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (!res.ok) return
        const json = (await res.json()) as CrewSummary[]
        if (!cancelled && Array.isArray(json)) {
          setCrews(
            json.map((c) => ({
              id: c.id,
              name: c.name,
              icon: c.icon ?? null,
              color: c.color ?? null,
            })),
          )
        }
      } catch {
        /* leave empty on failure */
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) {
      setAgents([])
      return
    }
    let cancelled = false
    const url = crewId
      ? `/api/v1/agents?workspace_id=${workspaceId}&crew_id=${crewId}`
      : `/api/v1/agents?workspace_id=${workspaceId}`
    ;(async () => {
      try {
        const res = await fetch(url)
        if (!res.ok) return
        const json = (await res.json()) as AgentSummary[]
        if (!cancelled && Array.isArray(json)) {
          setAgents(
            json.map((a) => ({
              id: a.id,
              name: a.name,
              avatarSeed: a.avatar_seed ?? null,
              avatarStyle: a.avatar_style ?? a.crew?.avatar_style ?? null,
            })),
          )
        }
      } catch {
        /* leave empty on failure */
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId, crewId])

  // Crew change clears any agent selection that's no longer in scope.
  const onCrewChange = useCallback((id: string) => {
    setCrewId(id)
    setAgentId("")
  }, [])

  // id → name lookup so the LogsPanel stats rail can render "viktor"
  // instead of a UUID.
  const agentLookup = useMemo<Record<string, string>>(() => {
    const out: Record<string, string> = {}
    for (const a of agents) out[a.id] = a.name
    return out
  }, [agents])

  // Deeplink — mirror filter state into URL search params on change so
  // the user can share / bookmark a specific view. Severity + muted
  // groups are part of the filter contract: a saved bookmark must
  // restore the exact view, not just the time range.
  useEffect(() => {
    const sp = new URLSearchParams()
    if (timeRange !== "24h") sp.set("time", timeRange)
    if (timeRange === "custom" && customRange) {
      sp.set("from", String(customRange.fromMs))
      sp.set("to", String(customRange.toMs))
    }
    if (crewId) sp.set("crew_id", crewId)
    if (agentId) sp.set("agent_id", agentId)
    if (traceId) sp.set("trace_id", traceId)
    if (severity !== "all") sp.set("severity", severity)
    if (muted.size > 0) sp.set("mute", Array.from(muted).join(","))
    if (activeTab !== "timeline") sp.set("tab", activeTab)
    const qs = sp.toString()
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false })
  }, [timeRange, customRange, crewId, agentId, traceId, severity, muted, activeTab, router, pathname])

  // Structured-query split: tokens like `agent:viktor severity:error
  // type:exec.command` get peeled off the search box and routed to
  // server-side query params instead of being narrowed client-side over
  // the 5,000-row buffer. Free text + payload keys (`payload.foo:bar`)
  // stay in clientSearchQuery and feed LogsPanel's local matcher.
  const structured = useMemo(() => parseStructuredQuery(serverQuery), [serverQuery])

  const queryParams = useMemo<Record<string, string | undefined>>(() => {
    const since = sinceFromTimeRange(timeRange, customRange)
    const until = timeRange === "custom" && customRange
      ? new Date(customRange.toMs).toISOString()
      : undefined
    // Server-side severity: skip when "all" so we don't bind a filter.
    const severityParam = severity === "all" ? undefined : severity
    // Server-side mute → exclude_entry_type. "other" can't be expanded
    // server-side (its membership is the complement of every known
    // type) so it remains client-only — entryTypesForGroups handles
    // that gracefully by returning [] for "other".
    const excludeTypes = entryTypesForGroups(muted)
    // structured.serverParams takes precedence over scope-level
    // filters when both are set so the user can use the search box to
    // pin one specific agent/crew/trace without first clearing the
    // toolbar selects. trace_id from URL still wins over an
    // unstructured token if both somehow coexist.
    return {
      crew_id: structured.serverParams.crew_id ?? (crewId || undefined),
      agent_id: structured.serverParams.agent_id ?? (agentId || undefined),
      trace_id: traceId || structured.serverParams.trace_id || undefined,
      entry_type: structured.serverParams.entry_type,
      severity: structured.serverParams.severity ?? severityParam,
      actor_type: structured.serverParams.actor_type,
      priority: structured.serverParams.priority,
      exclude_entry_type: excludeTypes.length > 0 ? excludeTypes.join(",") : undefined,
      q: structured.clientQuery.trim() || undefined,
      since,
      until,
    }
  }, [timeRange, customRange, crewId, agentId, traceId, severity, muted, structured])

  // Only the Timeline tab consumes the journal list + SSE stream.
  const timelineEnabled = !wsLoading && activeTab === "timeline"
  const { entries, nextCursor, loading, loadingMore, error, refresh, loadMore, prependLive } =
    useJournalList({
      workspaceId,
      params: queryParams,
      enabled: timelineEnabled,
      maxEntries: JOURNAL_MAX_ENTRIES,
    })

  const liveRef = useRef(live)
  useEffect(() => { liveRef.current = live }, [live])

  // SSE prepend batching — chatty workspaces can fire 50+ events/sec.
  // Without batching, each event triggers a state update + full filter
  // chain re-render (toolbar counts, histogram 60 buckets, virtuoso
  // viewport, stats rail). Buffer up to 250 ms and flush as a group.
  const pendingRef = useRef<Parameters<typeof prependLive>[0][]>([])
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const flushPending = useCallback(() => {
    flushTimerRef.current = null
    const batch = pendingRef.current
    if (batch.length === 0) return
    pendingRef.current = []
    for (const e of batch) prependLive(e)
  }, [prependLive])
  useEffect(() => {
    return () => {
      if (flushTimerRef.current) clearTimeout(flushTimerRef.current)
    }
  }, [])

  const handleLive = useCallback(
    (entry: Parameters<typeof prependLive>[0]) => {
      if (!liveRef.current) return
      pendingRef.current.push(entry)
      if (!flushTimerRef.current) {
        flushTimerRef.current = setTimeout(flushPending, 250)
      }
    },
    [flushPending],
  )
  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params: queryParams,
    enabled: timelineEnabled,
    onEntry: handleLive,
  })

  const handleRefresh = useCallback(() => { void refresh() }, [refresh])

  // Periodic auto-refresh — only when the user has explicitly opted
  // in via the picker. "live" / "off" → no timer.
  useEffect(() => {
    if (!timelineEnabled) return
    const ms = refreshRateMs(refreshRate)
    if (ms === null) return
    const id = setInterval(() => {
      if (!liveRef.current) return // paused live tail → don't auto-refresh either
      void refresh()
    }, ms)
    return () => clearInterval(id)
  }, [timelineEnabled, refreshRate, refresh])

  // Eager pagination — once the initial fetch lands, keep walking the
  // cursor until the backend reports no more pages OR we hit the
  // in-memory cap. This is the Elastic Discover / Grafana Logs
  // behaviour: the time-range select determines what the user sees,
  // not a scroll position. Scroll-triggered loadMore is intentionally
  // not wired so the list stays "what's in the window" and nothing
  // sneaks in mid-scroll.
  useEffect(() => {
    if (!timelineEnabled) return
    if (loading || loadingMore) return
    if (!nextCursor) return
    if (entries.length >= JOURNAL_MAX_ENTRIES) return
    void loadMore()
  }, [timelineEnabled, loading, loadingMore, nextCursor, entries.length, loadMore])

  // Stats-rail Network card — admin-only and only meaningful when a single
  // crew is in scope (metrics are per-container).
  const showNetworkCard = isAdmin && Boolean(crewId)

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Header strip ---- */}
      <div className="shrink-0 flex items-center h-9 bg-card border-b border-border/60 px-3 gap-2">
        <BookOpen className="h-3.5 w-3.5 text-foreground/60" />
        <h1 className="text-body font-medium text-foreground/80">Crew Journal</h1>
        {activeTab === "timeline" && (
          <Badge variant="outline" className="text-[10px] border-border/60 font-mono">
            {entries.length} loaded
          </Badge>
        )}
        {activeTab === "timeline" && <StreamStatusBadge status={streamStatus} />}
        {activeTab === "timeline" && (
          <AnomalyBadge
            entries={entries}
            onClick={() => {
              setSeverity("error")
              setTimeRange("5m")
            }}
          />
        )}
        <div className="flex-1" />
      </div>

      {/* ---- Tab strip ---- */}
      <div
        role="tablist"
        aria-label="Journal views"
        className="shrink-0 flex items-center h-9 bg-card border-b border-border/60 px-2 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]"
      >
        {visibleTabs.map(({ id, label, icon: Icon, adminOnly, locked }) => {
          const isActive = activeTab === id
          return (
            <motion.button
              key={id}
              role="tab"
              aria-selected={isActive}
              aria-disabled={locked || undefined}
              onClick={() => {
                // Locked tabs never accept activation. Clicks fall
                // through to the cursor-not-allowed style so the user
                // sees the tooltip but the URL/state stays put.
                if (locked) return
                setActiveTab(id)
              }}
              whileHover={locked ? undefined : { y: -1 }}
              whileTap={locked ? undefined : { y: 0, scale: 0.97 }}
              transition={{ duration: 0.12 }}
              title={locked ? `${label} — coming soon` : undefined}
              className={cn(
                "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-colors duration-100 relative top-px whitespace-nowrap shrink-0",
                locked
                  ? "border-transparent text-muted-foreground/40 cursor-not-allowed"
                  : isActive
                    ? "border-blue-400 text-blue-400"
                    : "border-transparent text-muted-foreground hover:text-foreground/80",
              )}
            >
              <Icon className="h-3 w-3 opacity-75" />
              {label}
              {locked && (
                <>
                  <Lock className="h-2.5 w-2.5 opacity-60" />
                  <span className="text-[9px] uppercase tracking-wider text-amber-400/70 font-mono">
                    soon
                  </span>
                </>
              )}
              {adminOnly && !locked && (
                <span className="text-[9px] uppercase tracking-wider text-muted-foreground/60 font-mono">
                  admin
                </span>
              )}
            </motion.button>
          )
        })}
      </div>

      {/* ---- Tab content (animated swap) ---- */}
      <AnimatePresence mode="wait">
        {activeTab === "timeline" && (
          <motion.div
            key="timeline"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 flex flex-col"
          >
            <motion.div
              key="resources-strip"
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              transition={{ duration: 0.22, ease: "easeOut" }}
              className="overflow-hidden"
            >
              <ResourcesStrip
                entries={entries}
                mode={crewId ? "single" : "aggregate"}
              />
            </motion.div>
            <div className="flex-1 min-h-0">
              <LogsPanel
                entries={entries}
                timeRange={timeRange}
                onTimeRangeChange={setTimeRange}
                customRange={customRange}
                onCustomRangeChange={setCustomRange}
                crewScope={{ value: crewId, options: crews, onChange: onCrewChange }}
                agentScope={{ value: agentId, options: agents, onChange: setAgentId }}
                agentLookup={agentLookup}
                showNetworkCard={showNetworkCard}
                severity={severity}
                onSeverityChange={setSeverity}
                muted={muted}
                onMutedChange={setMuted}
                traceId={traceId}
                onClearTraceId={() => setTraceId("")}
                onSelectTrace={setTraceId}
                onSelectAgent={setAgentId}
                onSelectCrew={onCrewChange}
                onServerSearch={setServerQuery}
                onRefresh={handleRefresh}
                loading={loading}
                error={error}
                refreshRate={refreshRate}
                onRefreshRateChange={setRefreshRate}
                live={live}
                onLiveChange={setLive}
                hasMore={Boolean(nextCursor)}
                loadingMore={loadingMore}
                cappedAt={entries.length >= JOURNAL_MAX_ENTRIES ? JOURNAL_MAX_ENTRIES : undefined}
              />
            </div>
          </motion.div>
        )}

        {activeTab === "runs" && (
          <motion.div
            key="runs"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 overflow-hidden flex flex-col"
          >
            <RunsView workspaceId={workspaceId} workspaceLoading={wsLoading} />
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

/**
 * Live error/warn count over the last ANOMALY_WINDOW_MS milliseconds,
 * surfaced as a pulsing red pill in the header. Acts as a "you should
 * look at this" signal so a viewer scrolling through routine
 * exec.command + container.metrics traffic doesn't miss a fresh
 * cluster of failures. Clicking jumps the filter to severity=error +
 * time=5m so the pill always resolves to a useful narrowed view.
 *
 * Threshold (ANOMALY_THRESHOLD) deliberately starts low (>= 3) — false
 * positives here are cheap (a quick glance) and false negatives are
 * not (a missed cluster of run.failed events).
 */
const ANOMALY_WINDOW_MS = 5 * 60 * 1000
const ANOMALY_THRESHOLD = 3

function AnomalyBadge({
  entries,
  onClick,
}: {
  entries: Array<{ ts: string; severity?: string }>
  onClick: () => void
}) {
  // Wall-clock tick keeps the rolling window honest when no new entries
  // arrive: without it the cutoff would freeze at the timestamp of the
  // most recent render, so a quiet stream of errors three minutes ago
  // would keep firing the badge forever instead of aging out.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])
  const errCount = useMemo(() => {
    const cutoff = now - ANOMALY_WINDOW_MS
    let n = 0
    for (const e of entries) {
      if (e.severity !== "error" && e.severity !== "warn") continue
      const t = Date.parse(e.ts)
      if (Number.isFinite(t) && t >= cutoff) n++
    }
    return n
  }, [entries, now])
  if (errCount < ANOMALY_THRESHOLD) return null
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex items-center gap-1.5 h-5 px-2 rounded-full border border-red-500/40 bg-red-500/10 text-[10px] font-mono text-red-300 hover:bg-red-500/20 transition-colors"
      title={`${errCount} error/warn events in the last 5 minutes — click to focus`}
    >
      <span className="relative inline-flex">
        <span className="absolute inline-flex h-1.5 w-1.5 rounded-full bg-red-400 opacity-75 animate-ping" />
        <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-red-400" />
      </span>
      <span className="tabular-nums">{errCount}</span>
      <span className="opacity-80">in 5m</span>
    </button>
  )
}

function StreamStatusBadge({ status }: { status: string }) {
  if (status === "connected") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-emerald-500/10 text-emerald-300 border-emerald-500/30">
        <Zap className="h-3 w-3" /> Live
      </Badge>
    )
  }
  if (status === "polling") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-amber-500/10 text-amber-300 border-amber-500/30">
        <RadioTower className="h-3 w-3" /> Polling
      </Badge>
    )
  }
  if (status === "connecting") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-blue-500/10 text-blue-300 border-blue-500/30">
        <Radio className="h-3 w-3" /> Connecting
      </Badge>
    )
  }
  if (status === "error") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-red-500/10 text-red-300 border-red-500/30">
        Offline
      </Badge>
    )
  }
  return null
}
