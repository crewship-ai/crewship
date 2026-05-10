"use client"

import { useState } from "react"
import {
  ArrowDownUp,
  Calendar,
  Filter as FilterIcon,
  LayoutGrid,
  PauseCircle,
  Search,
  Users,
  Workflow,
  X,
  Zap,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"
import type {
  DateRangeKey,
  GroupAxis,
  RunFilter,
  TriggerSource,
} from "@/lib/activity/run-filters"
import { activeFilterCount } from "@/lib/activity/run-filters"

// RailToolbar — search + filter / sort / group dropdowns + active
// filter chips. Modeled on Linear's pattern: filter as a typed,
// composable predicate the user assembles from a single popover.

export type SortAxis = "newest" | "oldest" | "cost-desc" | "duration-desc"

export interface RailToolbarProps {
  filter: RunFilter
  onFilterChange: (next: RunFilter) => void
  search: string
  onSearchChange: (next: string) => void
  sort: SortAxis
  onSortChange: (next: SortAxis) => void
  group: GroupAxis
  onGroupChange: (next: GroupAxis) => void
  // Counts for the segmented status switch
  counts: { active: number; all: number; completed: number; failed: number }
  // Filter dropdown options (computed from the current run set so we
  // don't surface dimensions that have no data).
  options: {
    crews: { id: string; name: string }[]
    routines: { slug: string; name: string }[]
    sources: TriggerSource[]
  }
}

export function RailToolbar(props: RailToolbarProps) {
  const { filter, onFilterChange, search, onSearchChange, sort, onSortChange, group, onGroupChange, counts, options } = props
  const filterCount = activeFilterCount(filter)

  return (
    <div className="shrink-0 space-y-1.5 border-b border-white/[0.06] p-2">
      {/* Search box */}
      <div className="relative">
        <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground/50" />
        <input
          type="text"
          value={search}
          onChange={(e) => onSearchChange(e.target.value)}
          placeholder="Search ID, routine, issue, agent…"
          aria-label="Search runs"
          className="h-7 w-full rounded border border-white/[0.06] bg-background pl-7 pr-7 text-xs placeholder:text-muted-foreground/50 focus:border-blue-500/50 focus:outline-none"
        />
        {search && (
          <button
            type="button"
            onClick={() => onSearchChange("")}
            aria-label="Clear search"
            className="absolute right-1 top-1/2 -translate-y-1/2 rounded p-0.5 text-muted-foreground/50 hover:text-foreground"
          >
            <X className="h-3 w-3" />
          </button>
        )}
      </div>

      {/* Toolbar dropdowns */}
      <div className="flex items-center gap-1 text-[11px]">
        <FilterMenu filter={filter} onFilterChange={onFilterChange} options={options} count={filterCount} />

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="xs" className="h-6 gap-1 px-2 text-[11px] text-muted-foreground/80">
              <ArrowDownUp className="h-3 w-3" />
              <span>Sort: {SORT_LABEL[sort]}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="text-xs">
            <DropdownMenuRadioGroup value={sort} onValueChange={(v) => onSortChange(v as SortAxis)}>
              <DropdownMenuRadioItem value="newest">Newest first</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="oldest">Oldest first</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="cost-desc">Cost (high → low)</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="duration-desc">Duration (high → low)</DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="xs" className="h-6 gap-1 px-2 text-[11px] text-muted-foreground/80">
              <LayoutGrid className="h-3 w-3" />
              <span>Group: {GROUP_LABEL[group]}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="text-xs">
            <DropdownMenuRadioGroup value={group} onValueChange={(v) => onGroupChange(v as GroupAxis)}>
              <DropdownMenuRadioItem value="source">Source</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="routine">Routine</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="crew">Crew</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="issue">Issue</DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="none">None (flat)</DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* Active filter chips */}
      {filterCount > 0 && (
        <FilterChips filter={filter} onFilterChange={onFilterChange} options={options} />
      )}

      {/* Status segmented control — always visible, separate from
        * the filter popover because users hit it constantly. */}
      <div className="flex items-center gap-0.5 rounded border border-white/[0.06] bg-background p-0.5">
        <SegBtn label="Active" count={counts.active} active={filter.status === "active"}
          onClick={() => onFilterChange({ ...filter, status: "active" })} />
        <SegBtn label="All" count={counts.all} active={!filter.status || filter.status === "all"}
          onClick={() => onFilterChange({ ...filter, status: "all" })} />
        <SegBtn label="Done" count={counts.completed} active={filter.status === "completed"}
          onClick={() => onFilterChange({ ...filter, status: "completed" })} />
        <SegBtn label="Failed" count={counts.failed} active={filter.status === "failed"}
          onClick={() => onFilterChange({ ...filter, status: "failed" })} />
      </div>
    </div>
  )
}

const SORT_LABEL: Record<SortAxis, string> = {
  "newest": "Newest",
  "oldest": "Oldest",
  "cost-desc": "Cost",
  "duration-desc": "Duration",
}
const GROUP_LABEL: Record<GroupAxis, string> = {
  source: "Source",
  routine: "Routine",
  crew: "Crew",
  issue: "Issue",
  none: "None",
}

function SegBtn({ label, count, active, onClick }: { label: string; count: number; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "flex flex-1 items-center justify-center gap-1 rounded px-2 py-0.5 text-[10px] transition-colors",
        active ? "bg-blue-500/15 text-blue-300" : "text-muted-foreground/70 hover:text-foreground/80",
      )}
    >
      <span>{label}</span>
      <span className="font-mono text-[9px] opacity-70">{count}</span>
    </button>
  )
}

// ── Filter popover ──────────────────────────────────────────────────

function FilterMenu({
  filter,
  onFilterChange,
  options,
  count,
}: {
  filter: RunFilter
  onFilterChange: (next: RunFilter) => void
  options: RailToolbarProps["options"]
  count: number
}) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="xs" className="h-6 gap-1 px-2 text-[11px]">
          <FilterIcon className="h-3 w-3" />
          Filter
          {count > 0 && (
            <span className="rounded bg-blue-500/15 px-1 text-[9px] text-blue-300">{count}</span>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[260px] p-0" sideOffset={4}>
        <div className="border-b border-border px-3 py-2 text-[11px] font-semibold">
          Add filter
        </div>

        <SourceSection filter={filter} onChange={onFilterChange} sources={options.sources} />
        <DateSection filter={filter} onChange={onFilterChange} />
        <CrewSection filter={filter} onChange={onFilterChange} crews={options.crews} />
        <RoutineSection filter={filter} onChange={onFilterChange} routines={options.routines} />
        <WaitpointSection filter={filter} onChange={onFilterChange} />

        <div className="border-t border-border px-3 py-2 flex items-center justify-between text-[10px]">
          <span className="text-muted-foreground/60">{count} filter{count !== 1 ? "s" : ""} active</span>
          {count > 0 && (
            <button
              type="button"
              onClick={() => onFilterChange({ status: filter.status })}
              className="text-blue-300 hover:text-blue-200"
            >
              Clear all
            </button>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}

function SectionHeader({ Icon, label }: { Icon: typeof FilterIcon; label: string }) {
  return (
    <div className="flex items-center gap-1.5 px-3 pt-2 text-[10px] uppercase tracking-wider text-muted-foreground/60">
      <Icon className="h-3 w-3" /> {label}
    </div>
  )
}

function SourceSection({ filter, onChange, sources }: {
  filter: RunFilter
  onChange: (next: RunFilter) => void
  sources: TriggerSource[]
}) {
  const sel = new Set(filter.sources ?? [])
  const toggle = (s: TriggerSource) => {
    const next = new Set(sel)
    next.has(s) ? next.delete(s) : next.add(s)
    onChange({ ...filter, sources: next.size === 0 ? undefined : Array.from(next) })
  }
  return (
    <div className="space-y-1 pb-2">
      <SectionHeader Icon={Zap} label="Source" />
      <div className="flex flex-wrap gap-1 px-3">
        {sources.length === 0 ? (
          <span className="text-[10px] text-muted-foreground/50">No sources in current set</span>
        ) : (
          sources.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => toggle(s)}
              aria-pressed={sel.has(s)}
              className={cn(
                "rounded border px-1.5 py-0.5 text-[10px] transition-colors",
                sel.has(s)
                  ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                  : "border-white/[0.06] text-muted-foreground/70 hover:text-foreground/80",
              )}
            >
              {SOURCE_LABEL[s]}
            </button>
          ))
        )}
      </div>
    </div>
  )
}

const SOURCE_LABEL: Record<TriggerSource, string> = {
  manual: "Manual",
  schedule: "Schedule",
  webhook: "Webhook",
  issue: "Issue",
  call_pipeline: "Sub-routine",
}

function DateSection({ filter, onChange }: { filter: RunFilter; onChange: (next: RunFilter) => void }) {
  const value = filter.dateRange ?? "all"
  const set = (v: DateRangeKey) =>
    onChange({ ...filter, dateRange: v === "all" ? undefined : v })
  return (
    <div className="space-y-1 pb-2">
      <SectionHeader Icon={Calendar} label="Date range" />
      <div className="flex gap-1 px-3">
        {(["1h", "24h", "7d", "all"] as const).map((v) => (
          <button
            key={v}
            type="button"
            onClick={() => set(v)}
            aria-pressed={value === v}
            className={cn(
              "flex-1 rounded border px-1.5 py-0.5 text-[10px]",
              value === v
                ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                : "border-white/[0.06] text-muted-foreground/70 hover:text-foreground/80",
            )}
          >
            {v === "all" ? "All time" : `Last ${v}`}
          </button>
        ))}
      </div>
    </div>
  )
}

function CrewSection({ filter, onChange, crews }: {
  filter: RunFilter
  onChange: (next: RunFilter) => void
  crews: { id: string; name: string }[]
}) {
  const sel = new Set(filter.crews ?? [])
  if (crews.length === 0) return null
  const toggle = (id: string) => {
    const next = new Set(sel)
    next.has(id) ? next.delete(id) : next.add(id)
    onChange({ ...filter, crews: next.size === 0 ? undefined : Array.from(next) })
  }
  return (
    <div className="space-y-1 pb-2">
      <SectionHeader Icon={Users} label="Crew" />
      <div className="max-h-[120px] overflow-y-auto px-3 space-y-0.5">
        {crews.map((c) => (
          <label
            key={c.id}
            className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-[11px] hover:bg-white/[0.04]"
          >
            <input type="checkbox" checked={sel.has(c.id)} onChange={() => toggle(c.id)} className="h-3 w-3" />
            <span className="truncate">{c.name}</span>
          </label>
        ))}
      </div>
    </div>
  )
}

function RoutineSection({ filter, onChange, routines }: {
  filter: RunFilter
  onChange: (next: RunFilter) => void
  routines: { slug: string; name: string }[]
}) {
  const sel = new Set(filter.routines ?? [])
  if (routines.length === 0) return null
  const toggle = (slug: string) => {
    const next = new Set(sel)
    next.has(slug) ? next.delete(slug) : next.add(slug)
    onChange({ ...filter, routines: next.size === 0 ? undefined : Array.from(next) })
  }
  return (
    <div className="space-y-1 pb-2">
      <SectionHeader Icon={Workflow} label="Routine" />
      <div className="max-h-[120px] overflow-y-auto px-3 space-y-0.5">
        {routines.map((r) => (
          <label
            key={r.slug}
            className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-[11px] hover:bg-white/[0.04]"
          >
            <input type="checkbox" checked={sel.has(r.slug)} onChange={() => toggle(r.slug)} className="h-3 w-3" />
            <span className="truncate">{r.name}</span>
          </label>
        ))}
      </div>
    </div>
  )
}

function WaitpointSection({ filter, onChange }: { filter: RunFilter; onChange: (next: RunFilter) => void }) {
  return (
    <div className="space-y-1 pb-2">
      <SectionHeader Icon={PauseCircle} label="Waitpoint" />
      <div className="px-3">
        <label className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-[11px] hover:bg-white/[0.04]">
          <input
            type="checkbox"
            checked={filter.hasWaitpoint ?? false}
            onChange={(e) => onChange({ ...filter, hasWaitpoint: e.target.checked || undefined })}
            className="h-3 w-3"
          />
          <span>Only runs awaiting approval</span>
        </label>
      </div>
    </div>
  )
}

// ── Active filter chips below the toolbar ──────────────────────────

function FilterChips({
  filter,
  onFilterChange,
  options,
}: {
  filter: RunFilter
  onFilterChange: (next: RunFilter) => void
  options: RailToolbarProps["options"]
}) {
  const chips: { key: string; label: string; remove: () => void }[] = []
  if (filter.sources?.length) {
    chips.push({
      key: "src",
      label: `source: ${filter.sources.map((s) => SOURCE_LABEL[s]).join(", ")}`,
      remove: () => onFilterChange({ ...filter, sources: undefined }),
    })
  }
  if (filter.dateRange && filter.dateRange !== "all") {
    chips.push({
      key: "date",
      label: `last ${filter.dateRange}`,
      remove: () => onFilterChange({ ...filter, dateRange: undefined }),
    })
  }
  if (filter.crews?.length) {
    const names = filter.crews
      .map((id) => options.crews.find((c) => c.id === id)?.name ?? id)
      .join(", ")
    chips.push({
      key: "crew",
      label: `crew: ${names}`,
      remove: () => onFilterChange({ ...filter, crews: undefined }),
    })
  }
  if (filter.routines?.length) {
    const names = filter.routines
      .map((s) => options.routines.find((r) => r.slug === s)?.name ?? s)
      .join(", ")
    chips.push({
      key: "rt",
      label: `routine: ${names}`,
      remove: () => onFilterChange({ ...filter, routines: undefined }),
    })
  }
  if (filter.hasWaitpoint) {
    chips.push({
      key: "wp",
      label: "awaiting approval",
      remove: () => onFilterChange({ ...filter, hasWaitpoint: undefined }),
    })
  }

  if (chips.length === 0) return null
  return (
    <div className="flex flex-wrap gap-1">
      {chips.map((c) => (
        <span
          key={c.key}
          className="inline-flex items-center gap-1 rounded border border-blue-500/25 bg-blue-500/10 px-1.5 py-0.5 text-[10px] text-blue-200"
        >
          <span className="truncate max-w-[180px]">{c.label}</span>
          <button
            type="button"
            onClick={c.remove}
            aria-label={`Remove filter ${c.label}`}
            className="text-blue-300/70 hover:text-blue-100"
          >
            <X className="h-2.5 w-2.5" />
          </button>
        </span>
      ))}
    </div>
  )
}
