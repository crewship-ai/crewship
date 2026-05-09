"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { ChevronDown, Search, X } from "lucide-react"
import type { Pipeline } from "@/hooks/use-pipelines"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { cn } from "@/lib/utils"

// RoutinesFilterSidebar — left panel for /routines. Visually mirrors
// the /issues sidebar (UnifiedExplorer): same bg, same collapsible
// section headers, same blue-line selected state, same search bar
// chrome. Filter facets stay routine-specific (status, usage, author
// agent), but the styling is identical so the two pages feel like
// the same app.

export interface RoutineFilters {
  status: "all" | "completed" | "failed" | "never"
  invocations: "all" | "popular" | "fresh"
  authorAgentId: string | null
  showEphemeral: boolean
}

interface Props {
  filters: RoutineFilters
  onChange: (next: RoutineFilters) => void
  routines: Pipeline[]
  totalRoutines: number
  filteredCount: number
  search: string
  onSearchChange: (value: string) => void
}

const sectionAnim = {
  initial: { height: 0, opacity: 0 },
  animate: { height: "auto", opacity: 1, transition: { duration: 0.2, ease: "easeOut" as const } },
  exit: { height: 0, opacity: 0, transition: { duration: 0.15, ease: "easeIn" as const } },
}

export function RoutinesFilterSidebar({
  filters,
  onChange,
  routines,
  totalRoutines,
  filteredCount,
  search,
  onSearchChange,
}: Props) {
  const [statusOpen, setStatusOpen] = useState(true)
  const [usageOpen, setUsageOpen] = useState(true)
  const [agentsOpen, setAgentsOpen] = useState(true)

  const counts = useMemo(() => {
    let completed = 0
    let failed = 0
    let never = 0
    let popular = 0
    let fresh = 0
    let ephemeral = 0
    for (const p of routines) {
      const s = p.last_invocation_status?.toLowerCase()
      if (p.invocation_count === 0) {
        never++
        fresh++
      }
      if (s === "completed") completed++
      if (s === "failed") failed++
      if (p.invocation_count >= 10) popular++
      if (p.ephemeral) ephemeral++
    }
    return { completed, failed, never, popular, fresh, ephemeral }
  }, [routines])

  // Build the unique author-agent list from the loaded routines plus
  // a per-agent count. We don't query /agents separately — the routine
  // payload now carries author_agent_name (backend join), so the list
  // shows up immediately and stays consistent with what the user sees.
  const agents = useMemo(() => {
    const map = new Map<string, { id: string; name: string; count: number }>()
    for (const p of routines) {
      if (!p.author_agent_id) continue
      const cur = map.get(p.author_agent_id)
      if (cur) {
        cur.count++
      } else {
        map.set(p.author_agent_id, {
          id: p.author_agent_id,
          name: p.author_agent_name ?? p.author_agent_id.slice(0, 8),
          count: 1,
        })
      }
    }
    return Array.from(map.values()).sort((a, b) => a.name.localeCompare(b.name))
  }, [routines])

  return (
    <div className="flex h-full flex-col">
      {/* ── Search ── */}
      <div className="shrink-0 px-2 py-2">
        <div className="flex h-8 items-center gap-1.5 rounded-md border border-white/[0.08] bg-white/[0.04] px-2.5">
          <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
          <input
            type="text"
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder="Search routines, agents..."
            className="min-w-0 flex-1 bg-transparent text-xs text-foreground placeholder:text-muted-foreground/40 outline-none"
          />
          <AnimatePresence>
            {search && (
              <motion.button
                initial={{ opacity: 0, scale: 0.5 }}
                animate={{ opacity: 1, scale: 1 }}
                exit={{ opacity: 0, scale: 0.5 }}
                onClick={() => onSearchChange("")}
                className="text-muted-foreground/50 hover:text-foreground"
              >
                <X className="h-3.5 w-3.5" />
              </motion.button>
            )}
          </AnimatePresence>
        </div>
      </div>

      {/* ── Counts strip ── */}
      <div className="mx-2 mb-2 shrink-0 rounded-md border border-white/[0.06] bg-white/[0.02] px-2 py-1.5">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60">Showing</div>
        <div className="text-sm font-medium tabular-nums">
          {filteredCount} <span className="font-normal text-muted-foreground/60">of {totalRoutines}</span>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto">
        {/* ── Status ── */}
        <Section
          label="Status"
          count={null}
          open={statusOpen}
          onToggle={() => setStatusOpen((v) => !v)}
        >
          <FacetRow
            label="All"
            count={totalRoutines}
            active={filters.status === "all"}
            onClick={() => onChange({ ...filters, status: "all" })}
          />
          <FacetRow
            label="Completed"
            count={counts.completed}
            active={filters.status === "completed"}
            dot="emerald"
            onClick={() => onChange({ ...filters, status: "completed" })}
          />
          <FacetRow
            label="Failed"
            count={counts.failed}
            active={filters.status === "failed"}
            dot="red"
            onClick={() => onChange({ ...filters, status: "failed" })}
          />
          <FacetRow
            label="Never invoked"
            count={counts.never}
            active={filters.status === "never"}
            dot="muted"
            onClick={() => onChange({ ...filters, status: "never" })}
          />
        </Section>

        {/* ── Usage ── */}
        <Section
          label="Usage"
          count={null}
          open={usageOpen}
          onToggle={() => setUsageOpen((v) => !v)}
        >
          <FacetRow
            label="All"
            count={totalRoutines}
            active={filters.invocations === "all"}
            onClick={() => onChange({ ...filters, invocations: "all" })}
          />
          <FacetRow
            label="Popular (10+)"
            count={counts.popular}
            active={filters.invocations === "popular"}
            onClick={() => onChange({ ...filters, invocations: "popular" })}
          />
          <FacetRow
            label="Fresh (no runs)"
            count={counts.fresh}
            active={filters.invocations === "fresh"}
            onClick={() => onChange({ ...filters, invocations: "fresh" })}
          />
        </Section>

        {/* ── Agents (authored by) ── */}
        {agents.length > 0 && (
          <Section
            label="Agents"
            count={agents.length}
            open={agentsOpen}
            onToggle={() => setAgentsOpen((v) => !v)}
          >
            <FacetRow
              label="All authors"
              count={totalRoutines}
              active={filters.authorAgentId === null}
              onClick={() => onChange({ ...filters, authorAgentId: null })}
            />
            {agents.map((a) => (
              <FacetRow
                key={a.id}
                label={a.name}
                count={a.count}
                active={filters.authorAgentId === a.id}
                avatar={a.id}
                onClick={() => onChange({ ...filters, authorAgentId: a.id })}
              />
            ))}
          </Section>
        )}

        {/* ── Visibility (ephemeral toggle) ── */}
        <div className="border-t border-white/[0.06]">
          <div className="px-3 py-2">
            <label className="flex cursor-pointer items-center justify-between rounded px-2 py-1 text-xs hover:bg-white/[0.04]">
              <span className="text-foreground/80">Show ephemeral</span>
              <input
                type="checkbox"
                checked={filters.showEphemeral}
                onChange={(e) => onChange({ ...filters, showEphemeral: e.target.checked })}
                className="h-3 w-3 cursor-pointer accent-blue-500"
              />
            </label>
            <p className="mt-1 px-2 text-[10px] text-muted-foreground/60">
              {counts.ephemeral} ephemeral routines (auto-generated delegation wraps).
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}

interface SectionProps {
  label: string
  count: number | null
  open: boolean
  onToggle: () => void
  children: React.ReactNode
}

function Section({ label, count, open, onToggle, children }: SectionProps) {
  return (
    <div className="border-b border-white/[0.06]">
      <button
        onClick={onToggle}
        className="flex w-full items-center gap-1.5 px-3 py-1.5 hover:bg-white/[0.02]"
      >
        <motion.div animate={{ rotate: open ? 0 : -90 }} transition={{ duration: 0.15 }}>
          <ChevronDown className="h-3 w-3 text-muted-foreground/40" />
        </motion.div>
        <span className="flex-1 text-left text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
          {label}
        </span>
        {count !== null && (
          <span className="text-[10px] tabular-nums text-foreground/35">{count}</span>
        )}
      </button>
      <AnimatePresence initial={false}>
        {open && (
          <motion.div {...sectionAnim} className="overflow-hidden">
            <div className="pb-1">{children}</div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

interface FacetRowProps {
  label: string
  count: number
  active: boolean
  onClick: () => void
  dot?: "emerald" | "red" | "muted"
  avatar?: string
}

function FacetRow({ label, count, active, onClick, dot, avatar }: FacetRowProps) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 px-3 py-1.5 text-left transition-colors",
        active
          ? "border-l-2 border-blue-500 bg-blue-500/10"
          : "border-l-2 border-transparent hover:bg-white/[0.04]",
      )}
    >
      {avatar ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img src={getAgentAvatarUrl(avatar)} alt="" className="h-4 w-4 shrink-0 rounded-full" />
      ) : dot ? (
        <span
          className={cn(
            "h-1.5 w-1.5 shrink-0 rounded-full",
            dot === "emerald" && "bg-emerald-500",
            dot === "red" && "bg-red-500",
            dot === "muted" && "bg-muted-foreground/40",
          )}
        />
      ) : (
        <span className="h-1.5 w-1.5 shrink-0" />
      )}
      <span className={cn("flex-1 truncate text-xs", active ? "text-blue-300" : "text-foreground/80")}>
        {label}
      </span>
      <span className="font-mono text-[10px] tabular-nums text-foreground/35">{count}</span>
    </button>
  )
}
