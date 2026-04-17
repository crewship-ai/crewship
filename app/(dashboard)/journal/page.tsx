"use client"

import { useCallback, useMemo, useState } from "react"
import { useSearchParams } from "next/navigation"
import { BookOpen, Radio, RadioTower, RefreshCw, Zap } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { JournalFilters, DEFAULT_JOURNAL_FILTERS, type JournalFilterValue } from "@/components/features/journal/journal-filters"
import { JournalTimeline } from "@/components/features/journal/journal-timeline"

/** Convert the UI `timeRange` selection into an RFC3339 `since` string. */
function sinceFromRange(range: JournalFilterValue["timeRange"]): string | undefined {
  const now = Date.now()
  switch (range) {
    case "1h": return new Date(now - 60 * 60 * 1000).toISOString()
    case "24h": return new Date(now - 24 * 60 * 60 * 1000).toISOString()
    case "7d": return new Date(now - 7 * 24 * 60 * 60 * 1000).toISOString()
    case "30d": return new Date(now - 30 * 24 * 60 * 60 * 1000).toISOString()
    default: return undefined
  }
}

/**
 * Crew Journal — workspace-wide, append-only event stream. Uses
 * `/api/v1/journal` for paginated history and `/api/v1/journal/stream`
 * (SSE) for live updates, with graceful fallback to polling.
 */
export default function JournalPage() {
  const searchParams = useSearchParams()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  // Seed filters from query params (?crew_id=...&type=...) so the "View full
  // journal" link from crew cards and deeplinks lands on the expected view.
  const initialFilters = useMemo<JournalFilterValue>(() => {
    const base = { ...DEFAULT_JOURNAL_FILTERS }
    const crewId = searchParams.get("crew_id")
    const agentId = searchParams.get("agent_id")
    if (crewId) base.crewId = crewId
    if (agentId) base.agentId = agentId
    const types = searchParams.get("entry_type")
    if (types) base.types = types.split(",") as JournalFilterValue["types"]
    return base
  // Intentionally read once on mount; navigating with new params re-mounts.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const [filters, setFilters] = useState<JournalFilterValue>(initialFilters)

  // Apply UI filters to backend query params. The backend accepts CSV for
  // list-shaped filters (entry_type, severity).
  const queryParams = useMemo<Record<string, string | undefined>>(() => {
    const since = sinceFromRange(filters.timeRange)
    return {
      crew_id: filters.crewId || undefined,
      agent_id: filters.agentId || undefined,
      entry_type: filters.types.length ? filters.types.join(",") : undefined,
      severity: filters.severities.length ? filters.severities.join(",") : undefined,
      since,
    }
  }, [filters])

  const { entries, nextCursor, loading, loadingMore, error, refresh, loadMore, prependLive } =
    useJournalList({ workspaceId, params: queryParams, enabled: !wsLoading })

  // SSE prepend — the hook dedupes by id, so re-firing is safe.
  const handleLive = useCallback(
    (entry: Parameters<typeof prependLive>[0]) => {
      prependLive(entry)
    },
    [prependLive],
  )
  const { status: streamStatus } = useJournalStream({
    workspaceId,
    params: queryParams,
    enabled: !wsLoading,
    onEntry: handleLive,
  })

  // Client-side search filter — the backend doesn't yet support free text, so
  // we scope it to the already-fetched slice. Reasonable since we cap at
  // ~100 entries per page.
  const visibleEntries = useMemo(() => {
    if (!filters.search.trim()) return entries
    const needle = filters.search.toLowerCase()
    return entries.filter(
      (e) =>
        e.summary.toLowerCase().includes(needle) ||
        e.entry_type.toLowerCase().includes(needle) ||
        (e.actor_id?.toLowerCase().includes(needle) ?? false),
    )
  }, [entries, filters.search])

  return (
    <div className="flex flex-col lg:flex-row gap-6 p-4 md:p-6 bg-background min-h-[calc(100vh-48px)]">
      {/* Main column — header + timeline */}
      <div className="flex-1 min-w-0 space-y-4">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div className="flex items-center gap-2">
            <BookOpen className="h-4 w-4 text-foreground/60" />
            <h1 className="text-body font-medium text-foreground/80">Crew Journal</h1>
            <Badge variant="outline" className="text-[10px] border-border/60 font-mono uppercase tracking-wider">
              {entries.length} loaded
            </Badge>
            <StreamStatusBadge status={streamStatus} />
          </div>
          <div className="flex items-center gap-1.5">
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2.5 text-xs"
              onClick={() => refresh()}
              disabled={loading}
            >
              <RefreshCw className={cn("h-3 w-3 mr-1.5", loading && "animate-spin")} />
              Refresh
            </Button>
          </div>
        </div>

        <JournalTimeline
          entries={visibleEntries}
          loading={loading}
          loadingMore={loadingMore}
          hasMore={Boolean(nextCursor)}
          error={error}
          onLoadMore={loadMore}
        />
      </div>

      {/* Filter rail */}
      <JournalFilters workspaceId={workspaceId} value={filters} onChange={setFilters} />
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
