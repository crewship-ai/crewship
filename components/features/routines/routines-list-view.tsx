"use client"

import { useState } from "react"
import { ScrollText, RefreshCw, ArrowUpDown, ArrowUp, ArrowDown } from "lucide-react"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

// RoutinesListView — the main /routines page table when the Routines
// tab is active. Reuses Pipeline rows from usePipelines (filtered by
// the layout); presents them as a sortable table with rich columns
// (name, slug, runs, last status, author, updated). Click selects a
// row → routine detail panel opens on the right.

interface Props {
  routines: Pipeline[]
  loading: boolean
  error: string | null
  selectedSlug: string | null
  onSelect: (slug: string) => void
  onRefresh: () => void
}

type SortKey = "name" | "invocation_count" | "last_invoked_at" | "updated_at" | "status"
type SortDir = "asc" | "desc"

export function RoutinesListView({ routines, loading, error, selectedSlug, onSelect, onRefresh }: Props) {
  const [sortKey, setSortKey] = useState<SortKey>("invocation_count")
  const [sortDir, setSortDir] = useState<SortDir>("desc")

  const sorted = [...routines].sort((a, b) => {
    const cmp = compareBy(a, b, sortKey)
    return sortDir === "asc" ? cmp : -cmp
  })

  const toggleSort = (key: SortKey) => {
    if (sortKey === key) setSortDir((d) => (d === "asc" ? "desc" : "asc"))
    else {
      setSortKey(key)
      setSortDir(key === "name" ? "asc" : "desc")
    }
  }

  return (
    <div className="flex h-full flex-col">
      {/* Header strip */}
      <div className="flex items-center justify-between border-b border-white/[0.06] bg-card/30 px-4 py-2 shrink-0">
        <div className="text-xs text-muted-foreground">
          {loading ? "Loading…" : error ? <span className="text-red-400">Error: {error}</span> : `${routines.length} routines`}
        </div>
        <Button size="sm" variant="ghost" onClick={onRefresh} className="h-7 gap-1.5 text-xs">
          <RefreshCw className={cn("h-3 w-3", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* Table */}
      {!loading && routines.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="flex-1 overflow-auto">
          <table className="w-full text-xs">
            <thead className="sticky top-0 z-10 border-b border-white/[0.06] bg-card/80 backdrop-blur-sm">
              <tr className="text-left text-[11px] uppercase tracking-wider text-muted-foreground">
                <th className="px-4 py-2 font-medium">
                  <SortBtn label="Routine" col="name" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                </th>
                <th className="px-3 py-2 font-medium">
                  <SortBtn label="Last status" col="status" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  <SortBtn
                    label="Runs"
                    col="invocation_count"
                    sortKey={sortKey}
                    sortDir={sortDir}
                    onClick={toggleSort}
                    align="right"
                  />
                </th>
                <th className="px-3 py-2 font-medium">
                  <SortBtn label="Last run" col="last_invoked_at" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                </th>
                <th className="px-3 py-2 font-medium">Author</th>
                <th className="px-3 py-2 font-medium">
                  <SortBtn label="Updated" col="updated_at" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                </th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((r) => (
                <RoutineRow key={r.id} routine={r} selected={selectedSlug === r.slug} onClick={() => onSelect(r.slug)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function SortBtn({
  label,
  col,
  sortKey,
  sortDir,
  onClick,
  align = "left",
}: {
  label: string
  col: SortKey
  sortKey: SortKey
  sortDir: SortDir
  onClick: (col: SortKey) => void
  align?: "left" | "right"
}) {
  const active = sortKey === col
  const Icon = !active ? ArrowUpDown : sortDir === "asc" ? ArrowUp : ArrowDown
  return (
    <button
      onClick={() => onClick(col)}
      className={cn(
        "flex items-center gap-1 transition-colors hover:text-foreground",
        active ? "text-foreground" : "",
        align === "right" && "ml-auto justify-end",
      )}
    >
      <span>{label}</span>
      <Icon className="h-2.5 w-2.5" />
    </button>
  )
}

function RoutineRow({
  routine,
  selected,
  onClick,
}: {
  routine: Pipeline
  selected: boolean
  onClick: () => void
}) {
  const status = routine.last_invocation_status?.toLowerCase()
  const statusBadge = !status ? null : (
    <Badge
      variant="outline"
      className={cn(
        "text-[10px] capitalize",
        status === "completed" && "border-emerald-500/30 text-emerald-400",
        status === "failed" && "border-red-500/30 text-red-400",
        status !== "completed" && status !== "failed" && "border-border text-muted-foreground",
      )}
    >
      {status}
    </Badge>
  )

  return (
    <tr
      onClick={onClick}
      className={cn(
        "cursor-pointer border-b border-white/[0.04] transition-colors hover:bg-muted/40",
        selected && "bg-blue-500/10",
      )}
    >
      <td className="px-4 py-2.5">
        <div className="flex items-center gap-2">
          <ScrollText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{routine.name || routine.slug}</span>
              {routine.ephemeral && (
                <Badge variant="outline" className="px-1 py-0 text-[9px]">ephemeral</Badge>
              )}
            </div>
            <div className="font-mono text-[10px] text-muted-foreground">{routine.slug}</div>
          </div>
        </div>
      </td>
      <td className="px-3 py-2.5">{statusBadge ?? <span className="text-[10px] text-muted-foreground">never invoked</span>}</td>
      <td className="px-3 py-2.5 text-right font-mono tabular-nums">{routine.invocation_count}</td>
      <td className="px-3 py-2.5 text-muted-foreground">
        {routine.last_invoked_at ? formatRelative(routine.last_invoked_at) : "—"}
      </td>
      <td className="px-3 py-2.5">
        <span className="font-mono text-[10px] text-muted-foreground">
          {routine.author_crew_id ? truncate(routine.author_crew_id, 16) : "—"}
        </span>
        <div className="text-[9px] capitalize text-muted-foreground/70">
          {routine.authored_via.replace(/_/g, " ")}
        </div>
      </td>
      <td className="px-3 py-2.5 text-muted-foreground">{formatRelative(routine.updated_at)}</td>
    </tr>
  )
}

function EmptyState() {
  // Three quickstart paths surface the same flows the docs explain.
  // Visual quickstart > a single explanation paragraph because users
  // who land here cold scan more than they read. Each card dives at
  // the concrete next click (routes that are wired in this PR), no
  // dead ends.
  const quickstarts: Array<{
    icon: string
    title: string
    description: string
    cta: string
    href?: string
  }> = [
    {
      icon: "+",
      title: "Create from a template",
      description: "Open the New routine dialog and pick a starter (Empty / Summarize / Two-step). Edit the JSON DSL, then Test & Save.",
      cta: "Click + New routine in the toolbar above",
    },
    {
      icon: "⬇",
      title: "Import from another workspace",
      description: "Bundle JSON exports from another Crewship workspace drop in via Import. Slugs are preserved; conflicts update the existing routine.",
      cta: "Click Import in the toolbar above",
    },
    {
      icon: "🤖",
      title: "Let an agent author one",
      description: "Agents that spot a repetitive pattern call POST localhost:9119/pipelines/save from inside their container. The next [AVAILABLE ROUTINES] block advertises the new routine to other crews.",
      cta: "Open any chat with an authoring-tier agent",
    },
  ]

  return (
    <div className="flex flex-1 flex-col items-center justify-center p-12">
      <div className="mb-8 max-w-xl text-center">
        <ScrollText className="mx-auto mb-4 h-12 w-12 text-muted-foreground/40" />
        <h3 className="mb-2 text-sm font-medium">No routines in this workspace yet</h3>
        <p className="text-xs text-muted-foreground">
          Routines are repeatable AI workflow recipes — workspace-scoped, declarative, AI-authored or hand-written.
          Three ways to get the first one in:
        </p>
      </div>
      <div className="grid w-full max-w-3xl grid-cols-1 gap-3 md:grid-cols-3">
        {quickstarts.map((q) => (
          <div
            key={q.title}
            className="rounded-md border border-white/[0.06] bg-card/40 p-4"
          >
            <div className="mb-2 inline-flex h-7 w-7 items-center justify-center rounded-md bg-blue-500/15 text-sm text-blue-300">
              {q.icon}
            </div>
            <h4 className="mb-1 text-xs font-medium">{q.title}</h4>
            <p className="mb-3 text-[11px] text-muted-foreground leading-relaxed">{q.description}</p>
            <p className="text-[10px] uppercase tracking-wider text-muted-foreground/70">{q.cta}</p>
          </div>
        ))}
      </div>
      <p className="mt-6 max-w-xl text-center text-[11px] text-muted-foreground/70">
        See the <a className="underline" href="/docs/guides/routines">Routines guide</a> for the DSL spec, all 6 step types, two-tier execution, validation gates, and trigger setup.
      </p>
    </div>
  )
}

function compareBy(a: Pipeline, b: Pipeline, key: SortKey): number {
  switch (key) {
    case "name":
      return (a.name || a.slug).localeCompare(b.name || b.slug)
    case "invocation_count":
      return a.invocation_count - b.invocation_count
    case "last_invoked_at":
      return (a.last_invoked_at ?? "").localeCompare(b.last_invoked_at ?? "")
    case "updated_at":
      return a.updated_at.localeCompare(b.updated_at)
    case "status":
      return (a.last_invocation_status ?? "").localeCompare(b.last_invocation_status ?? "")
  }
}

function formatRelative(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return "—"
  const diffMs = Date.now() - then
  const sec = Math.floor(diffMs / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const day = Math.floor(hr / 24)
  if (day < 30) return `${day}d ago`
  return new Date(iso).toLocaleDateString()
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + "…"
}
