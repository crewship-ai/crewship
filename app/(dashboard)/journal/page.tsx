"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import {
  Activity,
  BookOpen,
  DollarSign,
  LineChart,
  ListOrdered,
  Radio,
  RadioTower,
  Shield,
  Zap,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspace } from "@/hooks/use-workspace"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { RunsView } from "@/components/features/journal/runs-view"
import { AuditView } from "@/components/features/journal/audit-view"
import { SpendView } from "@/components/features/journal/spend-view"
import { EvalView } from "@/components/features/journal/eval-view"
import { LogsPanel } from "@/components/features/logs/logs-panel"
import { ResourcesStrip } from "@/components/features/logs/resources-strip"
import { sinceFromTimeRange, type CustomRange, type TimeRange } from "@/components/features/logs/time-range-picker"
import { refreshRateMs, type RefreshRate } from "@/components/features/logs/refresh-rate-picker"
import type { ScopeOption } from "@/components/features/logs/logs-toolbar"

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

type JournalTab = "timeline" | "runs" | "eval" | "audit" | "spend"

interface TabDef {
  id: JournalTab
  label: string
  icon: typeof ListOrdered
  /** When true, only OWNER/ADMIN see the tab. */
  adminOnly?: boolean
}

const ALL_TABS: TabDef[] = [
  { id: "timeline", label: "Timeline", icon: ListOrdered },
  { id: "runs", label: "Runs", icon: Activity },
  { id: "eval", label: "Eval", icon: LineChart },
  { id: "audit", label: "Audit", icon: Shield, adminOnly: true },
  { id: "spend", label: "Spend", icon: DollarSign, adminOnly: true },
]

/**
 * Crew Journal — workspace-wide records center.
 *
 * Five tabs render different immutable record types under one roof:
 *   - Timeline: runtime events from `journal_entries` (Grafana-style)
 *   - Runs:     agent run aggregates from `/api/v1/runs`
 *   - Eval:     eval.* journal projection
 *   - Audit:    entity CRUD log from `/api/v1/audit` (admin-only)
 *   - Spend:    cost ledger from `/api/v1/paymaster/*` (admin-only)
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
  const [serverQuery, setServerQuery] = useState<string>("")
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

  // If admin permissions resolve to non-admin, demote out of an admin-only tab.
  useEffect(() => {
    if (rolesLoading) return
    const tabDef = ALL_TABS.find((t) => t.id === activeTab)
    if (tabDef?.adminOnly && !isAdmin) setActiveTab("timeline")
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
  // the user can share / bookmark a specific view.
  useEffect(() => {
    const sp = new URLSearchParams()
    if (timeRange !== "24h") sp.set("time", timeRange)
    if (timeRange === "custom" && customRange) {
      sp.set("from", String(customRange.fromMs))
      sp.set("to", String(customRange.toMs))
    }
    if (crewId) sp.set("crew_id", crewId)
    if (agentId) sp.set("agent_id", agentId)
    if (activeTab !== "timeline") sp.set("tab", activeTab)
    const qs = sp.toString()
    router.replace(qs ? `${pathname}?${qs}` : pathname, { scroll: false })
  }, [timeRange, customRange, crewId, agentId, activeTab, router, pathname])

  const queryParams = useMemo<Record<string, string | undefined>>(() => {
    const since = sinceFromTimeRange(timeRange, customRange)
    const until = timeRange === "custom" && customRange
      ? new Date(customRange.toMs).toISOString()
      : undefined
    return {
      crew_id: crewId || undefined,
      agent_id: agentId || undefined,
      q: serverQuery.trim() || undefined,
      since,
      until,
    }
  }, [timeRange, customRange, crewId, agentId, serverQuery])

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
        <div className="flex-1" />
      </div>

      {/* ---- Tab strip ---- */}
      <div
        role="tablist"
        aria-label="Journal views"
        className="shrink-0 flex items-center h-9 bg-card border-b border-border/60 px-2 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]"
      >
        {visibleTabs.map(({ id, label, icon: Icon, adminOnly }) => (
          <motion.button
            key={id}
            role="tab"
            aria-selected={activeTab === id}
            onClick={() => setActiveTab(id)}
            whileHover={{ y: -1 }}
            whileTap={{ y: 0, scale: 0.97 }}
            transition={{ duration: 0.12 }}
            className={cn(
              "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-colors duration-100 relative top-px whitespace-nowrap shrink-0",
              activeTab === id
                ? "border-blue-400 text-blue-400"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            <Icon className="h-3 w-3 opacity-75" />
            {label}
            {adminOnly && (
              <span className="text-[9px] uppercase tracking-wider text-muted-foreground/60 font-mono">
                admin
              </span>
            )}
          </motion.button>
        ))}
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
            className="flex-1 min-h-0 overflow-auto p-4"
          >
            <RunsView workspaceId={workspaceId} workspaceLoading={wsLoading} />
          </motion.div>
        )}

        {activeTab === "eval" && (
          <motion.div
            key="eval"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 overflow-hidden"
          >
            <EvalView />
          </motion.div>
        )}

        {activeTab === "audit" && isAdmin && (
          <motion.div
            key="audit"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 overflow-hidden"
          >
            <AuditView />
          </motion.div>
        )}

        {activeTab === "spend" && isAdmin && (
          <motion.div
            key="spend"
            initial={{ opacity: 0, y: 4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.18, ease: "easeOut" }}
            className="flex-1 min-h-0 overflow-hidden"
          >
            <SpendView />
          </motion.div>
        )}
      </AnimatePresence>
    </div>
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
