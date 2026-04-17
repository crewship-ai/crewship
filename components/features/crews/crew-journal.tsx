"use client"

import { useState } from "react"
import Link from "next/link"
import { BookOpen, Sparkles, ExternalLink, Loader2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { formatRelativeTime } from "@/lib/time"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"

const SEVERITY_PILL: Record<string, string> = {
  info: "bg-blue-500/15 text-blue-300 border-blue-500/30",
  notice: "bg-cyan-500/15 text-cyan-300 border-cyan-500/30",
  warn: "bg-amber-500/15 text-amber-300 border-amber-500/30",
  error: "bg-red-500/15 text-red-300 border-red-500/30",
}

interface CrewJournalProps {
  crewId: string
  workspaceId: string
}

/**
 * Condensed crew-scoped journal widget for the crew detail page. Replaces the
 * older CrewStandup component; it pulls the same 24h window from the journal
 * API and exposes a "Generate Summary" action that delegates to the backend
 * summarizer (gracefully degrades if the endpoint isn't wired yet).
 */
export function CrewJournal({ crewId, workspaceId }: CrewJournalProps) {
  const [summarizing, setSummarizing] = useState(false)

  const since = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString()
  const queryParams = { crew_id: crewId, since }

  const { entries, loading, prependLive, refresh } = useJournalList({
    workspaceId,
    params: queryParams,
    limit: 30,
  })

  useJournalStream({
    workspaceId,
    params: queryParams,
    onEntry: prependLive,
  })

  async function handleGenerateSummary() {
    setSummarizing(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${encodeURIComponent(crewId)}/journal/summarize?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST", headers: { "Content-Type": "application/json" } },
      )
      if (res.ok) {
        toast.success("Summary generation started")
        await refresh()
      } else if (res.status === 404) {
        toast.info("Summary generation not yet available")
      } else {
        toast.error(`Summary failed (${res.status})`)
      }
    } catch (err) {
      toast.error("Summary failed", {
        description: err instanceof Error ? err.message : undefined,
      })
    } finally {
      setSummarizing(false)
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-body font-medium flex items-center gap-2">
          <BookOpen className="h-4 w-4 text-muted-foreground" />
          Crew Journal (last 24h)
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {loading && entries.length === 0 ? (
          <div className="flex items-center gap-2 py-6 text-label text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" /> Loading journal…
          </div>
        ) : entries.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-8 text-center">
            <BookOpen className="h-7 w-7 text-muted-foreground/40" />
            <p className="text-body text-muted-foreground">No events in the last 24 hours.</p>
            <p className="text-label text-muted-foreground/70">
              Journal entries appear when agents communicate, run missions, or use tools.
            </p>
          </div>
        ) : (
          <ul className="divide-y divide-border/40 rounded-md border border-border/50 max-h-72 overflow-auto">
            {entries.slice(0, 30).map((e) => {
              const pill = SEVERITY_PILL[typeof e.severity === "string" ? e.severity : "info"] ?? SEVERITY_PILL.info
              return (
                <li key={e.id} className="flex items-start gap-2 px-2.5 py-1.5">
                  <Badge variant="outline" className={cn("text-[10px] border mt-0.5", pill)}>
                    {typeof e.severity === "string" ? e.severity : "info"}
                  </Badge>
                  <span className="flex-1 min-w-0 text-[12px] text-foreground/85 truncate">
                    <span className="text-muted-foreground font-mono text-[10px] mr-1.5">{e.entry_type}</span>
                    {e.summary}
                  </span>
                  <span className="text-[10px] text-muted-foreground font-mono tabular-nums shrink-0">
                    {formatRelativeTime(e.ts)}
                  </span>
                </li>
              )
            })}
          </ul>
        )}

        <div className="flex items-center justify-between pt-1">
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={handleGenerateSummary}
            disabled={summarizing}
          >
            {summarizing ? (
              <Loader2 className="h-3 w-3 mr-1.5 animate-spin" />
            ) : (
              <Sparkles className="h-3 w-3 mr-1.5" />
            )}
            Generate Summary
          </Button>
          <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" asChild>
            <Link href={`/journal?crew_id=${encodeURIComponent(crewId)}`}>
              View full journal <ExternalLink className="h-3 w-3 ml-1" />
            </Link>
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
