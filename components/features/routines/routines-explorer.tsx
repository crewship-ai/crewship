"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  ScrollText,
  CheckCircle2,
  XCircle,
  CircleDashed,
  Sparkles,
  Flame,
  Users,
  EyeOff,
} from "lucide-react"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  SidebarToolbar,
  SidebarSearch,
  SidebarFilterButton,
  SidebarSection,
  SidebarRow,
  SidebarCollapseButton,
} from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { RoutineFilters } from "./routines-filter-sidebar"

// RoutinesExplorer — the /routines left sidebar, built on the shared
// sidebar-kit primitives (SidebarToolbar/Search/FilterButton/Section/
// Row) so it reads as one app with every other in-page sidebar. Search
// + Filter chrome up top, a collapsible STATUS bucket section, and the
// ROUTINES list (per-row status dot + name + invocation count + author
// avatar). The usage/author/visibility facets stay routine-specific and
// live in the Filter popover; everything routes selection through
// SidebarRow's tokenized brand accent-bar.

interface RoutinesExplorerProps {
  routines: Pipeline[]
  search: string
  onSearchChange: (value: string) => void
  selectedSlug: string | null
  onSelectRoutine: (slug: string) => void
  filters: RoutineFilters
  onChange: (next: RoutineFilters) => void
  /** Collapse toggle — rendered in the toolbar next to search. */
  onToggleCollapse?: () => void
}

const dropdownAnim = {
  initial: { opacity: 0, scale: 0.95, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } },
  exit: { opacity: 0, scale: 0.95, y: -4, transition: { duration: 0.1 } },
}

type StatusBucket = RoutineFilters["status"]

const STATUS_BUCKETS: { id: StatusBucket; label: string; icon: typeof ScrollText; tone: string }[] = [
  { id: "all", label: "All", icon: ScrollText, tone: "text-foreground/70" },
  { id: "completed", label: "Completed", icon: CheckCircle2, tone: "text-emerald-400" },
  { id: "failed", label: "Failed", icon: XCircle, tone: "text-rose-400" },
  { id: "never", label: "Never invoked", icon: CircleDashed, tone: "text-muted-foreground" },
]

export function RoutinesExplorer({
  routines,
  search,
  onSearchChange,
  selectedSlug,
  onSelectRoutine,
  filters,
  onChange,
  onToggleCollapse,
}: RoutinesExplorerProps) {
  const [statusOpen, setStatusOpen] = useState(true)
  const [filterDropdownOpen, setFilterDropdownOpen] = useState(false)

  // Counts per status bucket (computed against the workspace-wide list,
  // not the post-status-filter view — otherwise switching to "Failed"
  // would make every other bucket show 0).
  const statusCounts = useMemo(() => {
    const c: Record<StatusBucket, number> = { all: routines.length, completed: 0, failed: 0, never: 0 }
    for (const p of routines) {
      const s = p.last_invocation_status?.toLowerCase()
      if (p.invocation_count === 0) c.never++
      if (s === "completed") c.completed++
      if (s === "failed") c.failed++
    }
    return c
  }, [routines])

  // Author agents derived from loaded routines — same as Issues' agents
  // facet but using author_agent_id/name instead of assignee.
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

  // Active facet count for the Filter button badge. Status has its own
  // section above, so it's excluded here — only the popover facets
  // (Usage / Authors / Visibility) count toward the badge.
  const activeFilterCount = useMemo(() => {
    let n = 0
    if (filters.invocations !== "all") n++
    if (filters.authorAgentId) n++
    if (filters.showEphemeral) n++
    return n
  }, [filters])

  // Routines visible in the sidebar list section — search + facet
  // filters applied. Status bucket is handled by the sidebar STATUS
  // section above the list, the rest by the Filter dropdown.
  const displayed = useMemo(() => {
    let filtered = routines
    if (filters.status !== "all") {
      if (filters.status === "never") {
        filtered = filtered.filter((p) => p.invocation_count === 0)
      } else {
        filtered = filtered.filter(
          (p) => p.last_invocation_status?.toLowerCase() === filters.status,
        )
      }
    }
    if (filters.invocations === "popular") {
      filtered = filtered.filter((p) => p.invocation_count >= 10)
    }
    if (filters.invocations === "fresh") {
      filtered = filtered.filter((p) => p.invocation_count === 0)
    }
    if (filters.authorAgentId) {
      filtered = filtered.filter((p) => p.author_agent_id === filters.authorAgentId)
    }
    if (!filters.showEphemeral) {
      filtered = filtered.filter((p) => !p.ephemeral)
    }
    if (search) {
      const q = search.toLowerCase()
      filtered = filtered.filter(
        (p) =>
          p.slug.toLowerCase().includes(q) ||
          p.name.toLowerCase().includes(q) ||
          (p.description ?? "").toLowerCase().includes(q) ||
          (p.author_agent_name ?? "").toLowerCase().includes(q),
      )
    }
    return filtered
  }, [routines, search, filters])

  return (
    <div className="flex flex-col h-full">
      {/* ── Search + Filter ── */}
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder="Search routines, agents…"
        />
        <div className="relative shrink-0">
          <SidebarFilterButton
            activeCount={activeFilterCount}
            onClick={() => setFilterDropdownOpen(!filterDropdownOpen)}
          />
          <AnimatePresence>
            {filterDropdownOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterDropdownOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  className="absolute right-0 top-9 z-50 bg-card border border-white/[0.1] rounded-lg shadow-xl py-1 min-w-[200px] max-h-[360px] overflow-y-auto"
                >
                  <div className="px-3 py-1 text-[9px] font-semibold text-muted-foreground-soft uppercase tracking-wider">Usage</div>
                  {(["all", "popular", "fresh"] as RoutineFilters["invocations"][]).map((v) => (
                    <button
                      key={v}
                      onClick={() => {
                        onChange({ ...filters, invocations: v })
                        setFilterDropdownOpen(false)
                      }}
                      className={cn(
                        "w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2",
                        filters.invocations === v ? "text-primary-hover" : "text-muted-foreground/80",
                      )}
                    >
                      {v === "popular" && <Flame className="h-3.5 w-3.5 shrink-0" />}
                      {v === "fresh" && <Sparkles className="h-3.5 w-3.5 shrink-0" />}
                      {v === "all" && <ScrollText className="h-3.5 w-3.5 shrink-0 opacity-60" />}
                      {v === "all" ? "All usage" : v === "popular" ? "Popular (10+)" : "Fresh (no runs)"}
                    </button>
                  ))}
                  {agents.length > 0 && (
                    <>
                      <div className="border-t border-white/[0.06] mt-1" />
                      <div className="px-3 py-1 text-[9px] font-semibold text-muted-foreground-soft uppercase tracking-wider">
                        Authors
                      </div>
                      <button
                        onClick={() => {
                          onChange({ ...filters, authorAgentId: null })
                          setFilterDropdownOpen(false)
                        }}
                        className={cn(
                          "w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2",
                          filters.authorAgentId === null ? "text-primary-hover" : "text-muted-foreground/80",
                        )}
                      >
                        <Users className="h-3.5 w-3.5 shrink-0 opacity-60" />
                        All authors
                      </button>
                      {agents.map((a) => (
                        <button
                          key={a.id}
                          onClick={() => {
                            onChange({ ...filters, authorAgentId: a.id })
                            setFilterDropdownOpen(false)
                          }}
                          className={cn(
                            "w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2",
                            filters.authorAgentId === a.id ? "text-primary-hover" : "text-muted-foreground/80",
                          )}
                        >
                          <span
                            aria-hidden
                            className="h-4 w-4 shrink-0 rounded-full bg-cover bg-center"
                            style={{ backgroundImage: `url(${getAgentAvatarUrl(a.id)})` }}
                          />
                          <span className="truncate flex-1">{a.name}</span>
                          <span className="text-[10px] tabular-nums text-muted-foreground-soft">{a.count}</span>
                        </button>
                      ))}
                    </>
                  )}
                  <div className="border-t border-white/[0.06] mt-1" />
                  <div className="px-3 py-1 text-[9px] font-semibold text-muted-foreground-soft uppercase tracking-wider">
                    Visibility
                  </div>
                  <button
                    onClick={() => onChange({ ...filters, showEphemeral: !filters.showEphemeral })}
                    className={cn(
                      "w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2",
                      filters.showEphemeral ? "text-primary-hover" : "text-muted-foreground/80",
                    )}
                  >
                    <EyeOff className="h-3.5 w-3.5 shrink-0" />
                    {filters.showEphemeral ? "Hiding nothing" : "Show ephemeral"}
                  </button>
                </motion.div>
              </>
            )}
          </AnimatePresence>
        </div>
        {onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />}
      </SidebarToolbar>

      {/* ── Status ── (single-select bucket) */}
      <SidebarSection
        label="Status"
        count={STATUS_BUCKETS.length}
        collapsible
        collapsed={!statusOpen}
        onToggle={() => setStatusOpen(!statusOpen)}
        className="border-b border-white/[0.06]"
      >
        {STATUS_BUCKETS.map((b) => {
          const IconComp = b.icon
          const isSelected = filters.status === b.id
          const count = statusCounts[b.id]
          return (
            <SidebarRow
              key={b.id}
              selected={isSelected}
              onSelect={() => onChange({ ...filters, status: b.id })}
            >
              <IconComp className={cn("h-3.5 w-3.5 shrink-0", b.tone)} />
              <span className="text-foreground/80 truncate flex-1">{b.label}</span>
              <span className="text-[10px] text-muted-foreground-soft tabular-nums">{count}</span>
            </SidebarRow>
          )
        })}
      </SidebarSection>

      {/* ── Routines ── */}
      <div className="flex-1 min-h-0 flex flex-col">
        <SidebarSection label="Routines" count={displayed.length} />
        <div className="flex-1 min-h-0 overflow-y-auto pb-1">
          <TooltipProvider delayDuration={400}>
            {displayed.map((routine) => {
              const isSelected = selectedSlug === routine.slug
              const lastStatus = routine.last_invocation_status?.toLowerCase()
              const statusTone =
                lastStatus === "completed"
                  ? "bg-emerald-500"
                  : lastStatus === "failed"
                    ? "bg-rose-500"
                    : routine.invocation_count === 0
                      ? "bg-muted-foreground/30"
                      : "bg-primary"
              return (
                <Tooltip key={routine.id}>
                  <TooltipTrigger asChild>
                    <div>
                      <SidebarRow
                        selected={isSelected}
                        onSelect={() => onSelectRoutine(routine.slug)}
                      >
                        <span
                          aria-hidden
                          title={lastStatus ?? "never invoked"}
                          className={cn("h-2 w-2 shrink-0 rounded-full", statusTone)}
                        />
                        <span className="text-foreground/80 truncate flex-1">
                          {routine.name || routine.slug}
                        </span>
                        {routine.invocation_count > 0 && (
                          <span className="text-[10px] font-mono tabular-nums text-muted-foreground-soft shrink-0">
                            {routine.invocation_count}
                          </span>
                        )}
                        {routine.author_agent_id && (
                          <span
                            aria-hidden
                            className="h-4 w-4 rounded-full bg-cover bg-center shrink-0"
                            style={{ backgroundImage: `url(${getAgentAvatarUrl(routine.author_agent_id)})` }}
                          />
                        )}
                      </SidebarRow>
                    </div>
                  </TooltipTrigger>
                  <TooltipContent side="right" sideOffset={8}>
                    <div className="space-y-0.5">
                      <div className="font-medium">{routine.name || routine.slug}</div>
                      <div className="text-[10px] font-mono opacity-70">{routine.slug}</div>
                      {routine.description && (
                        <div className="text-[10px] opacity-80 max-w-[260px]">{routine.description}</div>
                      )}
                    </div>
                  </TooltipContent>
                </Tooltip>
              )
            })}
          </TooltipProvider>
          {displayed.length === 0 && (
            <motion.div
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              transition={{ delay: 0.1 }}
              className="flex items-center justify-center py-6 text-xs text-muted-foreground-soft"
            >
              No routines found
            </motion.div>
          )}
        </div>
      </div>
    </div>
  )
}
