"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Activity,
  PauseCircle,
  ScrollText,
  CheckCircle2,
  XCircle,
  CircleDashed,
  Sparkles,
  Flame,
  Users,
  EyeOff,
  Check,
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
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import type { Pipeline } from "@/hooks/use-pipelines"
import { isAwaitingApproval, useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import { useTick } from "@/hooks/use-tick"
import type { RoutineFilters } from "@/components/features/routines/routines-filter-sidebar"
import { formatElapsedSince } from "./routine-cost-format"

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
  // Live buckets first. A routine parked on a human is the only state on
  // this page that is waiting for the person reading it — burying that
  // under three historical outcomes gets it found last.
  { id: "awaiting", label: "Awaiting approval", icon: PauseCircle, tone: "text-warn" },
  { id: "running", label: "Running", icon: Activity, tone: "text-primary" },
  { id: "completed", label: "Completed", icon: CheckCircle2, tone: "text-success" },
  { id: "failed", label: "Failed", icon: XCircle, tone: "text-destructive" },
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

  // Shared workspace live-runs subscription (one fetch loop app-wide,
  // provided by the dashboard layout) — newest active run per slug.
  const { bySlug: liveBySlug } = useActiveRoutineRuns()

  // Counts per status bucket (computed against the workspace-wide list,
  // not the post-status-filter view — otherwise switching to "Failed"
  // would make every other bucket show 0).
  const statusCounts = useMemo(() => {
    const c: Record<StatusBucket, number> = {
      all: routines.length,
      awaiting: 0,
      running: 0,
      completed: 0,
      failed: 0,
      never: 0,
    }
    for (const p of routines) {
      const s = p.last_invocation_status?.toLowerCase()
      // Live state comes from the workspace run feed, not from the
      // routine row: last_invocation_status still reads "running" while
      // a run is parked on a human, so the row alone cannot tell the two
      // apart — which is the distinction the bucket exists to make.
      const live = liveBySlug.get(p.slug)
      if (live) {
        if (isAwaitingApproval(live.status)) c.awaiting++
        else c.running++
      }
      if (p.invocation_count === 0) c.never++
      if (s === "completed") c.completed++
      if (s === "failed") c.failed++
    }
    return c
  }, [routines, liveBySlug])

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

  // 1s re-render tick ONLY while a displayed routine has a live run,
  // so the "· 0:12" elapsed segment counts up. Idle sidebar = no
  // interval at all (useTick treats <=0 as off).
  const hasLiveRow = displayed.some((p) => liveBySlug.has(p.slug))
  useTick(hasLiveRow ? 1000 : 0)

  return (
    <div className="flex flex-col h-full">
      {/* ── Search + Filter ── */}
      <SidebarToolbar>
        {/* data-routines-search wrapper keeps the `/` focus shortcut working
            (routines-layout targets `[data-routines-search] input`). */}
        <div data-routines-search className="flex-1 min-w-0">
          <SidebarSearch
            value={search}
            onValueChange={onSearchChange}
            placeholder="Search routines, agents…"
          />
        </div>
        <div className="relative shrink-0">
          <SidebarFilterButton
            activeCount={activeFilterCount}
            aria-expanded={filterDropdownOpen}
            onClick={() => setFilterDropdownOpen(!filterDropdownOpen)}
          />
          <AnimatePresence>
            {filterDropdownOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterDropdownOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  role="menu"
                  className="absolute right-0 top-9 z-50 min-w-[220px] max-h-[360px] overflow-y-auto rounded-lg border border-white/[0.08] bg-card/95 py-1 shadow-2xl ring-1 ring-black/40 backdrop-blur-xl"
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
                        "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors",
                        filters.invocations === v
                          ? "bg-primary/10 text-primary-hover"
                          : "text-muted-foreground/80 hover:bg-white/[0.06] hover:text-foreground",
                      )}
                    >
                      {v === "popular" && <Flame className="h-3.5 w-3.5 shrink-0" />}
                      {v === "fresh" && <Sparkles className="h-3.5 w-3.5 shrink-0" />}
                      {v === "all" && <ScrollText className="h-3.5 w-3.5 shrink-0 opacity-60" />}
                      <span className="flex-1">
                        {v === "all" ? "All usage" : v === "popular" ? "Popular (10+)" : "Fresh (no runs)"}
                      </span>
                      {filters.invocations === v && <Check className="h-3 w-3 shrink-0" />}
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
                          "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors",
                          filters.authorAgentId === null
                            ? "bg-primary/10 text-primary-hover"
                            : "text-muted-foreground/80 hover:bg-white/[0.06] hover:text-foreground",
                        )}
                      >
                        <Users className="h-3.5 w-3.5 shrink-0 opacity-60" />
                        <span className="flex-1">All authors</span>
                        {filters.authorAgentId === null && <Check className="h-3 w-3 shrink-0" />}
                      </button>
                      {agents.map((a) => (
                        <button
                          key={a.id}
                          onClick={() => {
                            onChange({ ...filters, authorAgentId: a.id })
                            setFilterDropdownOpen(false)
                          }}
                          className={cn(
                            "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors",
                            filters.authorAgentId === a.id
                              ? "bg-primary/10 text-primary-hover"
                              : "text-muted-foreground/80 hover:bg-white/[0.06] hover:text-foreground",
                          )}
                        >
                          <span
                            aria-hidden
                            className="h-4 w-4 shrink-0 rounded-full bg-cover bg-center"
                            style={{ backgroundImage: `url(${getAgentAvatarUrl(a.id)})` }}
                          />
                          <span className="truncate flex-1">{a.name}</span>
                          <span className="text-[10px] tabular-nums text-muted-foreground-soft">{a.count}</span>
                          {filters.authorAgentId === a.id && <Check className="h-3 w-3 shrink-0" />}
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
                      "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors",
                      filters.showEphemeral
                        ? "bg-primary/10 text-primary-hover"
                        : "text-muted-foreground/80 hover:bg-white/[0.06] hover:text-foreground",
                    )}
                  >
                    <EyeOff className="h-3.5 w-3.5 shrink-0" />
                    <span className="flex-1">
                      {filters.showEphemeral ? "Hiding nothing" : "Show ephemeral"}
                    </span>
                    {filters.showEphemeral && <Check className="h-3 w-3 shrink-0" />}
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
              {/* A bucket holding nothing dims to match. Six rows of
                  equal weight, four of them zero, is what made this
                  column read as a wall — and the ones with something
                  in them are the only ones worth a click. */}
              <IconComp
                className={cn("h-3.5 w-3.5 shrink-0", b.tone, count === 0 && !isSelected && "opacity-40")}
              />
              <span
                className={cn(
                  "truncate flex-1",
                  count === 0 && !isSelected ? "text-foreground/40" : "text-foreground/80",
                )}
              >
                {b.label}
              </span>
              <span
                className={cn(
                  "rounded-full px-1.5 py-px text-[10px] tabular-nums",
                  count === 0
                    ? "text-muted-foreground-soft/50"
                    : isSelected
                      ? "bg-primary/15 text-primary"
                      : "bg-white/[0.05] text-muted-foreground",
                )}
              >
                {count}
              </span>
            </SidebarRow>
          )
        })}
      </SidebarSection>

      {/* ── Routines ── */}
      <div className="flex-1 min-h-0 flex flex-col">
        <SidebarSection label="Routines" count={displayed.length} />
        <div className="flex-1 min-h-0 overflow-y-auto pb-1">
          <TooltipProvider delayDuration={400}>
            {displayed.map((routine, i) => {
              const isSelected = selectedSlug === routine.slug
              const lastStatus = routine.last_invocation_status?.toLowerCase()
              // Live run for this routine (shared workspace hook,
              // matched by pipeline_slug) — the row "comes alive":
              // pulsing dot + a sub-line with the current step and
              // elapsed time (amber ⏸ variant while parked on a
              // human approval).
              const liveRun = liveBySlug.get(routine.slug)
              const liveAwaiting = liveRun ? isAwaitingApproval(liveRun.status) : false
              const statusTone = liveRun
                ? liveAwaiting
                  ? "bg-warn animate-pulse"
                  : "bg-primary animate-pulse"
                : lastStatus === "completed"
                  ? "bg-success"
                  : lastStatus === "failed"
                    ? "bg-destructive"
                    : routine.invocation_count === 0
                      ? "bg-muted-foreground/30"
                      : "bg-primary"
              return (
                <Tooltip key={routine.id}>
                  <TooltipTrigger asChild>
                    {/* Rows arrive in sequence rather than all at once,
                        which is what makes a filter read as the list
                        narrowing instead of the list being replaced. The
                        delay is capped: past a dozen rows a per-row
                        stagger stops being a cascade and starts being a
                        wait. */}
                    <motion.div
                      initial={{ opacity: 0, y: 4 }}
                      animate={{ opacity: 1, y: 0 }}
                      transition={{
                        duration: 0.22,
                        ease: [0.22, 1, 0.36, 1],
                        delay: Math.min(i, 12) * 0.018,
                      }}
                    >
                      <SidebarRow
                        selected={isSelected}
                        onSelect={() => onSelectRoutine(routine.slug)}
                      >
                        {/* Icon first, then the status dot. Thirty rows
                            of identical text was the problem; the icon is
                            what makes one findable at a glance, and the
                            dot still carries the state. Same derivation
                            the detail header uses — two surfaces showing
                            a different icon for one routine would be
                            worse than showing none. */}
                        <span className="relative shrink-0">
                          {/* A halo, not a moved icon. While a run is
                              live the icon keeps its place — a row
                              whose contents shift position is harder
                              to track than one that just glows — and
                              the ring pulses outward from behind it.
                              Absolutely positioned so it costs no
                              layout: a ring that changed the row's
                              width would nudge every name beside it. */}
                          {liveRun && (
                            <span
                              aria-hidden
                              className={cn(
                                "absolute -inset-1 animate-ping rounded-lg opacity-60",
                                liveAwaiting ? "bg-warn/20" : "bg-primary/20",
                              )}
                            />
                          )}
                          <CrewIcon
                            icon={resolveRoutineIcon(routine)}
                            color={resolveRoutineColor(routine)}
                            size="sm"
                            className={cn(
                              "relative !h-5 !w-5 !rounded-md transition-shadow",
                              liveRun &&
                                (liveAwaiting
                                  ? "ring-2 ring-warn/60"
                                  : "ring-2 ring-primary/60"),
                            )}
                          />
                          <span
                            aria-hidden
                            title={liveRun ? liveRun.status : (lastStatus ?? "never invoked")}
                            className={cn(
                              "absolute -bottom-0.5 -right-0.5 z-10 h-2 w-2 rounded-full ring-2 ring-card",
                              statusTone,
                            )}
                          />
                        </span>
                        <span className="min-w-0 flex-1 text-foreground/80">
                          <span className="block truncate">{routine.name || routine.slug}</span>
                          {/* The sub-line grows the row from one line to
                              two. Popping that in doubles the row's
                              height between two frames and shoves every
                              row below it down a step; animating the
                              height reads as the row opening rather
                              than the list jumping. */}
                          <AnimatePresence initial={false}>
                            {liveRun && (
                              <motion.span
                                key="live"
                                initial={{ height: 0, opacity: 0 }}
                                animate={{ height: "auto", opacity: 1 }}
                                exit={{ height: 0, opacity: 0 }}
                                transition={{ duration: 0.2, ease: [0.22, 1, 0.36, 1] }}
                                className={cn(
                                  "block overflow-hidden truncate text-[10px]",
                                  liveAwaiting ? "text-warn" : "text-primary",
                                )}
                              >
                                {liveAwaiting
                                  ? `⏸ awaiting approval · ${formatElapsedSince(liveRun.started_at)}`
                                  : `▶ ${liveRun.current_step_id || "starting…"} · ${formatElapsedSince(liveRun.started_at)}`}
                              </motion.span>
                            )}
                          </AnimatePresence>
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
                    </motion.div>
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
