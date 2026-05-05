"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import dynamic from "next/dynamic"
import Link from "next/link"
import {
  ArrowLeft,
  Binoculars,
  FolderTree,
  Loader2,
  Network,
  Radio,
  RadioTower,
  ScrollText,
  Terminal,
  Wifi,
  WifiOff,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { NetworkPanel } from "@/components/features/crows-nest/network-panel"
import { FilesystemPanel } from "@/components/features/crows-nest/filesystem-panel"
import { ResourcesStrip } from "@/components/features/crows-nest/resources-strip"
import { LogsPanel } from "@/components/features/crows-nest/logs-panel"
import type { JournalEntry } from "@/lib/types/journal"
import { useRouter } from "next/navigation"

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

// Types subscribed to by the page. Includes everything the Logs tab cares
// about plus the dedicated panels for Terminal / Network / Filesystem and
// resources sparkline. Keep ordering loose — server filters by exact match.
const OBSERVABILITY_TYPES = [
  // Terminal
  "exec.command",
  "exec.output_chunk",
  // Network
  "network.port_opened",
  "network.port_closed",
  "network.egress",
  // Filesystem
  "file.written",
  // Resources
  "container.metrics",
  // Logs feed — broader signal of what the crew is doing
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

/**
 * Read the crew id from the live URL after client hydration.
 *
 * useParams() is unreliable in Next.js static export: layout.tsx prerenders
 * with [{ crewId: "_" }] and useParams returns "_" for the prerendered file
 * even after the user navigates to /crows-nest/<real-id>. Pulling from
 * window.location.pathname bypasses that and gives us the real id.
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
 * Layout: top strip → resources strip → tabs (Logs / Terminal / Network /
 * Filesystem). The Logs tab is a dedicated Grafana Explore-style log
 * viewer — see `components/features/crows-nest/logs-panel.tsx`.
 */
export default function CrowsNestCrewPage() {
  const crewId = useCrewIdFromUrl()
  const router = useRouter()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { role, loading: rolesLoading } = useAbilities()

  const [crew, setCrew] = useState<Crew | null>(null)
  const [crewLoading, setCrewLoading] = useState(true)
  const [allCrews, setAllCrews] = useState<Crew[]>([])

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
    enabled: !wsLoading && !!workspaceId && !!crewId,
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

  // Merge history + live — newest first.
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

  // Terminal wants oldest→newest to play back in order.
  const terminalEntries = useMemo(() => {
    return mergedEntries
      .filter((e) => e.entry_type === "exec.command" || e.entry_type === "exec.output_chunk")
      .slice()
      .reverse()
  }, [mergedEntries])

  // Fetch current crew + sibling crews for the picker.
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

  const crewSlug = crew?.slug ?? crewId.slice(0, 6)

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Top strip ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-border/60 px-2 sm:px-3 gap-2 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs shrink-0" asChild>
          <Link href="/crows-nest">
            <ArrowLeft className="h-3 w-3 mr-1" /> Back
          </Link>
        </Button>
        <Binoculars className="h-3.5 w-3.5 text-foreground/60 shrink-0" />

        {/* Crew picker — replaces the old left sidebar */}
        {allCrews.length > 1 ? (
          <Select value={crewId} onValueChange={(v) => router.push(`/crows-nest/${v}`)}>
            <SelectTrigger size="sm" className="h-7 text-xs gap-1 shrink-0">
              <SelectValue>{crew?.name ?? "Crew"}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {allCrews.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <h1 className="text-body font-medium text-foreground/80 truncate">
            {crew?.name ?? "Crow's Nest"}
          </h1>
        )}

        <Badge variant="outline" className="text-[10px] border-border/60 font-mono shrink-0">
          crewship-team-{crewSlug}
        </Badge>
        <div className="flex-1" />
        <StreamPill status={streamStatus} />
      </div>

      {/* ---- Always-visible resources strip ---- */}
      <ResourcesStrip entries={mergedEntries} />

      {/* ---- Tabs ---- */}
      <Tabs defaultValue="logs" className="flex-1 min-h-0 flex flex-col gap-0">
        <TabsList variant="line" className="px-2 h-9 border-b border-border/50 bg-card/40 gap-1 rounded-none w-full justify-start">
          <TabsTrigger value="logs" className="gap-1.5 text-xs">
            <ScrollText className="h-3.5 w-3.5" /> Logs
            <span className="text-[10px] font-mono text-muted-foreground/70 tabular-nums">
              {mergedEntries.length}
            </span>
          </TabsTrigger>
          <TabsTrigger value="terminal" className="gap-1.5 text-xs">
            <Terminal className="h-3.5 w-3.5" /> Terminal
            <span className="text-[10px] font-mono text-muted-foreground/70 tabular-nums">
              {terminalEntries.length}
            </span>
          </TabsTrigger>
          <TabsTrigger value="network" className="gap-1.5 text-xs">
            <Network className="h-3.5 w-3.5" /> Network
          </TabsTrigger>
          <TabsTrigger value="filesystem" className="gap-1.5 text-xs">
            <FolderTree className="h-3.5 w-3.5" /> Filesystem
          </TabsTrigger>
        </TabsList>

        <TabsContent value="logs" className="flex-1 min-h-0 mt-0">
          <LogsPanel entries={mergedEntries} />
        </TabsContent>

        <TabsContent value="terminal" className="flex-1 min-h-0 mt-0 p-3">
          <div className="h-full rounded-lg overflow-hidden border border-border/50">
            <LiveTerminal
              entries={terminalEntries}
              connected={streamStatus === "connected" || streamStatus === "polling"}
            />
          </div>
        </TabsContent>

        <TabsContent value="network" className="flex-1 min-h-0 mt-0 p-3">
          <div className="h-full rounded-lg overflow-auto border border-border/50">
            <NetworkPanel entries={mergedEntries} />
          </div>
        </TabsContent>

        <TabsContent value="filesystem" className="flex-1 min-h-0 mt-0 p-3">
          <div className="h-full rounded-lg overflow-auto border border-border/50">
            <FilesystemPanel entries={mergedEntries} />
          </div>
        </TabsContent>
      </Tabs>
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
