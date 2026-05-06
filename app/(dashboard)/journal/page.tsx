"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
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
import type { ScopeOption } from "@/components/features/logs/logs-toolbar"

/** SSE buffer cap — prevents unbounded growth on a chatty workspace. */
const JOURNAL_MAX_ENTRIES = 1000

interface CrewSummary { id: string; name: string }
interface AgentSummary { id: string; name: string; crew_id?: string | null }

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
          setCrews(json.map((c) => ({ id: c.id, name: c.name })))
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
          setAgents(json.map((a) => ({ id: a.id, name: a.name })))
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

  const handleLive = useCallback(
    (entry: Parameters<typeof prependLive>[0]) => {
      if (!liveRef.current) return
      prependLive(entry)
    },
    [prependLive],
  )
  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params: queryParams,
    enabled: timelineEnabled,
    onEntry: handleLive,
  })

  const handleRefresh = useCallback(() => { void refresh() }, [refresh])
  const handleLoadMore = useCallback(() => { void loadMore() }, [loadMore])

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
          <button
            key={id}
            role="tab"
            aria-selected={activeTab === id}
            onClick={() => setActiveTab(id)}
            className={cn(
              "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-all duration-100 relative top-px whitespace-nowrap shrink-0",
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
          </button>
        ))}
      </div>

      {/* ---- Tab content ---- */}
      {activeTab === "timeline" && (
        <div className="flex-1 min-h-0 flex flex-col">
          {crewId && <ResourcesStrip entries={entries} />}
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
              live={live}
              onLiveChange={setLive}
              hasMore={Boolean(nextCursor)}
              loadingMore={loadingMore}
              onLoadMore={handleLoadMore}
            />
          </div>
        </div>
      )}

      {activeTab === "runs" && (
        <div className="flex-1 min-h-0 overflow-auto p-4">
          <RunsView workspaceId={workspaceId} workspaceLoading={wsLoading} />
        </div>
      )}

      {activeTab === "eval" && (
        <div className="flex-1 min-h-0 overflow-hidden">
          <EvalView />
        </div>
      )}

      {activeTab === "audit" && isAdmin && (
        <div className="flex-1 min-h-0 overflow-hidden">
          <AuditView />
        </div>
      )}

      {activeTab === "spend" && isAdmin && (
        <div className="flex-1 min-h-0 overflow-hidden">
          <SpendView />
        </div>
      )}
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
