"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import dynamic from "next/dynamic"
import { useParams } from "next/navigation"
import Link from "next/link"
import { ArrowLeft, Binoculars, Loader2, Radio, RadioTower, Wifi, WifiOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { NetworkPanel } from "@/components/features/crows-nest/network-panel"
import { FilesystemPanel } from "@/components/features/crows-nest/filesystem-panel"
import { ResourceSparklines } from "@/components/features/crows-nest/resource-sparklines"
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

const OBSERVABILITY_TYPES = [
  "exec.command",
  "exec.output_chunk",
  "network.port_opened",
  "network.port_closed",
  "network.egress",
  "file.written",
  "container.metrics",
].join(",")

/**
 * Crow's Nest — live observability dashboard for a single crew container.
 * Subscribes to the journal stream filtered to the observability entry types
 * and fans the feed out to the four child panels. Admin-only.
 */
export default function CrowsNestCrewPage() {
  const params = useParams<{ crewId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { role, loading: rolesLoading } = useAbilities()
  const [crew, setCrew] = useState<Crew | null>(null)
  const [crewLoading, setCrewLoading] = useState(true)

  const queryParams = useMemo(
    () => ({
      crew_id: params.crewId,
      entry_type: OBSERVABILITY_TYPES,
    }),
    [params.crewId],
  )

  // Seed the panels with history via the list endpoint; then stream in new
  // entries. `prependLive` dedupes so we never double-render.
  const { entries: historyEntries, prependLive } = useJournalList({
    workspaceId,
    params: queryParams,
    limit: 200,
    enabled: !wsLoading,
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
    enabled: !wsLoading && !!workspaceId,
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

  // Terminal wants oldest→newest to play back in order.
  const terminalEntries = useMemo(() => {
    return mergedEntries
      .filter((e) => e.entry_type === "exec.command" || e.entry_type === "exec.output_chunk")
      .slice()
      .reverse()
  }, [mergedEntries])

  useEffect(() => {
    if (!workspaceId || !params.crewId) {
      if (!wsLoading) setCrewLoading(false)
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`/api/v1/crews/${encodeURIComponent(params.crewId)}?workspace_id=${encodeURIComponent(workspaceId)}`)
        if (!res.ok) return
        const json = await res.json()
        if (!cancelled) setCrew(json)
      } finally {
        if (!cancelled) setCrewLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading, params.crewId])

  if (wsLoading || rolesLoading || crewLoading) {
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

  const sseConnected = streamStatus === "connected"

  return (
    <div className="p-4 md:p-6 flex flex-col gap-3 h-[calc(100vh-48px)]">
      <div className="flex items-center justify-between gap-3 flex-wrap shrink-0">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" asChild>
            <Link href="/crows-nest">
              <ArrowLeft className="h-3 w-3 mr-1" /> Back
            </Link>
          </Button>
          <Binoculars className="h-4 w-4 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">
            {crew?.name ?? "Crow's Nest"}
          </h1>
          <Badge variant="outline" className="text-[10px] border-border/60 font-mono">
            crewship-team-{crew?.slug ?? params.crewId.slice(0, 6)}
          </Badge>
        </div>
        <div className="flex items-center gap-2">
          <StreamPill status={streamStatus} />
        </div>
      </div>

      {/* 3-column grid, collapses on small screens. The left (terminal) takes
           the most space; middle and right are stacked panels. */}
      <div className="grid flex-1 min-h-0 gap-3 grid-cols-1 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)]">
        <div className="min-h-[320px] lg:min-h-0">
          <LiveTerminal entries={terminalEntries} connected={sseConnected} />
        </div>
        <div className="min-h-[320px] lg:min-h-0">
          <NetworkPanel entries={mergedEntries} />
        </div>
        <div className="grid gap-3 grid-rows-2 min-h-[640px] lg:min-h-0">
          <ResourceSparklines entries={mergedEntries} />
          <FilesystemPanel entries={mergedEntries} />
        </div>
      </div>
    </div>
  )
}

function StreamPill({ status }: { status: string }) {
  if (status === "connected") {
    return (
      <Badge variant="outline" className={cn("gap-1 text-[10px] bg-emerald-500/10 text-emerald-300 border-emerald-500/30")}>
        <Wifi className="h-3 w-3" /> Live
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
        <Radio className="h-3 w-3" /> Connecting…
      </Badge>
    )
  }
  return (
    <Badge variant="outline" className="gap-1 text-[10px] bg-red-500/10 text-red-300 border-red-500/30">
      <WifiOff className="h-3 w-3" /> Offline
    </Badge>
  )
}
