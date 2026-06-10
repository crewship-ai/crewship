"use client"

import { useMemo, useState } from "react"
import { ScrollText, Search, Filter, Play } from "lucide-react"
import Link from "next/link"
import { usePipelines, type Pipeline } from "@/hooks/use-pipelines"
import { PipelineDetailSheet } from "@/components/features/orchestration/pipeline-detail-sheet"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

// RoutinesTab is the compact list view rendered as the 5th tab inside
// /orchestration. Click on a row opens the existing PipelineDetailSheet
// (Versions / Runs / Rollback / Export). For the full management
// surface (Editor, Schedules, Webhooks, Waitpoints, Run buttons), users
// jump to /routines via the sidebar — that page has the same list with
// a wider detail panel and the action toolbar.
//
// Why two surfaces (tab + standalone page): the tab is for users who
// are already in mission/issue context and want to invoke or inspect
// without leaving orchestration. The /routines page is the full
// asset-management surface, like /skills or /credentials. Both share
// the same usePipelines hook so data stays consistent.

interface RoutinesTabProps {
  workspaceId: string
}

export function RoutinesTab({ workspaceId }: RoutinesTabProps) {
  const { pipelines, loading, error } = usePipelines(workspaceId)
  const [search, setSearch] = useState("")
  const [statusFilter, setStatusFilter] = useState<"all" | "completed" | "failed" | "never">("all")
  const [selectedSlug, setSelectedSlug] = useState<string | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)

  const visible = useMemo(() => filterRoutines(pipelines, search, statusFilter), [pipelines, search, statusFilter])

  const handleOpen = (slug: string) => {
    setSelectedSlug(slug)
    setSheetOpen(true)
  }

  return (
    <div className="flex h-full flex-col gap-3 p-4 overflow-auto">
      {/* Header: search + filter chips + open-full-page CTA */}
      <div className="flex items-center gap-2">
        <div className="relative flex-1 max-w-xs">
          <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search routines…"
            className="h-8 pl-7 text-xs"
          />
        </div>
        <div className="flex items-center gap-1.5">
          <Filter className="h-3.5 w-3.5 text-muted-foreground" />
          {(["all", "completed", "failed", "never"] as const).map((s) => (
            <button
              key={s}
              onClick={() => setStatusFilter(s)}
              className={cn(
                "rounded-sm px-2 py-0.5 text-[11px] capitalize transition-colors",
                statusFilter === s
                  ? "bg-primary/15 text-primary-hover"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {s}
            </button>
          ))}
        </div>
        <div className="flex-1" />
        <Button asChild size="sm" variant="outline" className="h-8 text-xs">
          <Link href="/routines">Open full page →</Link>
        </Button>
      </div>

      {/* Status summary */}
      <div className="text-[11px] text-muted-foreground">
        {loading
          ? "Loading routines…"
          : error
            ? `Error: ${error}`
            : `${visible.length} of ${pipelines.length} routines · click to inspect, runs land in Graph tab`}
      </div>

      {/* List */}
      {visible.length === 0 ? (
        <EmptyState query={search} hasAny={pipelines.length > 0} />
      ) : (
        <ul className="flex flex-col gap-1.5">
          {visible.map((p) => (
            <RoutineRow key={p.id} routine={p} onOpen={() => handleOpen(p.slug)} />
          ))}
        </ul>
      )}

      <PipelineDetailSheet
        workspaceId={workspaceId}
        slug={selectedSlug}
        open={sheetOpen}
        onClose={() => {
          setSheetOpen(false)
          setSelectedSlug(null)
        }}
      />
    </div>
  )
}

function RoutineRow({ routine, onOpen }: { routine: Pipeline; onOpen: () => void }) {
  const status = routine.last_invocation_status?.toLowerCase()
  const dotColor =
    status === "completed"
      ? "bg-emerald-500"
      : status === "failed"
        ? "bg-red-500"
        : "bg-muted-foreground/40"

  return (
    <li
      onClick={onOpen}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onOpen()
        }
      }}
      className="group flex items-center gap-3 rounded-md border border-white/[0.06] bg-card/50 px-3 py-2 cursor-pointer hover:border-white/15 hover:bg-card transition-colors"
    >
      <ScrollText className="h-4 w-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{routine.name || routine.slug}</span>
          <span className="font-mono text-[10px] text-muted-foreground">{routine.slug}</span>
          {routine.ephemeral && (
            <Badge variant="outline" className="text-[9px] px-1 py-0">ephemeral</Badge>
          )}
        </div>
        {routine.description && (
          <p className="mt-0.5 truncate text-xs text-muted-foreground">{routine.description}</p>
        )}
      </div>
      <div className="flex items-center gap-2 shrink-0">
        <span className="text-[11px] text-muted-foreground tabular-nums">
          {routine.invocation_count} runs
        </span>
        <span
          className={cn("h-1.5 w-1.5 rounded-full", dotColor)}
          title={status ? `Last: ${status}` : "Never invoked"}
        />
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-2 opacity-0 group-hover:opacity-100 transition-opacity"
          onClick={(e) => {
            e.stopPropagation()
            onOpen()
          }}
          title="Inspect"
        >
          <Play className="h-3 w-3" />
        </Button>
      </div>
    </li>
  )
}

function EmptyState({ query, hasAny }: { query: string; hasAny: boolean }) {
  if (query) {
    return (
      <div className="rounded-md border border-dashed border-border/60 p-6 text-center text-sm text-muted-foreground">
        No routines match <span className="font-mono">{query}</span>.
      </div>
    )
  }
  if (!hasAny) {
    return (
      <div className="rounded-md border border-dashed border-border/60 p-8 text-center">
        <ScrollText className="mx-auto mb-3 h-8 w-8 text-muted-foreground/50" />
        <p className="text-sm font-medium">No routines yet in this workspace</p>
        <p className="mt-1 text-xs text-muted-foreground">
          Routines are repeatable AI workflow recipes. Agents can author them via the sidecar
          API, or you can import a bundle from <Link href="/routines" className="underline">the full Routines page</Link>.
        </p>
      </div>
    )
  }
  return (
    <div className="rounded-md border border-dashed border-border/60 p-6 text-center text-sm text-muted-foreground">
      No routines match the current filter.
    </div>
  )
}

function filterRoutines(
  routines: Pipeline[],
  query: string,
  status: "all" | "completed" | "failed" | "never",
): Pipeline[] {
  const q = query.trim().toLowerCase()
  return routines.filter((p) => {
    if (q) {
      const haystack = `${p.slug} ${p.name} ${p.description ?? ""}`.toLowerCase()
      if (!haystack.includes(q)) return false
    }
    if (status === "all") return true
    if (status === "never") return p.invocation_count === 0
    return p.last_invocation_status?.toLowerCase() === status
  })
}
