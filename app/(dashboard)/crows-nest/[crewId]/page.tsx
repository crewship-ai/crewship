"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import dynamic from "next/dynamic"
import Link from "next/link"
import {
  Activity,
  ArrowLeft,
  Binoculars,
  FolderTree,
  Gauge,
  Loader2,
  Network,
  PanelLeftClose,
  PanelLeftOpen,
  Radio,
  RadioTower,
  Terminal,
  Wifi,
  WifiOff,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { NetworkPanel } from "@/components/features/crows-nest/network-panel"
import { FilesystemPanel } from "@/components/features/crows-nest/filesystem-panel"
import { ResourceSparklines } from "@/components/features/crows-nest/resource-sparklines"
import { ActivityPanel } from "@/components/features/crows-nest/activity-panel"
import { useIsMobile } from "@/hooks/use-mobile"
import type { JournalEntry } from "@/lib/types/journal"

// xterm is a client-only library — dynamic import with ssr:false keeps it out
// of the static-export bundle on first render and avoids SSR crashes.
const LiveTerminal = dynamic(
  () => import("@/components/features/crows-nest/live-terminal").then((m) => m.LiveTerminal),
  { ssr: false, loading: () => <div className="h-full w-full bg-[#0a0a0a] rounded-lg border border-border/50" /> },
)

interface Crew {
  id: string
  name: string
  slug: string
  _count?: { agents: number }
}

// Types subscribed to by the page. The first 7 feed the dedicated panels
// (Terminal / Network / Resources / Filesystem). The rest power the
// Activity feed so the page surfaces something useful even when the
// container.metrics + file.written emitters aren't wired yet — see the
// follow-up tracked in PR #272 / Crow's Nest emit gaps.
const OBSERVABILITY_TYPES = [
  // Terminal
  "exec.command",
  "exec.output_chunk",
  // Network
  "network.port_opened",
  "network.port_closed",
  "network.egress",
  // Filesystem (emitter not yet wired)
  "file.written",
  // Resources (emitter not yet wired)
  "container.metrics",
  // Activity feed — broader signal of what the crew is doing
  "container.snapshot",
  "agent.status_change",
  "run.started",
  "run.completed",
  "run.failed",
  "run.cancelled",
  "peer.conversation",
  "peer.escalation",
  "keeper.request",
  "keeper.decision",
  "mission.status_change",
  "assignment.created",
  "assignment.completed",
  "assignment.failed",
  "task.delegated",
  "approval.requested",
  "approval.granted",
  "approval.denied",
  "cost.incurred",
  "budget.warning",
  "budget.exceeded",
  "skill.assigned",
  "memory.updated",
  "system.compaction",
].join(",")

type SeverityFilter = "all" | "info" | "notice" | "warn" | "error"

/**
 * Read the crew id from the live URL after client hydration.
 *
 * useParams() is unreliable in Next.js static export: layout.tsx prerenders
 * with [{ crewId: "_" }] and useParams returns "_" for the prerendered file
 * even after the user navigates to /crows-nest/<real-id>. Pulling from
 * window.location.pathname bypasses that and gives us the real id. Same
 * pattern as chat/[agentSlug]/chat-page-client.tsx.
 */
function useCrewIdFromUrl(): string | null {
  const [id, setId] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const m = window.location.pathname.match(/^\/crows-nest\/([^/]+)\/?$/)
    if (m) setId(decodeURIComponent(m[1]))
  }, [])
  return id
}

/**
 * Crow's Nest — live observability dashboard for a single crew container.
 *
 * Layout pattern: "3-panel master-detail" (sidebar + center grid + top strip).
 * See `docs/design/patterns.md` #1.
 *
 * Subscribes to the journal stream filtered to the observability entry types
 * and fans the feed out to the child panels (terminal, network, resources,
 * filesystem). Admin-only.
 */
export default function CrowsNestCrewPage() {
  const crewId = useCrewIdFromUrl()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { role, loading: rolesLoading } = useAbilities()
  const isMobile = useIsMobile()

  const [crew, setCrew] = useState<Crew | null>(null)
  const [crewLoading, setCrewLoading] = useState(true)
  const [allCrews, setAllCrews] = useState<Crew[]>([])
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [severityFilter, setSeverityFilter] = useState<SeverityFilter>("all")

  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  const queryParams = useMemo(
    () => (crewId ? { crew_id: crewId, entry_type: OBSERVABILITY_TYPES } : undefined),
    [crewId],
  )

  // Seed the panels with history via the list endpoint; then stream in new
  // entries. `prependLive` dedupes so we never double-render. Both hooks
  // stay disabled until the crewId is resolved from the URL so we don't
  // fire a fetch with crew_id="_" against the prerendered placeholder.
  const { entries: historyEntries, prependLive } = useJournalList({
    workspaceId,
    params: queryParams,
    limit: 200,
    enabled: !wsLoading && !!crewId,
  })

  // Keep a separate, time-capped buffer so the resource sparklines only see a
  // bounded window even if the stream runs for hours.
  const [liveEntries, setLiveEntries] = useState<JournalEntry[]>([])
  const liveEntriesRef = useRef<JournalEntry[]>([])
  const handleLive = useCallback(
    (entry: JournalEntry) => {
      prependLive(entry)
      const next = [entry, ...liveEntriesRef.current].slice(0, 500)
      liveEntriesRef.current = next
      setLiveEntries(next)
    },
    [prependLive],
  )

  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params: queryParams,
    enabled: !wsLoading && !!workspaceId && !!crewId,
    onEntry: handleLive,
  })

  // Merge history + live — newest first. Used by panels that derive state
  // from the full recent feed (network, filesystem, resources).
  const mergedEntries = useMemo<JournalEntry[]>(() => {
    const seen = new Set<string>()
    const out: JournalEntry[] = []
    for (const e of [...liveEntries, ...historyEntries]) {
      if (seen.has(e.id)) continue
      seen.add(e.id)
      out.push(e)
    }
    return out
  }, [liveEntries, historyEntries])

  // Apply severity filter — note the hook returns all entries; filtering
  // happens here so each panel gets a consistent slice.
  const filteredEntries = useMemo(() => {
    if (severityFilter === "all") return mergedEntries
    return mergedEntries.filter((e) => e.severity === severityFilter)
  }, [mergedEntries, severityFilter])

  // Terminal wants oldest→newest to play back in order.
  const terminalEntries = useMemo(() => {
    return filteredEntries
      .filter((e) => e.entry_type === "exec.command" || e.entry_type === "exec.output_chunk")
      .slice()
      .reverse()
  }, [filteredEntries])

  // Fetch current crew + sibling crews for the sidebar picker.
  useEffect(() => {
    if (!workspaceId || !crewId) {
      if (!wsLoading) setCrewLoading(false)
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        const [crewRes, listRes] = await Promise.all([
          fetch(`/api/v1/crews/${encodeURIComponent(crewId)}?workspace_id=${encodeURIComponent(workspaceId)}`),
          fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`),
        ])
        if (crewRes.ok) {
          const json = await crewRes.json()
          if (!cancelled) setCrew(json)
        }
        if (listRes.ok) {
          const list = await listRes.json()
          if (!cancelled && Array.isArray(list)) setAllCrews(list)
        }
      } catch (err) {
        // Network / JSON parse failures must not escape this async IIFE
        // as unhandled rejections — they'd surface in console but leave
        // crewLoading stuck at true, so the page never renders anything.
        // The page falls back to null crew (same as 404) and the UI
        // shows the "crew not found" empty state below.
        if (!cancelled) {
          console.error("crows-nest: failed to load crew metadata", err)
        }
      } finally {
        if (!cancelled) setCrewLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading, crewId])

  if (wsLoading || rolesLoading || crewLoading || !crewId) {
    return (
      <div className="flex items-center justify-center py-20 text-muted-foreground">
        <Loader2 className="h-4 w-4 mr-2 animate-spin" /> Loading…
      </div>
    )
  }

  const isAdmin = role === "OWNER" || role === "ADMIN"
  if (!isAdmin) {
    return (
      <div className="flex flex-col items-center gap-2 py-20 text-center">
        <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
          <Binoculars className="h-4 w-4 text-muted-foreground/60" />
        </div>
        <div className="text-sm font-medium text-foreground/80">Crow&apos;s Nest requires admin role</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">
          Only workspace owners and admins can watch live crew container activity.
        </div>
      </div>
    )
  }

  // Treat polling the same as connected — data is still live-tailing to
  // the client, just via fallback polling instead of SSE. Users reading
  // "IDLE" when entries are actually arriving is more confusing than the
  // subtle difference in transport.
  const sseConnected = streamStatus === "connected" || streamStatus === "polling"
  const crewSlug = crew?.slug ?? crewId.slice(0, 6)

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Top strip (h-9) ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-border/60 px-2 sm:px-3 gap-2 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs shrink-0" asChild>
          <Link href="/crows-nest">
            <ArrowLeft className="h-3 w-3 mr-1" /> Back
          </Link>
        </Button>
        <Binoculars className="h-3.5 w-3.5 text-foreground/60 shrink-0" />
        <h1 className="text-body font-medium text-foreground/80 truncate">
          {crew?.name ?? "Crow's Nest"}
        </h1>
        <Badge variant="outline" className="text-[10px] border-border/60 font-mono shrink-0">
          crewship-team-{crewSlug}
        </Badge>
        <div className="flex-1" />
        <StreamPill status={streamStatus} />
      </div>

      {/* ---- Main 2-column layout (collapsible sidebar + center grid) ---- */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200 relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${leftCollapsed ? "48px" : "300px"} 1fr`,
        }}
      >
        {/* ---- Left sidebar ---- */}
        {!isMobile && (
          <div className="border-r border-border/60 bg-card flex flex-col min-h-0 transition-all duration-200 overflow-hidden">
            <div className="flex items-center justify-between px-2 py-1.5 border-b border-border/60 shrink-0">
              {!leftCollapsed && (
                <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                  Crews
                </span>
              )}
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground/70 hover:text-foreground/70 ml-auto"
                onClick={() => setLeftCollapsed(!leftCollapsed)}
                aria-label={leftCollapsed ? "Expand sidebar" : "Collapse sidebar"}
              >
                {leftCollapsed
                  ? <PanelLeftOpen className="h-3.5 w-3.5" />
                  : <PanelLeftClose className="h-3.5 w-3.5" />}
              </Button>
            </div>

            {!leftCollapsed && (
              <div className="flex-1 min-h-0 overflow-y-auto p-2 space-y-4">
                {/* Crew list */}
                <div>
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1.5 px-1">
                    Watching
                  </div>
                  <ul className="space-y-0.5">
                    {allCrews.length === 0 ? (
                      <li className="text-[11px] text-muted-foreground px-2 py-1">No crews.</li>
                    ) : (
                      allCrews.map((c) => {
                        const active = c.id === crewId
                        return (
                          <li key={c.id}>
                            <Link
                              href={`/crows-nest/${c.id}`}
                              className={cn(
                                "flex items-center gap-2 px-2 py-1.5 rounded-md text-[12px] transition-colors",
                                active
                                  ? "bg-primary/10 text-primary"
                                  : "text-foreground/80 hover:bg-accent/50",
                              )}
                            >
                              <Binoculars className="h-3 w-3 shrink-0 opacity-70" />
                              <span className="truncate">{c.name}</span>
                            </Link>
                          </li>
                        )
                      })
                    )}
                  </ul>
                </div>

                {/* Severity filter */}
                <div>
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1.5 px-1">
                    Severity
                  </div>
                  <div className="inline-flex w-full rounded-md border border-border/60 bg-card p-0.5">
                    {(["all", "info", "notice", "warn", "error"] as SeverityFilter[]).map((s) => (
                      <button
                        key={s}
                        type="button"
                        onClick={() => setSeverityFilter(s)}
                        className={cn(
                          "flex-1 h-6 px-1 text-[10px] font-mono uppercase tracking-wider rounded transition-colors",
                          severityFilter === s
                            ? "bg-primary text-primary-foreground"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        {s}
                      </button>
                    ))}
                  </div>
                </div>

                {/* Stats */}
                <div>
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1.5 px-1">
                    Events buffered
                  </div>
                  <div className="flex items-center justify-between rounded-md border border-border/60 bg-muted/20 px-2 py-1.5">
                    <span className="text-[11px] text-muted-foreground">In view</span>
                    <span className="text-[11px] font-mono tabular-nums text-foreground/80">
                      {filteredEntries.length}
                    </span>
                  </div>
                </div>
              </div>
            )}
          </div>
        )}

        {/* ---- Center content (3-column grid of observability panels) ---- */}
        <div className="min-h-0 overflow-hidden">
          <div className="p-3 h-full grid gap-3 grid-cols-1 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]">
            <Card className="py-0 gap-0 min-h-[320px] lg:min-h-0 flex flex-col overflow-hidden">
              <CardHeader className="px-3 py-2 border-b border-border/50">
                <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
                  <Terminal className="h-3 w-3 opacity-70" /> Terminal
                </CardTitle>
              </CardHeader>
              <CardContent className="p-0 flex-1 min-h-0">
                <LiveTerminal entries={terminalEntries} connected={sseConnected} />
              </CardContent>
            </Card>

            <Card className="py-0 gap-0 min-h-[320px] lg:min-h-0 flex flex-col overflow-hidden">
              <CardHeader className="px-3 py-2 border-b border-border/50">
                <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
                  <Network className="h-3 w-3 opacity-70" /> Network
                </CardTitle>
              </CardHeader>
              <CardContent className="p-0 flex-1 min-h-0 overflow-auto">
                <NetworkPanel entries={filteredEntries} />
              </CardContent>
            </Card>

            {/* Right column: Resources sparkline (top, compact),
                Filesystem (middle, compact), Activity feed (bottom,
                takes the remaining height). All three feeds are
                live — Resources via stats collector, Filesystem via
                fsnotify watcher, Activity via the broad event-type
                subscription on the journal stream. */}
            <div className="grid gap-3 grid-rows-[200px_200px_minmax(0,1fr)] min-h-[700px] lg:min-h-0">
              <Card className="py-0 gap-0 flex flex-col overflow-hidden">
                <CardHeader className="px-3 py-2 border-b border-border/50">
                  <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
                    <Gauge className="h-3 w-3 opacity-70" /> Resources
                  </CardTitle>
                </CardHeader>
                <CardContent className="p-0 flex-1 min-h-0 overflow-auto">
                  <ResourceSparklines entries={filteredEntries} />
                </CardContent>
              </Card>
              <Card className="py-0 gap-0 flex flex-col overflow-hidden">
                <CardHeader className="px-3 py-2 border-b border-border/50">
                  <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
                    <FolderTree className="h-3 w-3 opacity-70" /> Filesystem
                  </CardTitle>
                </CardHeader>
                <CardContent className="p-0 flex-1 min-h-0 overflow-auto">
                  <FilesystemPanel entries={filteredEntries} />
                </CardContent>
              </Card>
              <Card className="py-0 gap-0 flex flex-col overflow-hidden">
                <CardHeader className="px-3 py-2 border-b border-border/50">
                  <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-1.5">
                    <Activity className="h-3 w-3 opacity-70" /> Activity
                  </CardTitle>
                </CardHeader>
                <CardContent className="p-0 flex-1 min-h-0">
                  <ActivityPanel entries={filteredEntries} />
                </CardContent>
              </Card>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function StreamPill({ status }: { status: string }) {
  if (status === "connected") {
    return (
      <Badge variant="outline" className={cn("gap-1 text-[10px] bg-emerald-500/10 text-emerald-300 border-emerald-500/30 shrink-0")}>
        <Wifi className="h-3 w-3" /> Live
      </Badge>
    )
  }
  if (status === "polling") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-amber-500/10 text-amber-300 border-amber-500/30 shrink-0">
        <RadioTower className="h-3 w-3" /> Polling
      </Badge>
    )
  }
  if (status === "connecting") {
    return (
      <Badge variant="outline" className="gap-1 text-[10px] bg-blue-500/10 text-blue-300 border-blue-500/30 shrink-0">
        <Radio className="h-3 w-3" /> Connecting…
      </Badge>
    )
  }
  return (
    <Badge variant="outline" className="gap-1 text-[10px] bg-red-500/10 text-red-300 border-red-500/30 shrink-0">
      <WifiOff className="h-3 w-3" /> Offline
    </Badge>
  )
}
