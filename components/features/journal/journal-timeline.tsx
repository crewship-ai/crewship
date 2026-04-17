"use client"

import { useEffect, useRef } from "react"
import { BookOpen, AlertCircle, Loader2 } from "lucide-react"
import { JournalEntryCard } from "./journal-entry-card"
import type { JournalEntry } from "@/lib/types/journal"

interface JournalTimelineProps {
  entries: JournalEntry[]
  loading: boolean
  loadingMore: boolean
  hasMore: boolean
  error: string | null
  onLoadMore: () => void
}

/**
 * Vertical timeline of journal entries. Uses an IntersectionObserver sentinel
 * at the bottom to trigger `onLoadMore` when the user scrolls near the end —
 * this is cheaper than a scroll handler and the parent can cap pagination
 * itself by passing `hasMore=false`.
 */
export function JournalTimeline({ entries, loading, loadingMore, hasMore, error, onLoadMore }: JournalTimelineProps) {
  const sentinelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const node = sentinelRef.current
    if (!node || !hasMore || loading) return
    const observer = new IntersectionObserver(
      (entriesIO) => {
        if (entriesIO.some((e) => e.isIntersecting)) {
          onLoadMore()
        }
      },
      { rootMargin: "300px" },
    )
    observer.observe(node)
    return () => observer.disconnect()
  }, [hasMore, loading, onLoadMore])

  if (loading && entries.length === 0) {
    return (
      <div className="flex items-center justify-center py-16 text-muted-foreground">
        <Loader2 className="h-4 w-4 mr-2 animate-spin" /> Loading timeline…
      </div>
    )
  }

  if (error && entries.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 py-16 text-center">
        <div className="w-10 h-10 rounded-lg bg-red-500/10 flex items-center justify-center">
          <AlertCircle className="h-4 w-4 text-red-400" />
        </div>
        <div className="text-sm text-foreground/80">Couldn&apos;t load journal</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">{error}</div>
      </div>
    )
  }

  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 py-16 text-center">
        <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
          <BookOpen className="h-4 w-4 text-muted-foreground/60" />
        </div>
        <div className="text-sm font-medium text-foreground/80">No events yet</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">
          Create a mission to see the crew in action.
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-1.5">
      {entries.map((entry) => (
        <JournalEntryCard key={entry.id} entry={entry} />
      ))}

      <div ref={sentinelRef} className="h-6" aria-hidden />

      {loadingMore && (
        <div className="flex items-center justify-center py-4 text-[11px] text-muted-foreground">
          <Loader2 className="h-3 w-3 mr-1.5 animate-spin" /> Loading more…
        </div>
      )}

      {!hasMore && !loadingMore && entries.length > 0 && (
        <div className="text-center py-4 text-[10px] text-muted-foreground/60 font-mono uppercase tracking-wider">
          End of timeline
        </div>
      )}
    </div>
  )
}
