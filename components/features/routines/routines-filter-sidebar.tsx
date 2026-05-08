"use client"

import { useMemo } from "react"
import type { Pipeline } from "@/hooks/use-pipelines"
import { cn } from "@/lib/utils"

// RoutinesFilterSidebar — left panel saved-view-style filter facets.
// Mirrors orchestration's project sidebar shape but with routine-
// specific dimensions: invocation status, popularity, authorship
// origin (agent / user / imported / seed), and ephemeral toggle.
//
// Counts are computed locally from the parent's already-fetched list,
// avoiding a separate API call per facet.

export interface RoutineFilters {
  status: "all" | "completed" | "failed" | "never"
  invocations: "all" | "popular" | "fresh"
  authoredVia: "all" | "agent_tool_call" | "user_api" | "imported" | "seed"
  showEphemeral: boolean
}

interface Props {
  filters: RoutineFilters
  onChange: (next: RoutineFilters) => void
  routines: Pipeline[]
  totalRoutines: number
  filteredCount: number
}

export function RoutinesFilterSidebar({ filters, onChange, routines, totalRoutines, filteredCount }: Props) {
  const counts = useMemo(() => {
    let completed = 0
    let failed = 0
    let never = 0
    let popular = 0
    let fresh = 0
    let agent = 0
    let user = 0
    let imported = 0
    let seed = 0
    let ephemeral = 0
    for (const p of routines) {
      const s = p.last_invocation_status?.toLowerCase()
      if (p.invocation_count === 0) never++
      if (s === "completed") completed++
      if (s === "failed") failed++
      if (p.invocation_count >= 10) popular++
      if (p.invocation_count === 0) fresh++
      if (p.authored_via === "agent_tool_call") agent++
      else if (p.authored_via === "user_api") user++
      else if (p.authored_via === "imported") imported++
      else if (p.authored_via === "seed") seed++
      if (p.ephemeral) ephemeral++
    }
    return { completed, failed, never, popular, fresh, agent, user, imported, seed, ephemeral }
  }, [routines])

  return (
    <div className="flex-1 overflow-y-auto px-3 py-3 text-xs">
      {/* Counts strip */}
      <div className="mb-3 rounded-md bg-muted/40 px-2 py-1.5">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">Showing</div>
        <div className="text-sm font-medium">
          {filteredCount} <span className="text-muted-foreground font-normal">of {totalRoutines}</span>
        </div>
      </div>

      <Section label="Status">
        <FacetBtn
          label="All"
          count={totalRoutines}
          active={filters.status === "all"}
          onClick={() => onChange({ ...filters, status: "all" })}
        />
        <FacetBtn
          label="Completed"
          count={counts.completed}
          active={filters.status === "completed"}
          onClick={() => onChange({ ...filters, status: "completed" })}
          dot="emerald"
        />
        <FacetBtn
          label="Failed"
          count={counts.failed}
          active={filters.status === "failed"}
          onClick={() => onChange({ ...filters, status: "failed" })}
          dot="red"
        />
        <FacetBtn
          label="Never invoked"
          count={counts.never}
          active={filters.status === "never"}
          onClick={() => onChange({ ...filters, status: "never" })}
          dot="muted"
        />
      </Section>

      <Section label="Usage">
        <FacetBtn
          label="All"
          count={totalRoutines}
          active={filters.invocations === "all"}
          onClick={() => onChange({ ...filters, invocations: "all" })}
        />
        <FacetBtn
          label="Popular (10+ runs)"
          count={counts.popular}
          active={filters.invocations === "popular"}
          onClick={() => onChange({ ...filters, invocations: "popular" })}
        />
        <FacetBtn
          label="Fresh (no runs)"
          count={counts.fresh}
          active={filters.invocations === "fresh"}
          onClick={() => onChange({ ...filters, invocations: "fresh" })}
        />
      </Section>

      <Section label="Authored by">
        <FacetBtn
          label="All"
          count={totalRoutines}
          active={filters.authoredVia === "all"}
          onClick={() => onChange({ ...filters, authoredVia: "all" })}
        />
        <FacetBtn
          label="Agent tool call"
          count={counts.agent}
          active={filters.authoredVia === "agent_tool_call"}
          onClick={() => onChange({ ...filters, authoredVia: "agent_tool_call" })}
        />
        <FacetBtn
          label="User API"
          count={counts.user}
          active={filters.authoredVia === "user_api"}
          onClick={() => onChange({ ...filters, authoredVia: "user_api" })}
        />
        <FacetBtn
          label="Imported"
          count={counts.imported}
          active={filters.authoredVia === "imported"}
          onClick={() => onChange({ ...filters, authoredVia: "imported" })}
        />
        <FacetBtn
          label="Seed"
          count={counts.seed}
          active={filters.authoredVia === "seed"}
          onClick={() => onChange({ ...filters, authoredVia: "seed" })}
        />
      </Section>

      <Section label="Visibility">
        <label className="flex cursor-pointer items-center justify-between rounded px-2 py-1 hover:bg-muted/40">
          <span className="text-foreground/80">Show ephemeral</span>
          <input
            type="checkbox"
            checked={filters.showEphemeral}
            onChange={(e) => onChange({ ...filters, showEphemeral: e.target.checked })}
            className="h-3 w-3 cursor-pointer accent-blue-500"
          />
        </label>
        <p className="px-2 text-[10px] text-muted-foreground">
          {counts.ephemeral} ephemeral routines exist (auto-generated delegation wraps).
        </p>
      </Section>
    </div>
  )
}

function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <div className="mb-1 px-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      <div className="flex flex-col gap-0.5">{children}</div>
    </div>
  )
}

function FacetBtn({
  label,
  count,
  active,
  onClick,
  dot,
}: {
  label: string
  count: number
  active: boolean
  onClick: () => void
  dot?: "emerald" | "red" | "muted"
}) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "flex items-center justify-between rounded px-2 py-1 transition-colors",
        active ? "bg-blue-500/15 text-blue-300" : "text-foreground/80 hover:bg-muted/40",
      )}
    >
      <span className="flex items-center gap-1.5 truncate">
        {dot && (
          <span
            className={cn(
              "h-1.5 w-1.5 shrink-0 rounded-full",
              dot === "emerald" ? "bg-emerald-500" : dot === "red" ? "bg-red-500" : "bg-muted-foreground/40",
            )}
          />
        )}
        <span className="truncate">{label}</span>
      </span>
      <span className="font-mono text-[10px] tabular-nums text-muted-foreground">{count}</span>
    </button>
  )
}
