"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"
import {
  Activity,
  BarChart3,
  BookOpen,
  Clock,
  ListOrdered,
  Radio,
  RadioTower,
  Zap,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { RunsView } from "@/components/features/journal/runs-view"
import { LogsPanel } from "@/components/features/logs/logs-panel"
import { ResourcesStrip } from "@/components/features/logs/resources-strip"
import { sinceFromTimeRange, type CustomRange, type TimeRange } from "@/components/features/logs/time-range-picker"
import type { ScopeOption } from "@/components/features/logs/logs-toolbar"

/** SSE buffer cap — prevents unbounded growth on a chatty workspace. */
const JOURNAL_MAX_ENTRIES = 1000

interface CrewSummary { id: string; name: string }
interface AgentSummary { id: string; name: string; crew_id?: string | null }

type JournalTab = "timeline" | "runs" | "stats"

const JOURNAL_TABS: Array<{ id: JournalTab; label: string; icon: typeof ListOrdered }> = [
  { id: "timeline", label: "Timeline", icon: ListOrdered },
  { id: "runs", label: "Runs", icon: Activity },
  { id: "stats", label: "Stats", icon: BarChart3 },
]

/**
 * Crew Journal — workspace-wide, append-only event stream rendered
 * Grafana Explore-style. All UI controls live in the LogsPanel toolbar
 * (search, time range, scope, severity, types, live/wrap, refresh) so
 * the layout stays in one window — no spread-out filter rail.
 */
export default function JournalPage() {
  const searchParams = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()
  const { workspaceId, loading: wsLoading } = useWorkspace()

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
  // Live-tail state lifted here so we can also pause SSE prepend, not
  // just the auto-scroll. When `live` is false the user explicitly
  // froze the list — incoming entries get dropped on the floor.
  const [live, setLive] = useState(true)

  // Initial tab from `?tab=runs`. Unknown values fall back to timeline.
  const initialTab = useMemo<JournalTab>(() => {
    const t = searchParams.get("tab")
    return t === "runs" || t === "stats" ? t : "timeline"
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const [activeTab, setActiveTab] = useState<JournalTab>(initialTab)

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
  // the user can share / bookmark a specific view. Uses replace() to
  // avoid polluting browser history on every keystroke.
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

  // Apply UI filters to backend query params. Severity / types stay
  // client-side via LogsPanel — server returns all and the panel
  // narrows the rendered slice. The `q` param hits FTS5 server-side.
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

  // Only the Timeline tab consumes the journal list + SSE stream. Runs
  // and Stats render from their own data sources.
  const timelineEnabled = !wsLoading && activeTab === "timeline"
  const { entries, nextCursor, loading, loadingMore, error, refresh, loadMore, prependLive } =
    useJournalList({
      workspaceId,
      params: queryParams,
      enabled: timelineEnabled,
      maxEntries: JOURNAL_MAX_ENTRIES,
    })

  // The live ref keeps the latest pause state in scope for the SSE
  // callback without retriggering the stream subscription on toggle.
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

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
        {/* ---- Header strip ---- */}
        <div className="shrink-0 flex items-center h-9 bg-card border-b border-border/60 px-3 gap-2">
          <BookOpen className="h-3.5 w-3.5 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">Crew Journal</h1>
          <Badge variant="outline" className="text-[10px] border-border/60 font-mono">
            {entries.length} loaded
          </Badge>
          <StreamStatusBadge status={streamStatus} />
          <div className="flex-1" />
        </div>

        {/* ---- Tab strip ---- */}
        <div
          role="tablist"
          aria-label="Journal views"
          className="shrink-0 flex items-center h-9 bg-card border-b border-border/60 px-2 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]"
        >
          {JOURNAL_TABS.map(({ id, label, icon: Icon }) => (
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
            </button>
          ))}
        </div>

        {/* ---- Tab content ---- */}
        {activeTab === "timeline" && (
          <div className="flex-1 min-h-0 flex flex-col">
            {/* Resources strip is per-container — only useful when a
                single crew is selected. Hide for "All crews" since the
                metrics would mix across containers. */}
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

        {activeTab === "stats" && (
          <div className="flex-1 min-h-0 overflow-auto p-4">
            <Card>
              <CardHeader>
                <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                  <BarChart3 className="h-3.5 w-3.5" />
                  Journal statistics
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col items-center gap-2 py-10 text-center">
                <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
                  <Clock className="h-4 w-4 text-muted-foreground/60" />
                </div>
                <div className="text-sm font-medium text-foreground/80">Coming soon</div>
                <div className="text-[11px] text-muted-foreground max-w-sm">
                  Breakdowns by entry type, crew, and time-of-day will land here. For
                  now, the Timeline tab surfaces the raw feed.
                </div>
              </CardContent>
            </Card>
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
