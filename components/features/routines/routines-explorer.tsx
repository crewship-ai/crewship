"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Search,
  X,
  ChevronDown,
  Filter,
  ExternalLink,
  ScrollText,
  CheckCircle2,
  XCircle,
  CircleDashed,
  Sparkles,
  Flame,
  Users,
  EyeOff,
} from "lucide-react"
import Link from "next/link"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { UnifiedInbox } from "@/components/features/orchestration/unified-inbox"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { RoutineFilters } from "./routines-filter-sidebar"

// RoutinesExplorer — drop-in replacement for the older filter-only
// sidebar. Structurally mirrors UnifiedExplorer (the /issues sidebar)
// so the two pages feel like one app: same search-bar chrome at top,
// same Filter dropdown pattern, same collapsible STATUS section
// (modeled on Issues' PROJECTS), same ROUTINES list rendering with
// per-row icon + name + count + author avatar, same INBOX section at
// the bottom with hover proklik to /inbox.
//
// Why mirror rather than refactor both into a generic <Explorer>:
// the data shapes are different enough (Mission vs Pipeline) that a
// generic API would smear the per-page nuance — routines need a usage
// facet (popular/fresh) that issues don't, issues need priority that
// routines don't. The CHROME is the unified part; the rows + facets
// stay feature-specific.

interface RoutinesExplorerProps {
  routines: Pipeline[]
  search: string
  onSearchChange: (value: string) => void
  selectedSlug: string | null
  onSelectRoutine: (slug: string) => void
  filters: RoutineFilters
  onChange: (next: RoutineFilters) => void
  // Inbox reuse — same component as Issues sidebar to keep the
  // approvals/escalations list in identical shape on both pages.
  missions: Mission[]
  onTaskSelect: (task: MissionTask, mission: Mission) => void
  onApproveGate?: (taskId: string, missionId: string) => void
}

const sectionAnim = {
  initial: { height: 0, opacity: 0 },
  animate: { height: "auto", opacity: 1, transition: { duration: 0.2, ease: "easeOut" as const } },
  exit: { height: 0, opacity: 0, transition: { duration: 0.15, ease: "easeIn" as const } },
}

const dropdownAnim = {
  initial: { opacity: 0, scale: 0.95, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } },
  exit: { opacity: 0, scale: 0.95, y: -4, transition: { duration: 0.1 } },
}

const listItemAnim = {
  initial: { opacity: 0, x: -8 },
  animate: { opacity: 1, x: 0 },
  exit: { opacity: 0, x: 8 },
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
  missions,
  onTaskSelect,
  onApproveGate,
}: RoutinesExplorerProps) {
  const [statusOpen, setStatusOpen] = useState(true)
  const [inboxOpen, setInboxOpen] = useState(true)
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

  // Inbox count — reuse the same FAILED/BLOCKED/AWAITING_APPROVAL
  // criterion as UnifiedExplorer so the two pages report the same
  // number.
  const inboxCount = useMemo(() => {
    let count = 0
    for (const m of missions) {
      if (!m.tasks) continue
      for (const t of m.tasks) {
        if (t.status === "FAILED" || t.status === "BLOCKED" || t.status === "AWAITING_APPROVAL") count++
      }
    }
    return count
  }, [missions])

  const filterLabel = useMemo(() => {
    if (filters.authorAgentId) {
      return agents.find((a) => a.id === filters.authorAgentId)?.name || "Author"
    }
    if (filters.invocations === "popular") return "Popular"
    if (filters.invocations === "fresh") return "Fresh"
    if (!filters.showEphemeral === false) return null
    return null
  }, [filters, agents])

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

  const clearFilters = () =>
    onChange({ ...filters, invocations: "all", authorAgentId: null, showEphemeral: false })

  return (
    <div className="flex flex-col h-full">
      {/* ── Search + Filter ── */}
      <div className="px-2 py-2 shrink-0 flex items-center gap-1.5">
        <div className="flex items-center gap-1.5 h-8 px-2.5 bg-white/[0.04] border border-white/[0.08] rounded-md flex-1 min-w-0">
          <Search className="h-3.5 w-3.5 text-muted-foreground-soft shrink-0" />
          <input
            type="text"
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder="Search routines, agents..."
            data-routines-search-input
            className="flex-1 bg-transparent text-xs text-foreground placeholder:text-muted-foreground-soft outline-none min-w-0"
          />
          <AnimatePresence>
            {search && (
              <motion.button
                initial={{ opacity: 0, scale: 0.5 }}
                animate={{ opacity: 1, scale: 1 }}
                exit={{ opacity: 0, scale: 0.5 }}
                onClick={() => onSearchChange("")}
                className="text-muted-foreground-soft hover:text-foreground"
              >
                <X className="h-3.5 w-3.5" />
              </motion.button>
            )}
          </AnimatePresence>
        </div>
        <div className="relative shrink-0">
          <motion.button
            whileTap={{ scale: 0.95 }}
            onClick={() => setFilterDropdownOpen(!filterDropdownOpen)}
            className={cn(
              "flex items-center gap-1 h-8 px-2.5 rounded-md border text-[11px] whitespace-nowrap transition-colors",
              filterLabel
                ? "bg-blue-500/10 border-blue-500/30 text-blue-400"
                : "bg-white/[0.04] border-white/[0.08] text-muted-foreground-soft hover:text-muted-foreground",
            )}
          >
            <Filter className="h-3 w-3" />
            <span>{filterLabel || "Filter"}</span>
            {filterLabel && (
              <span
                role="button"
                tabIndex={0}
                aria-label="Clear filter"
                onClick={(e) => {
                  e.stopPropagation()
                  clearFilters()
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault()
                    e.stopPropagation()
                    clearFilters()
                  }
                }}
                className="ml-0.5 inline-flex items-center hover:text-white cursor-pointer"
              >
                <X className="h-3 w-3" />
              </span>
            )}
          </motion.button>
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
                        filters.invocations === v ? "text-blue-400" : "text-muted-foreground/80",
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
                          filters.authorAgentId === null ? "text-blue-400" : "text-muted-foreground/80",
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
                            filters.authorAgentId === a.id ? "text-blue-400" : "text-muted-foreground/80",
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
                      filters.showEphemeral ? "text-blue-400" : "text-muted-foreground/80",
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
      </div>

      {/* ── Status ── (mirrors PROJECTS in Issues — single-select bucket) */}
      <div className="shrink-0 border-b border-white/[0.06]">
        <button
          onClick={() => setStatusOpen(!statusOpen)}
          className="flex items-center gap-1.5 w-full px-3 py-1.5 hover:bg-white/[0.02]"
        >
          <motion.div animate={{ rotate: statusOpen ? 0 : -90 }} transition={{ duration: 0.15 }}>
            <ChevronDown className="h-3 w-3 text-muted-foreground-soft" />
          </motion.div>
          <span className="text-[10px] font-semibold text-foreground/50 uppercase tracking-wider flex-1 text-left">
            Status
          </span>
          <span className="text-[10px] text-muted-foreground-soft">{STATUS_BUCKETS.length}</span>
        </button>
        <AnimatePresence initial={false}>
          {statusOpen && (
            <motion.div {...sectionAnim} className="overflow-hidden">
              {STATUS_BUCKETS.map((b) => {
                const IconComp = b.icon
                const isSelected = filters.status === b.id
                const count = statusCounts[b.id]
                return (
                  <button
                    key={b.id}
                    onClick={() => onChange({ ...filters, status: b.id })}
                    className={cn(
                      "w-full flex items-center gap-2 px-3 py-1.5 text-left",
                      isSelected ? "row-interactive row-selected" : "row-interactive row-hover",
                    )}
                  >
                    <IconComp className={cn("h-3.5 w-3.5 shrink-0", b.tone)} />
                    <span className="text-xs text-foreground/80 truncate flex-1">{b.label}</span>
                    <span className="text-[10px] text-muted-foreground-soft tabular-nums">{count}</span>
                  </button>
                )
              })}
            </motion.div>
          )}
        </AnimatePresence>
      </div>

      {/* ── Routines ── (mirrors ISSUES section in unified-explorer) */}
      <div className="flex-1 min-h-0 flex flex-col border-b border-white/[0.06]">
        <div className="px-3 py-1.5 shrink-0 flex items-center justify-between">
          <span className="text-[10px] font-semibold text-foreground/50 uppercase tracking-wider">Routines</span>
          <motion.span
            key={displayed.length}
            initial={{ opacity: 0, y: -4 }}
            animate={{ opacity: 1, y: 0 }}
            className="text-[10px] text-muted-foreground-soft"
          >
            {displayed.length}
          </motion.span>
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto px-1 pb-1">
          <TooltipProvider delayDuration={400}>
            <AnimatePresence mode="popLayout">
              {displayed.map((routine, idx) => {
                const isSelected = selectedSlug === routine.slug
                const lastStatus = routine.last_invocation_status?.toLowerCase()
                const statusTone =
                  lastStatus === "completed"
                    ? "bg-emerald-500"
                    : lastStatus === "failed"
                      ? "bg-rose-500"
                      : routine.invocation_count === 0
                        ? "bg-muted-foreground/30"
                        : "bg-blue-400"
                return (
                  <Tooltip key={routine.id}>
                    <TooltipTrigger asChild>
                      <motion.button
                        layout
                        {...listItemAnim}
                        transition={{ duration: 0.15, delay: idx * 0.02 }}
                        onClick={() => onSelectRoutine(routine.slug)}
                        className={cn(
                          "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left transition-colors hover:bg-white/[0.04]",
                          isSelected
                            ? "bg-blue-500/10 border-l-2 border-l-blue-400"
                            : "border-l-2 border-l-transparent",
                        )}
                      >
                        <span
                          aria-hidden
                          title={lastStatus ?? "never invoked"}
                          className={cn("h-2 w-2 shrink-0 rounded-full", statusTone)}
                        />
                        <span className="text-xs text-foreground/80 truncate flex-1">
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
                      </motion.button>
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
            </AnimatePresence>
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

      {/* ── Inbox ── (same component as /issues sidebar) */}
      <div className="shrink-0">
        <div className="flex items-center gap-1 pr-2 hover:bg-white/[0.02] group">
          <button
            onClick={() => setInboxOpen(!inboxOpen)}
            className="flex items-center gap-1.5 flex-1 px-3 py-1.5 text-left min-w-0"
          >
            <motion.div animate={{ rotate: inboxOpen ? 0 : -90 }} transition={{ duration: 0.15 }}>
              <ChevronDown className="h-3 w-3 text-muted-foreground-soft" />
            </motion.div>
            <span className="text-[10px] font-semibold text-foreground/50 uppercase tracking-wider flex-1">
              Inbox
            </span>
            {inboxCount > 0 && (
              <motion.span
                initial={{ scale: 0.5 }}
                animate={{ scale: 1 }}
                className="text-[9px] bg-red-500 text-white rounded-full px-1.5 min-w-[16px] text-center leading-[16px]"
              >
                {inboxCount}
              </motion.span>
            )}
          </button>
          <Link
            href="/inbox"
            aria-label="Open full inbox page"
            title="Open full inbox"
            className="opacity-0 group-hover:opacity-100 p-1 rounded text-muted-foreground-soft hover:text-foreground hover:bg-white/[0.04] transition-opacity shrink-0"
          >
            <ExternalLink className="h-3 w-3" />
          </Link>
        </div>
        <AnimatePresence initial={false}>
          {inboxOpen && (
            <motion.div {...sectionAnim} className="overflow-hidden">
              <UnifiedInbox missions={missions} onTaskSelect={onTaskSelect} onApproveGate={onApproveGate} />
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}
